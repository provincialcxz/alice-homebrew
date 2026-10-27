package sharedaudio

import (
	"encoding/binary"
	"math"
)

// One full fade takes about 150 ms of 44.1 kHz stereo samples.
const duckStep = 1 / (44100 * 2 * 0.15)

// ducker lowers the PCM handed to aplay while Alice listens or answers. It
// never touches Alice's own volume. The zero value passes audio unchanged.
type ducker struct {
	current, target float64 // attenuation: 0 is full volume, 1 is silence
	carry           []byte  // a sample split across writes
}

func (d *ducker) idle() bool { return d.current == 0 && d.target == 0 && len(d.carry) == 0 }

// apply returns the scaled whole samples of carry+b and keeps a trailing
// half sample for the next write.
func (d *ducker) apply(b []byte) []byte {
	data := append(d.carry, b...)
	whole := len(data) &^ 1
	out := make([]byte, whole)
	for i := 0; i < whole; i += 2 {
		if d.current < d.target {
			d.current = math.Min(d.target, d.current+duckStep)
		} else if d.current > d.target {
			d.current = math.Max(d.target, d.current-duckStep)
		}
		s := float64(int16(binary.LittleEndian.Uint16(data[i:])))
		binary.LittleEndian.PutUint16(out[i:], uint16(int16(math.Round(s*(1-d.current)))))
	}
	d.carry = append(d.carry[:0], data[whole:]...)
	return out
}

// SetDuck fades music to the given attenuation (0 restores full volume).
func (p *Playback) SetDuck(attenuation float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.duck.target = math.Max(0, math.Min(1, attenuation))
}
