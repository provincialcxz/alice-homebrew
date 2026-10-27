package homebrew

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// hostCallWasm imports alice.<name> and, on every tick, stores its result at
// address 0. Functions taking a pointer receive address 16.
func hostCallWasm(name string, pointer bool) []byte {
	b := []byte{0, 97, 115, 109, 1, 0, 0, 0}
	imported := []byte{0x60, 1, 0x7f, 1, 0x7f} // (i32) -> i32
	store := []byte{0x36, 2, 0}                // i32.store
	call := []byte{0x41, 0, 0x41, 16, 0x10, 0}
	if !pointer {
		imported = []byte{0x60, 0, 1, 0x7c} // () -> f64
		store = []byte{0x39, 3, 0}          // f64.store
		call = []byte{0x41, 0, 0x10, 0}
	}
	types := append([]byte{2}, imported...)
	types = append(types, 0x60, 1, 0x7f, 0)
	b = append(b, section(1, types)...)
	imports := []byte{1}
	imports = append(imports, wasmString("alice")...)
	imports = append(imports, wasmString(name)...)
	imports = append(imports, 0, 0)
	b = append(b, section(2, imports)...)
	b = append(b, section(3, []byte{1, 1})...)
	b = append(b, section(5, []byte{1, 1, 1, 32})...)
	exports := []byte{2}
	exports = append(exports, wasmString("memory")...)
	exports = append(exports, 2, 0)
	exports = append(exports, wasmString("alice_tick")...)
	exports = append(exports, 0, 1)
	b = append(b, section(7, exports)...)
	body := append(append([]byte{0}, call...), store...)
	body = append(body, 0x0b)
	code := append([]byte{1}, leb(uint32(len(body)))...)
	code = append(code, body...)
	return append(b, section(10, code)...)
}

func api3Runner(t *testing.T, name string, pointer bool, permissions []string, env RunnerEnv) *Runner {
	t.Helper()
	m := manifest("1.0.0")
	m.API = 3
	m.Permissions = permissions
	p, e := Parse(packageBytes(t, m, hostCallWasm(name, pointer), ""))
	if e != nil {
		t.Fatal(e)
	}
	r, e := NewRunner(p, map[string]any{}, env)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(r.Close)
	return r
}

func memory(t *testing.T, r *Runner, offset, n uint32) []byte {
	t.Helper()
	b, ok := r.module.Memory().Read(offset, n)
	if !ok {
		t.Fatal("memory read")
	}
	return b
}

func TestAPI3PermissionsAreRequired(t *testing.T) {
	env := RunnerEnv{Inputs: NewInputs(), Audio: &AudioMeter{}}
	for _, c := range []struct {
		name, permission string
		pointer          bool
	}{{"button_event", "input.buttons", true}, {"proximity", "sensor.proximity", false}, {"audio_levels", "audio.levels", true}} {
		r := api3Runner(t, c.name, c.pointer, []string{}, env)
		if _, e := r.Tick(nil, 0); e == nil || !strings.Contains(e.Error(), "permission denied: "+c.permission) {
			t.Fatalf("%s ran without %s: %v", c.name, c.permission, e)
		}
	}
}

func TestButtonEventsAreQueuedPerPlugin(t *testing.T) {
	inputs := NewInputs()
	r := api3Runner(t, "button_event", true, []string{"input.buttons"}, RunnerEnv{Inputs: inputs})
	if _, e := r.Tick(nil, 0); e != nil {
		t.Fatal(e)
	}
	if binary.LittleEndian.Uint32(memory(t, r, 0, 4)) != 0 {
		t.Fatal("event reported before any press")
	}
	other := inputs.Subscribe()
	for code := uint16(0); code < 20; code++ {
		inputs.key(code, 1)
	}
	for i := 0; i < keyQueueSize; i++ {
		if _, e := r.Tick(nil, 0); e != nil {
			t.Fatal(e)
		}
		ev := memory(t, r, 16, 8)
		if binary.LittleEndian.Uint32(memory(t, r, 0, 4)) != 1 || binary.LittleEndian.Uint16(ev) != uint16(4+i) || ev[2] != 1 {
			t.Fatalf("event %d: %v", i, ev)
		}
	}
	r.Tick(nil, 0)
	if binary.LittleEndian.Uint32(memory(t, r, 0, 4)) != 0 {
		t.Fatal("queue kept more than its limit")
	}
	if ev, ok := inputs.NextKey(other); !ok || ev.Code != 4 {
		t.Fatal("subscribers share one queue")
	}
	r.Close()
	if _, ok := inputs.queues[r.keys]; ok {
		t.Fatal("closed plugin still subscribed")
	}
}

