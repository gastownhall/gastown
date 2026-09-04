package web

import (
	"os/exec"

	"github.com/steveyegge/gastown/internal/util"
)

// configureWebCommand is the common process-lifecycle seam for dashboard
// subprocesses. Dashboard commands often launch bd descendants, so killing
// only the immediate child on timeout leaks work and connections into Dolt.
func configureWebCommand(cmd *exec.Cmd) {
	util.SetProcessGroup(cmd)
}
