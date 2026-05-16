package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/session"
)

func setupSlingTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("mp", "my-project")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// TestNudgeRefinerySessionName verifies that nudgeRefinery constructs the
// correct tmux session name ({prefix}-refinery) and passes the message.
func TestNudgeRefinerySessionName(t *testing.T) {
	setupSlingTestRegistry(t)
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	tests := []struct {
		name        string
		rigName     string
		message     string
		wantSession string
	}{
		{
			name:        "simple rig name",
			rigName:     "gastown",
			message:     "MERGE_READY received - check inbox for pending work",
			wantSession: "gt-refinery",
		},
		{
			name:        "hyphenated rig name",
			rigName:     "my-project",
			message:     "MERGE_READY received - check inbox for pending work",
			wantSession: "mp-refinery",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Truncate log for each subtest
			if err := os.WriteFile(logPath, nil, 0644); err != nil {
				t.Fatalf("truncate log: %v", err)
			}

			nudgeRefinery(tt.rigName, tt.message)

			logBytes, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			logContent := string(logBytes)

			// Verify session name
			wantPrefix := "nudge:" + tt.wantSession + ":"
			if !strings.Contains(logContent, wantPrefix) {
				t.Errorf("nudgeRefinery(%q) session = got log %q, want prefix %q",
					tt.rigName, logContent, wantPrefix)
			}

			// Verify message is passed through
			if !strings.Contains(logContent, tt.message) {
				t.Errorf("nudgeRefinery() message not found in log: got %q, want %q",
					logContent, tt.message)
			}
		})
	}
}

// TestWakeRigAgentsDoesNotNudgeRefinery verifies that wakeRigAgents only
// nudges the witness, not the refinery. The refinery should only be nudged
// when an MR is actually created (via nudgeRefinery), not at polecat dispatch time.
func TestWakeRigAgentsDoesNotNudgeRefinery(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "nudge.log")
	t.Setenv("GT_TEST_NUDGE_LOG", logPath)

	// wakeRigAgents calls exec.Command("gt", "rig", "boot", ...) and tmux.NudgeSession.
	// The boot command and witness nudge will fail silently (no real rig/tmux).
	// We only care that nudgeRefinery is NOT called (no log entries).
	wakeRigAgents("testrig")

	// Check that no refinery nudge was logged
	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		// File doesn't exist = no nudges logged = correct
		return
	}
	if strings.Contains(string(logBytes), "refinery") {
		t.Errorf("wakeRigAgents() should not nudge refinery, but log contains: %s", string(logBytes))
	}
}

// TestNudgeRefineryNoOpWithoutLog verifies that nudgeRefinery doesn't panic
// or error when called without the test log env var and without a real tmux session.
// The tmux NudgeSession call should fail silently.
func TestNudgeRefineryNoOpWithoutLog(t *testing.T) {
	// Ensure test log is NOT set so we exercise the real tmux path
	t.Setenv("GT_TEST_NUDGE_LOG", "")

	// Should not panic even though no tmux session exists
	nudgeRefinery("nonexistent-rig", "test message")
}

func TestIsDeferredBead(t *testing.T) {
	tests := []struct {
		name string
		info *beadInfo
		want bool
	}{
		{"open bead is not deferred", &beadInfo{Status: "open", Description: "some task"}, false},
		{"in_progress bead is not deferred", &beadInfo{Status: "in_progress", Description: "working on it"}, false},
		{"deferred status", &beadInfo{Status: "deferred", Description: "some task"}, true},
		{"description says deferred to post-launch", &beadInfo{Status: "open", Description: "deferred to post-launch"}, true},
		{"description says deferred to post launch", &beadInfo{Status: "open", Description: "deferred to post launch"}, true},
		{"description says status: deferred", &beadInfo{Status: "open", Description: "status: deferred\nsome other notes"}, true},
		{"case insensitive description", &beadInfo{Status: "open", Description: "Deferred to Post-Launch"}, true},
		{"deferred keyword not in deferral phrase", &beadInfo{Status: "open", Description: "the user deferred this action"}, false},
		{"empty description", &beadInfo{Status: "open", Description: ""}, false},
		{"hooked bead not deferred", &beadInfo{Status: "hooked", Description: "some work"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isDeferredBead(tt.info); got != tt.want {
				t.Errorf("isDeferredBead(%+v) = %v, want %v", tt.info, got, tt.want)
			}
		})
	}
}

