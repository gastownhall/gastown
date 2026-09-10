package daemon

import (
	"testing"
	"time"
)

// The real message from the 2026-09-10 outage, as it appeared in the panes of
// brahmin, enclave, ghoul and deacon.
const realWallPane = `● Background command "Re-run excluding pre-existing collection errors" completed (exit code 0)
  ⎿  You've hit your weekly limit · resets Sep 14, 7am (Asia/Taipei)
     /usage-credits to finish what you're working on.
`

func TestWallRecovery_DetectsTheRealOutageMessage(t *testing.T) {
	if !IsClaudeUsageLimit(realWallPane) {
		t.Fatal("the pane text from the actual outage is not recognised as a usage-limit wall")
	}
}

// A patrol that repairs a SILENT failure must not itself default to off: if it
// is disabled nothing reports that it is disabled, which is how fourteen hours
// passed. Unlike quota_dog, absent config means enabled.
func TestWallRecovery_DefaultsToEnabledWhenUnconfigured(t *testing.T) {
	for name, cfg := range map[string]*DaemonPatrolConfig{
		"nil config":    nil,
		"no patrols":    {},
		"patrols empty": {Patrols: &PatrolsConfig{}},
	} {
		if !IsPatrolEnabled(cfg, "wall_recovery") {
			t.Errorf("%s: wall_recovery should default to ENABLED", name)
		}
	}
	off := &DaemonPatrolConfig{Patrols: &PatrolsConfig{WallRecovery: &WallRecoveryConfig{Enabled: false}}}
	// The town-level disabled_patrols list must still win over the default.
	d := &Daemon{disabledPatrols: map[string]bool{"wall_recovery": true}}
	if d.isPatrolActive("wall_recovery") {
		t.Error("disabled_patrols must be able to turn wall_recovery off")
	}
	if IsPatrolEnabled(off, "wall_recovery") {
		t.Error("an explicit Enabled:false must still turn it off")
	}
}

// A wall lasts hours. Probing every sweep would produce hundreds of nudges, which
// is the noise-without-action failure this patrol exists to correct.
func TestWallRecovery_ProbesAreRateLimitedPerSession(t *testing.T) {
	s := newWallRecoveryState()
	base := time.Now()
	const gap = 10 * time.Minute

	if !s.shouldProbe("ap-vault", base, gap) {
		t.Fatal("first sighting should probe immediately")
	}
	if s.shouldProbe("ap-vault", base.Add(1*time.Minute), gap) {
		t.Error("probed again inside the gap")
	}
	if s.shouldProbe("ap-vault", base.Add(9*time.Minute), gap) {
		t.Error("probed again inside the gap")
	}
	if !s.shouldProbe("ap-vault", base.Add(11*time.Minute), gap) {
		t.Error("should probe again once the gap has elapsed")
	}
	// Rate limiting is per session: a second walled agent is not starved by the
	// first. All four polecats were walled simultaneously.
	if !s.shouldProbe("ap-brahmin", base.Add(1*time.Minute), gap) {
		t.Error("a different session must be probed on its own schedule")
	}
}

// Once an agent resumes, the stale message scrolls out of the capture window and
// the session must be forgotten -- otherwise a later wall waits out a stale gap.
func TestWallRecovery_ForgetsARecoveredSession(t *testing.T) {
	s := newWallRecoveryState()
	base := time.Now()
	const gap = 10 * time.Minute

	s.shouldProbe("ap-vault", base, gap)
	s.clear("ap-vault")
	if !s.shouldProbe("ap-vault", base.Add(1*time.Minute), gap) {
		t.Error("a session walled again after recovering must be probed promptly, not held to the old gap")
	}
}

// The elapsed time goes into the nudge and the log, so an operator can see a
// wall as a wall. Reporting it as ordinary idleness is what hid the outage.
func TestWallRecovery_TracksHowLongASessionHasBeenWalled(t *testing.T) {
	s := newWallRecoveryState()
	base := time.Now()
	s.shouldProbe("ap-vault", base, time.Minute)

	if got := s.walledSince("ap-vault", base.Add(14*time.Hour)); got < 13*time.Hour {
		t.Errorf("walledSince = %v, want about 14h", got)
	}
	if got := s.walledSince("never-seen", base); got != 0 {
		t.Errorf("unknown session should report 0, got %v", got)
	}
	if s.describeWalledSessions() == "" {
		t.Error("walled sessions must be nameable for status output")
	}
}

func TestWallRecovery_IntervalDefaultsAndOverrides(t *testing.T) {
	if got := wallRecoveryInterval(nil); got != defaultWallRecoveryInterval {
		t.Errorf("default interval = %v, want %v", got, defaultWallRecoveryInterval)
	}
	cfg := &DaemonPatrolConfig{Patrols: &PatrolsConfig{WallRecovery: &WallRecoveryConfig{IntervalStr: "90s", ProbeGapStr: "3m"}}}
	if got := wallRecoveryInterval(cfg); got != 90*time.Second {
		t.Errorf("interval override = %v, want 90s", got)
	}
	if got := wallRecoveryProbeGap(cfg); got != 3*time.Minute {
		t.Errorf("probe gap override = %v, want 3m", got)
	}
	bad := &DaemonPatrolConfig{Patrols: &PatrolsConfig{WallRecovery: &WallRecoveryConfig{IntervalStr: "not-a-duration"}}}
	if got := wallRecoveryInterval(bad); got != defaultWallRecoveryInterval {
		t.Errorf("unparseable interval must fall back to the default, got %v", got)
	}
}
