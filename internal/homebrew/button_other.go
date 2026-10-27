//go:build !linux

package homebrew

import (
	"context"
	"errors"
)

func (g *ButtonGate) Listen(ctx context.Context) {
	g.setDevice(false, errors.New("speaker buttons are available only on Linux"))
}
