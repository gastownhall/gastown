package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/cigate"
	"github.com/steveyegge/gastown/internal/config"
)

// gateRecorder captures the side effects of a doneCIGateDeps.enforce run.
type gateRecorder struct {
	needsSetPR    int
	needsSet      bool
	needsCleared  bool
	intentCleared bool
	escalations   []cigate.Escalation
	mayorNudges   []string
}

func gateDeps(ghOutput string, rec *gateRecorder) *doneCIGateDeps {
	return &doneCIGateDeps{
		gate: &cigate.Gate{Run: func(dir, name string, args ...string) ([]byte, error) {
			return []byte(ghOutput), nil
		}},
		cfg:    &config.CIGateConfig{},
		dir:    "/tmp",
		branch: "polecat/test",
		agent:  "rig/polecats/test",
		out:    &bytes.Buffer{},
		setNeedsCIGreen: func(pr int) {
			rec.needsSet = true
			rec.needsSetPR = pr
		},
		clearNeedsCIGreen: func() { rec.needsCleared = true },
		clearDoneIntent:   func() { rec.intentCleared = true },
		escalate:          func(esc cigate.Escalation) { rec.escalations = append(rec.escalations, esc) },
		nudgeMayor:        func(msg string) { rec.mayorNudges = append(rec.mayorNudges, msg) },
	}
}

const (
	ghNoPR        = `[]`
	ghGreen       = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"context":"jenkins","state":"SUCCESS"}]}]`
	ghRed         = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"name":"test","conclusion":"failure"}]}]`
	ghPending     = `[{"number":5,"state":"OPEN","url":"u","statusCheckRollup":[{"context":"jenkins","state":"PENDING"}]}]`
	ghMerged      = `[{"number":5,"state":"MERGED","url":"u","statusCheckRollup":[]}]`
	ghClosed      = `[{"number":5,"state":"CLOSED","url":"u","statusCheckRollup":[]}]`
	ghUnparseable = `oops`
)

func TestDoneCIGateAllows(t *testing.T) {
	for _, tt := range []struct {
		name     string
		ghOutput string
	}{
		{"no PR passes through", ghNoPR},
		{"green PR allowed", ghGreen},
		{"merged PR allowed", ghMerged},
		{"closed-unmerged PR allowed with warning", ghClosed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := &gateRecorder{}
			if err := gateDeps(tt.ghOutput, rec).enforce(); err != nil {
				t.Fatalf("enforce() = %v, want nil", err)
			}
			if !rec.needsCleared {
				t.Error("expected stale needs-ci-green label cleared on pass")
			}
			if rec.needsSet || rec.intentCleared || len(rec.escalations) != 0 {
				t.Errorf("unexpected side effects: %+v", rec)
			}
		})
	}
}

func TestDoneCIGateBlocksRed(t *testing.T) {
	rec := &gateRecorder{}
	err := gateDeps(ghRed, rec).enforce()
	if err == nil {
		t.Fatal("enforce() = nil, want NEEDS_CI_GREEN error")
	}
	if !strings.Contains(err.Error(), "NEEDS_CI_GREEN") || !strings.Contains(err.Error(), "test") {
		t.Errorf("error should name the gate and failing check: %v", err)
	}
	if !rec.needsSet || rec.needsSetPR != 5 {
		t.Errorf("needs-ci-green label not set for PR 5: %+v", rec)
	}
	if !rec.intentCleared {
		t.Error("done-intent label must be cleared so witness doesn't treat abort as crash-mid-done")
	}
	if len(rec.escalations) != 0 {
		t.Errorf("red is the polecat's job, not a human escalation: %+v", rec.escalations)
	}
}

func TestDoneCIGateErrorFailsOpenAndEscalates(t *testing.T) {
	rec := &gateRecorder{}
	if err := gateDeps(ghUnparseable, rec).enforce(); err != nil {
		t.Fatalf("enforce() = %v, want nil (fail-open)", err)
	}
	if len(rec.escalations) != 1 || rec.escalations[0].Event != cigate.EventCIStatusError {
		t.Fatalf("want one ci_status_error escalation, got %+v", rec.escalations)
	}
	if rec.needsSet || rec.intentCleared {
		t.Errorf("fail-open must not block: %+v", rec)
	}
}

