package homebrew

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type Runner struct {
	rt       wazero.Runtime
	module   api.Module
	tick     api.Function
	manifest Manifest
	config   map[string]any
	pending  []byte
	frames   int
	hostErr  error
	Logs     []string
	env      RunnerEnv
	keys     int
	now      func() time.Time
}

// RunnerEnv carries the read-only speaker data offered to API 3 permissions.
// Either field may be nil, for example while a package is only validated.
type RunnerEnv struct {
	Inputs *Inputs
	Audio  *AudioMeter
	// AliceState returns quasar.State as an int, -1 when unknown.
	AliceState func() int32
}
type PluginRunner interface {
	Tick(map[string]any, uint32) ([]byte, error)
	Close()
	LogLines() []string
	MemoryBytes() uint32
}

func (r *Runner) LogLines() []string { return append([]string{}, r.Logs...) }
func (r *Runner) MemoryBytes() uint32 {
	if r.module.Memory() != nil {
		return r.module.Memory().Size()
	}
	return 0
}

func NewRunner(p *Package, config map[string]any, env RunnerEnv) (*Runner, error) {
	if e := wasmLimits(p.Module); e != nil {
		return nil, e
	}
	r := &Runner{manifest: p.Manifest, config: config, env: env, now: time.Now}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.rt = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(32).WithCloseOnContextDone(true).WithDebugInfoEnabled(false))
	fail := func(err error) (*Runner, error) { r.rt.Close(context.Background()); return nil, err }
	host := r.rt.NewHostModuleBuilder("alice")
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr, n uint32) float64 {
		key, ok := r.read(m, ptr, n, 64)
		if !ok {
			return 0
		}
		v := r.config[key]
		if x, ok := v.(float64); ok {
			return x
		}
		if x, ok := v.(bool); ok && x {
			return 1
		}
		return 0
	}).Export("config_number")
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr, n, out, capacity uint32) uint32 {
		key, ok := r.read(m, ptr, n, 64)
		if !ok {
			return ^uint32(0)
		}
		value, _ := r.config[key].(string)
		if uint32(len(value)) > capacity || capacity > 1024 || m.Memory() == nil || !m.Memory().Write(out, []byte(value)) {
			r.hostErr = errors.New("invalid config string buffer")
			return ^uint32(0)
		}
		return uint32(len(value))
	}).Export("config_string")
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr, n uint32) uint32 {
		if !Has(r.manifest, "led.ring") {
			r.hostErr = errors.New("permission denied: led.ring")
			return 1
		}
		r.frames++
		if n != 36 || r.frames > 1 || m.Memory() == nil {
			r.hostErr = errors.New("one 36-byte LED frame is allowed per tick")
			return 1
		}
		frame, ok := m.Memory().Read(ptr, n)
		if !ok {
			r.hostErr = errors.New("LED buffer is outside WebAssembly memory")
			return 1
		}
		r.pending = append(r.pending[:0], frame...)
		return 0
	}).Export("led_write")
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, ptr, n uint32) {
		if !Has(r.manifest, "log.write") {
			r.hostErr = errors.New("permission denied: log.write")
			return
		}
		s, ok := r.read(m, ptr, n, 1024)
		if !ok {
			return
		}
		r.Logs = append(r.Logs, strings.ToValidUTF8(s, "?"))
		if len(r.Logs) > 32 {
			r.Logs = r.Logs[len(r.Logs)-32:]
		}
	}).Export("log")
	// button_event(out) writes 8 bytes: code u16, value u8 (1 press, 0 release,
	// 2 repeat), reserved u8, milliseconds u32. Returns 1 when an event was read.
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, out uint32) uint32 {
		if !r.allowed("input.buttons") || r.env.Inputs == nil {
			return 0
		}
		if r.keys == 0 {
			r.keys = r.env.Inputs.Subscribe()
		}
		ev, ok := r.env.Inputs.NextKey(r.keys)
		if !ok {
			return 0
		}
		var b [8]byte
		binary.LittleEndian.PutUint16(b[0:], ev.Code)
		b[2] = ev.Value
		binary.LittleEndian.PutUint32(b[4:], ev.Millis)
		if m.Memory() == nil || !m.Memory().Write(out, b[:]) {
			r.hostErr = errors.New("button buffer is outside WebAssembly memory")
			return 0
		}
		return 1
	}).Export("button_event")
	// proximity() returns the last sensor distance, or -1 without data.
	host.NewFunctionBuilder().WithFunc(func(context.Context, api.Module) float64 {
		if !r.allowed("sensor.proximity") || r.env.Inputs == nil {
			return -1
		}
		return r.env.Inputs.Proximity()
	}).Export("proximity")
	// audio_levels(out) writes 14 f32: RMS, peak and 12 bands of what is playing.
	// Returns 1 while fresh levels exist; otherwise the buffer is zeroed.
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, out uint32) uint32 {
		if !r.allowed("audio.levels") {
			return 0
		}
		var levels AudioLevels
		if r.env.Audio != nil {
			levels = r.env.Audio.Snapshot()
		}
		var b [56]byte
		values := append([]float64{levels.RMS, levels.Peak}, levels.Bands[:]...)
		for i, v := range values {
			binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(float32(v)))
		}
		if m.Memory() == nil || !m.Memory().Write(out, b[:]) {
			r.hostErr = errors.New("audio buffer is outside WebAssembly memory")
			return 0
		}
		if levels.Available {
			return 1
		}
		return 0
	}).Export("audio_levels")
	// alice_state() returns 0 idle, 1 listening, 2 speaking, 3 thinking,
	// 5 recognizing music, 6 none, or -1 while the state is unknown.
	host.NewFunctionBuilder().WithFunc(func(context.Context, api.Module) int32 {
		if !r.allowed("assistant.state") || r.env.AliceState == nil {
			return -1
		}
		return r.env.AliceState()
	}).Export("alice_state")
	// utc_time(out) writes 8 bytes of UTC: year u16, month, day, hour, minute,
	// second, weekday (0 Sunday). The speaker's time zone setting is unknown,
	// so plugins apply their own offset. Returns 0 while the clock is unsynced.
	// Time and randomness reveal nothing about the user, so no permission, but
	// API 1 modules must keep working on older managers and cannot use them.
	host.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, out uint32) uint32 {
		if !r.api3("utc_time") {
			return 0
		}
		now := r.now().UTC()
		b := []byte{0, 0, byte(now.Month()), byte(now.Day()), byte(now.Hour()), byte(now.Minute()), byte(now.Second()), byte(now.Weekday())}
		binary.LittleEndian.PutUint16(b, uint16(now.Year()))
		if m.Memory() == nil || !m.Memory().Write(out, b) {
			r.hostErr = errors.New("time buffer is outside WebAssembly memory")
			return 0
		}
		if now.Year() < 2024 {
			return 0
		}
		return 1
	}).Export("utc_time")
	host.NewFunctionBuilder().WithFunc(func(context.Context, api.Module) uint32 {
		if !r.api3("random_u32") {
			return 0
		}
		return rand.Uint32()
	}).Export("random_u32")
	if _, err := host.Instantiate(ctx); err != nil {
		return fail(err)
	}
	// No WASI module, filesystem, environment, sockets or process imports exist.
	module, err := r.rt.InstantiateWithConfig(ctx, p.Module, wazero.NewModuleConfig().WithName(p.Manifest.ID).WithStartFunctions())
	if err != nil {
		return fail(err)
	}
	r.module = module
	r.tick = module.ExportedFunction("alice_tick")
	if r.tick == nil || module.Memory() == nil {
		return fail(errors.New("module must export memory and alice_tick(milliseconds)"))
	}
	def := r.tick.Definition()
	if len(def.ParamTypes()) != 1 || def.ParamTypes()[0] != api.ValueTypeI32 || len(def.ResultTypes()) != 0 {
		return fail(errors.New("alice_tick signature must be (i32) -> ()"))
	}
	if r.hostErr != nil {
		return fail(r.hostErr)
	}
	r.pending = nil
	r.frames = 0
	return r, nil
}

