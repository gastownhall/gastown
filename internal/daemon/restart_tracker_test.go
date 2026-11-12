package daemon

import (
	"testing"
	"time"
)

// op-r9fx: crash-loop state must decay. While the flag is set the daemon
// performs no restarts, so RecordRestart's stability reset can never fire —
// without an expiry the flag is permanent and heartbeat supervision stays
// disabled forever (observed: 6 weeks on a stale deacon flag).

func TestDefaultConfig_HasCrashLoopExpiry(t *testing.T) {
	cfg := DefaultRestartTrackerConfig()
	if cfg.CrashLoopExpiry != time.Hour {
		t.Fatalf("CrashLoopExpiry = %v, want 1h", cfg.CrashLoopExpiry)
	}

	filled := RestartTrackerConfig{}.withDefaults()
	if filled.CrashLoopExpiry != time.Hour {
		t.Fatalf("withDefaults CrashLoopExpiry = %v, want 1h", filled.CrashLoopExpiry)
	}
}

func TestIsInCrashLoop_ActiveBeforeExpiry(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		LastRestart:    time.Now().Add(-5 * time.Minute),
		RestartCount:   5,
		CrashLoopSince: time.Now().Add(-5 * time.Minute),
	}

	if !rt.IsInCrashLoop("deacon") {
		t.Fatal("IsInCrashLoop = false for 5-minute-old crash loop, want true")
	}
	if rt.CanRestart("deacon") {
		t.Fatal("CanRestart = true during active crash loop, want false")
	}
}

func TestIsInCrashLoop_ExpiresAfterCrashLoopExpiry(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		LastRestart:    time.Now().Add(-2 * time.Hour),
		RestartCount:   5,
		CrashLoopSince: time.Now().Add(-2 * time.Hour),
	}

	if rt.IsInCrashLoop("deacon") {
		t.Fatal("IsInCrashLoop = true for 2-hour-old crash loop, want expired (false)")
	}
	if !rt.CanRestart("deacon") {
		t.Fatal("CanRestart = false after crash-loop expiry, want true")
	}
}

func TestRecordRestart_ResetsBudgetAfterExpiredCrashLoop(t *testing.T) {
	rt := NewRestartTracker(t.TempDir(), RestartTrackerConfig{})
	rt.state.Agents["deacon"] = &AgentRestartInfo{
		LastRestart:    time.Now().Add(-2 * time.Hour),
		RestartCount:   5,
		CrashLoopSince: time.Now().Add(-2 * time.Hour),
	}

	rt.RecordRestart("deacon")

	info := rt.state.Agents["deacon"]
	if info.RestartCount != 1 {
		t.Fatalf("RestartCount after restart post-expiry = %d, want 1 (stability reset)", info.RestartCount)
	}
	if !info.CrashLoopSince.IsZero() {
		t.Fatalf("CrashLoopSince not cleared by stability reset: %v", info.CrashLoopSince)
	}
	if rt.IsInCrashLoop("deacon") {
		t.Fatal("IsInCrashLoop = true after post-expiry restart, want false")
	}
}
