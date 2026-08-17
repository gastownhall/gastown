//go:build !windows

package worker

import "syscall"

func terminateServePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

func killServePID(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
