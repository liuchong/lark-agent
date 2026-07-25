//go:build windows

package cmd

import (
	"context"
	"os"
	"os/signal"
)

func commandSignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt)
}
