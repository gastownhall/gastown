//go:build !windows

package cmd

import (
	"fmt"
	"strconv"
	"syscall"

	"github.com/jonbaldie/gastown/internal/tmux"
)

var (
	sigFreeze = syscall.SIGTSTP
	sigThaw   = syscall.SIGCONT
)

// signalSessionGroup sends a signal to the process group of a tmux session's
// pane process. This uses process-group signaling (kill(-pgid, sig)) instead
// of recursive pgrep, which is both safer and catches all descendants.
func signalSessionGroup(t *tmux.Tmux, sessionName string, sig syscall.Signal) error {
	pidStr, err := t.GetPanePID(sessionName)
	if err != nil {
		return fmt.Errorf("no PID: %w", err)
	}

	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return fmt.Errorf("invalid PID %q: %w", pidStr, err)
	}

	// Signal the entire process group. The pane's shell is typically the
	// process group leader, so -pid sends the signal to all processes in
	// the group (shell, claude, node, etc.) without needing to walk the
	// process tree.
	return syscall.Kill(-pid, sig)
}
