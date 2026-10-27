package homebrew

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSessionsSurviveRestartButNotACodeChange(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	m := manager(t)
	start := func(token string) *Server {
		s := NewServer(m, nil, token)
		s.SessionFile = file
		s.LoadSessions()
		return s
	}
	get := func(s *Server, c *http.Cookie) int {
		req := httptest.NewRequest("GET", "http://192.168.1.50:8088/api/state", nil)
		req.AddCookie(c)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w.Code
	}
	s := start("code-1")
	req := httptest.NewRequest("POST", "http://192.168.1.50:8088/api/login", strings.NewReader(`{"token":"code-1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	cookie := w.Result().Cookies()[0]
	if raw, _ := os.ReadFile(file); strings.Contains(string(raw), cookie.Value) || strings.Contains(string(raw), "code-1") {
		t.Fatal("session file holds a usable cookie or the access code")
	}
	if get(start("code-1"), cookie) != 200 {
		t.Fatal("session lost after restart")
	}
	if get(start("code-2"), cookie) != 401 {
		t.Fatal("session survived an access code change")
	}
	s = start("code-1")
	req = httptest.NewRequest("POST", "http://192.168.1.50:8088/api/logout", nil)
	req.AddCookie(cookie)
	s.ServeHTTP(httptest.NewRecorder(), req)
	if get(start("code-1"), cookie) != 401 {
		t.Fatal("logout did not persist")
	}
}

func TestConcurrentDuplicateSubmissionsAreRejected(t *testing.T) {
	store, e := NewStore(t.TempDir(), "")
	if e != nil {
		t.Fatal(e)
	}
	p := mustPackage(t, "1.0.0")
	// Simulate a submission that is still being scanned outside the lock.
	store.submitting[p.SHA256] = true
	if _, e = store.Submit(context.Background(), p.Raw); e == nil {
		t.Fatal("duplicate accepted while the first copy was being scanned")
	}
	delete(store.submitting, p.SHA256)

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := store.Submit(context.Background(), p.Raw)
			results <- e
		}()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for e := range results {
		if e == nil {
			accepted++
		}
	}
	if accepted != 1 || len(store.Queue()) != 1 || len(store.submitting) != 0 {
		t.Fatal("concurrent duplicates", accepted, len(store.Queue()), len(store.submitting))
	}
}
