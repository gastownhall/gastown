package cmd

import (
	"testing"
)

// clearAgentEnv blanks every environment variable the pr-workflow guard
// consults, so each case starts from a known state regardless of whether the
// test itself is running inside a Gas Town agent session.
func clearAgentEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"GT_POLECAT", "GT_CREW", "GT_WITNESS",
		"GT_REFINERY", "GT_MAYOR", "GT_DEACON", "GT_ROLE",
	} {
		t.Setenv(k, "")
	}
}

// The Refinery is the merge queue processor. To rebase a merge candidate onto
// its target it must create a local branch, which means `git checkout -b`.
// Blocking that made the Refinery structurally unable to merge anything: on
// 2026-08-03 the ad_unica_sw_opus queue stalled with approved MRs while every
// rebase checkout was denied by this guard.
//
// The mistake these tests catch is someone folding the Refinery back into the
// blocked set. That failure is silent - the Refinery keeps running and reports
// merge attempts, it just never lands anything.
func TestPRWorkflowGuard_RefineryIsExempt(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{
			name: "session env var",
			env:  map[string]string{"GT_REFINERY": "1"},
		},
		{
			name: "rig-qualified role",
			env:  map[string]string{"GT_ROLE": "ad_unica_sw_opus/refinery"},
		},
		{
			name: "bare role",
			env:  map[string]string{"GT_ROLE": "refinery"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearAgentEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			// Run from a directory with no agent path segment and no git
			// remote, so only the role signal under test can decide.
			t.Chdir(t.TempDir())

			if err := runTapGuardPRWorkflow(nil, nil); err != nil {
				t.Fatalf("Refinery must be allowed to create local branches, got %v", err)
			}
		})
	}
}

// The guard must still block every other agent role. Exempting the Refinery is
// a narrow carve-out, not a removal of the guard.
func TestPRWorkflowGuard_OtherAgentsStillBlocked(t *testing.T) {
	tests := []struct {
		name   string
		envVar string
	}{
		{name: "polecat", envVar: "GT_POLECAT"},
		{name: "crew", envVar: "GT_CREW"},
		{name: "witness", envVar: "GT_WITNESS"},
		{name: "mayor", envVar: "GT_MAYOR"},
		{name: "deacon", envVar: "GT_DEACON"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearAgentEnv(t)
			t.Setenv(tc.envVar, "1")
			t.Chdir(t.TempDir())

			err := runTapGuardPRWorkflow(nil, nil)
			if err == nil {
				t.Fatalf("%s must remain blocked by the PR workflow guard", tc.name)
			}
			code, ok := IsSilentExit(err)
			if !ok || code != 2 {
				t.Fatalf("guard must block with exit 2 so the hook denies the tool call, got %v", err)
			}
		})
	}
}

// A Refinery signal must not leak an exemption to a co-resident agent. Only the
// Refinery's own session sets GT_REFINERY, but GT_ROLE is a string match, so
// verify a role that merely contains the word does not slip through.
func TestPRWorkflowGuard_RoleMatchIsNotSubstringOfPolecatName(t *testing.T) {
	clearAgentEnv(t)
	t.Setenv("GT_POLECAT", "1")
	t.Setenv("GT_ROLE", "somerig/polecats/refinery-helper")
	t.Chdir(t.TempDir())

	if err := runTapGuardPRWorkflow(nil, nil); err == nil {
		t.Fatal("a polecat must stay blocked even when its name contains 'refinery'")
	}
}

func TestIsRefineryContext(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"refinery session", map[string]string{"GT_REFINERY": "1"}, true},
		{"refinery role", map[string]string{"GT_ROLE": "rig/refinery"}, true},
		{"polecat session", map[string]string{"GT_POLECAT": "1"}, false},
		{"no signals", map[string]string{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearAgentEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if got := isRefineryContext(); got != tc.want {
				t.Fatalf("isRefineryContext() = %v, want %v (env %v)", got, tc.want, tc.env)
			}
		})
	}
}
