package homebrew

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostAllowlistBlocksDNSRebinding(t *testing.T) {
	s := NewServer(manager(t), nil, "")
	for _, host := range []string{"evil.com:8088", "evil.com", "speaker.lan:8088", ""} {
		for _, method := range []string{"GET", "POST"} {
			req := httptest.NewRequest(method, "/api/state", strings.NewReader(`{}`))
			req.Host = host
			req.Header.Set("Content-Type", "application/json")
			// A rebinding page is same-origin with its own domain.
			req.Header.Set("Origin", "http://"+host)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != 403 {
				t.Fatalf("%s %q accepted with %d", method, host, w.Code)
			}
		}
	}
	for _, host := range []string{"192.168.1.50:8088", "127.0.0.1:8088", "[::1]:8088", "[fe80::1%25eth0]:8088", "localhost:8088", "Station.local:8088", "station.local.", "10.0.0.2"} {
		req := httptest.NewRequest("GET", "/api/state", nil)
		req.Host = host
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("%q rejected with %d: %s", host, w.Code, w.Body.String())
		}
	}
	s.AllowedHosts = []string{"Speaker.LAN"}
	req := httptest.NewRequest("GET", "/api/state", nil)
	req.Host = "speaker.lan:8088"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("configured host rejected", w.Code)
	}
}

func TestSessionCookieIsNotTheAccessCode(t *testing.T) {
	s := NewServer(manager(t), nil, "correct-token")
	login := func() *http.Cookie {
		req := httptest.NewRequest("POST", "http://192.168.1.50:8088/api/login", strings.NewReader(`{"token":"correct-token"}`))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		return w.Result().Cookies()[0]
	}
	get := func(c *http.Cookie) int {
		req := httptest.NewRequest("GET", "http://192.168.1.50:8088/api/state", nil)
		req.AddCookie(c)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w.Code
	}
	c := login()
	if c.Value == "correct-token" || strings.Contains(c.Value, "correct-token") {
		t.Fatal("access code stored in cookie")
	}
	if get(c) != 200 {
		t.Fatal("session rejected")
	}
	if get(&http.Cookie{Name: c.Name, Value: "correct-token"}) != 401 {
		t.Fatal("raw access code accepted as a session cookie")
	}
	req := httptest.NewRequest("POST", "http://192.168.1.50:8088/api/logout", nil)
	req.AddCookie(c)
	s.ServeHTTP(httptest.NewRecorder(), req)
	if get(c) != 401 {
		t.Fatal("session survived logout")
	}
	req = httptest.NewRequest("GET", "http://192.168.1.50:8088/api/state", nil)
	req.Header.Set("Authorization", "Bearer correct-token")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal("bearer access code rejected")
	}
}

func TestLoginLimiterDoesNotResetWhenFull(t *testing.T) {
	s := NewServer(manager(t), nil, "correct-token")
	attempt := func(remote string) int {
		req := httptest.NewRequest("POST", "http://192.168.1.50:8088/api/login", strings.NewReader(`{"token":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w.Code
	}
	for i := 0; i < 8; i++ {
		attempt("192.168.1.20:5000")
	}
	if attempt("192.168.1.20:5000") != 429 {
		t.Fatal("limit not applied")
	}
	// Addresses inside one IPv6 /64 share a counter.
	for i := 0; i < 8; i++ {
		attempt(fmt.Sprintf("[2001:db8::%x]:5000", i+1))
	}
	if attempt("[2001:db8::ff]:5000") != 429 {
		t.Fatal("IPv6 /64 rotation escaped the limit")
	}
	for i := 0; len(s.attempts) < maxLoginHosts; i++ {
		s.attempts[strings.Repeat("x", i+1)] = loginAttempt{Count: 1, Until: s.attempts["192.168.1.20"].Until}
	}
	if attempt("10.9.9.9:5000") != 429 {
		t.Fatal("full limiter accepted a new client")
	}
	if attempt("192.168.1.20:5000") != 429 {
		t.Fatal("full limiter forgot an active counter")
	}
}

func TestUploadsAreSerialized(t *testing.T) {
	s := NewServer(manager(t), nil, "")
	release, e := s.uploadSlot()
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("POST", "http://192.168.1.50:8088/api/inspect", strings.NewReader("x"))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatal("parallel upload accepted", w.Code, w.Body.String())
	}
	release()
	w = httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("POST", "http://192.168.1.50:8088/api/inspect", strings.NewReader("x")))
	if w.Code == 503 {
		t.Fatal("slot not released")
	}
	if len(s.uploads) != 0 {
		t.Fatal("slot leaked after request")
	}
}

func TestAccessCodeFileIsTrimmed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-token.txt")
	if e := os.WriteFile(path, []byte("  code-123\r\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if v, e := AccessToken(path); e != nil || v != "code-123" {
		t.Fatal(v, e)
	}
	if e := os.WriteFile(path, []byte("\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := AccessToken(path); e == nil {
		t.Fatal("empty access code accepted")
	}
}
