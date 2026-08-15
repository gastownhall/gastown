//go:build linux

package opencodeserver

import (
	"os/exec"
	"syscall"
)

func configureServerCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
}

func attachServerProcessGuard(pid int) (func() error, error) {
	return attachUnixParentWatchdog(pid)
}