func TestDoneCIGatePendingResolvesGreen(t *testing.T) {
	rec := &gateRecorder{}
	deps := gateDeps(ghPending, rec)
	deps.wait = func(timeout, poll time.Duration) (cigate.CheckResult, bool) {
		return cigate.CheckResult{Verdict: cigate.VerdictGreen, PRNumber: 5}, false
	}
	if err := deps.enforce(); err != nil {
		t.Fatalf("enforce() = %v, want nil after pending→green", err)
	}
	if !rec.needsCleared || len(rec.escalations) != 0 {
		t.Errorf("unexpected side effects: %+v", rec)
	}
}

func TestDoneCIGatePendingResolvesRed(t *testing.T) {
	rec := &gateRecorder{}
	deps := gateDeps(ghPending, rec)
	deps.wait = func(timeout, poll time.Duration) (cigate.CheckResult, bool) {
		return cigate.CheckResult{Verdict: cigate.VerdictRed, PRNumber: 5, FailingChecks: []string{"jenkins"}}, false
	}
	err := deps.enforce()
	if err == nil || !strings.Contains(err.Error(), "NEEDS_CI_GREEN") {
		t.Fatalf("enforce() = %v, want NEEDS_CI_GREEN block after pending→red", err)
	}
	if !rec.needsSet || !rec.intentCleared {
		t.Errorf("expected block side effects: %+v", rec)
	}
}

func TestDoneCIGatePendingTimeoutEscalatesAndBlocks(t *testing.T) {
	rec := &gateRecorder{}
	deps := gateDeps(ghPending, rec)
	deps.cfg = &config.CIGateConfig{PendingTimeout: "30m"}
	deps.wait = func(timeout, poll time.Duration) (cigate.CheckResult, bool) {
		if timeout != 30*time.Minute {
			t.Errorf("timeout = %s, want 30m", timeout)
		}
		return cigate.CheckResult{Verdict: cigate.VerdictPending, PRNumber: 5, PendingChecks: []string{"jenkins"}}, true
	}
	err := deps.enforce()
	if err == nil || !strings.Contains(err.Error(), "NEEDS_CI_GREEN") {
		t.Fatalf("enforce() = %v, want NEEDS_CI_GREEN block on timeout", err)
	}
	if len(rec.escalations) != 1 || rec.escalations[0].Event != cigate.EventPendingTimeout {
		t.Fatalf("want one pending_timeout escalation (Blair: comment + Requires Human Input), got %+v", rec.escalations)
	}
	if len(rec.mayorNudges) != 1 {
		t.Errorf("want mayor nudge on pending timeout, got %v", rec.mayorNudges)
	}
	if !rec.needsSet || !rec.intentCleared {
		t.Errorf("expected block side effects: %+v", rec)
	}
}

func TestCIGateConfigDefaults(t *testing.T) {
	var nilCfg *config.CIGateConfig
	if !nilCfg.IsEnabled() {
		t.Error("nil ci_gate config must default to enabled")
	}
	if got := nilCfg.PendingTimeoutOrDefault(); got != 30*time.Minute {
		t.Errorf("default pending_timeout = %s, want 30m (Blair: capital Jenkins green runs take ~18m)", got)
	}
	if got := nilCfg.PollIntervalOrDefault(); got != 30*time.Second {
		t.Errorf("default poll_interval = %s, want 30s", got)
	}
	if got := nilCfg.MayorAlertAfterOrDefault(); got != 30*time.Minute {
		t.Errorf("default mayor_alert_after = %s, want 30m", got)
	}

	off := false
	cfg := &config.CIGateConfig{Enabled: &off, PendingTimeout: "10m"}
	if cfg.IsEnabled() {
		t.Error("enabled=false must disable")
	}
	if got := cfg.PendingTimeoutOrDefault(); got != 10*time.Minute {
		t.Errorf("configured pending_timeout = %s, want 10m", got)
	}
	if got := (&config.CIGateConfig{PendingTimeout: "garbage"}).PendingTimeoutOrDefault(); got != 30*time.Minute {
		t.Errorf("invalid pending_timeout should fall back to 30m, got %s", got)
	}
}

func TestCIGateEnvKillSwitch(t *testing.T) {
	for _, v := range []string{"off", "0", "false", "disabled", "OFF"} {
		t.Setenv("GT_CI_GATE", v)
		if !cigate.EnvDisabled() {
			t.Errorf("GT_CI_GATE=%s should disable the gate", v)
		}
	}
	for _, v := range []string{"", "on", "1", "true"} {
		t.Setenv("GT_CI_GATE", v)
		if cigate.EnvDisabled() {
			t.Errorf("GT_CI_GATE=%s should NOT disable the gate", v)
		}
	}
}
