package homebrew

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func waitPending(t *testing.T, g *ButtonGate) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if g.Status()["pending"] != "" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("gate never started waiting")
}

func TestButtonGateApprovesOnlyTheWaitingAction(t *testing.T) {
	g := NewButtonGate(nil)
	g.Timeout = time.Second
	g.Press() // nothing waits: must not approve a later request
	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background(), "enable a") }()
	waitPending(t, g)
	if e := g.Wait(context.Background(), "enable b"); !errors.Is(e, errButtonPending) || !strings.Contains(e.Error(), "enable a") {
		t.Fatal("second action did not report the pending one", e)
	}
	g.Press()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if g.Status()["pending"] != "" {
		t.Fatal("pending label survived the press")
	}
}

func TestButtonGateTimesOutAndCancels(t *testing.T) {
	ring, e := NewRing(false)
	if e != nil {
		t.Fatal(e)
	}
	g := NewButtonGate(ring)
	g.Timeout = 450 * time.Millisecond // long enough for one lit blink frame
	if e := g.Wait(context.Background(), "enable a"); !errors.Is(e, errButtonTimeout) {
		t.Fatal("timeout not reported", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	g.Timeout = time.Second
	if e := g.Wait(ctx, "enable a"); !errors.Is(e, context.Canceled) {
		t.Fatal("cancelled request still waited", e)
	}
	if colors := g.Ring.Snapshot()["colors"].([]string); colors[0] != "#000000" {
		t.Fatal("ring left lit after waiting", colors[0])
	}
}

func TestNativeEnableNeedsSpeakerButton(t *testing.T) {
	p := nativeFixture(t)
	m := manager(t)
	staged, e := m.Stage(p.Raw, false)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Install(staged["ticket"].(string), p.Manifest.Permissions, true); e != nil {
		t.Fatal(e)
	}
	m.Button = NewButtonGate(nil)
	m.Button.Timeout = 30 * time.Millisecond
	s := NewServer(m, nil, "")
	req := httptest.NewRequest("POST", "http://192.168.1.50:8088/api/plugins/native-test/enable", strings.NewReader(`{"enabled":true,"acknowledge_native":true}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	if w.Code != 408 || m.state.Plugins["native-test"].Enabled || len(m.running) != 0 {
		t.Fatal("native plugin enabled without a button press", w.Code, w.Body.String())
	}
	// Disabling and non-native actions never wait for the button.
	if m.startsNative("native-test", "", false) {
		t.Fatal("disabled plugin update treated as a native start")
	}
}
