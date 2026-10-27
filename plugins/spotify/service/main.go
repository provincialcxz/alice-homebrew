//go:build spotify

// Spotify Connect service. MIT, Alice Homebrew contributors.
//
// The receiver is Spotify Soloist, Spotify's own headless Connect client. Its
// licence forbids redistribution, so the plugin downloads it from Spotify on
// the speaker and runs it on the bundled arm64 glibc (runtime/, LGPL). Soloist
// only plays through PulseAudio, so runtime/lib/libpulse.so.0 is a small shim
// that writes its PCM to a FIFO; this service reads the FIFO and plays it
// through Alice's mixer with sharedaudio.
package main

import (
	"alicehomebrew/plugins/internal/sharedaudio"
	"alicehomebrew/sdk/quasar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

type configuration struct {
	Name          string  `json:"name"`
	APIKey        string  `json:"api-key"`
	InitialVolume float64 `json:"initial-volume"`
	// DuckVolume is the music level, in percent, while Alice listens or
	// answers; 100 turns ducking off.
	DuckVolume float64 `json:"duck-volume"`
}

var apiKeyRE = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{7,127}$`)

func (c configuration) validate() error {
	if strings.TrimSpace(c.Name) == "" || len([]byte(c.Name)) > 48 || strings.ContainsAny(c.Name, "\x00\r\n") {
		return errors.New("name must be 1-48 UTF-8 bytes; newlines are not allowed")
	}
	if c.APIKey != "" && !apiKeyRE.MatchString(c.APIKey) {
		return errors.New("the Soloist API key has unexpected characters; paste it again from the Spotify dashboard")
	}
	if c.InitialVolume < 0 || c.InitialVolume > 100 || c.DuckVolume < 0 || c.DuckVolume > 100 {
		return errors.New("invalid volume")
	}
	return nil
}

// layout is where everything lives inside the plugin data directory.
type layout struct{ root string }

func (l layout) runtime() string { return filepath.Join(l.root, "runtime") }
func (l layout) lib() string     { return filepath.Join(l.runtime(), "lib") }
func (l layout) loader() string  { return filepath.Join(l.lib(), "ld-linux-aarch64.so.1") }
func (l layout) soloist() string { return filepath.Join(l.root, "soloist", "current", "soloist") }
func (l layout) state() string   { return filepath.Join(l.root, "state") }
func (l layout) cache() string   { return filepath.Join(l.root, "cache") }
func (l layout) fifo() string    { return filepath.Join(l.root, "audio.fifo") }

// command runs a Soloist binary through the bundled loader, so it gets the
// bundled glibc instead of the speaker's old one.
func (l layout) command(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, l.loader(), append([]string{"--library-path", l.lib(), binary}, args...)...)
	cmd.Env = []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8",
		"HOME=" + l.root, "TMPDIR=" + l.root,
		"SSL_CERT_FILE=" + filepath.Join(l.runtime(), "ca-certificates.crt"),
		"ALICE_PULSE_FIFO=" + l.fifo(),
	}
	return cmd
}

// arguments builds the Soloist command line. Soloist does not accept a cache
// below 100 MB, so the cache is trimmed separately (see trimCache).
func arguments(c configuration, l layout) []string {
	return []string{
		"--device-name", c.Name,
		"--api-key", c.APIKey,
		"--data-dir", l.state(),
		"--cache-dir", l.cache(),
		"--cache-size", "100",
		"--initial-volume", strconv.Itoa(int(c.InitialVolume)),
	}
}

// duckFor lowers the music while Alice is busy and restores it afterwards,
// including when her event socket disappears.
func duckFor(shared interface{ SetDuck(float64) }, volume float64) func(quasar.State) {
	busy := false
	return func(s quasar.State) {
		if s.Active() == busy {
			return
		}
		busy = s.Active()
		if busy {
			shared.SetDuck(1 - volume/100)
			log.Print("Alice is active: music ducked")
		} else {
			shared.SetDuck(0)
		}
	}
}

var (
	ansi     = regexp.MustCompile("\x1b\\[[0-9;]*m")
	accounts = regexp.MustCompile(`(logged in as|session:)\s*\[?(?:session:)?[A-Za-z0-9]+`)
	// Soloist tries to re-run itself as a crash handler through
	// /proc/self/exe, which is the loader here; that fails harmlessly.
	crashpad = regexp.MustCompile(`crashpad-handler|ld-linux-aarch64\.so\.1 --help|recvmsg: Connection reset`)
)

// cleanLog keeps Spotify account IDs out of the plugin log shown in the
// manager and drops noise. It returns "" for lines that should be skipped.
func cleanLog(line string) string {
	line = ansi.ReplaceAllString(line, "")
	if crashpad.MatchString(line) {
		return ""
	}
	return accounts.ReplaceAllString(line, "$1 …")
}

func forwardLog(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16384)
	for scanner.Scan() {
		if line := cleanLog(scanner.Text()); line != "" {
			log.Print(line)
		}
	}
}

func main() {
	log.SetFlags(0)
	if e := run(); e != nil {
		log.Fatal(e)
	}
}

func run() error {
	b, e := os.ReadFile(os.Getenv("ALICE_CONFIG_PATH"))
	if e != nil {
		return e
	}
	var c configuration
	if e = json.Unmarshal(b, &c); e != nil {
		return e
	}
	if e = c.validate(); e != nil {
		return e
	}
	root := os.Getenv("ALICE_PLUGIN_DATA")
	if root == "" {
		return errors.New("ALICE_PLUGIN_DATA missing")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if c.APIKey == "" {
		// Not an error: the plugin waits until the key is saved, which
		// restarts it.
		log.Print("Paste your Spotify Soloist API key in the plugin settings: developer.spotify.com/dashboard/soloist (Spotify Premium is required)")
		<-ctx.Done()
		return nil
	}
	l := layout{root}
	for _, dir := range []string{l.state(), l.cache(), filepath.Dir(filepath.Dir(l.soloist()))} {
		if e = os.MkdirAll(dir, 0700); e != nil {
			return e
		}
	}
	if e = installRuntime(l); e != nil {
		return fmt.Errorf("runtime: %w", e)
	}
	u := newUpdater(l)
	if e = u.ensure(ctx); e != nil {
		return e
	}
	shared, e := sharedaudio.New(root)
	if e != nil {
		return e
	}
	defer shared.Close()
	var lastAudio atomic.Int64
	if e = pipeAudio(ctx, l.fifo(), shared, &lastAudio); e != nil {
		return fmt.Errorf("audio FIFO: %w", e)
	}
	if c.DuckVolume < 100 {
		go quasar.Watch(ctx, quasar.Address, duckFor(shared, c.DuckVolume))
	}
	go trimCacheLoop(ctx, l.cache())
	ready := make(chan struct{}, 1)
	go u.loop(ctx, ready)
	return supervise(ctx, c, l, u, ready, &lastAudio)
}

// exitExpired is Soloist's exit code for a build past its 90 days.
const exitExpired = 10

// supervise keeps Soloist running. It restarts it after crashes with a
// growing delay, swaps in a downloaded update once nothing has played for a
// while, and fetches a new build at once if the current one has expired.
func supervise(ctx context.Context, c configuration, l layout, u *updater, ready chan struct{}, lastAudio *atomic.Int64) error {
	delay := 5 * time.Second
	for {
		started := time.Now()
		cmd := l.command(context.Background(), l.soloist(), arguments(c, l)...)
		stderr, e := cmd.StderrPipe()
		if e != nil {
			return e
		}
		cmd.Stdout = cmd.Stderr
		if e = cmd.Start(); e != nil {
			return fmt.Errorf("start Soloist: %w", e)
		}
		go forwardLog(stderr)
		log.Printf("Spotify Connect ready: %q; audio through Alice's shared mixer", c.Name)
		finished := make(chan error, 1)
		go func() { finished <- cmd.Wait() }()
		restart := ""
		for restart == "" {
			select {
			case <-ctx.Done():
				terminate(cmd, finished)
				return nil
			case <-ready:
				// Swap only when idle so an update never cuts a song.
				if time.Since(time.Unix(0, lastAudio.Load())) > 2*time.Minute {
					restart = "update"
				} else {
					go func() { time.Sleep(time.Minute); requeue(ready) }()
				}
			case e = <-finished:
				restart = "exit"
			}
		}
		if restart == "update" {
			terminate(cmd, finished)
			if e = u.promote(); e != nil {
				log.Printf("update not applied: %v", e)
			}
			continue
		}
		var exit *exec.ExitError
		if errors.As(e, &exit) && exit.ExitCode() == exitExpired {
			log.Print("this Soloist build has expired; downloading a new one")
			if e = u.refresh(ctx); e != nil {
				return fmt.Errorf("expired build and no update: %w", e)
			}
			continue
		}
		if time.Since(started) > 5*time.Minute {
			delay = 5 * time.Second
		}
		log.Printf("Soloist stopped (%v); restarting in %s", e, delay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		if delay *= 2; delay > 5*time.Minute {
			delay = 5 * time.Minute
		}
	}
}

func requeue(ready chan<- struct{}) {
	select {
	case ready <- struct{}{}:
	default:
	}
}

func terminate(cmd *exec.Cmd, finished <-chan error) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-finished
	}
}
