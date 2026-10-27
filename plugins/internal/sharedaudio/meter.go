package sharedaudio

import (
	"encoding/binary"
	"math"
	"net"
	"time"
)

// The stdout backend emits stereo S16_LE at 44100 Hz. Retain one analysis
// block in memory; send only levels, using a non-blocking one-packet queue.
type pcmMeter struct {
	block                 [1024]float64
	window                [1024]float64
	coeff                 [12]float64
	frame                 [4]byte
	partial, used, blocks int
	packets               chan [64]byte
	finished              chan struct{}
}

func newPCMMeter() *pcmMeter {
	m := &pcmMeter{packets: make(chan [64]byte, 1), finished: make(chan struct{})}
	for i := range m.window {
		m.window[i] = .5 - .5*math.Cos(2*math.Pi*float64(i)/1023)
	}
	for i, f := range []float64{80, 120, 180, 280, 430, 650, 1000, 1500, 2300, 3500, 5500, 8500} {
		m.coeff[i] = 2 * math.Cos(2*math.Pi*f/44100)
	}
	go func() {
		defer close(m.finished)
		conn, err := net.Dial("udp4", "127.0.0.1:18789")
		if err == nil {
			defer conn.Close()
		}
		for packet := range m.packets {
			if conn != nil {
				_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Millisecond))
				_, _ = conn.Write(packet[:])
			}
		}
	}()
	return m
}

func (m *pcmMeter) add(pcm []byte) {
	for _, b := range pcm {
		m.frame[m.partial] = b
		m.partial++
		if m.partial != 4 {
			continue
		}
		m.partial = 0
		left := float64(int16(binary.LittleEndian.Uint16(m.frame[:2]))) / 32768
		right := float64(int16(binary.LittleEndian.Uint16(m.frame[2:]))) / 32768
		m.block[m.used] = (left + right) / 2
		m.used++
		if m.used != len(m.block) {
			continue
		}
		m.used = 0
		m.blocks++
		if m.blocks%2 != 0 {
			continue
		}
		packet := m.summary()
		select {
		case m.packets <- packet:
		default:
		}
	}
}

func (m *pcmMeter) summary() [64]byte {
	var packet [64]byte
	copy(packet[:], "ALICEAU1")
	var values [14]float64
	for _, x := range m.block {
		values[0] += x * x
		values[1] = math.Max(values[1], math.Abs(x))
	}
	values[0] = math.Sqrt(values[0] / 1024)
	for k, coeff := range m.coeff {
		var a, b float64
		for i, x := range m.block {
			next := x*m.window[i] + coeff*a - b
			b = a
			a = next
		}
		values[k+2] = 4 * math.Sqrt(math.Max(0, a*a+b*b-coeff*a*b)) / 1024
	}
	for i, x := range values {
		binary.LittleEndian.PutUint32(packet[8+i*4:], math.Float32bits(float32(math.Min(1, x))))
	}
	return packet
}

func (m *pcmMeter) close() { close(m.packets); <-m.finished }
