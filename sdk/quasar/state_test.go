package quasar

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func putVarint(b []byte, v uint64) []byte {
	for v >= 128 {
		b = append(b, byte(v)|128)
		v >>= 7
	}
	return append(b, byte(v))
}
func number(b []byte, f, v uint64) []byte { return putVarint(putVarint(b, f<<3), v) }
func blob(b []byte, f uint64, v []byte) []byte {
	return append(putVarint(putVarint(b, f<<3|2), uint64(len(v))), v...)
}

// message builds {252: {1: state, 2: text}, 253: kind}, like spotiya's tests.
func message(state int, kind uint64, text string) []byte {
	var body []byte
	if state >= 0 {
		body = number(body, 1, uint64(state))
	}
	body = blob(body, 2, []byte(text))
	return number(blob(nil, 252, body), 253, kind)
}
func frame(m []byte) []byte {
	return append(binary.LittleEndian.AppendUint32(nil, uint32(len(m))), m...)
}

func TestParse(t *testing.T) {
	for state, active := range map[int]bool{0: false, 1: true, 2: true, 3: true, 5: true, 6: false} {
		s, ok := Parse(message(state, 3, "включи музыку"))
		if !ok || int(s) != state || s.Active() != active {
			t.Fatal(state, s, ok)
		}
	}
	if s, ok := Parse(message(-1, 3, "")); !ok || s != Idle {
		t.Fatal("omitted IDLE", s, ok)
	}
	for name, m := range map[string][]byte{"other kind": message(1, 210, ""), "unknown state": message(99, 3, ""), "truncated": message(1, 3, "x")[:3], "garbage": {0xff, 0xff, 0xff}} {
		if _, ok := Parse(m); ok {
			t.Fatal("accepted", name)
		}
	}
}

func TestReadSkipsOtherFramesAndRejectsHugeOnes(t *testing.T) {
	var stream bytes.Buffer
	stream.Write(frame(message(1, 210, "другое")))
	stream.Write(frame(message(1, 3, "")))
	stream.Write(frame(message(2, 3, "")))
	stream.Write(frame(message(0, 3, "")))
	var got []State
	if e := Read(&stream, func(s State) { got = append(got, s) }); e != io.EOF {
		t.Fatal(e)
	}
	if len(got) != 3 || got[0] != Listening || got[1] != Speaking || got[2] != Idle {
		t.Fatal(got)
	}
	huge := binary.LittleEndian.AppendUint32(nil, maxFrame+1)
	if e := Read(bytes.NewReader(huge), func(State) {}); e == nil || e == io.EOF {
		t.Fatal("oversized frame accepted", e)
	}
}

func TestWatchReportsUnknownWhenDisconnected(t *testing.T) {
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	go func() {
		conn, e := listener.Accept()
		if e != nil {
			return
		}
		conn.Write(frame(message(2, 3, "")))
		conn.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	states := make(chan State, 8)
	go Watch(ctx, listener.Addr().String(), func(s State) { states <- s })
	for _, want := range []State{Speaking, Unknown} {
		select {
		case s := <-states:
			if s != want {
				t.Fatal("got", s, "want", want)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no state", want)
		}
	}
}
