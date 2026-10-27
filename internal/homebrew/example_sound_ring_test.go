package homebrew

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Runs the committed examples/sound-ring build against simulated speaker data.
func TestSoundRingExample(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "sound-ring")
	module, e := os.ReadFile(filepath.Join(dir, "plugin.wasm"))
	if e != nil {
		t.Skip("build examples/sound-ring first:", e)
	}
	raw, e := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if e != nil {
		t.Fatal(e)
	}
	var m Manifest
	if e = json.Unmarshal(raw, &m); e != nil {
		t.Fatal(e)
	}
	p, e := Parse(packageBytes(t, m, module, ""))
	if e != nil {
		t.Fatal(e)
	}
	config, e := Config(m, nil)
	if e != nil {
		t.Fatal(e)
	}
	inputs, audio := NewInputs(), &AudioMeter{}
	r, e := NewRunner(p, config, RunnerEnv{Inputs: inputs, Audio: audio})
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	play := func(bands func(i int) float64) {
		audio.mu.Lock()
		audio.levels = AudioLevels{RMS: 0.4, Peak: 0.8, Updated: time.Now()}
		for i := range audio.levels.Bands {
			audio.levels.Bands[i] = bands(i)
		}
		audio.mu.Unlock()
	}
	tick := func(n int) []byte {
		var frame []byte
		for i := 0; i < n; i++ {
			if frame, e = r.Tick(config, uint32(i*50)); e != nil {
				t.Fatal(e)
			}
		}
		if len(frame) != 36 {
			t.Fatal("no ring frame", len(frame))
		}
		return frame
	}
	sum := func(f []byte) (s int) {
		for _, b := range f {
			s += int(b)
		}
		return
	}
	led := func(f []byte, i int) int { return int(f[i*3]) + int(f[i*3+1]) + int(f[i*3+2]) }

	if s := sum(tick(5)); s != 0 {
		t.Fatal("ring lit while nothing plays", s)
	}
	play(func(i int) float64 { return float64(i) / 11 }) // spectrum rises around the ring
	spectrum := tick(20)
	if led(spectrum, 0) != 0 || led(spectrum, 11) <= led(spectrum, 6) || led(spectrum, 6) == 0 {
		t.Fatal("spectrum does not follow bands", spectrum)
	}
	inputs.Subscribe() // another subscriber must not steal the plugin's events
	inputs.key(115, 1)
	inputs.key(115, 0)
	pulse := tick(20)
	// Pulse lights every LED alike; the spectrum's slow fall leaves a few units.
	if d := led(pulse, 0) - led(pulse, 11); led(pulse, 0) == 0 || d < -6 || d > 6 {
		t.Fatal("button press did not switch to pulse", pulse)
	}
	inputs.setProximity(30) // below the default threshold of 100
	near := tick(20)
	if sum(near)*3 > sum(pulse) {
		t.Fatal("hand near the sensor did not dim the ring", sum(near), sum(pulse))
	}
	inputs.setProximity(500)
	if sum(tick(30)) <= sum(near) {
		t.Fatal("ring did not recover when the hand moved away")
	}
	audio.mu.Lock()
	audio.levels.Updated = time.Now().Add(-time.Second)
	audio.mu.Unlock()
	if s := sum(tick(60)); s != 0 {
		t.Fatal("ring did not fade after music stopped", s)
	}
}
