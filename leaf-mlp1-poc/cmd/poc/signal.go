package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// signalContext is cancelled on Ctrl+C / SIGTERM, so a long --hub-sync stops
// between steps with its progress saved instead of dying mid-write.
func signalContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
