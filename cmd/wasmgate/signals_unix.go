//go:build !windows

package main

import (
	"os"
	"syscall"
)

// shutdownSignals returns the signals that trigger a graceful shutdown.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt, syscall.SIGTERM}
}

// reloadSignal returns the signal that triggers a configuration reload,
// or nil if the platform has none.
func reloadSignal() os.Signal {
	return syscall.SIGHUP
}
