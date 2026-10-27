//go:build linux

package homebrew

import (
	"alicehomebrew/sdk/hardware"
	"context"
	"os"
	"time"
)

const (
	evKey       = 1
	evAbs       = 3
	absDistance = 0x19
)

// Listen reads both devices without EVIOCGRAB and reopens them if they vanish.
func (h *Inputs) Listen(ctx context.Context) {
	go h.watch(ctx, "adc_keypad", true)
	h.watch(ctx, "STM VL53L0 proximity sensor", false)
}

func (h *Inputs) watch(ctx context.Context, name string, keypad bool) {
	for ctx.Err() == nil {
		path, e := hardware.FindInput(name)
		if e == nil {
			e = h.read(ctx, path, keypad)
		}
		h.setDevice(keypad, e)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (h *Inputs) read(ctx context.Context, path string, keypad bool) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		f.Close()
	}()
	h.setDevice(keypad, nil)
	for {
		ev, e := hardware.ReadInput(f)
		if e != nil {
			return e
		}
		switch {
		case keypad && ev.Type == evKey:
			h.key(ev.Code, ev.Value)
		case !keypad && ev.Type == evAbs && ev.Code == absDistance:
			h.setProximity(float64(ev.Value))
		}
	}
}
