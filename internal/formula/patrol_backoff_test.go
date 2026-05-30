package formula

import (
	"strings"
	"testing"
)

// TestPatrolFormulasHaveBackoffLogic verifies that patrol formulas include
// await-signal backoff logic in their loop-or-exit steps.
//
// This is a regression test for a bug where the witness patrol formula's
// await-signal logic was accidentally removed by subsequent commits,
// causing a tight loop when the rig was idle.
//
// See: PR #1052 (original fix), gt-tjm9q (regression report)
// See: gt-0hzeo (refinery stall bug — missing await-signal)
func TestPatrolFormulasHaveBackoffLogic(t *testing.T) {
	// Patrol formulas that must have backoff logic.
	// The loopStepID is the step that contains the await-signal logic;
	// witness/deacon use "loop-or-exit", refinery uses "burn-or-loop".
	type patrolFormula struct {
		name       string
		loopStepID string
		awaitCmd   string // "await-signal" or "await-event"
	}

	patrolFormulas := []patrolFormula{
		{"mol-witness-patrol.formula.toml", "loop-or-exit", "await-signal"},
		{"mol-deacon-patrol.formula.toml", "loop-or-exit", "await-signal"},
		{"mol-refinery-patrol.formula.toml", "burn-or-loop", "await-event"},
	}

	for _, pf := range patrolFormulas {
		t.Run(pf.name, func(t *testing.T) {
			// Read formula content directly from embedded FS
			content, err := formulasFS.ReadFile("formulas/" + pf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", pf.name, err)
			}

			contentStr := string(content)

			// Verify the formula contains the loop/decision step
			doubleQuoted := `id = "` + pf.loopStepID + `"`
			singleQuoted := `id = '` + pf.loopStepID + `'`
			if !strings.Contains(contentStr, doubleQuoted) &&
				!strings.Contains(contentStr, singleQuoted) {
				t.Fatalf("%s: %s step not found", pf.name, pf.loopStepID)
			}

			// Verify the formula contains the required backoff patterns.
			// Witness/deacon use await-signal; refinery uses await-event
			// (file-based event channel system). Both provide backoff logic.
			requiredPatterns := []string{
				pf.awaitCmd,
				"backoff",
				"gt mol step " + pf.awaitCmd,
			}

			for _, pattern := range requiredPatterns {
				if !strings.Contains(contentStr, pattern) {
					t.Errorf("%s missing required pattern %q\n"+
						"The %s step must include %s with backoff logic "+
						"to prevent tight loops when the rig is idle.\n"+
						"See PR #1052 for the original fix.",
						pf.name, pattern, pf.loopStepID, pf.awaitCmd)
				}
			}
		})
	}
}

// TestPatrolFormulasHaveReportCycle verifies that all three patrol formulas
// include `gt patrol report` in their loop step.
//
// The patrol report command atomically closes the current patrol wisp and
// starts a new one, replacing the old squash+new pattern.
//
// Regression test: replaces TestPatrolFormulasHaveSquashCycle (steveyegge/gastown#1371).
func TestPatrolFormulasHaveReportCycle(t *testing.T) {
	type patrolFormula struct {
		name       string
		loopStepID string
	}

	patrolFormulas := []patrolFormula{
		{"mol-witness-patrol.formula.toml", "loop-or-exit"},
		{"mol-deacon-patrol.formula.toml", "loop-or-exit"},
		{"mol-refinery-patrol.formula.toml", "burn-or-loop"},
	}

	for _, pf := range patrolFormulas {
		t.Run(pf.name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + pf.name)
			if err != nil {
				t.Fatalf("reading %s: %v", pf.name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", pf.name, err)
			}

			var loopDesc string
			for _, step := range f.Steps {
				if step.ID == pf.loopStepID {
					loopDesc = step.Description
					break
				}
			}
			if loopDesc == "" {
				t.Fatalf("%s: %s step not found or has empty description", pf.name, pf.loopStepID)
			}

			// The loop step must use gt patrol report to close current and start next cycle
			if !strings.Contains(loopDesc, "gt patrol report") {
				t.Errorf("%s %s step missing \"gt patrol report\" (close current patrol and start next cycle)\n"+
					"All patrol formulas must use gt patrol report in their loop step.",
					pf.name, pf.loopStepID)
			}
		})
	}
}

// TestPatrolFormulasHaveWispGC verifies that all three patrol formulas
// include `bd mol wisp gc` in their inbox-check step to clean up stale
// wisps from abnormal exits in previous cycles.
//
// Without this, patrol agents that die/restart abnormally before reaching
// the loop-or-exit squash step leave their wisps open indefinitely.
//
// Regression test for steveyegge/gastown#1712.
func TestPatrolFormulasHaveWispGC(t *testing.T) {
	patrolFormulas := []string{
		"mol-witness-patrol.formula.toml",
		"mol-deacon-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
	}

	for _, name := range patrolFormulas {
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}

			// Find the inbox-check step (first step in all patrol formulas)
			var inboxDesc string
			for _, step := range f.Steps {
				if step.ID == "inbox-check" {
					inboxDesc = step.Description
					break
				}
			}
			if inboxDesc == "" {
				t.Fatalf("%s: inbox-check step not found or has empty description", name)
			}

			if !strings.Contains(inboxDesc, "bd mol wisp gc") {
				t.Errorf("%s inbox-check step missing \"bd mol wisp gc\"\n"+
					"All patrol formulas must run wisp GC at the start of each cycle\n"+
					"to clean up stale wisps from abnormal exits.\n"+
					"See steveyegge/gastown#1712.",
					name)
			}
		})
	}
}