func (r *Runner) api3(function string) bool {
	if r.manifest.API >= 3 {
		return true
	}
	r.hostErr = errors.New(function + " requires API 3")
	return false
}

// allowed stops the plugin, like led_write does, when a permission is missing.
func (r *Runner) allowed(permission string) bool {
	if Has(r.manifest, permission) {
		return true
	}
	r.hostErr = errors.New("permission denied: " + permission)
	return false
}
func (r *Runner) read(m api.Module, ptr, n, limit uint32) (string, bool) {
	if n > limit || m.Memory() == nil {
		r.hostErr = errors.New("invalid host string length")
		return "", false
	}
	b, ok := m.Memory().Read(ptr, n)
	if !ok {
		r.hostErr = errors.New("host string is outside WebAssembly memory")
		return "", false
	}
	return string(b), true
}
func (r *Runner) Tick(config map[string]any, now uint32) ([]byte, error) {
	r.config = config
	r.pending = nil
	r.frames = 0
	r.hostErr = nil
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := r.tick.Call(ctx, uint64(now)); err != nil {
		return nil, fmt.Errorf("plugin stopped: %w", err)
	}
	if r.hostErr != nil {
		return nil, r.hostErr
	}
	return append([]byte(nil), r.pending...), nil
}
func (r *Runner) Close() {
	if r.keys != 0 {
		r.env.Inputs.Unsubscribe(r.keys)
	}
	r.rt.Close(context.Background())
}

// ABI stays small: only led.ring and log.write cause effects; API 3 adds
// read-only inputs (buttons, proximity, played audio levels).
func ValidateModule(p *Package) error {
	if p.Manifest.Engine == "native" {
		return validateELF(p.Module)
	}
	c, e := Config(p.Manifest, nil)
	if e != nil {
		return e
	}
	r, e := NewRunner(p, c, RunnerEnv{})
	if e != nil {
		return e
	}
	r.Close()
	return nil
}

// Used by the moderator service: validate bytecode and ABI without executing
// a guest start function or tick supplied by an anonymous author.
func ValidateModuleFormat(p *Package) error {
	if p.Manifest.Engine == "native" {
		return validateELF(p.Module)
	}
	if e := wasmLimits(p.Module); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfigInterpreter().WithMemoryLimitPages(32).WithDebugInfoEnabled(false))
	defer rt.Close(context.Background())
	compiled, e := rt.CompileModule(ctx, p.Module)
	if e != nil {
		return e
	}
	defer compiled.Close(context.Background())
	f := compiled.ExportedFunctions()["alice_tick"]
	if f == nil || len(f.ParamTypes()) != 1 || f.ParamTypes()[0] != api.ValueTypeI32 || len(f.ResultTypes()) != 0 {
		return errors.New("alice_tick signature must be (i32) -> ()")
	}
	if compiled.ExportedMemories()["memory"] == nil {
		return errors.New("module must export memory")
	}
	return nil
}
