//go:build !windows

package opencodeserver

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func attachUnixParentWatchdog(serverPID int) (func() error, error) {
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	const script = `group=$1
IFS= read -r _ <&3
kill -TERM -"$group" 2>/dev/null || true
sleep 1
kill -KILL -"$group" 2>/dev/null || true`
	watchdog := exec.Command("sh", "-c", script, "opencode-parent-watchdog", strconv.Itoa(serverPID))
	watchdog.ExtraFiles = []*os.File{readPipe}
	watchdog.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := watchdog.Start(); err != nil {
		_ = readPipe.Close()
		_ = writePipe.Close()
		return nil, err
	}
	_ = readPipe.Close()
	return func() error {
		killErr := watchdog.Process.Kill()
		closeErr := writePipe.Close()
		waitErr := watchdog.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			waitErr = nil
		}
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		return errors.Join(killErr, closeErr, waitErr)
	}, nil
}
