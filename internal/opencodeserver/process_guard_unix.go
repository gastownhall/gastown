//go:build !windows && !linux

package opencodeserver

import "os/exec"

func configureServerCommand(*exec.Cmd) {}

func attachServerProcessGuard(pid int) (func() error, error) {
	return attachUnixParentWatchdog(pid)
}
