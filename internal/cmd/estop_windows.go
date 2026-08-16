//go:build windows

package cmd

import (
	"fmt"
	"syscall"

	"github.com/jonbaldie/gastown/internal/tmux"
)

var (
	sigFreeze syscall.Signal = 0
	sigThaw   syscall.Signal = 0
)

// signalSessionGroup is a no-op on Windows since SIGTSTP/SIGCONT and
// process-group signaling are not available.
func signalSessionGroup(t *tmux.Tmux, sessionName string, sig syscall.Signal) error {
	return fmt.Errorf("process-group signaling not supported on Windows")
}
