package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// signalContext returns a context that is cancelled on SIGINT or SIGTERM,
// with an additional timeout. This ensures all goroutines, API calls, and
// exec sessions are cleaned up when the user presses Ctrl+C.
//
// The returned cancel function must be called to release resources.
func signalContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	sigCtx, sigStop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	// Composite cancel: stops both signal listening and the timeout context.
	return sigCtx, func() {
		sigStop()
		cancel()
	}
}

// installForceQuit sets up a second SIGINT handler that force-exits the process.
// After the first Ctrl+C cancels the context gracefully, a second Ctrl+C
// immediately terminates with exit code 130 (128 + SIGINT).
//
// Call this once at the start of any long-running command.
func installForceQuit() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c // first signal is handled by signalContext
		// Show hint that we're shutting down.
		fmt.Fprintf(os.Stderr, "\ninterrupt received, cleaning up... (press Ctrl+C again to force quit)\n")
		<-c // second signal = force exit
		os.Exit(130)
	}()
}
