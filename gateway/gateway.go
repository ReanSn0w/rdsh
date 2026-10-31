package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"time"
)

type gateway struct {
	config         config
	auth           *authState
	transport      *http.Transport
	exchangeClient *http.Client
	proxy          *httputil.ReverseProxy
}

type sessionContextKey struct{}

func newGateway(c config) *gateway {
	g := &gateway{config: c, auth: newAuth(c.SessionKey, c.SessionTTL)}
	g.transport = &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, ForceAttemptHTTP2: false, MaxIdleConns: 64, MaxIdleConnsPerHost: 32, MaxConnsPerHost: 128, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 30 * time.Second, ExpectContinueTimeout: time.Second, MaxResponseHeaderBytes: 128 << 10}
	g.exchangeClient = &http.Client{Transport: g.transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	g.proxy = &httputil.ReverseProxy{
		Transport: g.transport, FlushInterval: -1, ErrorLog: log.New(io.Discard, "", 0),
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(g.config.Upstream)
			p.Out.Host = g.config.PublicURL.Host
			p.Out.Header.Del("Forwarded")
			p.Out.Header.Del("X-Real-IP")
			p.Out.Header.Del("Authorization")
			p.Out.Header.Del("Proxy-Authorization")
			p.Out.Header.Del("Cookie")
			// A browser never chooses the upstream credential, address, or identity.
			if s, ok := p.In.Context().Value(sessionContextKey{}).(*session); ok {
				s.bridgeMu.Lock()
				if s.bridgeCookie != nil {
					p.Out.AddCookie(&http.Cookie{Name: s.bridgeCookie.Name, Value: s.bridgeCookie.Value})
				}
				s.bridgeMu.Unlock()
			}
		},
		ModifyResponse: func(r *http.Response) error {
			// Only the explicit exchange endpoint can set browser auth cookies.
			r.Header.Del("Set-Cookie")
			if r.StatusCode == http.StatusUnauthorized {
				if s, ok := r.Request.Context().Value(sessionContextKey{}).(*session); ok {
					s.bridgeMu.Lock()
					s.bridgeExpires = time.Time{}
					s.bridgeMu.Unlock()
				}
				r.Header.Set("X-RDSH-Reauthenticate", "reload")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "DSH is unavailable. Please try again shortly.", http.StatusBadGateway)
		},
	}
	return g
}

func (g *gateway) close() { g.auth.close(); g.transport.CloseIdleConnections() }

func authority(host string) string {
	u, err := url.Parse("https://" + host)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	return net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

func (g *gateway) validOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || authority(u.Host) != authority(g.config.PublicURL.Host) || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return false
		}
	}
	return true
}

func securityHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	securityHeaders(w)
	if r.URL.Path == "/_rdsh/health" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok\n")
		return
	}
	if authority(r.Host) != authority(g.config.PublicURL.Host) || !g.validOrigin(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	cleanPath := path.Clean(r.URL.Path)
	if (cleanPath != r.URL.Path && cleanPath+"/" != r.URL.Path) || strings.Contains(r.URL.Path, "\\") {
		http.Error(w, "invalid path", 400)
		return
	}
	if _, ok := r.URL.Query()["token"]; ok {
		http.Error(w, "use the sign-in page", 403)
		return
	}
	switch r.URL.Path {
	case "/_rdsh/login":
		g.login(w, r)
		return
	case "/_rdsh/logout":
		g.logout(w, r)
		return
	case "/_rdsh/style.css":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", 405)
			return
		}
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		io.WriteString(w, stylesheet)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/_rdsh") {
		http.NotFound(w, r)
		return
	}
	s := g.auth.get(r)
	if s == nil {
		if r.Method == http.MethodGet && (r.URL.Path == "/" || r.URL.Path == "/index.html") {
			http.Redirect(w, r, "/_rdsh/login", http.StatusSeeOther)
		} else {
			http.Error(w, "authentication required", 401)
		}
		return
	}
	ctx, cancel := context.WithCancel(context.WithValue(r.Context(), sessionContextKey{}, s))
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	if err := g.ensureBridge(ctx, s); err != nil {
		http.Error(w, "DSH is starting or unavailable. Please try again shortly.", 503)
		return
	}
	g.writeBridgeCookie(w, s)
	g.proxy.ServeHTTP(w, r.WithContext(ctx))
}
