//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// signalDaemonReload sends SIGUSR2 to the daemon process to trigger a reload.
func signalDaemonReload(process *os.Process) error {
	return process.Signal(syscall.SIGUSR2)
}

// signalDaemonLifecycle sends SIGUSR1 to the daemon process to trigger
// immediate lifecycle request processing (e.g., dispatch triggers).
func signalDaemonLifecycle(process *os.Process) error {
	return process.Signal(syscall.SIGUSR1)
}