func TestCollectExistingMoleculesFiltersClosedMolecules(t *testing.T) {
	tests := []struct {
		name string
		info *beadInfo
		want []string
	}{
		{
			name: "open molecule is collected",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "open"},
				},
			},
			want: []string{"bd-wisp-abc"},
		},
		{
			name: "closed molecule is skipped",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "closed"},
				},
			},
			want: nil,
		},
		{
			name: "tombstone molecule is skipped",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-abc", Status: "tombstone"},
				},
			},
			want: nil,
		},
		{
			name: "mixed: open kept, closed skipped",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-wisp-dead", Status: "closed"},
					{ID: "bd-wisp-live", Status: "in_progress"},
				},
			},
			want: []string{"bd-wisp-live"},
		},
		{
			name: "non-wisp dependency ignored regardless of status",
			info: &beadInfo{
				Dependencies: []beads.IssueDep{
					{ID: "bd-regular-dep", Status: "open"},
				},
			},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectExistingMolecules(tt.info)
			if len(got) != len(tt.want) {
				t.Fatalf("collectExistingMolecules() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("collectExistingMolecules()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestIsSlingConfigError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"not initialized", fmt.Errorf("database not initialized"), true},
		{"no such table", fmt.Errorf("no such table: issues"), true},
		{"table not found", fmt.Errorf("table not found: issues"), true},
		{"issue_prefix missing", fmt.Errorf("issue_prefix not configured"), true},
		{"no database", fmt.Errorf("no database found"), true},
		{"database not found", fmt.Errorf("database not found"), true},
		{"connection refused", fmt.Errorf("connection refused"), true},
		{"transient error", fmt.Errorf("optimistic lock failed"), false},
		{"generic error", fmt.Errorf("something else"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSlingConfigError(tt.err); got != tt.want {
				t.Errorf("isSlingConfigError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsCapacityNeutralTarget verifies the deferred-dispatch gate's target
// classification: capacity-neutral targets (dogs + standing singleton agents)
// bypass the scheduler because they do not occupy a polecat slot; bare rigs
// and explicit polecat targets are capacity-managed (gt-3798 follow-up).
func TestIsCapacityNeutralTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		want   bool
	}{
		// Capacity-managed: occupies / spawns a polecat slot -> must go through scheduler.
		{"bare rig spawns polecat", "agreement_hub", false},
		{"explicit polecat occupies slot", "agreement_hub/polecats/alpha", false},
		{"another rig", "gastown", false},

		// Capacity-neutral: dogs (Deacon's self-managed pool, aa-4yf2).
		{"dog pool", "deacon/dogs", true},
		{"specific dog", "deacon/dogs/alpha", true},
		{"dog shorthand pool", "dog:", true},
		{"dog shorthand named", "dog:bravo", true},

		// Capacity-neutral: town-level standing singletons.
		{"mayor", "mayor", true},
		{"mayor case-insensitive", "MAYOR", true},
		{"mayor trailing slash", "mayor/", true},
		{"deacon", "deacon", true},

		// Capacity-neutral: rig-scoped standing agents.
		{"rig witness", "agreement_hub/witness", true},
		{"rig refinery", "agreement_hub/refinery", true},
		{"named crew", "agreement_hub/crew/hub_sheriff", true},
		{"crew without name", "agreement_hub/crew", true},

		// Not capacity-neutral: unrecognized / typo'd targets must still be
		// rejected by the gate so they cannot silently strand a workflow.
		{"typo'd role", "mayer", false},
		{"unknown bare token", "nobody", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isCapacityNeutralTarget(tt.target); got != tt.want {
				t.Errorf("isCapacityNeutralTarget(%q) = %v, want %v", tt.target, got, tt.want)
			}
		})
	}
}

// TestIsUnroutableTarget locks gt-3798 Approach C Part 2: the pour-time
// pre-flight must block targets that are neither a rig nor capacity-neutral,
// and must pass targets that are valid under the deferred-dispatch gate.
func TestIsUnroutableTarget(t *testing.T) {
	t.Parallel()

	stubIsRig := func(target string) (string, bool) {
		if target == "my-rig" {
			return "my-rig", true
		}
		return "", false
	}

	tests := []struct {
		target  string
		blocked bool
	}{
		// Valid targets: rig or capacity-neutral
		{"my-rig", false},
		{"mayor", false},
		{"deacon", false},
		{"agreement_hub/witness", false},
		{"agreement_hub/crew/hub_sheriff", false},
		{"deacon/dogs/alpha", false},
		// Blocked: unknown / typo'd targets
		{"badtarget", true},
		{"mayors", true},
		{"", true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.target, func(t *testing.T) {
			t.Parallel()
			if got := isUnroutableTarget(tt.target, stubIsRig); got != tt.blocked {
				t.Fatalf("isUnroutableTarget(%q) = %v, want %v", tt.target, got, tt.blocked)
			}
		})
	}
}
