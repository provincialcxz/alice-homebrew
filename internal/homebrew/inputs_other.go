//go:build !linux

package homebrew

import (
	"context"
	"errors"
)

func (h *Inputs) Listen(ctx context.Context) {
	e := errors.New("speaker inputs are available only on Linux")
	h.setDevice(true, e)
	h.setDevice(false, e)
}
