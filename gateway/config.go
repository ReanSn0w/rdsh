package main

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type config struct {
	PublicURL, Upstream                                                                     *url.URL
	TLSMode, HTTPAddr, HTTPSAddr, Username, HandoffFile, StateDir, ACMEEmail, ACMEDirectory string
	PasswordHash, SessionKey                                                                []byte
	TrustedProxies                                                                          []*net.IPNet
	SessionTTL                                                                              time.Duration
}

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func loadConfig() (config, error) {
	c := config{
		TLSMode: env("TLS_MODE", "external"), HTTPAddr: env("HTTP_ADDR", ":8080"), HTTPSAddr: env("HTTPS_ADDR", ":8443"),
		Username: env("AUTH_USERNAME", "token"), HandoffFile: env("DSH_HANDOFF_FILE", "/run/rdsh/connection.json"),
		StateDir: env("STATE_DIR", "/var/lib/rdsh"), ACMEEmail: os.Getenv("ACME_EMAIL"), ACMEDirectory: os.Getenv("ACME_DIRECTORY_URL"),
	}
	var err error
	c.PublicURL, err = url.Parse(os.Getenv("PUBLIC_URL"))
	if err != nil || c.PublicURL.Scheme != "https" || c.PublicURL.Hostname() == "" || c.PublicURL.User != nil || c.PublicURL.RawQuery != "" || c.PublicURL.Fragment != "" || (c.PublicURL.Path != "" && c.PublicURL.Path != "/") || c.PublicURL.RawPath != "" {
		return c, errors.New("PUBLIC_URL must be an HTTPS origin without credentials, query, fragment or path prefix")
	}
	c.PublicURL.Path = ""
	if c.TLSMode != "auto" && c.TLSMode != "external" {
		return c, errors.New("TLS_MODE must be auto or external")
	}
	if c.TLSMode == "auto" && (net.ParseIP(c.PublicURL.Hostname()) != nil || (c.PublicURL.Port() != "" && c.PublicURL.Port() != "443")) {
		return c, errors.New("automatic HTTPS needs a DNS hostname on public port 443")
	}
	c.Upstream, err = url.Parse(env("DSH_UPSTREAM", "http://dsh:3080"))
	if err != nil || (c.Upstream.Scheme != "http" && c.Upstream.Scheme != "https") || c.Upstream.Hostname() == "" || c.Upstream.User != nil || c.Upstream.RawQuery != "" || c.Upstream.Fragment != "" || (c.Upstream.Path != "" && c.Upstream.Path != "/") || c.Upstream.RawPath != "" {
		return c, errors.New("DSH_UPSTREAM must be a configured HTTP(S) origin")
	}
	c.Upstream.Path = ""
	c.SessionTTL, err = time.ParseDuration(env("SESSION_TTL", "12h"))
	if err != nil || c.SessionTTL < 5*time.Minute || c.SessionTTL > 30*24*time.Hour {
		return c, errors.New("SESSION_TTL must be between 5m and 720h")
	}
	for _, s := range strings.Split(os.Getenv("TRUSTED_PROXIES"), ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		_, network, e := net.ParseCIDR(s)
		if e != nil {
			return c, errors.New("TRUSTED_PROXIES must contain CIDR networks")
		}
		c.TrustedProxies = append(c.TrustedProxies, network)
	}
	hash, hashFile := os.Getenv("AUTH_PASSWORD_HASH"), os.Getenv("AUTH_PASSWORD_HASH_FILE")
	if hash != "" && hashFile != "" {
		return c, errors.New("set only one of AUTH_PASSWORD_HASH and AUTH_PASSWORD_HASH_FILE")
	}
	if hashFile != "" {
		c.PasswordHash, err = readPrivateFile(hashFile)
		if err != nil {
			return c, fmt.Errorf("password hash file: %w", err)
		}
	} else if hash != "" {
		c.PasswordHash = []byte(hash)
	} else {
		return c, errors.New("set AUTH_PASSWORD_HASH or AUTH_PASSWORD_HASH_FILE; generate a hash with hash-password")
	}
	c.PasswordHash = []byte(strings.TrimSpace(string(c.PasswordHash)))
	cost, err := bcrypt.Cost(c.PasswordHash)
	if err != nil || cost < 10 || cost > 14 {
		return c, errors.New("password hash must be bcrypt with cost between 10 and 14")
	}
	if err := bcrypt.CompareHashAndPassword(c.PasswordHash, []byte("rdsh configuration validation")); err != nil && !errors.Is(err, bcrypt.ErrMismatchedHashAndPassword) {
		return c, errors.New("malformed bcrypt password hash")
	}
	// Sessions are process-local. A fresh signing key has the same lifetime.
	if os.Getenv("SESSION_SECRET_FILE") == "" {
		c.SessionKey = make([]byte, 32)
		if _, err := rand.Read(c.SessionKey); err != nil {
			return c, errors.New("cannot generate session signing key")
		}
		return c, nil
	}
	// Explicit file support preserves compatibility with existing deployments.
	key, err := readPrivateFile(os.Getenv("SESSION_SECRET_FILE"))
	if err != nil {
		return c, fmt.Errorf("session secret file: %w", err)
	}
	c.SessionKey, err = base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(key)))
	if err != nil || len(c.SessionKey) != 32 {
		return c, errors.New("session secret must encode exactly 32 random bytes as unpadded base64url")
	}
	return c, nil
}

func readPrivateFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open configured file")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 4096 || info.Mode().Perm()&0007 != 0 {
		return nil, errors.New("expected a small regular file without permissions for others")
	}
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return nil, errors.New("cannot read configured file")
	}
	return data, nil
}
