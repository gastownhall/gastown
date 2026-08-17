package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/worker"
)

// stopTownDoltAndWorker stops this town's Dolt SQL server and
// `gt worker serve --town <town>` process. Other towns are left running.
func stopTownDoltAndWorker(townRoot string) {
	if townRoot == "" {
		return
	}
	stopTownDoltServer(townRoot)
	stopTownWorkerServe(townRoot)
}

func stopTownDoltServer(townRoot string) {
	cfg := doltserver.DefaultConfig(townRoot)
	if cfg.IsRemote() {
		fmt.Printf("  %s Dolt is remote (%s) — not stopped\n",
			style.Dim.Render("○"), cfg.HostPort())
		return
	}

	idleMonitors := doltserver.FindIdleMonitorProcesses(townRoot)
	if len(idleMonitors) > 0 {
		stopped := stopIdleMonitors(idleMonitors)
		if stopped > 0 {
			fmt.Printf("  %s Dolt idle-monitors stopped (%d)\n",
				style.Bold.Render("✓"), stopped)
		}
	}

	if _, err := os.Stat(cfg.DataDir); err == nil {
		running, pid, err := doltserver.IsRunning(townRoot)
		if err != nil {
			fmt.Printf("  %s Dolt status check failed: %v\n",
				style.Bold.Render("⚠"), err)
		} else if running {
			if err := doltserver.Stop(townRoot); err != nil {
				fmt.Printf("  %s Failed to stop Dolt (PID %d): %v\n",
					style.Bold.Render("✗"), pid, err)
			} else {
				fmt.Printf("  %s Dolt stopped (was PID %d)\n",
					style.Bold.Render("✓"), pid)
			}
		} else {
			fmt.Printf("  %s Dolt not running\n", style.Dim.Render("○"))
		}
	}

	if err := doltserver.KillImposters(townRoot); err != nil {
		fmt.Printf("  %s Dolt imposter cleanup failed: %v\n",
			style.Bold.Render("⚠"), err)
	}
	if pids := findOrphanDoltServers(townRoot); len(pids) > 0 {
		if stopped := stopOrphanDoltServers(pids); stopped > 0 {
			fmt.Printf("  %s Stopped %d orphan Dolt server(s)\n",
				style.Bold.Render("✓"), stopped)
		}
	}

	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil {
		fmt.Printf("  %s Could not verify Dolt stop: %v\n",
			style.Bold.Render("⚠"), err)
		return
	}
	if running {
		fmt.Printf("  %s Dolt still running after stop (PID %d)\n",
			style.Bold.Render("⚠"), pid)
	}
}

func stopTownWorkerServe(townRoot string) {
	pids := worker.FindServePIDs(townRoot)
	if len(pids) == 0 {
		fmt.Printf("  %s Worker serve not running\n", style.Dim.Render("○"))
		_ = os.Remove(worker.SocketPath(townRoot))
		_ = os.Remove(worker.PortPath(townRoot))
		return
	}
	stopped := worker.StopServe(townRoot)
	if leftover := worker.FindServePIDs(townRoot); len(leftover) > 0 {
		fmt.Printf("  %s Worker serve still running (PIDs %v)\n",
			style.Bold.Render("⚠"), leftover)
		return
	}
	if stopped > 0 {
		fmt.Printf("  %s Worker serve stopped (%d, town %s)\n",
			style.Bold.Render("✓"), stopped, filepath.Base(townRoot))
		return
	}
	fmt.Printf("  %s Worker serve not running\n", style.Dim.Render("○"))
}

func verifyTownInfraStopped(townRoot string) {
	running, pid, err := doltserver.IsRunning(townRoot)
	switch {
	case err != nil:
		fmt.Printf("  %s Could not verify Dolt: %v\n", style.Bold.Render("⚠"), err)
	case running:
		fmt.Printf("  %s Dolt still running (PID %d)\n", style.Bold.Render("⚠"), pid)
	default:
		fmt.Printf("  %s Dolt not running\n", style.Bold.Render("✓"))
	}
	if leftover := worker.FindServePIDs(townRoot); len(leftover) > 0 {
		fmt.Printf("  %s Worker serve still running (PIDs %v)\n",
			style.Bold.Render("⚠"), leftover)
		return
	}
	fmt.Printf("  %s Worker serve not running\n", style.Bold.Render("✓"))
}
