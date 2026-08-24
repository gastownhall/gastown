package pressure

import (
	"testing"
)

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
