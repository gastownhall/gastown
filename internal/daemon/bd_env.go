package daemon

import (
	"os"
	"path/filepath"

	"github.com/jonbaldie/gastown/internal/beads"
	agentconfig "github.com/jonbaldie/gastown/internal/config"
)

// bdReadOnlyEnv returns an environment slice for read-only bd/gt subprocess
// calls invoked by the daemon. It forces BD_DOLT_AUTO_COMMIT=off so that
// read-only operations (status checks, list, show) do not request a Dolt
// auto-commit on completion. Without this, every read-only call opens a
// fresh connection to attempt a no-op commit, producing thousands of
// failed-but-counted connections per hour on idle towns and spamming
// dolt.log. See gh#3596.
//
// Existing BD_DOLT_AUTO_COMMIT entries are filtered out before appending
// the authoritative "off" value, because glibc getenv() returns the first
// matching entry — a stale "on" earlier in the slice would otherwise win.
func bdReadOnlyEnv() []string {
	return beads.BuildReadOnlyRoutingBDEnv(os.Environ(), "")
}

func bdReadOnlyRoutingEnv(townRoot string) []string {
	fallback := ""
	base := os.Environ()
	if townRoot != "" {
		fallback = filepath.Join(townRoot, ".beads")
		base = agentconfig.NormalizeConfiguredDoltEnv(base, townRoot)
	}
	return beads.BuildReadOnlyRoutingBDEnv(base, fallback)
}

func bdMutationRoutingEnv(townRoot string) []string {
	fallback := ""
	base := os.Environ()
	if townRoot != "" {
		fallback = filepath.Join(townRoot, ".beads")
		base = agentconfig.NormalizeConfiguredDoltEnv(base, townRoot)
	}
	return beads.BuildMutationRoutingBDEnv(base, fallback)
}

func bdReadOnlyPinnedEnv(beadsDir string) []string {
	base := os.Environ()
	if townRoot := beads.FindTownRoot(filepath.Dir(beads.ResolveBeadsDir(beadsDir))); townRoot != "" {
		base = agentconfig.NormalizeConfiguredDoltEnv(base, townRoot)
	}
	return beads.BuildReadOnlyPinnedBDEnv(base, beadsDir)
}
