package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/securecookie"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "__Host-rdsh_session"
const csrfCookieName = "__Host-rdsh_login"

type session struct {
	id, csrf      string
	expires       time.Time
	ctx           context.Context
	cancel        context.CancelFunc
	timer         *time.Timer
	bridgeMu      sync.Mutex
	bridgeCookie  *http.Cookie
	bridgeExpires time.Time
	generation    string
}

type loginWindow struct {
	start time.Time
	count int
}

type authState struct {
	mu               sync.Mutex
	sessions         map[string]*session
	attempts         map[string]loginWindow
	global           loginWindow
	codec, csrfCodec *securecookie.SecureCookie
}

func newAuth(key []byte, ttl time.Duration) *authState {
	codec := securecookie.New(key, nil).MaxAge(int(ttl.Seconds())).SetSerializer(securecookie.JSONEncoder{})
	csrfCodec := securecookie.New(key, nil).MaxAge(900).SetSerializer(securecookie.JSONEncoder{})
	return &authState{sessions: make(map[string]*session), attempts: make(map[string]loginWindow), codec: codec, csrfCodec: csrfCodec}
}

func randomID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("random source unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (a *authState) create(ttl time.Duration) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.sessions) >= 256 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{id: randomID(), csrf: randomID(), expires: time.Now().Add(ttl), ctx: ctx, cancel: cancel}
	a.sessions[s.id] = s
	s.timer = time.AfterFunc(ttl, func() { a.remove(s.id) })
	return s
}

func (a *authState) remove(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[id]; s != nil {
		delete(a.sessions, id)
		if s.timer != nil {
			s.timer.Stop()
		}
		s.cancel()
	}
}

func (a *authState) close() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for id, s := range a.sessions {
		s.timer.Stop()
		s.cancel()
		delete(a.sessions, id)
	}
}

func (a *authState) get(r *http.Request) *session {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return nil
	}
	var id string
	if a.codec.Decode(sessionCookieName, c.Value, &id) != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := a.sessions[id]
	if s == nil || !time.Now().Before(s.expires) {
		return nil
	}
	return s
}

func (a *authState) setSession(w http.ResponseWriter, s *session) {
	value, _ := a.codec.Encode(sessionCookieName, s.id)
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode, Expires: s.expires, MaxAge: int(time.Until(s.expires).Seconds())})
}

func (a *authState) loginNonce(w http.ResponseWriter) string {
	nonce := randomID()
	value, _ := a.csrfCodec.Encode(csrfCookieName, nonce)
	http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 900})
	return nonce
}

func (a *authState) validLoginNonce(r *http.Request) bool {
	c, err := r.Cookie(csrfCookieName)
	if err != nil {
		return false
	}
	var expected string
	if a.csrfCodec.Decode(csrfCookieName, c.Value, &expected) != nil {
		return false
	}
	return sameSecret(expected, r.PostForm.Get("csrf"))
}

func sameSecret(a, b string) bool {
	return len(a) != 0 && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func expireCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Expires: time.Unix(1, 0)})
}

func (a *authState) allowLogin(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	if now.Sub(a.global.start) >= time.Minute {
		a.global = loginWindow{start: now}
	}
	if a.global.count >= 30 {
		return false
	}
	for key, value := range a.attempts {
		if now.Sub(value.start) >= time.Minute {
			delete(a.attempts, key)
		}
	}
	entry, exists := a.attempts[ip]
	if !exists {
		if len(a.attempts) >= 1024 {
			return false
		}
		entry = loginWindow{start: now}
	}
	if entry.count >= 5 {
		return false
	}
	entry.count++
	a.attempts[ip] = entry
	a.global.count++
	return true
}

func (c config) trusted(ip net.IP) bool {
	for _, network := range c.TrustedProxies {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (c config) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "unknown"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "unknown"
	}
	if !c.trusted(ip) {
		return ip.String()
	}
	forwarded := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(forwarded) - 1; i >= 0; i-- {
		candidate := net.ParseIP(strings.TrimSpace(forwarded[i]))
		if candidate == nil {
			return ip.String()
		}
		ip = candidate
		if !c.trusted(ip) {
			break
		}
	}
	return ip.String()
}

func (g *gateway) login(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		g.renderLogin(w, http.StatusOK, g.auth.loginNonce(w), "")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !g.auth.validLoginNonce(r) {
		http.Error(w, "invalid login request", 403)
		return
	}
	if !g.auth.allowLogin(g.config.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		g.renderLogin(w, 429, g.auth.loginNonce(w), "Too many attempts. Try again in a minute.")
		return
	}
	password := r.PostForm.Get("password")
	valid := bcrypt.CompareHashAndPassword(g.config.PasswordHash, []byte(password)) == nil
	if !sameSecret(g.config.Username, r.PostForm.Get("username")) || !valid {
		g.renderLogin(w, 401, g.auth.loginNonce(w), "Incorrect username or password.")
		return
	}
	s := g.auth.create(g.config.SessionTTL)
	if s == nil {
		http.Error(w, "session capacity reached; try later", 503)
		return
	}
	if err := g.ensureBridge(r.Context(), s); err != nil {
		g.auth.remove(s.id)
		g.renderLogin(w, 503, g.auth.loginNonce(w), "DSH is starting or unavailable. Please try again shortly.")
		return
	}
	if old := g.auth.get(r); old != nil {
		g.auth.remove(old.id)
	}
	g.auth.setSession(w, s)
	g.writeBridgeCookie(w, s)
	expireCookie(w, csrfCookieName)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (g *gateway) logout(w http.ResponseWriter, r *http.Request) {
	s := g.auth.get(r)
	if s == nil {
		http.Redirect(w, r, "/_rdsh/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodGet {
		g.renderLogout(w, s.csrf)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	if r.ParseForm() != nil || !sameSecret(s.csrf, r.PostForm.Get("csrf")) {
		http.Error(w, "invalid logout request", 403)
		return
	}
	g.auth.remove(s.id)
	expireCookie(w, sessionCookieName)
	s.bridgeMu.Lock()
	if s.bridgeCookie != nil {
		expireCookie(w, s.bridgeCookie.Name)
	}
	s.bridgeMu.Unlock()
	http.Redirect(w, r, "/_rdsh/login", http.StatusSeeOther)
}
