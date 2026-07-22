package testutil

import (
	"os"
	"os/exec"
	"strings"
)

// doltTargetSelectorEnvVars select a database or local data directory instead
// of the configured SQL endpoint. They must never leak into tests: a developer
// commonly runs tests from an agent session where these variables point at the
// production town or rig database.
var doltTargetSelectorEnvVars = []string{
	"BEADS_DIR",
	"BEADS_DB",
	"BD_DB",
	"BEADS_SHARED_SERVER_DIR",
	"BEADS_DOLT_DATA_DIR",
	"BEADS_DOLT_DATABASE",
	"BEADS_DOLT_SERVER_DATABASE",
	"BEADS_DOLT_HOST",
	"BEADS_DOLT_SHARED_SERVER",
	"BEADS_DOLT_SERVER_MODE",
	"BEADS_DOLT_SERVER_SOCKET",
	"GT_DOLT_DATA",
}

// CleanGTEnv returns os.Environ() with GT_* and BD_* variables removed, except
// GT_DOLT_PORT, GT_DOLT_HOST, and GT_TEST_EXTERNAL_DOLT which are preserved so
// subprocesses connect to and reuse the test Dolt server. Dolt database and
// local-data selectors are always stripped so an inherited agent environment
// cannot route a test command back to production storage.
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
		key, _, _ := strings.Cut(e, "=")
		if isDoltTargetSelector(key) {
			continue
		}
		clean = append(clean, e)
	}
	return append(clean, extraEnv...)
}

func isDoltTargetSelector(key string) bool {
	for _, selector := range doltTargetSelectorEnvVars {
		if strings.EqualFold(key, selector) {
			return true
		}
	}
	return false
}

// NewBDCommand creates an exec.Command for the bd CLI with GT_DOLT_PORT
// automatically propagated. The command inherits the full process environment
// (which includes GT_DOLT_PORT set by TestMain).
//
// Use this instead of bare exec.Command("bd", ...) in tests.
func NewBDCommand(args ...string) *exec.Cmd {
	return exec.Command("bd", args...)
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
	cmd := exec.Command("bd", args...)
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
