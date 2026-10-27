//go:build spotify && linux

package main

import (
	"context"
	"io"
	"log"
	"os"
	"sync/atomic"
	"syscall"
	"time"
)

// pipeAudio creates the FIFO the libpulse shim writes to and copies what
// arrives into w. The FIFO is opened read-write, so it never reports EOF
// when Soloist restarts and reopens it.
func pipeAudio(ctx context.Context, path string, w io.Writer, last *atomic.Int64) error {
	os.Remove(path)
	if e := syscall.Mkfifo(path, 0600); e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_RDWR, 0)
	if e != nil {
		return e
	}
	go func() {
		<-ctx.Done()
		f.Close()
	}()
	go func() {
		// 4096 bytes is 23 ms of 44.1 kHz stereo S16.
		buf := make([]byte, 4096)
		for {
			n, e := f.Read(buf)
			if n > 0 {
				last.Store(time.Now().UnixNano())
				if _, e := w.Write(buf[:n]); e != nil {
					log.Printf("audio output: %v", e)
				}
			}
			if e != nil {
				return
			}
		}
	}()
	return nil
}
