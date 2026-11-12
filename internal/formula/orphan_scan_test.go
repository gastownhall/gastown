package formula

import (
	"strings"
	"testing"
)

func TestOrphanScanRequiresCoherentPolecatOwnership(t *testing.T) {
	f := loadOrphanScanFormula(t)
	scan := requireFormulaStep(t, f, "scan-orphaned-issues")

	for _, want := range []string{
		"gt polecat status <rig>/<name> --json",
		"`session_running` is `true`",
		"`state` is `working`",
		"`issue` exactly equals the in_progress issue being checked",
		"`state=review-needed` with an absent, null, or different `issue` is orphaned",
		"Polecat state, reported issue, and session state",
		"Recovery action (`RESET`, `REASSIGN`, `RECOVER`, or `ESCALATE`)",
	} {
		if !strings.Contains(scan.Description, want) {
			t.Errorf("scan-orphaned-issues missing ownership requirement %q", want)
		}
	}
}

func TestOrphanScanFailsClosedForDefaultTimestamps(t *testing.T) {
	f := loadOrphanScanFormula(t)
	scan := requireFormulaStep(t, f, "scan-orphaned-issues")

	for _, want := range []string{
		"absent, null,",
		"Go zero/default value (`0001-01-01...`)",
		"`age unknown`",
		"never treat a zero/default timestamp as fresh activity",
		"Do not compute an age from it",
	} {
		if !strings.Contains(scan.Description, want) {
			t.Errorf("scan-orphaned-issues missing timestamp guard %q", want)
		}
	}
}

func TestOrphanScanPreservesRecoverableBranchesAndReportsAction(t *testing.T) {
	f := loadOrphanScanFormula(t)
	triage := requireFormulaStep(t, f, "triage-orphans")
	recover := requireFormulaStep(t, f, "execute-recovery")
	report := requireFormulaStep(t, f, "report-findings")

	for _, want := range []string{
		"gt polecat check-recovery <rig>/<polecat> --json",
		"full HEAD SHA",
		"ahead/behind counts",
		"resume/reassign from preserved",
	} {
		if !strings.Contains(triage.Description, want) {
			t.Errorf("triage-orphans missing recovery evidence %q", want)
		}
	}

	for _, want := range []string{
		"Preserve the existing branch and exact HEAD commit in place",
		"Never run `gt polecat nuke`, `git branch -d`, `git branch -D`",
		"resume/reassign preserved branch <branch> at <full-head-sha>",
	} {
		if !strings.Contains(recover.Description, want) {
			t.Errorf("execute-recovery missing branch preservation rule %q", want)
		}
	}
	if !strings.Contains(report.Description, "recovery: {{recovery_action}}") {
		t.Fatal("report-findings does not identify the concrete recovery action")
	}
}

func loadOrphanScanFormula(t *testing.T) *Formula {
	t.Helper()
	content, err := formulasFS.ReadFile("formulas/mol-orphan-scan.formula.toml")
	if err != nil {
		t.Fatalf("reading orphan scan formula: %v", err)
	}
	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing orphan scan formula: %v", err)
	}
	return f
}
