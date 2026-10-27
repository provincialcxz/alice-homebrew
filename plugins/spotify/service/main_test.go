//go:build spotify

package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestArguments(t *testing.T) {
	c := configuration{Name: "Alice Mini", Bitrate: "160", InitialVolume: 50, Port: 4070}
	args, e := arguments(c, "/data/plugin")
	if e != nil {
		t.Fatal(e)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--backend pipe", "--format S16", "--bitrate 160", "--initial-volume 50", "--zeroconf-port 4070", "--disable-audio-cache", "--cache " + filepath.Join("/data/plugin", "cache")} {
		if !strings.Contains(joined, want) {
			t.Fatal("missing", want, joined)
		}
	}
	if slices.Contains(args, "--enable-volume-normalisation") {
		t.Fatal("normalisation enabled by default")
	}
	// The name stays one argument even with spaces, so no shell is involved.
	if args[slices.Index(args, "--name")+1] != "Alice Mini" {
		t.Fatal("name split", args)
	}
	c.Normalise = true
	if args, _ = arguments(c, "/x"); !slices.Contains(args, "--enable-volume-normalisation") {
		t.Fatal("normalisation not passed")
	}
	for _, bad := range []configuration{
		{Name: "", Bitrate: "160", Port: 4070},
		{Name: "a\nb", Bitrate: "160", Port: 4070},
		{Name: strings.Repeat("x", 49), Bitrate: "160", Port: 4070},
		{Name: "ok", Bitrate: "128", Port: 4070},
		{Name: "ok", Bitrate: "160", Port: 80},
		{Name: "ok", Bitrate: "160", Port: 4070, InitialVolume: 101},
	} {
		if _, e := arguments(bad, "/x"); e == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestRedactAccountName(t *testing.T) {
	line := "[INFO librespot_core::session] Authenticated as 'drh8spxczmnxeqlywln0iptuo' !"
	if got := redact(line); strings.Contains(got, "drh8") || !strings.Contains(got, "Authenticated as") {
		t.Fatal(got)
	}
	if redact("Using pipe sink") != "Using pipe sink" {
		t.Fatal("unrelated line changed")
	}
}
