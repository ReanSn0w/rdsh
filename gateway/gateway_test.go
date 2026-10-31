package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const testToken = "test-token-abcdefghijklmnopqrstuvwxyz012345"
const testGeneration = "test-generation-abcdefghijklmnopqrstuvwxyz"
const testDSHCookie = "dsh-auth-abcdefghijklmnopqrstuvwxyz012345"
const testPassword = "testing-password-only-123"

type testDSH struct {
	mu                sync.Mutex
	token, cookie     string
	exchanges, writes int
	unauthorized      bool
}

func (d *testDSH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Host != "workspace.example" {
		http.Error(w, "host rejected", 403)
		return
	}
	if r.URL.Path == "/" && r.URL.Query().Get("token") != "" {
		if r.URL.Query().Get("token") != d.token {
			http.Error(w, "bad token", 401)
			return
		}
		d.exchanges++
		http.SetCookie(w, &http.Cookie{Name: testDSHCookie, Value: d.cookie, Path: "/", MaxAge: 30 * 24 * 60 * 60, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.Header().Set("Location", "./")
		w.WriteHeader(303)
		return
	}
	cookie, err := r.Cookie(testDSHCookie)
	if err != nil || cookie.Value != d.cookie {
		http.Error(w, "unauthorized", 401)
		return
	}
	if strings.Contains(r.Header.Get("Cookie"), "rdsh") || r.Header.Get("Authorization") != "" || r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" {
		http.Error(w, "credential leaked", 500)
		return
	}
	if r.Method == "POST" {
		d.writes++
	}
	if d.unauthorized {
		http.Error(w, "unauthorized", 401)
		return
	}
	if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprint(buf, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
		d.mu.Unlock()
		io.Copy(io.Discard, conn)
		d.mu.Lock()
		return
	}
	w.Header().Set("Set-Cookie", "attacker=unexpected; Path=/")
	fmt.Fprint(w, "DSH response")
}

func fixture(t *testing.T) (*gateway, *testDSH, *httptest.Server) {
	t.Helper()
	d := &testDSH{token: testToken, cookie: "native-test-cookie"}
	upstream := httptest.NewServer(d)
	t.Cleanup(upstream.Close)
	u, _ := url.Parse(upstream.URL)
	public, _ := url.Parse("https://workspace.example")
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), bcrypt.MinCost)
	file := filepath.Join(t.TempDir(), "connection.json")
	writeHandoff(t, file, testToken, testGeneration)
	g := newGateway(config{PublicURL: public, Upstream: u, Username: "token", HandoffFile: file, PasswordHash: hash, SessionKey: []byte("12345678901234567890123456789012"), SessionTTL: time.Hour, TLSMode: "external"})
	t.Cleanup(g.close)
	return g, d, upstream
}

func writeHandoff(t *testing.T, file, token, generation string) {
	t.Helper()
	b, _ := json.Marshal(handoff{Token: token, Generation: generation})
	if err := os.WriteFile(file, b, 0600); err != nil {
		t.Fatal(err)
	}
}

func request(g *gateway, method, target string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, "https://workspace.example"+target, body)
	r.RemoteAddr = "192.0.2.1:1234"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://workspace.example")
	}
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	return w
}

var csrfPattern = regexp.MustCompile(`name="csrf" value="([^"]+)"`)

func login(t *testing.T, g *gateway) []*http.Cookie {
	t.Helper()
	page := request(g, "GET", "/_rdsh/login", nil, nil)
	if page.Header().Get("Referrer-Policy") != "same-origin" {
		t.Fatal("login policy would suppress Safari form Origin")
	}
	csrf := csrfPattern.FindStringSubmatch(page.Body.String())
	if len(csrf) != 2 {
		t.Fatal("missing csrf field")
	}
	res := request(g, "POST", "/_rdsh/login", url.Values{"username": {"token"}, "password": {testPassword}, "csrf": {csrf[1]}}, page.Result().Cookies())
	if res.Code != 303 {
		t.Fatalf("login status %d", res.Code)
	}
	for _, c := range res.Result().Cookies() {
		if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Domain != "" || c.Path != "/" {
			t.Fatal("insecure cookie attributes")
		}
	}
	return res.Result().Cookies()
}