func TestProximityAndAudioLevels(t *testing.T) {
	inputs, audio := NewInputs(), &AudioMeter{}
	env := RunnerEnv{Inputs: inputs, Audio: audio}
	prox := api3Runner(t, "proximity", false, []string{"sensor.proximity"}, env)
	prox.Tick(nil, 0)
	if math.Float64frombits(binary.LittleEndian.Uint64(memory(t, prox, 0, 8))) != -1 {
		t.Fatal("missing sensor not reported as -1")
	}
	inputs.setProximity(42)
	prox.Tick(nil, 0)
	if math.Float64frombits(binary.LittleEndian.Uint64(memory(t, prox, 0, 8))) != 42 {
		t.Fatal("distance not passed through")
	}

	levels := api3Runner(t, "audio_levels", true, []string{"audio.levels"}, env)
	audio.levels = AudioLevels{RMS: 0.25, Peak: 0.5, Updated: time.Now()}
	audio.levels.Bands[11] = 0.75
	if _, e := levels.Tick(nil, 0); e != nil {
		t.Fatal(e)
	}
	out := memory(t, levels, 16, 56)
	f := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(out[i*4:])) }
	if binary.LittleEndian.Uint32(memory(t, levels, 0, 4)) != 1 || f(0) != 0.25 || f(1) != 0.5 || f(13) != 0.75 {
		t.Fatal("audio levels", f(0), f(1), f(13))
	}
	audio.levels.Updated = time.Now().Add(-time.Second)
	levels.Tick(nil, 0)
	out = memory(t, levels, 16, 56)
	if binary.LittleEndian.Uint32(memory(t, levels, 0, 4)) != 0 || f(0) != 0 || f(13) != 0 {
		t.Fatal("stale audio levels exposed")
	}
}

func TestUTCTimeAndRandom(t *testing.T) {
	r := api3Runner(t, "utc_time", true, []string{}, RunnerEnv{})
	r.now = func() time.Time { return time.Date(2026, 10, 27, 23, 30, 15, 0, time.FixedZone("MSK", 3*3600)) }
	if _, e := r.Tick(nil, 0); e != nil {
		t.Fatal(e)
	}
	b := memory(t, r, 16, 8)
	if binary.LittleEndian.Uint32(memory(t, r, 0, 4)) != 1 || binary.LittleEndian.Uint16(b) != 2026 || b[2] != 10 || b[3] != 27 || b[4] != 20 || b[5] != 30 || b[6] != 15 || b[7] != 2 {
		t.Fatal("UTC time", b)
	}
	r.now = func() time.Time { return time.Unix(0, 0) }
	r.Tick(nil, 0)
	if binary.LittleEndian.Uint32(memory(t, r, 0, 4)) != 0 {
		t.Fatal("unsynced clock reported as valid")
	}
	m := manifest("1.0.0") // API 1
	p, e := Parse(packageBytes(t, m, hostCallWasm("utc_time", true), ""))
	if e != nil {
		t.Fatal(e)
	}
	old, e := NewRunner(p, map[string]any{}, RunnerEnv{})
	if e != nil {
		t.Fatal(e)
	}
	defer old.Close()
	if _, e = old.Tick(nil, 0); e == nil || !strings.Contains(e.Error(), "requires API 3") {
		t.Fatal("API 1 module used an API 3 function", e)
	}
}

func TestAPI3ManifestRules(t *testing.T) {
	ok := manifest("1.0.0")
	ok.API, ok.Permissions = 3, []string{"led.ring", "input.buttons", "sensor.proximity", "audio.levels"}
	if e := ValidateManifest(ok); e != nil {
		t.Fatal(e)
	}
	old := manifest("1.0.0")
	old.Permissions = []string{"input.buttons"}
	native := ok
	native.Engine, native.API, native.Permissions = "native", 2, []string{"system.full-access", "audio.levels"}
	api3Native := ok
	api3Native.Engine = "native"
	unknown := ok
	unknown.API = 4
	for name, m := range map[string]Manifest{"API 1 sensor permission": old, "native sensor permission": native, "native API 3": api3Native, "API 4": unknown} {
		if ValidateManifest(m) == nil {
			t.Fatal("accepted", name)
		}
	}
}

func api3Package(t *testing.T) *Package {
	t.Helper()
	m := manifest("1.0.0")
	m.ID, m.API, m.Permissions = "alice.sensors", 3, []string{"input.buttons"}
	p, e := Parse(packageBytes(t, m, hostCallWasm("button_event", true), ""))
	if e != nil {
		t.Fatal(e)
	}
	return p
}

func TestCatalogV3IsPreferredAndOlderCatalogsHideAPI3(t *testing.T) {
	store, e := NewStore(t.TempDir(), "")
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	for _, p := range []*Package{mustPackage(t, "1.0.0"), api3Package(t)} {
		s, e := store.Submit(ctx, p.Raw)
		if e != nil {
			t.Fatal(e)
		}
		if e = store.Review(s.ID, "approve", "tested", true); e != nil {
			t.Fatal(e)
		}
	}
	for api, want := range map[int]int{1: 1, 2: 1, 3: 2} {
		env, e := store.CatalogFor(api)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := json.Marshal(env)
		c, e := VerifyCatalog(raw, store.PublicKey(), 0)
		if e != nil || len(c.Entries) != want {
			t.Fatal("catalog", api, len(c.Entries), e)
		}
	}
	for _, publishV3 := range []bool{true, false} {
		handler := NewServer(nil, store, "admin")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !publishV3 && r.URL.Path == "/catalog/v3.json" {
				http.NotFound(w, r)
				return
			}
			handler.ServeHTTP(w, r)
		}))
		m := manager(t)
		if e = m.SetStore(server.URL+"/catalog/v2.json", store.PublicKey()); e != nil {
			t.Fatal(e)
		}
		if e = m.Refresh(ctx); e != nil {
			t.Fatal(e)
		}
		want := 1
		if publishV3 {
			want = 2
		}
		if m.catalog == nil || len(m.catalog.Entries) != want {
			t.Fatal("v3 preference", publishV3, m.catalog)
		}
		server.Close()
	}
}
