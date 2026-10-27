// Package quasar follows Alice's activity through the firmware's local event
// socket. Frames are a little-endian u32 length and a QuasarMessage protobuf:
// field 253 is the message kind and 252 its payload. Only kind 3 (AliceState)
// is decoded, and only its state number: recognized phrases and every other
// payload are skipped without being kept.
//
// Protocol notes come from spotiya (MIT, github.com/klockky/spotiya).
package quasar

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"time"
)

// Address is the event socket on the Mini 1 and Mini 2.
const Address = "127.0.0.1:9875"

const maxFrame = 1 << 20

type State int

const (
	Unknown   State = -1
	Idle      State = 0
	Listening State = 1
	Speaking  State = 2
	Busy      State = 3 // thinking about a request
	Shazam    State = 5 // recognizing music
	None      State = 6
)

// Active reports whether Alice is listening, thinking or answering.
func (s State) Active() bool {
	return s == Listening || s == Speaking || s == Busy || s == Shazam
}

func varint(b []byte, i *int) (uint64, bool) {
	var v uint64
	for shift := uint(0); shift < 70; shift += 7 {
		if *i >= len(b) {
			return 0, false
		}
		c := b[*i]
		*i++
		if shift == 63 && c > 1 {
			return 0, false
		}
		v |= uint64(c&127) << shift
		if c < 128 {
			return v, true
		}
	}
	return 0, false
}

// field returns the first occurrence of a protobuf field as a number (wire
// type 0) or bytes (wire type 2).
func field(b []byte, wanted uint64) (number uint64, data []byte, found bool) {
	for i := 0; i < len(b); {
		key, ok := varint(b, &i)
		if !ok || key>>3 == 0 {
			return 0, nil, false
		}
		switch key & 7 {
		case 0:
			v, ok := varint(b, &i)
			if !ok {
				return 0, nil, false
			}
			if key>>3 == wanted {
				return v, nil, true
			}
		case 2:
			n, ok := varint(b, &i)
			if !ok || n > uint64(len(b)-i) {
				return 0, nil, false
			}
			if key>>3 == wanted {
				return 0, b[i : i+int(n)], true
			}
			i += int(n)
		case 1, 5:
			size := 8
			if key&7 == 5 {
				size = 4
			}
			if size > len(b)-i {
				return 0, nil, false
			}
			i += size
		default:
			return 0, nil, false
		}
	}
	return 0, nil, false
}

// Parse returns Alice's state when frame is an AliceState message.
func Parse(frame []byte) (State, bool) {
	if kind, _, ok := field(frame, 253); !ok || kind != 3 {
		return Unknown, false
	}
	_, payload, ok := field(frame, 252)
	if !ok {
		return Unknown, false
	}
	state, _, present := field(payload, 1)
	if !present {
		return Idle, true // proto3 omits the default IDLE value
	}
	switch s := State(state); s {
	case Idle, Listening, Speaking, Busy, Shazam, None:
		return s, true
	}
	return Unknown, false
}

// Read reports states from r until it fails.
func Read(r io.Reader, onState func(State)) error {
	var size [4]byte
	buf := make([]byte, 0, 4096)
	for {
		if _, e := io.ReadFull(r, size[:]); e != nil {
			return e
		}
		n := binary.LittleEndian.Uint32(size[:])
		if n == 0 || n > maxFrame {
			return errors.New("invalid Alice event frame length")
		}
		if cap(buf) < int(n) || cap(buf) > 64<<10 {
			buf = make([]byte, n)
		}
		buf = buf[:n]
		if _, e := io.ReadFull(r, buf); e != nil {
			return e
		}
		if s, ok := Parse(buf); ok {
			onState(s)
		}
	}
}

// Watch follows address until ctx ends, reconnecting every 2 seconds. While
// disconnected it reports Unknown, so callers never stay ducked.
func Watch(ctx context.Context, address string, onState func(State)) {
	for ctx.Err() == nil {
		var d net.Dialer
		conn, e := d.DialContext(ctx, "tcp", address)
		if e == nil {
			stop := context.AfterFunc(ctx, func() { conn.Close() })
			_ = Read(conn, onState)
			stop()
			conn.Close()
		}
		onState(Unknown)
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}
