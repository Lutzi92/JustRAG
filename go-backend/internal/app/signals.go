package app

import (
	"os"
	"os/signal"
	"syscall"
)

// newShutdownSignals registers for process termination signals and returns a
// channel plus a cleanup function that removes the registration.
func newShutdownSignals() (<-chan os.Signal, func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	return sigCh, func() {
		signal.Stop(sigCh)
	}
}
