package web

import (
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// configureWebCommand is the common process-lifecycle seam for dashboard
// subprocesses. Dashboard commands often launch bd descendants, so killing
// only the immediate child on timeout leaks work and connections into Dolt.
func configureWebCommand(cmd *exec.Cmd) {
	util.SetProcessGroup(cmd)
	cmd.WaitDelay = time.Second
}

// Keep synchronous read descendants in the group owned by the dashboard.
// Otherwise gt mail detaches bd, which survives cancellation of the outer gt.
func configureWebReadCommand(cmd *exec.Cmd) {
	env := cmd.Environ()
	cmd.Env = make([]string, 0, len(env)+1)
	for _, value := range env {
		if !strings.HasPrefix(value, util.ManagedReadEnv+"=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, util.ManagedReadEnv+"=1")
}

func managedDashboardRead(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "status" || args[0] == "ready" {
		return true
	}
	if len(args) < 2 {
		return false
	}
	switch args[0] + " " + args[1] {
	case "mail inbox", "mail check", "hooks list", "convoy list", "convoy status", "rig list", "agents list", "crew list", "polecat list":
		return true
	}
	return false
}
