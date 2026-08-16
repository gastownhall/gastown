package testutil

import (
	"github.com/jonbaldie/gastown/internal/beads"
	"os"
	"os/exec"
	"strings"
)

// CleanGTEnv returns os.Environ() with GT_* and BD_* variables removed, except
// GT_DOLT_PORT, GT_DOLT_HOST, and GT_TEST_EXTERNAL_DOLT which are preserved so
// subprocesses connect to and reuse the test Dolt server. BEADS_DOLT_PORT and
// BEADS_DOLT_SERVER_HOST (prefix BEADS_, not BD_) pass through implicitly since
// only BD_* is stripped.
//
// Use this when setting cmd.Env on bd/gt subprocess calls in tests.
// If you do NOT set cmd.Env, the process env (including GT_DOLT_PORT) is
// inherited automatically — no need for this function in that case.
func CleanGTEnv(extraEnv ...string) []string {
	var clean []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GT_") &&
			!strings.HasPrefix(e, "GT_DOLT_PORT=") &&
			!strings.HasPrefix(e, "GT_DOLT_HOST=") &&
			!strings.HasPrefix(e, "GT_TEST_EXTERNAL_DOLT=") {
			continue
		}
		if strings.HasPrefix(e, "BD_") {
			continue
		}
		clean = append(clean, e)
	}
	return append(clean, extraEnv...)
}

// NewBDCommand creates an exec.Command for the bd CLI. Dolt targeting comes
// from beads.Spawn so host, port, and database choice stay in Authority.
//
// Use this instead of bare beads.Spawn(...) in tests.
func NewBDCommand(args ...string) *exec.Cmd {
	return beads.Spawn(args...)
}

// NewGTCommand creates an exec.Command for the gt CLI with GT_DOLT_PORT
// automatically propagated. The command inherits the full process environment
// (which includes GT_DOLT_PORT set by TestMain).
//
// Use this instead of bare exec.Command("gt", ...) in tests.
func NewGTCommand(args ...string) *exec.Cmd {
	return exec.Command("gt", args...)
}

// NewIsolatedBDCommand creates an exec.Command for the bd CLI with GT_*/BD_*
// env stripped except GT_DOLT_PORT and BEADS_DOLT_PORT. Use this when you need
// to isolate a subprocess from the parent Gas Town workspace but still route
// to the test Dolt server.
func NewIsolatedBDCommand(args ...string) *exec.Cmd {
	cmd := beads.Spawn(args...)
	cmd.Env = CleanGTEnv()
	return cmd
}

// NewIsolatedGTCommand creates an exec.Command for the gt CLI with GT_*/BD_*
// env stripped except GT_DOLT_PORT and BEADS_DOLT_PORT. Use this when you need
// to isolate a subprocess from the parent Gas Town workspace but still route
// to the test Dolt server.
func NewIsolatedGTCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("gt", args...)
	cmd.Env = CleanGTEnv()
	return cmd
}
