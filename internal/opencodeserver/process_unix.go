//go:build !windows

package opencodeserver

import "syscall"

func terminateProcessTree(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
