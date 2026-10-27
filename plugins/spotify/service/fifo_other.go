//go:build spotify && !linux

package main

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

func pipeAudio(context.Context, string, io.Writer, *atomic.Int64) error {
	return errors.New("the audio FIFO needs Linux")
}