// TestPatrolFormulasUseDynamicBeadResolution verifies that patrol formulas
// resolve their agent bead ID dynamically at runtime via `bd list`, rather
// than hardcoding a prefix like `gt-<rig>-refinery`.
//
// Hardcoded IDs break when AgentBeadIDWithPrefix collapses the rig component
// (prefix == rig), producing e.g. "cp-refinery" instead of "gt-cp-refinery".
//
// Regression test for hq-9xs.
func TestPatrolFormulasUseDynamicBeadResolution(t *testing.T) {
	patrolFormulas := []string{
		"mol-witness-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
	}

	for _, name := range patrolFormulas {
		t.Run(name, func(t *testing.T) {
			content, err := formulasFS.ReadFile("formulas/" + name)
			if err != nil {
				t.Fatalf("reading %s: %v", name, err)
			}

			f, err := Parse(content)
			if err != nil {
				t.Fatalf("parsing %s: %v", name, err)
			}

			// Find the loop/exit step
			var loopDesc string
			for _, step := range f.Steps {
				if step.ID == "loop-or-exit" || step.ID == "burn-or-loop" {
					loopDesc = step.Description
					break
				}
			}
			if loopDesc == "" {
				t.Fatalf("%s: loop step not found or has empty description", name)
			}

			// Must use dynamic resolution via bd list
			if !strings.Contains(loopDesc, "bd list --label=gt:agent") {
				t.Errorf("%s loop step missing dynamic agent bead resolution (bd list --label=gt:agent).\n"+
					"Agent bead IDs must be resolved at runtime, not hardcoded.\n"+
					"See hq-9xs.",
					name)
			}

			// Must NOT hardcode gt-<rig> prefix pattern
			if strings.Contains(loopDesc, "gt-<rig>") {
				t.Errorf("%s loop step hardcodes gt-<rig> prefix.\n"+
					"This breaks when AgentBeadIDWithPrefix collapses the ID (prefix == rig).\n"+
					"See hq-9xs.",
					name)
			}
		})
	}
}

// TestDeaconPatrolHasHeartbeatSteps verifies the deacon patrol formula
// includes heartbeat refresh steps to prevent the daemon from killing a
// healthy Deacon mid-cycle.
//
// Without heartbeat refreshes, a patrol cycle that exceeds 20 minutes
// (HeartbeatVeryStaleThreshold = 20m) causes the daemon to consider the Deacon
// stuck and kill it, even though the Deacon is actively executing steps.
func TestDeaconPatrolHasHeartbeatSteps(t *testing.T) {
	content, err := formulasFS.ReadFile("formulas/mol-deacon-patrol.formula.toml")
	if err != nil {
		t.Fatalf("reading deacon patrol formula: %v", err)
	}

	f, err := Parse(content)
	if err != nil {
		t.Fatalf("parsing deacon patrol formula: %v", err)
	}

	// The first step must be the heartbeat step (no dependencies)
	if len(f.Steps) == 0 {
		t.Fatal("deacon patrol formula has no steps")
	}
	if f.Steps[0].ID != "heartbeat" {
		t.Errorf("first step should be \"heartbeat\", got %q", f.Steps[0].ID)
	}
	if !strings.Contains(f.Steps[0].Description, "gt deacon heartbeat") {
		t.Error("heartbeat step must contain \"gt deacon heartbeat\" command")
	}

	// inbox-check must depend on heartbeat
	for _, step := range f.Steps {
		if step.ID == "inbox-check" {
			hasHeartbeatDep := false
			for _, dep := range step.Needs {
				if dep == "heartbeat" {
					hasHeartbeatDep = true
					break
				}
			}
			if !hasHeartbeatDep {
				t.Error("inbox-check step must depend on \"heartbeat\" step")
			}
			break
		}
	}

	// There should be a mid-cycle heartbeat step
	foundMid := false
	foundPreAwait := false
	foundMandatoryHandoff := false
	for _, step := range f.Steps {
		if step.ID == "heartbeat-mid" {
			foundMid = true
			if !strings.Contains(step.Description, "gt deacon heartbeat") {
				t.Error("heartbeat-mid step must contain \"gt deacon heartbeat\" command")
			}
		}
		if step.ID == "loop-or-exit" && strings.Contains(step.Description, "pre-await checkpoint") {
			foundPreAwait = true
			if !strings.Contains(step.Description, "gt deacon heartbeat") {
				t.Error("loop-or-exit step must refresh heartbeat before await-signal")
			}
			if strings.Contains(step.Description, "gt handoff -s") && strings.Contains(step.Description, "mandatory") {
				foundMandatoryHandoff = true
			}
			heartbeatPos := strings.Index(step.Description, "gt deacon heartbeat \"pre-await checkpoint\"")
			awaitPos := strings.Index(step.Description, "gt mol step await-signal")
			if heartbeatPos == -1 || awaitPos == -1 {
				t.Error("loop-or-exit step must contain both pre-await heartbeat and await-signal commands")
			} else if heartbeatPos > awaitPos {
				t.Error("pre-await heartbeat must appear before await-signal to close the stale-heartbeat window")
			}
		}
	}
	if !foundMid {
		t.Error("deacon patrol formula must have a \"heartbeat-mid\" step for mid-cycle refresh")
	}
	if !foundPreAwait {
		t.Error("deacon patrol formula must refresh heartbeat again before await-signal")
	}
	if !foundMandatoryHandoff {
		t.Error("deacon patrol formula must require gt handoff after patrol report")
	}
}
