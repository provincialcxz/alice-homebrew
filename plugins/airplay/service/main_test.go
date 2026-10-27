//go:build airplay

package main

import (
	"strings"
	"testing"
)

func TestSharedConfigAndDirectOptIn(t *testing.T) {
	c := configuration{Name: "Mini", Device: "plughw:0,0", Port: 7000, MaxVolume: -10}
	text, e := configText(c)
	if e != nil || !strings.Contains(text, `output_backend = "stdout"`) || !strings.Contains(text, `output_format = "S16"`) {
		t.Fatal(text, e)
	}
	c.AudioMode = "direct"
	text, e = configText(c)
	if e != nil || !strings.Contains(text, `output_backend = "alsa"`) || !strings.Contains(text, `disable_standby_mode = "never"`) {
		t.Fatal(text, e)
	}
	c.AudioMode = "unknown"
	if _, e = configText(c); e == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestConfigAndDescriptor(t *testing.T) {
	c := configuration{Name: "Mini \"Room\"", Device: "plughw:0,0", MaxVolume: -10, Port: 7000}
	s, e := configText(c)
	if e != nil || !strings.Contains(s, `name = "Mini \"Room\""`) {
		t.Fatalf("%s %v", s, e)
	}
	c.Name = "bad\nname"
	if _, e = configText(c); e == nil {
		t.Fatal("newline accepted")
	}
	n, _, p, txt, e := descriptor([]byte("7000\nAABBCCDDEEFF@Mini\n_raop._tcp\ncn=0,1\npw=false\n"))
	if e != nil || p != 7000 || n != "AABBCCDDEEFF@Mini" || len(txt) != 2 {
		t.Fatal(n, p, txt, e)
	}
	if _, _, _, _, e = descriptor([]byte("7000\nx\n_other._tcp\nx=y\n")); e == nil {
		t.Fatal("bad service accepted")
	}
}
