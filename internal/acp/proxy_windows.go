//go:build windows

package acp

import (
	"os"

	"golang.org/x/sys/windows"

	"github.com/jonbaldie/gastown/internal/util"
)

// signalsToHandle returns the signals that Forward() should listen for.
// On Windows, only os.Interrupt is available (CTRL+C).
func signalsToHandle() []os.Signal {
	return []os.Signal{os.Interrupt}
}

// setupProcessGroup configures the command to suppress the transient console
// window that Windows creates for console-subsystem children spawned from a
// GUI/no-console parent (e.g. the daemon).
func (p *Proxy) setupProcessGroup() {
	util.SetDetachedProcessGroup(p.cmd)
}

// isProcessAlive checks if the agent process is still running.
// On Windows, use OpenProcess with limited query access to probe liveness.
func (p *Proxy) isProcessAlive() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}

	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(p.cmd.Process.Pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	_ = windows.CloseHandle(handle)
	return true
}

// terminateProcess kills the agent process.
// On Windows, we use Process.Kill() as there's no graceful SIGTERM equivalent.
func (p *Proxy) terminateProcess() {
	if p.cmd != nil && p.cmd.Process != nil {
		p.cmd.Process.Kill()
	}
}
