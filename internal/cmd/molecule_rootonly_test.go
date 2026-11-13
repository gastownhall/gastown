package cmd

import (
	"reflect"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/formula"
)

func TestRootOnlyFormulaName(t *testing.T) {
	tests := []struct {
		name       string
		root       *beads.Issue
		attachment *beads.AttachmentFields
		want       string
	}{
		{
			name:       "explicit attached_formula wins",
			root:       &beads.Issue{ID: "hq-wisp-a", Title: "mol-dog-reaper"},
			attachment: &beads.AttachmentFields{AttachedFormula: "mol-dog-doctor"},
			want:       "mol-dog-doctor",
		},
		{
			name: "falls back to a title that names a real formula",
			root: &beads.Issue{ID: "hq-wisp-b", Title: "mol-dog-reaper"},
			want: "mol-dog-reaper",
		},
		{
			name: "prose title is not mistaken for a formula",
			root: &beads.Issue{ID: "hq-abc", Title: "Fix the reaper so it stops orphaning wisps"},
			want: "",
		},
		{
			name: "single-word title that is not a formula",
			root: &beads.Issue{ID: "hq-abc", Title: "mol-does-not-exist"},
			want: "",
		},
		{
			name: "empty title",
			root: &beads.Issue{ID: "hq-abc", Title: ""},
			want: "",
		},
		{
			name: "path-like title is rejected without touching the filesystem",
			root: &beads.Issue{ID: "hq-abc", Title: "../../etc/passwd"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Empty townRoot/rigName forces resolution against embedded formulas,
			// so the test does not depend on a checked-out town.
			got := rootOnlyFormulaName(tt.root, tt.attachment, "", "")
			if got != tt.want {
				t.Errorf("rootOnlyFormulaName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRootOnlyProgress(t *testing.T) {
	steps := []formula.Step{
		{ID: "scan", Title: "Scan databases"},
		{ID: "reap", Title: "Reap stale wisps"},
	}

	t.Run("open root reports no completed steps and no false readiness", func(t *testing.T) {
		got := rootOnlyProgress(&beads.Issue{ID: "hq-wisp-a", Title: "mol-dog-reaper", Status: "hooked"}, steps, "mol-dog-reaper")

		if !got.RootOnly {
			t.Error("RootOnly = false, want true")
		}
		if got.Formula != "mol-dog-reaper" {
			t.Errorf("Formula = %q, want %q", got.Formula, "mol-dog-reaper")
		}
		if got.TotalSteps != 2 || got.DoneSteps != 0 || got.Complete {
			t.Errorf("got %d/%d complete=%v, want 0/2 complete=false", got.DoneSteps, got.TotalSteps, got.Complete)
		}
		// Without step rows there is no per-step readiness to report.
		if len(got.ReadySteps) != 0 || len(got.BlockedSteps) != 0 {
			t.Errorf("ready=%v blocked=%v, want both empty", got.ReadySteps, got.BlockedSteps)
		}
		want := []string{"scan: Scan databases", "reap: Reap stale wisps"}
		if !reflect.DeepEqual(got.Steps, want) {
			t.Errorf("Steps = %v, want %v", got.Steps, want)
		}
	})

	t.Run("closed root means the whole checklist is done", func(t *testing.T) {
		got := rootOnlyProgress(&beads.Issue{ID: "hq-wisp-a", Title: "mol-dog-reaper", Status: "closed"}, steps, "mol-dog-reaper")

		if !got.Complete || got.DoneSteps != 2 || got.Percent != 100 {
			t.Errorf("got %d/%d %d%% complete=%v, want 2/2 100%% complete=true",
				got.DoneSteps, got.TotalSteps, got.Percent, got.Complete)
		}
	})
}

func TestStepLabel(t *testing.T) {
	tests := []struct {
		name string
		step formula.Step
		want string
	}{
		{"id and title", formula.Step{ID: "scan", Title: "Scan databases"}, "scan: Scan databases"},
		{"title only", formula.Step{Title: "Scan databases"}, "Scan databases"},
		{"id only", formula.Step{ID: "scan"}, "scan"},
		{"neither", formula.Step{}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stepLabel(tt.step); got != tt.want {
				t.Errorf("stepLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBuildFormulaDAG(t *testing.T) {
	root := &beads.Issue{ID: "hq-wisp-a", Title: "mol-dog-reaper"}
	steps := []formula.Step{
		{ID: "scan", Title: "Scan"},
		{ID: "reap", Title: "Reap", Needs: []string{"scan"}},
		{ID: "purge", Title: "Purge", Needs: []string{"reap"}},
		{ID: "notify", Title: "Notify", Needs: []string{"scan"}, Parallel: true},
	}

	dag := buildFormulaDAG(root, steps, "mol-dog-reaper")

	if !dag.RootOnly || dag.Formula != "mol-dog-reaper" {
		t.Errorf("RootOnly=%v Formula=%q, want true/%q", dag.RootOnly, dag.Formula, "mol-dog-reaper")
	}
	if dag.TotalNodes != 4 {
		t.Errorf("TotalNodes = %d, want 4", dag.TotalNodes)
	}
	for id, node := range dag.Nodes {
		if node.Status != dagStatusInline {
			t.Errorf("node %s status = %q, want %q", id, node.Status, dagStatusInline)
		}
	}
	if !dag.Nodes["notify"].Parallel {
		t.Error("notify.Parallel = false, want true")
	}
	if got := dag.Nodes["scan"].Tier; got != 0 {
		t.Errorf("scan tier = %d, want 0", got)
	}
	if got := dag.Nodes["purge"].Tier; got != 2 {
		t.Errorf("purge tier = %d, want 2", got)
	}
	if got := dag.Nodes["scan"].Dependents; len(got) != 2 {
		t.Errorf("scan dependents = %v, want 2 entries", got)
	}
}

func TestBuildFormulaDAGDropsUnknownNeeds(t *testing.T) {
	// A `needs` pointing outside the formula would leave a permanent in-degree,
	// so computeTiers would strand the dependent and every step after it.
	root := &beads.Issue{ID: "hq-wisp-a", Title: "mol-x"}
	steps := []formula.Step{
		{ID: "scan", Title: "Scan"},
		{ID: "reap", Title: "Reap", Needs: []string{"scan", "step-from-another-formula"}},
	}

	dag := buildFormulaDAG(root, steps, "mol-x")

	if got := dag.Nodes["reap"].Dependencies; !reflect.DeepEqual(got, []string{"scan"}) {
		t.Errorf("reap dependencies = %v, want [scan]", got)
	}
	if dag.Tiers != 2 {
		t.Errorf("Tiers = %d, want 2 (every step reachable)", dag.Tiers)
	}
}

func TestNoStepsErrorNamesTheIssue(t *testing.T) {
	err := noStepsError("hq-abc")
	if err == nil {
		t.Fatal("noStepsError() = nil, want an error")
	}
	if got := err.Error(); got != "no steps found for hq-abc"+rootOnlyStepsHint {
		t.Errorf("noStepsError() = %q", got)
	}
}
