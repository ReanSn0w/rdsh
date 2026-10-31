package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"time"
)

type handoff struct {
	Token      string `json:"token"`
	Generation string `json:"generation"`
}

var opaqueValue = regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`)
var dshCookieName = regexp.MustCompile(`^dsh-auth-[A-Za-z0-9_-]{16,128}$`)

func readHandoff(path string) (handoff, error) {
	var h handoff
	f, err := os.Open(path)
	if err != nil {
		return h, errors.New("DSH handoff unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 2048 {
		return h, errors.New("invalid DSH handoff")
	}
	decoder := json.NewDecoder(io.LimitReader(f, 2049))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&h) != nil || !opaqueValue.MatchString(h.Token) || !opaqueValue.MatchString(h.Generation) {
		return h, errors.New("invalid DSH handoff")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return h, errors.New("invalid DSH handoff")
	}
	return h, nil
}

func (g *gateway) ensureBridge(ctx context.Context, s *session) error {
	s.bridgeMu.Lock()
	defer s.bridgeMu.Unlock()
	for attempt := 0; attempt < 2; attempt++ {
		h, err := readHandoff(g.config.HandoffFile)
		if err != nil {
			return err
		}
		if s.bridgeCookie != nil && s.generation == h.Generation && time.Now().Add(time.Minute).Before(s.bridgeExpires) {
			return nil
		}
		cookie, err := g.exchange(ctx, h.Token)
		if err != nil {
			// Retry only the read-only exchange when a process rotation is observed.
			latest, readErr := readHandoff(g.config.HandoffFile)
			if attempt == 0 && readErr == nil && latest.Generation != h.Generation {
				continue
			}
			return errors.New("DSH authentication unavailable")
		}
		latest, err := readHandoff(g.config.HandoffFile)
		if err != nil {
			return err
		}
		if latest.Generation != h.Generation {
			continue
		}
		s.bridgeCookie = cookie
		s.bridgeExpires = time.Now().Add(time.Duration(cookie.MaxAge) * time.Second)
		s.generation = h.Generation
		return nil
	}
	return errors.New("DSH is restarting")
}

func (g *gateway) exchange(ctx context.Context, token string) (*http.Cookie, error) {
	u := *g.config.Upstream
	u.Path = "/"
	query := u.Query()
	query.Set("token", token)
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("invalid exchange request")
	}
	req.Host = g.config.PublicURL.Host
	req.Header.Set("Origin", g.config.PublicURL.String())
	res, err := g.exchangeClient.Do(req)
	if err != nil {
		return nil, errors.New("DSH exchange failed")
	}
	defer res.Body.Close()
	if (res.StatusCode != 302 && res.StatusCode != 303) || (res.Header.Get("Location") != "./" && res.Header.Get("Location") != "/") {
		return nil, errors.New("unexpected DSH exchange response")
	}
	var found *http.Cookie
	for _, c := range res.Cookies() {
		if !dshCookieName.MatchString(c.Name) {
			continue
		}
		if found != nil || c.Domain != "" || c.Path != "/" || c.MaxAge <= 0 || c.MaxAge > 31*24*60*60 || c.Value == "" || len(c.Value) > 4096 {
			return nil, errors.New("unexpected DSH cookie")
		}
		c.Secure = true
		c.HttpOnly = true
		c.SameSite = http.SameSiteStrictMode
		found = c
	}
	if found == nil {
		return nil, errors.New("DSH did not issue a session")
	}
	return found, nil
}

func (g *gateway) writeBridgeCookie(w http.ResponseWriter, s *session) {
	s.bridgeMu.Lock()
	defer s.bridgeMu.Unlock()
	if s.bridgeCookie == nil {
		return
	}
	c := *s.bridgeCookie
	c.MaxAge = int(time.Until(s.bridgeExpires).Seconds())
	http.SetCookie(w, &c)
}
