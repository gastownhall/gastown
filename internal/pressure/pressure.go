// Package pressure centralizes host-pressure detection used by the Gas Town
// spawn gate and the daemon patrol loop. There is exactly ONE implementation:
// the spawn choke point (session.StartSession) and the daemon respawn loop both
// call Check/CheckHostSpawn, so pressure logic cannot drift between callers
// (the original defect that let concurrent spawns exhaust the host).
//
// A check is a cascade of independent tiers. Each tier is opt-in via its
// threshold; a zero threshold disables that tier. When a tier trips, Check
// returns OK=false with a stable Reason and a human DeferralReason. The caller
// never kills a running session — it defers/enqueues the spawn and retries.
package pressure

import (
	"fmt"
)

// Threshold configures the spawn/patrol pressure gate. Zero values disable the
// corresponding tier (the gate is opt-in by default; safe when unset).
type Threshold struct {
	// CPULoadPerCore, when > 0, trips if the 1m load average normalized per CPU
	// exceeds this value (e.g. 0.9). 0 disables.
	CPULoadPerCore float64
	// MemAvailableGB trips if available memory drops below this many GiB.
	// 0 disables.
	MemAvailableGB float64
	// SwapUsedPercent trips if swap used exceeds this percentage. 0 disables.
	SwapUsedPercent float64
	// MaxSessions trips if the number of live agent sessions exceeds this.
	// 0 disables (no cap).
	MaxSessions int
}

// Result is the raw host sample plus the outcome of the last tier that tripped.
type Result struct {
	OK               bool    // false if the spawn must be deferred
	Reason           string  // stable machine reason (see ReasonCPU, etc.)
	DeferralReason   string  // human-readable why this spawn was deferred
	LoadAvg1         float64 // 1-minute load average
	LoadPerCore      float64 // LoadAvg1 / NumCPU
	MemAvailableGB   float64 // available RAM in GiB
	SwapTotalGB      float64
	SwapFreeGB       float64
	SwapUsedPercent  float64 // 0..100
	ActiveSessions   int
	NumCPU           int
}

// Check evaluates the host against t. It returns (result, ok) where ok is true
// when no tier tripped. osOnly is false in production; tests inject samples.
func Check(t Threshold) (Result, bool) {
	return check(t, sampleHost)
}

// CheckHostSpawn is the spawn-gate entry point called from session.StartSession.
// It returns nil when a spawn is allowed, or *DeferredError describing why the
// spawn was deferred (caller should enqueue/requeue rather than fail hard).
func CheckHostSpawn(townRoot string) error {
	t := ThresholdFromConfig(townRoot)
	r, ok := Check(t)
	if ok {
		return nil
	}
	return Defer(r.Reason, r.DeferralReason)
}

func check(t Threshold, sample func() Result) (Result, bool) {
	r := sample()
	if t.CPULoadPerCore > 0 && r.NumCPU > 0 {
		if r.LoadPerCore > t.CPULoadPerCore {
			r.OK = false
			r.Reason = ReasonCPU
			r.DeferralReason = fmt.Sprintf(
				"load %.2f exceeds per-core ceiling %.2f on %d cores",
				r.LoadPerCore, t.CPULoadPerCore, r.NumCPU)
			return r, false
		}
	}
	if t.MemAvailableGB > 0 && r.MemAvailableGB < t.MemAvailableGB {
		r.OK = false
		r.Reason = ReasonMemory
		r.DeferralReason = fmt.Sprintf(
			"%.2f GiB available memory below %.2f GiB floor",
			r.MemAvailableGB, t.MemAvailableGB)
		return r, false
	}
	if t.SwapUsedPercent > 0 && r.SwapUsedPercent > t.SwapUsedPercent {
		r.OK = false
		r.Reason = ReasonSwap
		r.DeferralReason = fmt.Sprintf(
			"swap used %.1f%% exceeds %.1f%% ceiling",
			r.SwapUsedPercent, t.SwapUsedPercent)
		return r, false
	}
	if t.MaxSessions > 0 && r.ActiveSessions >= t.MaxSessions {
		r.OK = false
		r.Reason = ReasonSession
		r.DeferralReason = fmt.Sprintf(
			"%d active agent sessions at cap of %d",
			r.ActiveSessions, t.MaxSessions)
		return r, false
	}
	r.OK = true
	return r, true
}
