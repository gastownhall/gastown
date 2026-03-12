//go:build windows

package cmd

import (
	"fmt"
	"os"
)

// signalDaemonReload is a no-op on Windows since SIGUSR2 is not available.
func signalDaemonReload(process *os.Process) error {
	return fmt.Errorf("daemon reload signal not supported on Windows")
}

// signalDaemonLifecycle is a no-op on Windows since SIGUSR1 is not available.
func signalDaemonLifecycle(process *os.Process) error {
	return fmt.Errorf("daemon lifecycle signal not supported on Windows")
}
