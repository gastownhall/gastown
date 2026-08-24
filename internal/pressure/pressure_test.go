package pressure

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// townRoot walks up from cwd looking for a Gas Town town root marker
// (mayor/rigs.json). Self-contained: pressure must not depend on internal/
// workspace to stay cycle-free and testable.
func townRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "mayor", "rigs.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func TestIsAgentSession(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"hq-mayor", true},
		{"rig-witness", true},
		{"rig-refinery", true},
		{"rig-polecat-abc", true},
		{"hq-deacon", true},
		{"hq-boot", true},
		{"rig-dog-fido", true},
		{"my-personal-session", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isAgentSession(tt.name); got != tt.want {
			t.Errorf("isAgentSession(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLoadAverage1_DoesNotPanic(t *testing.T) {
	load := hostLoadAvg()
	if load < 0 {
		t.Errorf("load average should be >= 0, got %f", load)
	}
}

func TestAvailableMemoryGB_DoesNotPanic(t *testing.T) {
	mem := hostMemAvailableGB()
	if mem < 0 {
		t.Errorf("available memory should be >= 0, got %f", mem)
	}
}

func TestCheck_AllDisabledReturnsOK(t *testing.T) {
	r, ok := Check(Threshold{}) // all zero
	if !ok || !r.OK {
		t.Fatalf("all-zero threshold should be OK")
	}
}

func TestCheck_NoPressureReturnsOK(t *testing.T) {
	t.Run("mem_ok", func(t *testing.T) {
		r, ok := Check(Threshold{MemAvailableGB: 0.0001})
		if !ok {
			t.Fatalf("expected OK, got Reason=%s", r.Reason)
		}
	})
}

// forceCheck exercises Check with a hand-built sample
func TestCheck_TiersBlockOnPressure(t *testing.T) {
	base := Result{NumCPU: 8, MemAvailableGB: 16, SwapTotalGB: 8, SwapFreeGB: 8, ActiveSessions: 0}
	withPerCore := func(r Result) Result {
		if r.NumCPU > 0 {
			r.LoadPerCore = r.LoadAvg1 / float64(r.NumCPU)
		}
		if r.SwapTotalGB > 0 {
			r.SwapUsedPercent = (r.SwapTotalGB - r.SwapFreeGB) / r.SwapTotalGB * 100
		}
		return r
	}
	base = withPerCore(base)

	// CPU tier.
	r, ok := check(Threshold{CPULoadPerCore: 1.0}, func() Result {
		b := base
		b.LoadAvg1 = 16 // 16/8 = 2.0 per core > 1.0
		return withPerCore(b)
	})
	if ok || r.Reason == "" {
		t.Fatalf("CPU tier should defer, got OK=%v Reason=%q", ok, r.Reason)
	}

	// Memory tier.
	r, ok = check(Threshold{MemAvailableGB: 1.0}, func() Result {
		b := base
		b.MemAvailableGB = 0.3
		return b
	})
	if ok || r.Reason == "" {
		t.Fatalf("memory tier should defer, got OK=%v Reason=%q", ok, r.Reason)
	}

	// Swap tier.
	r, ok = check(Threshold{SwapUsedPercent: 50.0}, func() Result {
		b := base
		b.SwapTotalGB = 8
		b.SwapFreeGB = 2 // 6/8 = 75% used > 50
		return withPerCore(b)
	})
	if ok || r.Reason == "" {
		t.Fatalf("swap tier should defer, got OK=%v Reason=%q", ok, r.Reason)
	}

	// Session tier.
	r, ok = check(Threshold{MaxSessions: 4}, func() Result {
		b := base
		b.ActiveSessions = 5
		return b
	})
	if ok || r.Reason == "" {
		t.Fatalf("session tier should defer, got OK=%v Reason=%q", ok, r.Reason)
	}

	// Explicit 0 disables session tier even when over the computed ceiling.
	r, ok = check(Threshold{MaxSessions: 0, MemAvailableGB: 0.0001}, func() Result {
		b := base
		b.ActiveSessions = 999
		return b
	})
	if !ok {
		t.Fatalf("explicit 0 MaxSessions disables session tier; got blocked: %q", r.Reason)
	}
}

func TestCheckHost_LiveHostDefersImpossibleThreshold(t *testing.T) {
	// An impossible memory floor can never be satisfied on a real host, so the
	// gate MUST defer with a stable reason. This pins the runtime contract
	// (defer, never kill) end-to-end on the live host — independent of any
	// config path resolution.
	_, ok := Check(Threshold{MemAvailableGB: 1e9})
	if ok {
		t.Fatal("expected live host to fail an impossible memory floor; the gate must defer, not allow an unchecked spawn")
	}
}

// TestCheckHostSpawn_RealTownConfigIsSafe proves the spawn gate end-to-end
// against the RESOLVED production town config (config load + threshold + LIVE
// host sample) WITHOUT spawning anything — critical: this test must never
// worsen host saturation by launching polecats. It asserts the gate honors the
// current load either by deferring (DeferredError) or by returning nil when the
// host is healthy. Both outcomes are valid; a panic or a non-deferred failure
// to read config is a regression.
func TestCheckHostSpawn_RealTownConfigIsSafe(t *testing.T) {
	root := townRoot()
	if root == "" {
		t.Skipf("no Gas Town town root from this test cwd (not a real incident)")
	}
	err := CheckHostSpawn(root)
	var derr *DeferredError
	switch {
	case err == nil:
		// healthy host: gate allows. Acceptable.
	case errors.As(err, &derr):
		// pressured host: gate defers with a stable, non-empty reason. Acceptable.
		if derr.Reason == "" {
			t.Fatalf("deferred spawn must carry a stable reason; got empty")
		}
	default:
		t.Fatalf("CheckHostSpawn must only return nil or *DeferredError; got %T: %v", err, err)
	}
}
