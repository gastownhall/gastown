package formula

import (
	"strings"
	"testing"
)

func readBootTriageFormula(t *testing.T) string {
	t.Helper()

	content, err := formulasFS.ReadFile("formulas/mol-boot-triage.formula.toml")
	if err != nil {
		t.Fatalf("reading boot triage formula: %v", err)
	}
	return string(content)
}

func TestBootTriageFormulaUsesNudgeForWake(t *testing.T) {
	formula := readBootTriageFormula(t)
	for _, want := range []string{
		`gt nudge --mode=immediate deacon "Boot wake: please check your inbox and pending work"`,
		"Raw tmux send-keys is blocked for Boot",
	} {
		if !strings.Contains(formula, want) {
			t.Fatalf("boot triage formula missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"tmux send-keys -t hq-deacon",
		"sleep 1",
		"escape + message",
	} {
		if strings.Contains(formula, forbidden) {
			t.Fatalf("boot triage formula still contains forbidden raw tmux wake guidance %q", forbidden)
		}
	}
}

func TestBootTriageFormulaRoutesWatchdogIncidentPhases(t *testing.T) {
	formula := readBootTriageFormula(t)

	for _, want := range []string{
		"`healthy`",
		"`suspect`",
		"`recovering-local`",
		"`escalated-go`",
		"`go-exhausted`",
		"`claude-last-ditch`",
		"`recovered-by-claude`",
		"`report-pending`",
		"`awaiting-human`",
	} {
		if !strings.Contains(formula, want) {
			t.Errorf("boot triage formula missing watchdog phase %q", want)
		}
	}
}

func TestBootTriageFormulaConstrainsIncidentRecoveryAgents(t *testing.T) {
	formula := readBootTriageFormula(t)

	for _, want := range []string{
		"incident dossier",
		`$GT_ROOT/deacon/recovery-incidents/<incident-id>.json`,
		`status + $GT_AGENT`,
		"incident scope",
		"qwen/qwen3.6-35b-a3b",
		"parallel=2",
		"TTL=14400",
		"exactly one invocation",
		"exit immediately",
		"opencode-local",
		"gt mail send overseer",
		"--permanent --type notification",
		"human-readable",
		"watchdog",
	} {
		if !strings.Contains(formula, want) {
			t.Errorf("boot triage formula missing incident recovery constraint %q", want)
		}
	}

	for _, forbidden := range []string{
		"gt boot spawn --agent claude",
		"gt boot spawn --agent codex",
	} {
		if strings.Contains(formula, forbidden) {
			t.Errorf("boot triage formula must not self-escalate with %q", forbidden)
		}
	}
}

func TestBootTriageFormulaPreservesRoutineTriageAndMechanicalAuthority(t *testing.T) {
	formula := readBootTriageFormula(t)

	for _, want := range []string{
		"Ordinary healthy triage",
		"must not spawn cloud recovery",
		"Do not change gateways",
		"Do not change the model hierarchy",
		"Never invoke Codex yourself",
		"Only the watchdog may declare recovery",
	} {
		if !strings.Contains(formula, want) {
			t.Errorf("boot triage formula missing safety invariant %q", want)
		}
	}
}

func TestBootTriageFormulaRequiresFiniteInfrastructureLadder(t *testing.T) {
	formula := readBootTriageFormula(t)

	for _, want := range []string{
		"Only the watchdog may advance the finite Go → Claude → Codex ladder",
		"`claude-last-ditch` | `claude`",
		"`codex-last-ditch` | `codex`",
		"Failure freezes the",
		"incident in `awaiting-human`",
		"Local mechanical recovery owns the 90-second head start before Go is spawned",
	} {
		if !strings.Contains(formula, want) {
			t.Errorf("boot triage formula missing Go exhaustion invariant %q", want)
		}
	}

	for _, forbidden := range []string{
		"Go Boot receives a 90-second head start",
		"Go attempt did not recover service",
	} {
		if strings.Contains(formula, forbidden) {
			t.Errorf("boot triage formula contains incorrect Go exhaustion guidance %q", forbidden)
		}
	}
}
