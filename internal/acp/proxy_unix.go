//go:build unix

package acp

import (
	"os"
	"syscall"
	"time"

	"github.com/jonbaldie/gastown/internal/util"
)

// signalsToHandle returns the signals that Forward() should listen for.
// On Unix, we handle both SIGTERM and SIGINT for graceful shutdown.
func signalsToHandle() []os.Signal {
	return []os.Signal{syscall.SIGTERM, syscall.SIGINT}
}

// setupProcessGroup configures the command to run in its own process group.
// This allows us to terminate the agent and all its children on shutdown.
func (p *Proxy) setupProcessGroup() {
	util.SetProcessGroup(p.cmd)
}

// isProcessAlive checks if the agent process is still running.
// On Unix, we use signal 0 to check process liveness.
func (p *Proxy) isProcessAlive() bool {
	if p.cmd == nil || p.cmd.Process == nil {
		return false
	}
	err := p.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// terminateProcess gracefully terminates the agent process.
// On Unix, we send SIGTERM to the process group, then SIGKILL after 2 seconds
// if the process hasn't exited.
func (p *Proxy) terminateProcess() {
	if p.cmd != nil && p.cmd.Process != nil {
		debugLog(p.townRoot, "[Proxy] Shutdown: sending SIGTERM to agent process (pid=%d)", p.cmd.Process.Pid)
		pgid, err := syscall.Getpgid(p.cmd.Process.Pid)
		if err == nil {
			// SAFETY: Never kill our own process group during tests/local runs
			myPgid, _ := syscall.Getpgid(0)
			if pgid != myPgid {
				// Send SIGTERM to the entire process group
				_ = syscall.Kill(-pgid, syscall.SIGTERM)
			} else {
				// Only kill the process itself if it shares our group
				_ = syscall.Kill(p.cmd.Process.Pid, syscall.SIGTERM)
			}
		} else {
			_ = syscall.Kill(p.cmd.Process.Pid, syscall.SIGTERM)
		}

		time.AfterFunc(2*time.Second, func() {
			if p.cmd.ProcessState == nil || !p.cmd.ProcessState.Exited() {
				if pgid == 0 {
					pgid, _ = syscall.Getpgid(p.cmd.Process.Pid)
				}
				myPgid, _ := syscall.Getpgid(0)
				if pgid > 0 && pgid != myPgid {
					_ = syscall.Kill(-pgid, syscall.SIGKILL)
				} else {
					_ = syscall.Kill(p.cmd.Process.Pid, syscall.SIGKILL)
				}
			}
		})
	}
}
