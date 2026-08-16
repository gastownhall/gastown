//go:build !windows

package process

import "syscall"

// Alive reports whether pid exists. EPERM means the process is alive but this
// caller cannot signal it — the same answer gt status and tmux must share.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
