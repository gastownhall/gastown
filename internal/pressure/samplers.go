package pressure

import (
	"runtime"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/tmux"
)

// numCPU reports the usable CPU count for per-core load normalization.
func numCPU() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

// isAgentSession reports whether a tmux session name corresponds to a Gas Town
// agent session (mayor, rig polecat/dog/refinery, hq agents). These names are
// the canonical naming convention from gt prime / rig dispatch.
func isAgentSession(name string, prefixes map[string]struct{}) bool {
	switch {
	case name == "hq-mayor", name == "hq-deacon", name == "hq-boot", name == "hq-witness":
		return true
	case len(name) > 4 && name[:4] == "rig-":
		// rig-<kind>-<slug>: polecat, dog, refinery, witness, deacon.
		return true
	case len(name) > 4 && name[:4] == "hq-":
		return true
	default:
		prefix, _, ok := strings.Cut(name, "-")
		if !ok {
			return false
		}
		_, registered := prefixes[prefix]
		return registered
	}
}

// countAgentSessions enumerates live tmux sessions that are Gas Town agent
// sessions. Uses the canonical tmux package (the owner of session enumeration),
// not a re-implementation.
func countAgentSessions(townRoot string) int {
	prefixes := make(map[string]struct{})
	if townRoot != "" {
		for _, prefix := range config.AllRigPrefixes(townRoot) {
			prefixes[strings.TrimSuffix(prefix, "-")] = struct{}{}
		}
	}
	t := tmux.NewTmux()
	sessions, err := t.ListSessions()
	if err != nil {
		return 0
	}
	n := 0
	for _, s := range sessions {
		if isAgentSession(s, prefixes) {
			n++
		}
	}
	return n
}

// sampleHost is the default sampler: reads CPU/memory/swap from the platform
// implementation and counts live agent sessions. Tests inject a fake via
// check(t, sampleFn).
func sampleHost(townRoot string) Result {
	return Result{
		LoadAvg1:        hostLoadAvg(),
		LoadPerCore:     loadPerCore(hostLoadAvg()),
		MemAvailableGB:  hostMemAvailableGB(),
		SwapTotalGB:     hostSwapTotalGB(),
		SwapFreeGB:      hostSwapFreeGB(),
		SwapUsedPercent: swapUsedPercent(hostSwapTotalGB(), hostSwapFreeGB()),
		ActiveSessions:  countAgentSessions(townRoot),
		NumCPU:          numCPU(),
	}
}

func loadPerCore(load float64) float64 {
	cpu := numCPU()
	if cpu <= 0 || load <= 0 {
		return 0
	}
	return load / float64(cpu)
}

func swapUsedPercent(total, free float64) float64 {
	if total <= 0 {
		return 0
	}
	used := total - free
	if used < 0 {
		used = 0
	}
	return used / total * 100
}

// Platform hooks. Implemented per-OS in pressure_linux.go / _darwin.go /
// _other.go / _windows.go. They MUST each define:
//   hostLoadAvg() float64
//   hostMemAvailableGB() float64
//   hostSwapTotalGB() float64
//   hostSwapFreeGB() float64
