package sharedaudio

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"strings"
	"testing"
	"time"
)

type audioSink struct {
	bytes.Buffer
	closed bool
}

func (s *audioSink) Close() error { s.closed = true; return nil }

func TestSharedPlaybackLazyOpenReleaseAndResume(t *testing.T) {
	var sinks []*audioSink
	p := &Playback{open: func() (io.WriteCloser, error) {
		s := &audioSink{}
		sinks = append(sinks, s)
		return s, nil
	}}
	if len(sinks) != 0 {
		t.Fatal("opened an idle PCM")
	}
	pcm := []byte{0, 1, 2, 3, 4, 5, 6, 7}
	if n, e := p.Write(pcm); e != nil || n != len(pcm) {
		t.Fatal(n, e)
	}
	if !bytes.Equal(sinks[0].Bytes(), pcm) {
		t.Fatal("PCM changed")
	}
	p.releaseIdle(p.last.Add(time.Second))
	if sinks[0].closed {
		t.Fatal("released during playback")
	}
	p.releaseIdle(p.last.Add(3 * time.Second))
	if !sinks[0].closed {
		t.Fatal("PCM stayed open after pause")
	}
	if _, e := p.Write(pcm); e != nil {
		t.Fatal(e)
	}
	if len(sinks) != 2 || !bytes.Equal(sinks[1].Bytes(), pcm) {
		t.Fatal("resume failed")
	}
	p.Close()
	p.Close()
	if !sinks[1].closed {
		t.Fatal("player leaked on stop")
	}
	if _, e := p.Write(pcm); e != io.ErrClosedPipe {
		t.Fatal("write after stop", e)
	}
}

func TestSharedRouteUsesStockMixer(t *testing.T) {
	if !strings.Contains(sharedALSAConfig, `slave.pcm "dmixer"`) || strings.Contains(sharedALSAConfig, `type hw`) {
		t.Fatal("shared mode bypasses stock mixer")
	}
}

func TestPCMMeterStereoFramesAndSplitWrites(t *testing.T) {
	// No socket is needed: exercise the exact packet producer with split frames.
	m := &pcmMeter{packets: make(chan [64]byte, 1)}
	for i := range m.window {
		m.window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/1023)
	}
	for i, f := range []float64{80, 120, 180, 280, 430, 650, 1000, 1500, 2300, 3500, 5500, 8500} {
		m.coeff[i] = 2 * math.Cos(2*math.Pi*f/44100)
	}
	pcm := make([]byte, 2048*4)
	for i := 0; i < 2048; i++ {
		x := int16(16384 * math.Sin(2*math.Pi*1000*float64(i)/44100))
		binary.LittleEndian.PutUint16(pcm[i*4:], uint16(x))
		binary.LittleEndian.PutUint16(pcm[i*4+2:], uint16(x))
	}
	for i := 0; i < len(pcm); i += 7 {
		end := i + 7
		if end > len(pcm) {
			end = len(pcm)
		}
		m.add(pcm[i:end])
	}
	select {
	case packet := <-m.packets:
		if string(packet[:8]) != "ALICEAU1" {
			t.Fatal("wrong meter protocol")
		}
		value := func(i int) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(packet[8+i*4:]))) }
		if rms := value(0); rms < .34 || rms > .37 {
			t.Fatal("RMS", rms)
		}
		if peak := value(1); peak < .49 || peak > .51 {
			t.Fatal("peak", peak)
		}
		if value(8) < value(2)*5 || value(8) < value(13)*5 {
			t.Fatal("1 kHz band was not detected")
		}
	default:
		t.Fatal("no meter packet")
	}
	if m.partial != 0 || m.used != 0 {
		t.Fatal("split PCM frames lost alignment")
	}
}
