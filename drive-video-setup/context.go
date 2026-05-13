//go:build windows

package main

import (
	"context"
	"os/signal"
	"syscall"
	"time"
)

// newBootstrapContext creates the cancellable context the
// bootstrap pipeline runs under.
//
// Two cancellation paths:
//
//   1. Hard timeout (15 minutes). Covers the worst-case scenario
//      where a user on a 1 Mbps connection waits 12 minutes for
//      the 100 MB MSIX download. Beyond that, something is wrong;
//      better to surface a clear timeout error than spin forever.
//
//   2. Ctrl-C / WM_CLOSE / SIGTERM. Lets the user cancel the
//      install. Also fires automatically when the user clicks
//      the X button on the progress window (we forward the close
//      event to ctx cancel via the GUI layer).
func newBootstrapContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	return ctx, func() {
		stop()
		cancel()
	}
}
