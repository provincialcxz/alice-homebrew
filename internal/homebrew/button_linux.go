//go:build linux

package homebrew

import (
	"alicehomebrew/sdk/hardware"
	"context"
	"os"
	"time"
)

// Listen reads the Mini 1 keypad without EVIOCGRAB, so Alice still receives
// every press. Any key press counts; the device is reopened if it disappears.
func (g *ButtonGate) Listen(ctx context.Context) {
	for ctx.Err() == nil {
		path, e := hardware.FindInput("adc_keypad")
		if e == nil {
			e = g.read(ctx, path)
		}
		g.setDevice(false, e)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func (g *ButtonGate) read(ctx context.Context, path string) error {
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
	g.setDevice(true, nil)
	for {
		ev, e := hardware.ReadInput(f)
		if e != nil {
			return e
		}
		if ev.Type == 1 && ev.Value == 1 { // EV_KEY press; 0 is release, 2 is repeat
			g.Press()
		}
	}
}
