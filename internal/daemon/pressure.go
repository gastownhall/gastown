package daemon

import (
	"github.com/steveyegge/gastown/internal/pressure"
)

// Result holds the outcome of a host pressure check at spawn time.
// Aliased to the canonical pressure type so the daemon patrol loop and the
// session spawn gate share one definition (no duplicated pressure logic).
type Result = pressure.Result

// checkPressure evaluates host pressure via the canonical pressure package.
// The daemon patrol loop (refinery/dog/polecat respawn) calls this to DEFER
// a respawn rather than kill a live session — never SIGTERM. The actual spawn
// choke point (session.StartSession) calls pressure.CheckHostSpawn directly.
func (d *Daemon) checkPressure(_ string) Result {
	t := pressure.ThresholdFromDaemon(d.loadOperationalConfig().GetDaemonConfig())
	r, _ := pressure.CheckAt(t, d.config.TownRoot)
	return r
}