func TestLoginProxyLogoutAndBoundaries(t *testing.T) {
	g, _, _ := fixture(t)
	for _, endpoint := range []string{"/api/test", "/plugin.js", "/api/remote.mux", "/connect"} {
		if res := request(g, "GET", endpoint, nil, []*http.Cookie{{Name: testDSHCookie, Value: "native-test-cookie"}}); res.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", endpoint, res.Code)
		}
	}
	cookies := login(t, g)
	for _, endpoint := range []string{"/", "/api/test", "/plugins/example/"} {
		res := request(g, "GET", endpoint, nil, cookies)
		if res.Code != 200 {
			t.Fatalf("authenticated %s: %d", endpoint, res.Code)
		}
		for _, c := range res.Result().Cookies() {
			if c.Name == "attacker" {
				t.Fatal("unexpected upstream cookie leaked")
			}
		}
	}
	page := request(g, "GET", "/_rdsh/logout", nil, cookies)
	csrf := csrfPattern.FindStringSubmatch(page.Body.String())[1]
	res := request(g, "POST", "/_rdsh/logout", url.Values{"csrf": {csrf}}, cookies)
	if res.Code != 303 {
		t.Fatalf("logout %d", res.Code)
	}
	if request(g, "GET", "/api/test", nil, cookies).Code != 401 {
		t.Fatal("revoked session accepted")
	}
}

func TestCSRFHostOriginAndPathRejection(t *testing.T) {
	g, _, _ := fixture(t)
	if request(g, "POST", "/_rdsh/login", url.Values{"username": {"token"}, "password": {testPassword}}, nil).Code != 403 {
		t.Fatal("login csrf bypass")
	}
	cookies := login(t, g)
	if request(g, "POST", "/_rdsh/logout", url.Values{"csrf": {"forged"}}, cookies).Code != 403 {
		t.Fatal("logout csrf bypass")
	}
	for _, target := range []string{"//_rdsh/login", "/x/../_rdsh/login", "/x/%2e%2e/_rdsh/login", "/?token=" + testToken} {
		res := request(g, "GET", target, nil, cookies)
		if res.Code != 400 && res.Code != 403 {
			t.Fatalf("unsafe path %s accepted: %d", target, res.Code)
		}
	}
	for _, mode := range []string{"host", "origin", "null-origin", "fetch-site"} {
		r := httptest.NewRequest("GET", "https://workspace.example/", nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		switch mode {
		case "host":
			r.Host = "evil.example"
		case "origin":
			r.Header.Set("Origin", "https://evil.example")
		case "null-origin":
			r.Header.Set("Origin", "null")
		case "fetch-site":
			r.Header.Set("Sec-Fetch-Site", "cross-site")
		}
		w := httptest.NewRecorder()
		g.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("%s bypass", mode)
		}
	}
	for _, c := range cookies {
		if c.Name == sessionCookieName {
			c.Value += "forged"
		}
	}
	if request(g, "GET", "/api/test", nil, cookies).Code != 401 {
		t.Fatal("forged session accepted")
	}
}

func TestRotateTokenAndNeverReplayPost(t *testing.T) {
	g, d, _ := fixture(t)
	cookies := login(t, g)
	d.mu.Lock()
	d.token = "new-token-abcdefghijklmnopqrstuvwxyz012345"
	d.cookie = "new-native-cookie"
	d.mu.Unlock()
	writeHandoff(t, g.config.HandoffFile, "new-token-abcdefghijklmnopqrstuvwxyz012345", "new-generation-abcdefghijklmnopqrstuvwxyz")
	if request(g, "GET", "/api/test", nil, cookies).Code != 200 {
		t.Fatal("rotation failed")
	}
	d.mu.Lock()
	d.unauthorized = true
	d.mu.Unlock()
	res := request(g, "POST", "/api/change", url.Values{"value": {"one"}}, cookies)
	if res.Code != 401 {
		t.Fatalf("expected upstream unauthorized: %d", res.Code)
	}
	d.mu.Lock()
	writes := d.writes
	exchanges := d.exchanges
	d.mu.Unlock()
	if writes != 1 || exchanges != 2 {
		t.Fatalf("request replay or extra exchange: writes=%d exchanges=%d", writes, exchanges)
	}
	d.mu.Lock()
	d.unauthorized = false
	d.mu.Unlock()
	if request(g, "GET", "/api/test", nil, cookies).Code != 200 {
		t.Fatal("reauthentication on next request failed")
	}
}

