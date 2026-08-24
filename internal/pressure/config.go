package pressure

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/config"
)

// ThresholdFromConfig builds a Threshold from the town operational config at
// townRoot, resolving all defaults via the config accessors. This is the single
// owner mapping from settings -> gate so spawn call-sites stay uniform.
//
// An unset config (all defaults) yields the safe compiled defaults:
// memory 0.5GB, swap 80%, session ceiling NumCPU*2 (>=4); CPU load-per-core
// stays opt-in (0 = disabled) since it is noisy on shared hosts.
func ThresholdFromConfig(townRoot string) Threshold {
	cfg := config.LoadOperationalConfig(townRoot)
	return ThresholdFromDaemon(cfg.GetDaemonConfig())
}

// ThresholdFromDaemon builds a Threshold from a DaemonThresholds, reusing the
// same accessors the daemon already uses (no second config load).
func ThresholdFromDaemon(d *config.DaemonThresholds) Threshold {
	if d == nil {
		d = &config.DaemonThresholds{}
	}
	return Threshold{
		CPULoadPerCore:  d.PressureCPUThresholdV(),
		MemAvailableGB:  d.PressureMemThresholdGBV(),
		SwapUsedPercent: d.PressureSwapUsedPercentV(),
		MaxSessions:     d.PressureMaxSessionsV(),
	}
}

// Sentinel reasons returned in Result.Reason, stable for logging/grep.
const (
	ReasonCPU     = "cpu pressure"
	ReasonMemory  = "memory pressure"
	ReasonSwap    = "swap pressure"
	ReasonSession = "session cap"
)

// DeferredError wraps a pressure check failure so callers can distinguish a
// deferred spawn (retry later) from a fatal error.
type DeferredError struct {
	Reason          string // stable machine reason (e.g. pressure.ReasonCPU)
	DeferralReason  string // human-readable why this spawn was deferred
}

func (e *DeferredError) Error() string {
	if e.DeferralReason != "" {
		return fmt.Sprintf("spawn deferred: %s (%s)", e.Reason, e.DeferralReason)
	}
	return fmt.Sprintf("spawn deferred: %s", e.Reason)
}

// Defer returns a DeferredError carrying the pressure reason + human detail.
func Defer(reason, deferralReason string) *DeferredError {
	return &DeferredError{Reason: reason, DeferralReason: deferralReason}
}
