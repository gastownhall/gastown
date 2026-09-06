//go:build !windows

package util

import (
	"os"
	"os/exec"
	"syscall"
)

// SetProcessGroup configures a command to run in its own process group so that
// context cancellation kills the entire process tree, preventing orphaned children.
func SetProcessGroup(cmd *exec.Cmd) {
	if os.Getenv(ManagedReadEnv) == "1" {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}

// SetDetachedProcessGroup configures a command to run in its own process
// group without installing a cancellation hook.
func SetDetachedProcessGroup(cmd *exec.Cmd) {
	if os.Getenv(ManagedReadEnv) == "1" {
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
