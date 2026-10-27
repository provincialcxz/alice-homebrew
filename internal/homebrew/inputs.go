package homebrew

import (
	"sync"
	"time"
)

const keyQueueSize = 16

// KeyEvent is one speaker button change: Value 1 is press, 0 release, 2 repeat.
type KeyEvent struct {
	Code   uint16
	Value  uint8
	Millis uint32
}

// Inputs shares the speaker keypad and proximity sensor between the button gate
// and sandboxed plugins. Devices are only read: nothing is grabbed or enabled,
// so Alice keeps receiving presses and gestures exactly as before.
type Inputs struct {
	// Gate, when set, is approved by any key press.
	Gate      *ButtonGate
	mu        sync.Mutex
	boot      time.Time
	next      int
	queues    map[int][]KeyEvent
	proximity float64
	keypad    string
	sensor    string
}

func NewInputs() *Inputs {
	return &Inputs{boot: time.Now(), queues: map[int][]KeyEvent{}, proximity: -1}
}

func (h *Inputs) Subscribe() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.next++
	h.queues[h.next] = nil
	return h.next
}

func (h *Inputs) Unsubscribe(id int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.queues, id)
}

// NextKey pops the oldest unread event for one subscriber.
func (h *Inputs) NextKey(id int) (KeyEvent, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	q := h.queues[id]
	if len(q) == 0 {
		return KeyEvent{}, false
	}
	h.queues[id] = q[1:]
	return q[0], true
}

// Proximity is the last reported distance, or -1 before the first report.
func (h *Inputs) Proximity() float64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.proximity
}

func (h *Inputs) Status() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	return map[string]any{"keypad": h.keypad, "proximity": h.sensor, "distance": h.proximity}
}

func (h *Inputs) key(code uint16, value int32) {
	if value < 0 || value > 2 {
		return
	}
	h.mu.Lock()
	ev := KeyEvent{Code: code, Value: uint8(value), Millis: uint32(time.Since(h.boot).Milliseconds())}
	for id, q := range h.queues {
		if len(q) >= keyQueueSize {
			q = q[1:] // a stalled plugin loses its oldest events, never the newest
		}
		h.queues[id] = append(q, ev)
	}
	gate := h.Gate
	h.mu.Unlock()
	if gate != nil && value == 1 {
		gate.Press()
	}
}

func (h *Inputs) setProximity(v float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.proximity = v
}

// setDevice records "ready" or the last error for the status page.
func (h *Inputs) setDevice(keypad bool, e error) {
	state := "ready"
	if e != nil {
		state = e.Error()
	}
	h.mu.Lock()
	if keypad {
		h.keypad = state
	} else {
		h.sensor = state
	}
	gate := h.Gate
	h.mu.Unlock()
	if keypad && gate != nil {
		gate.setDevice(e == nil, e)
	}
}
