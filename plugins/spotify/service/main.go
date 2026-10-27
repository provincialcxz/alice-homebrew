//go:build spotify

// Spotify Connect service. MIT, Alice Homebrew contributors. The embedded
// librespot receiver (MIT) and its Rust dependencies retain their licenses.
package main

import (
	"alicehomebrew/plugins/internal/sharedaudio"
	"bufio"
	_ "embed"
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
	"syscall"
	"time"
)

//go:embed receiver-arm64
var receiver []byte

type configuration struct {
	Name          string  `json:"name"`
	Bitrate       string  `json:"bitrate"`
	InitialVolume float64 `json:"initial-volume"`
	Normalise     bool    `json:"normalisation"`
	Port          int     `json:"port"`
}

// arguments builds the librespot command line. Audio always leaves through the
// pipe backend as 44.1 kHz S16 stereo, which sharedaudio plays through Alice's
// mixer; librespot itself never opens the sound card.
func arguments(c configuration, root string) ([]string, error) {
	if strings.TrimSpace(c.Name) == "" || len([]byte(c.Name)) > 48 || strings.ContainsAny(c.Name, "\x00\r\n") {
		return nil, errors.New("name must be 1-48 UTF-8 bytes; newlines are not allowed")
	}
	switch c.Bitrate {
	case "96", "160", "320":
	default:
		return nil, errors.New("bitrate must be 96, 160 or 320")
	}
	if c.InitialVolume < 0 || c.InitialVolume > 100 || c.Port < 1024 || c.Port > 65535 {
		return nil, errors.New("invalid initial volume or port")
	}
	args := []string{
		"--name", c.Name,
		"--device-type", "speaker",
		"--backend", "pipe",
		"--format", "S16",
		"--bitrate", c.Bitrate,
		"--initial-volume", strconv.Itoa(int(c.InitialVolume)),
		"--volume-ctrl", "log",
		// The credential cache keeps the login across restarts; audio files
		// are not cached so the speaker's small /data is not filled.
		"--cache", filepath.Join(root, "cache"),
		"--system-cache", filepath.Join(root, "cache"),
		"--disable-audio-cache",
		"--zeroconf-port", strconv.Itoa(c.Port),
	}
	if c.Normalise {
		args = append(args, "--enable-volume-normalisation")
	}
	return args, nil
}

var accountName = regexp.MustCompile(`(Authenticated as) '[^']*'`)

// redact keeps Spotify user names out of the plugin log shown in the manager.
func redact(line string) string {
	return accountName.ReplaceAllString(line, "$1 '…'")
}

func forwardLog(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16384)
	for scanner.Scan() {
		log.Print(redact(scanner.Text()))
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
	root := os.Getenv("ALICE_PLUGIN_DATA")
	if root == "" {
		return errors.New("ALICE_PLUGIN_DATA missing")
	}
	args, e := arguments(c, root)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Join(root, "cache"), 0700); e != nil {
		return e
	}
	binary := filepath.Join(root, "librespot")
	if e = os.WriteFile(binary, receiver, 0700); e != nil {
		return e
	}
	if e = os.Chmod(binary, 0700); e != nil {
		return e
	}
	shared, e := sharedaudio.New(root)
	if e != nil {
		return e
	}
	defer shared.Close()
	cmd := exec.Command(binary, args...)
	cmd.Stdout = shared
	stderr, e := cmd.StderrPipe()
	if e != nil {
		return e
	}
	if e = cmd.Start(); e != nil {
		return e
	}
	go forwardLog(stderr)
	log.Printf("Spotify Connect ready: %q, %s kbps, TCP %d; audio through Alice's shared mixer", c.Name, c.Bitrate, c.Port)
	finished := make(chan error, 1)
	go func() { finished <- cmd.Wait() }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	select {
	case <-signals:
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-finished:
		case <-time.After(time.Second):
			_ = cmd.Process.Kill()
		}
		return nil
	case e := <-finished:
		if e == nil {
			return errors.New("receiver exited")
		}
		return fmt.Errorf("receiver stopped: %w", e)
	}
}
