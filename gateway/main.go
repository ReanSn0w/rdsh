package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/term"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 0 {
		if len(args) != 1 {
			return errors.New("expected one command without password arguments")
		}
		switch args[0] {
		case "hash-password":
			return hashPassword()
		case "generate-secret":
			_, err := fmt.Fprintln(os.Stdout, randomID())
			return err
		case "healthcheck":
			return healthcheck()
		default:
			return errors.New("supported commands: hash-password, generate-secret, healthcheck")
		}
	}
	c, err := loadConfig()
	if err != nil {
		return err
	}
	g := newGateway(c)
	defer g.close()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return serve(ctx, c, g)
}

func hashPassword() error {
	var password []byte
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Password: ")
		password, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return errors.New("cannot read password")
		}
		fmt.Fprint(os.Stderr, "Confirm password: ")
		confirmation, e := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if e != nil || !sameSecret(string(password), string(confirmation)) {
			return errors.New("passwords do not match")
		}
	} else {
		password, err = io.ReadAll(io.LimitReader(bufio.NewReader(os.Stdin), 75))
		if err != nil {
			return errors.New("cannot read password")
		}
		password = []byte(strings.TrimSuffix(strings.TrimSuffix(string(password), "\n"), "\r"))
	}
	if len(password) < 12 || len(password) > 72 {
		return errors.New("password must contain between 12 and 72 UTF-8 bytes")
	}
	hash, err := bcrypt.GenerateFromPassword(password, 12)
	if err != nil {
		return errors.New("cannot hash password")
	}
	_, err = fmt.Fprintln(os.Stdout, string(hash))
	return err
}

func healthcheck() error {
	_, port, err := net.SplitHostPort(env("HTTP_ADDR", ":8080"))
	if err != nil {
		return errors.New("invalid HTTP_ADDR")
	}
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	res, err := client.Get("http://127.0.0.1:" + port + "/_rdsh/health")
	if err != nil {
		return errors.New("gateway is not healthy")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return errors.New("gateway is not healthy")
	}
	return nil
}

func server(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 5 * time.Minute, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 64 << 10, ErrorLog: log.New(io.Discard, "", 0)}
}

func serve(ctx context.Context, c config, g *gateway) error {
	if c.TLSMode == "external" {
		// This branch constructs neither ACME manager nor HTTPS listener.
		return runServers(ctx, g, server(c.HTTPAddr, g))
	}
	manager, err := newACMEManager(c)
	if err != nil {
		return err
	}
	secure := server(c.HTTPSAddr, g)
	secure.TLSConfig = manager.TLSConfig()
	secure.TLSConfig.MinVersion = tls.VersionTLS12
	fallback := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "use HTTPS", 400)
			return
		}
		u := *c.PublicURL
		u.Path, u.RawPath, u.RawQuery = r.URL.Path, r.URL.RawPath, r.URL.RawQuery
		http.Redirect(w, r, u.String(), http.StatusPermanentRedirect)
	})
	acmeHandler := manager.HTTPHandler(fallback)
	plain := server(c.HTTPAddr, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if r.URL.Path == "/_rdsh/health" && net.ParseIP(host).IsLoopback() {
			g.ServeHTTP(w, r)
			return
		}
		acmeHandler.ServeHTTP(w, r)
	}))
	return runServers(ctx, g, plain, secure)
}

func newACMEManager(c config) (*autocert.Manager, error) {
	if err := os.MkdirAll(filepath.Join(c.StateDir, "acme"), 0700); err != nil {
		return nil, errors.New("cannot create ACME state directory")
	}
	manager := &autocert.Manager{Prompt: autocert.AcceptTOS, Email: c.ACMEEmail, HostPolicy: autocert.HostWhitelist(c.PublicURL.Hostname()), Cache: autocert.DirCache(filepath.Join(c.StateDir, "acme"))}
	if c.ACMEDirectory != "" {
		manager.Client = &acme.Client{DirectoryURL: c.ACMEDirectory}
	}
	return manager, nil
}

func runServers(ctx context.Context, g *gateway, servers ...*http.Server) error {
	errorsCh := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *http.Server) {
			var err error
			if s.TLSConfig != nil {
				err = s.ListenAndServeTLS("", "")
			} else {
				err = s.ListenAndServe()
			}
			errorsCh <- err
		}(s)
	}
	var err error
	select {
	case <-ctx.Done():
	case err = <-errorsCh:
	}
	g.auth.close() // also terminates hijacked WebSockets, which Shutdown does not own.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, s := range servers {
		if s.Shutdown(shutdownCtx) != nil {
			_ = s.Close()
		}
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return errors.New("gateway listener failed")
	}
	return nil
}
