//go:build windows

package main

import "os"

// shutdownSignals returns the signals that trigger a graceful shutdown.
// On Windows only Ctrl+C (os.Interrupt) is portably supported.
func shutdownSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// reloadSignal returns the signal that triggers a configuration reload.
// Windows has no SIGHUP, so reload is not wired to a signal here (a control
// plane / CLI will trigger it in a later milestone).
func reloadSignal() os.Signal {
	return nil
}
