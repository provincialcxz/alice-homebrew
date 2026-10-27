//go:build spotify

package main

import (
	"alicehomebrew/sdk/quasar"
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestArguments(t *testing.T) {
	c := configuration{Name: "Alice Mini", APIKey: "spak_test123", InitialVolume: 40, DuckVolume: 20}
	if e := c.validate(); e != nil {
		t.Fatal(e)
	}
	l := layout{"/data/plugin"}
	args := arguments(c, l)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--api-key spak_test123", "--initial-volume 40", "--cache-size 100", "--data-dir " + l.state(), "--cache-dir " + l.cache()} {
		if !strings.Contains(joined, want) {
			t.Fatal("missing", want, joined)
		}
	}
	// The name stays one argument even with spaces, so no shell is involved.
	if args[slices.Index(args, "--device-name")+1] != "Alice Mini" {
		t.Fatal("name split", args)
	}
	cmd := l.command(t.Context(), l.soloist(), args...)
	if cmd.Path != l.loader() || cmd.Args[1] != "--library-path" || cmd.Args[3] != l.soloist() {
		t.Fatal("Soloist must run through the bundled loader", cmd.Args)
	}
	if !slices.Contains(cmd.Env, "ALICE_PULSE_FIFO="+l.fifo()) {
		t.Fatal("shim FIFO not passed", cmd.Env)
	}
	for _, bad := range []configuration{
		{Name: ""},
		{Name: "a\nb"},
		{Name: strings.Repeat("x", 49)},
		{Name: "ok", APIKey: "has space"},
		{Name: "ok", APIKey: "--verbose"},
		{Name: "ok", InitialVolume: 101},
		{Name: "ok", DuckVolume: -1},
	} {
		if bad.validate() == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestCleanLog(t *testing.T) {
	for in, want := range map[string]string{
		"\x1b[0m17:16:43.454 I [f:415] logged in as 3175fzz5tqakj47koff5hxe53qqu\x1b[0m":           "17:16:43.454 I [f:415] logged in as …",
		"Session created via Connect, session: [session:3175fzz5tqakj47koff5hxe53qqu]":             "Session created via Connect, session: …]",
		"/data/x/runtime/lib/ld-linux-aarch64.so.1: unrecognized option '--type=crashpad-handler'": "",
		"waiting for login — connect to \"Alice Mini\" from your Spotify app":                      "waiting for login — connect to \"Alice Mini\" from your Spotify app",
	} {
		if got := cleanLog(in); got != want {
			t.Fatalf("cleanLog(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseVersion(t *testing.T) {
	b, e := parseVersion("soloist 1.3.10.20 build 1793102484 (20261027) (g19412df6ea) (linux/aarch64)\n")
	if e != nil || b.Version != "1.3.10.20" || !b.Built.Equal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatal(b, e)
	}
	if _, e = parseVersion("Usage: soloist"); e == nil {
		t.Fatal("garbage accepted")
	}
}

func archive(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	z := gzip.NewWriter(&b)
	w := tar.NewWriter(z)
	for name, body := range files {
		w.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		w.Write([]byte(body))
	}
	w.WriteHeader(&tar.Header{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	w.Close()
	z.Close()
	return b.Bytes()
}

func TestExtractOnlyKnownFiles(t *testing.T) {
	dir := t.TempDir()
	raw := archive(t, map[string]string{"soloist": "ELF", "../../evil": "x", "CHANGELOG.md": "log", "sub/../THIRD_PARTY_LICENSES.txt": "lic"})
	if e := extract(bytes.NewReader(raw), dir); e != nil {
		t.Fatal(e)
	}
	entries, _ := os.ReadDir(dir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if strings.Join(names, ",") != "CHANGELOG.md,THIRD_PARTY_LICENSES.txt,soloist" {
		t.Fatal(names)
	}
	if _, e := os.Stat(filepath.Join(filepath.Dir(filepath.Dir(dir)), "evil")); e == nil {
		t.Fatal("path escaped")
	}
	if extract(bytes.NewReader(archive(t, map[string]string{"README": "x"})), t.TempDir()) == nil {
		t.Fatal("archive without soloist accepted")
	}
}

func TestTrimCacheKeepsNewestAudio(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	for i, name := range []string{"aa/old.file", "bb/mid.file", "cc/new.file", "Storage/index.dat"} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		os.MkdirAll(filepath.Dir(p), 0700)
		os.WriteFile(p, make([]byte, 1000), 0600)
		os.Chtimes(p, now, now.Add(time.Duration(i)*time.Minute))
	}
	if cacheSize(dir) != 3000 {
		t.Fatal("index counted as audio", cacheSize(dir))
	}
	trimCache(dir, 1500)
	for name, kept := range map[string]bool{"aa/old.file": false, "bb/mid.file": false, "cc/new.file": true, "Storage/index.dat": true} {
		if _, e := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); (e == nil) != kept {
			t.Fatal(name, "kept:", e == nil)
		}
	}
}

type duckRecorder struct{ values []float64 }

func (d *duckRecorder) SetDuck(v float64) { d.values = append(d.values, v) }

func TestDuckFollowsAlice(t *testing.T) {
	d := &duckRecorder{}
	f := duckFor(d, 20)
	for _, s := range []quasar.State{quasar.Idle, quasar.Listening, quasar.Speaking, quasar.Idle, quasar.Unknown} {
		f(s)
	}
	if len(d.values) != 2 || d.values[0] != 0.8 || d.values[1] != 0 {
		t.Fatal(d.values)
	}
}
