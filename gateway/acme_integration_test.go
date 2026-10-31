package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
)

// This opt-in integration uses an isolated test CA, never Let's Encrypt production.
// Pebble reaches the test listeners through Docker's host gateway for real domain validation.
func TestACMEIssuanceCacheAndRenewal(t *testing.T) {
	if os.Getenv("RDSH_ACME_TEST") != "1" {
		t.Skip("set RDSH_ACME_TEST=1 with Docker available")
	}
	dir := t.TempDir()
	httpListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer httpListener.Close()
	tlsListener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tlsListener.Close()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "rdsh test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"cert.pem": certPEM, "key.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	pebbleConfig := map[string]any{"pebble": map[string]any{"listenAddress": "0.0.0.0:14000", "managementListenAddress": "", "certificate": "/test/cert.pem", "privateKey": "/test/key.pem", "httpPort": httpListener.Addr().(*net.TCPAddr).Port, "tlsPort": tlsListener.Addr().(*net.TCPAddr).Port, "keyAlgorithm": "ecdsa", "profiles": map[string]any{"default": map[string]any{"description": "rdsh short-lived test", "validityPeriod": 20}}, "retryAfter": map[string]any{"authz": 0, "order": 0}}}
	data, _ := json.Marshal(pebbleConfig)
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	name := "rdsh-acme-" + strings.ToLower(randomID()[:10])
	image := env("RDSH_PEBBLE_IMAGE", "ghcr.io/letsencrypt/pebble@sha256:ddf230642b1a584f519f32e347de1b05a6e4c1f6c35c1863b33effeab5f78199")
	cmd := exec.Command("docker", "run", "--rm", "-d", "--name", name, "--user", "0:0", "--add-host", "workspace.example:host-gateway", "-p", "127.0.0.1::14000", "-v", dir+":/test:ro", "-e", "PEBBLE_VA_NOSLEEP=1", "-e", "PEBBLE_WFE_NONCEREJECT=0", "-e", "PEBBLE_AUTHZREUSE=0", image, "-config", "/test/config.json", "-strict=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start Pebble: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	portOutput, err := exec.Command("docker", "port", name, "14000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://" + strings.TrimSpace(string(portOutput)) + "/dir"
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
	for deadline := time.Now().Add(10 * time.Second); ; {
		res, e := client.Get(endpoint)
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("Pebble not ready")
		}
		time.Sleep(100 * time.Millisecond)
	}
	public, _ := url.Parse("https://workspace.example")
	c := config{PublicURL: public, StateDir: t.TempDir(), ACMEDirectory: endpoint}
	manager, err := newACMEManager(c)
	if err != nil {
		t.Fatal(err)
	}
	manager.Client.HTTPClient = client
	var challengeCount atomic.Int32
	tlsConfig := manager.TLSConfig()
	tlsConfig.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		for _, protocol := range hello.SupportedProtos {
			if protocol == acme.ALPNProto {
				challengeCount.Add(1)
			}
		}
		return nil, nil
	}
	tlsServer := &http.Server{Handler: http.NotFoundHandler(), TLSConfig: tlsConfig}
	go tlsServer.ServeTLS(tlsListener, "", "")
	defer tlsServer.Close()
	httpServer := &http.Server{Handler: manager.HTTPHandler(nil)}
	go httpServer.Serve(httpListener)
	defer httpServer.Close()
	hello := &tls.ClientHelloInfo{ServerName: "workspace.example", CipherSuites: []uint16{tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256}, SupportedCurves: []tls.CurveID{tls.CurveP256}, SignatureSchemes: []tls.SignatureScheme{tls.ECDSAWithP256AndSHA256}, SupportedVersions: []uint16{tls.VersionTLS12, tls.VersionTLS13}}
	first, err := manager.GetCertificate(hello)
	if err != nil {
		t.Fatalf("issue certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(first.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if leaf.VerifyHostname("workspace.example") != nil {
		t.Fatal("wrong certificate name")
	}
	if challengeCount.Load() == 0 {
		t.Fatal("no real TLS-ALPN challenge observed")
	}
	if _, err := manager.GetCertificate(&tls.ClientHelloInfo{ServerName: "other.example"}); err == nil {
		t.Fatal("unlisted host received certificate")
	}
	// A second manager with network disabled must load the existing certificate from disk.
	cached, err := newACMEManager(c)
	if err != nil {
		t.Fatal(err)
	}
	cached.Client = &acme.Client{DirectoryURL: endpoint, HTTPClient: &http.Client{Transport: rejectNetwork{}}}
	fromCache, err := cached.GetCertificate(hello)
	if err != nil {
		t.Fatalf("cache reload: %v", err)
	}
	cachedLeaf, _ := x509.ParseCertificate(fromCache.Certificate[0])
	if cachedLeaf.SerialNumber.Cmp(leaf.SerialNumber) != 0 {
		t.Fatal("cache missed")
	}
	for deadline := time.Now().Add(35 * time.Second); ; {
		current, e := manager.GetCertificate(hello)
		if e != nil {
			t.Fatalf("renewal lookup: %v", e)
		}
		currentLeaf, _ := x509.ParseCertificate(current.Certificate[0])
		if currentLeaf.SerialNumber.Cmp(leaf.SerialNumber) != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("automatic renewal did not replace certificate")
		}
		time.Sleep(250 * time.Millisecond)
	}
	// Real automatic TLS listener and HTTP redirect use the same manager settings.
	response := httptest.NewRecorder()
	manager.HTTPHandler(nil).ServeHTTP(response, httptest.NewRequest("GET", "http://workspace.example/example", nil))
	if response.Code != 302 || !strings.HasPrefix(response.Header().Get("Location"), "https://workspace.example/") {
		t.Fatal("HTTP redirect missing")
	}
}

type rejectNetwork struct{}

func (rejectNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network disabled for cache test")
}
