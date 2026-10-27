package sharedaudio

import (
	"bytes"
	"encoding/binary"
	"io"
	"testing"
)

func constantPCM(samples int, value int16) []byte {
	b := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(value))
	}
	return b
}

func samples(b []byte) []int16 {
	out := make([]int16, len(b)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*2:]))
	}
	return out
}

func TestDuckFadesWithoutClicksAndRestores(t *testing.T) {
	sink := &audioSink{}
	p := &Playback{open: func() (io.WriteCloser, error) { return sink, nil }}
	pcm := constantPCM(44100*2, 10000) // one second of stereo
	p.Write(pcm[:1000])
	if !bytes.Equal(sink.Bytes(), pcm[:1000]) {
		t.Fatal("audio changed without ducking")
	}
	p.SetDuck(0.8)
	// Odd-sized writes split samples; the output must stay sample-aligned.
	for i := 1000; i < len(pcm); i += 777 {
		end := min(i+777, len(pcm))
		if n, e := p.Write(pcm[i:end]); e != nil || n != end-i {
			t.Fatal(n, e)
		}
	}
	out := samples(sink.Bytes())
	last := out[len(out)-1]
	if last < 1990 || last > 2010 {
		t.Fatal("not ducked to 20%", last)
	}
	for i := 1; i < len(out); i++ {
		if d := int(out[i-1]) - int(out[i]); d > 3 || d < 0 {
			t.Fatal("fade jumps at sample", i, out[i-1], out[i])
		}
	}
	p.SetDuck(0)
	sink.Reset()
	p.Write(pcm)
	out = samples(sink.Bytes())
	if out[len(out)-1] != 10000 || !p.duck.idle() {
		t.Fatal("volume not restored", out[len(out)-1])
	}
}