func TestMissingOrMalformedHandoffFailsClosed(t *testing.T) {
	g, _, _ := fixture(t)
	cookies := login(t, g)
	for _, data := range []string{`{"token":"short","generation":"short"}`, `{"token":"` + testToken + `","generation":"` + testGeneration + `","upstream":"http://evil"}`, strings.Repeat("a", 3000)} {
		if err := os.WriteFile(g.config.HandoffFile, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if request(g, "GET", "/api/test", nil, cookies).Code != 503 {
			t.Fatal("malformed handoff admitted")
		}
	}
	if err := os.Remove(g.config.HandoffFile); err != nil {
		t.Fatal(err)
	}
	if request(g, "GET", "/api/test", nil, cookies).Code != 503 {
		t.Fatal("missing handoff admitted")
	}
}

func TestHandoffRejectsLinksAndFIFOWithoutReading(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.json")
	writeHandoff(t, target, testToken, testGeneration)
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readHandoff(link); err == nil {
		t.Fatal("handoff symlink followed")
	}
	fifo := filepath.Join(dir, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := readHandoff(fifo); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("FIFO accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("handoff read blocked on FIFO")
	}
}

func TestRateLimitAndTrustedProxy(t *testing.T) {
	g, _, _ := fixture(t)
	for i := 0; i < 6; i++ {
		page := request(g, "GET", "/_rdsh/login", nil, nil)
		csrf := csrfPattern.FindStringSubmatch(page.Body.String())[1]
		res := request(g, "POST", "/_rdsh/login", url.Values{"username": {"token"}, "password": {"wrong"}, "csrf": {csrf}}, page.Result().Cookies())
		want := 401
		if i == 5 {
			want = 429
		}
		if res.Code != want {
			t.Fatalf("attempt %d status %d", i, res.Code)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.1:1000"
	r.Header.Set("X-Forwarded-For", "203.0.113.4")
	if g.config.clientIP(r) != "192.0.2.1" {
		t.Fatal("untrusted header accepted")
	}
	_, network, _ := net.ParseCIDR("192.0.2.0/24")
	g.config.TrustedProxies = []*net.IPNet{network}
	r.Header.Set("X-Forwarded-For", "198.51.100.123, 203.0.113.4, 192.0.2.5")
	if g.config.clientIP(r) != "203.0.113.4" {
		t.Fatal("incorrect trust chain")
	}
}

func TestWebSocketClosesOnLogoutAndExpiry(t *testing.T) {
	for _, mode := range []string{"logout", "expiry"} {
		t.Run(mode, func(t *testing.T) {
			g, _, _ := fixture(t)
			if mode == "expiry" {
				g.config.SessionTTL = 250 * time.Millisecond
			}
			cookies := login(t, g)
			server := httptest.NewServer(g)
			defer server.Close()
			u, _ := url.Parse(server.URL)
			conn, err := net.Dial("tcp", u.Host)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			var cookieParts []string
			for _, c := range cookies {
				cookieParts = append(cookieParts, c.Name+"="+c.Value)
			}
			fmt.Fprintf(conn, "GET /api/remote.mux HTTP/1.1\r\nHost: workspace.example\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nCookie: %s\r\n\r\n", strings.Join(cookieParts, "; "))
			reader := bufio.NewReader(conn)
			res, err := http.ReadResponse(reader, nil)
			if err != nil {
				t.Fatal(err)
			}
			if res.StatusCode != 101 {
				t.Fatalf("upgrade %d", res.StatusCode)
			}
			if mode == "logout" {
				page := request(g, "GET", "/_rdsh/logout", nil, cookies)
				csrf := csrfPattern.FindStringSubmatch(page.Body.String())[1]
				if request(g, "POST", "/_rdsh/logout", url.Values{"csrf": {csrf}}, cookies).Code != 303 {
					t.Fatal("logout failed")
				}
			}
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, err = reader.ReadByte()
			if err == nil {
				t.Fatal("websocket still readable")
			}
			if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("websocket did not close")
			}
		})
	}
}

func TestExternalModeStartsWithoutACMEState(t *testing.T) {
	g, _, _ := fixture(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	g.config.HTTPAddr = addr
	g.config.StateDir = "/this-path-must-not-be-used"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, g.config, g) }()
	client := &http.Client{Timeout: time.Second}
	for deadline := time.Now().Add(3 * time.Second); ; {
		res, e := client.Get("http://" + addr + "/_rdsh/health")
		if e == nil {
			res.Body.Close()
			if res.StatusCode != 200 {
				t.Fatal(res.StatusCode)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestConfigurationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPassword), 10)
	hashPath := filepath.Join(dir, "hash")
	keyPath := filepath.Join(dir, "key")
	os.WriteFile(hashPath, hash, 0600)
	os.WriteFile(keyPath, []byte(base64.RawURLEncoding.EncodeToString(make([]byte, 32))), 0600)
	t.Setenv("PUBLIC_URL", "https://workspace.example")
	t.Setenv("AUTH_PASSWORD_HASH_FILE", hashPath)
	t.Setenv("SESSION_SECRET_FILE", keyPath)
	if _, err := loadConfig(); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"http://workspace.example", "https://user:pass@workspace.example", "https://workspace.example/prefix", "https://workspace.example?token=no"} {
		t.Setenv("PUBLIC_URL", v)
		if _, err := loadConfig(); err == nil {
			t.Fatal("unsafe public URL accepted")
		}
	}
	t.Setenv("PUBLIC_URL", "https://workspace.example")
	os.WriteFile(hashPath, []byte("$2a$12$broken"), 0600)
	if _, err := loadConfig(); err == nil {
		t.Fatal("malformed hash accepted")
	}
	os.WriteFile(hashPath, hash, 0600)
	os.Chmod(keyPath, 0644)
	if _, err := loadConfig(); err == nil {
		t.Fatal("world readable key accepted")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private upstream URL ?token=" + testToken)
}

func TestTransportFailuresNeverExposeCredentials(t *testing.T) {
	g, _, _ := fixture(t)
	cookies := login(t, g)
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	g.proxy.Transport = failingTransport{}
	res := request(g, "GET", "/api/test", nil, cookies)
	if res.Code != 502 {
		t.Fatal(res.Code)
	}
	g.exchangeClient.Transport = failingTransport{}
	writeHandoff(t, g.config.HandoffFile, testToken, "another-generation-abcdefghijklmnopqrstuvwxyz")
	failedExchange := request(g, "GET", "/api/test", nil, cookies)
	if failedExchange.Code != 503 {
		t.Fatal(failedExchange.Code)
	}
	all := output.String() + res.Body.String() + failedExchange.Body.String()
	for _, secret := range []string{testToken, testPassword, "native-test-cookie"} {
		if strings.Contains(all, secret) {
			t.Fatal("credential appeared in response or logs")
		}
	}
}

func TestStreamingIsNotBuffered(t *testing.T) {
	g, _, _ := fixture(t)
	cookies := login(t, g)
	release := make(chan struct{})
	stream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "data: last\n\n")
	}))
	defer stream.Close()
	defer close(release)
	u, _ := url.Parse(stream.URL)
	g.config.Upstream = u
	server := httptest.NewServer(g)
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/events", nil)
	req.Host = "workspace.example"
	for _, cookie := range cookies {
		req.AddCookie(cookie)
	}
	client := &http.Client{Timeout: time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line, err := bufio.NewReader(res.Body).ReadString('\n')
	if err != nil || line != "data: first\n" {
		t.Fatalf("first streaming chunk not received: %v", err)
	}
}
