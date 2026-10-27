package homebrew

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	errButtonPending = errors.New("another action is waiting for the speaker button")
	errButtonTimeout = errors.New("the speaker button was not pressed in time; nothing was changed")
)

// ButtonGate asks for a physical key press on the speaker before code with full
// root access starts, so another device on the network cannot grant itself root.
// Only one action waits at a time, and a press approves only the action that was
// already waiting: an earlier press can never approve a later request.
type ButtonGate struct {
	Timeout time.Duration
	Ring    *Ring
	mu      sync.Mutex
	waiting chan struct{}
	label   string
	ready   bool
	err     string
}

func NewButtonGate(ring *Ring) *ButtonGate {
	return &ButtonGate{Timeout: 30 * time.Second, Ring: ring}
}

func (g *ButtonGate) Press() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiting != nil {
		close(g.waiting)
		g.waiting, g.label = nil, ""
	}
}

func (g *ButtonGate) Status() map[string]any {
	g.mu.Lock()
	defer g.mu.Unlock()
	return map[string]any{"ready": g.ready, "pending": g.label, "error": g.err}
}

func (g *ButtonGate) setDevice(ready bool, e error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ready, g.err = ready, ""
	if e != nil {
		g.err = e.Error()
	}
}

// Wait blocks until the button is pressed, the timeout passes or ctx ends.
func (g *ButtonGate) Wait(ctx context.Context, label string) error {
	g.mu.Lock()
	if g.waiting != nil {
		pending := g.label
		g.mu.Unlock()
		return fmt.Errorf("%w: %s", errButtonPending, pending)
	}
	ch := make(chan struct{})
	g.waiting, g.label = ch, label
	g.mu.Unlock()
	stopBlink := g.blink()
	defer stopBlink()
	timer := time.NewTimer(g.Timeout)
	defer timer.Stop()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		if g.cancel(ch) {
			return ctx.Err()
		}
	case <-timer.C:
		if g.cancel(ch) {
			return errButtonTimeout
		}
	}
	return nil
}

// cancel reports false when a press won the race against the timeout.
func (g *ButtonGate) cancel(ch chan struct{}) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.waiting != ch {
		return false
	}
	g.waiting, g.label = nil, ""
	return true
}

// blink pulses the ring while waiting. A plugin that owns the ring repaints it
// on its next tick, so the hint is only visible when the ring is free.
func (g *ButtonGate) blink() func() {
	if g.Ring == nil {
		return func() {}
	}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		on := make([]byte, 36)
		for i := 0; i < 12; i++ {
			on[i*3+1], on[i*3+2] = 0x18, 0x40 // amber: frames are stored as BGR
		}
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		lit := false
		for {
			select {
			case <-done:
				g.Ring.Publish(make([]byte, 36))
				return
			case <-ticker.C:
				lit = !lit
				if lit {
					g.Ring.Publish(on)
				} else {
					g.Ring.Publish(make([]byte, 36))
				}
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
