package sling

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/scheduler/capacity"
)

func TestFromContextPreservesCompleteIntent(t *testing.T) {
	fields := &capacity.SlingContextFields{
		Version:      1,
		WorkBeadID:   "gt-abc",
		TargetRig:    "gastown",
		Formula:      "mol-polecat-work",
		Args:         "implement the thing",
		Vars:         "feature=widget\nissue=gt-abc",
		Merge:        "local",
		Convoy:       "hq-cv-recorded",
		BaseBranch:   "develop",
		ResumeBranch: "feature/resume-me",
		NoMerge:      true,
		ReviewOnly:   true,
		Account:      "work",
		Agent:        "codex",
		HookRawBead:  true,
		Owned:        true,
		Mode:         "ralph",
	}

	intent := FromContext(fields)

	if intent.BeadID != "gt-abc" {
		t.Errorf("BeadID = %q", intent.BeadID)
	}
	if intent.RigName != "gastown" {
		t.Errorf("RigName = %q", intent.RigName)
	}
	if intent.Formula != "mol-polecat-work" {
		t.Errorf("Formula = %q", intent.Formula)
	}
	if intent.Args != "implement the thing" {
		t.Errorf("Args = %q", intent.Args)
	}
	if len(intent.Vars) != 2 || intent.Vars[0] != "feature=widget" || intent.Vars[1] != "issue=gt-abc" {
		t.Errorf("Vars = %v", intent.Vars)
	}
	if intent.Merge != "local" {
		t.Errorf("Merge = %q", intent.Merge)
	}
	if intent.Convoy != "hq-cv-recorded" {
		t.Errorf("Convoy = %q, want recorded identity", intent.Convoy)
	}
	if intent.BaseBranch != "develop" {
		t.Errorf("BaseBranch = %q", intent.BaseBranch)
	}
	if intent.ResumeBranch != "feature/resume-me" {
		t.Errorf("ResumeBranch = %q", intent.ResumeBranch)
	}
	if !intent.NoMerge || !intent.ReviewOnly || !intent.HookRawBead || !intent.Owned {
		t.Errorf("bools: NoMerge=%v ReviewOnly=%v HookRawBead=%v Owned=%v",
			intent.NoMerge, intent.ReviewOnly, intent.HookRawBead, intent.Owned)
	}
	if intent.Account != "work" || intent.Agent != "codex" || intent.Mode != "ralph" {
		t.Errorf("Account=%q Agent=%q Mode=%q", intent.Account, intent.Agent, intent.Mode)
	}
}

func TestFromContextRoundTripThroughDurableJSON(t *testing.T) {
	original := Intent{
		BeadID:       "gt-abc",
		RigName:      "gastown",
		Args:         "do it",
		Vars:         []string{"a=1", "b=2"},
		Merge:        "local",
		Convoy:       "hq-cv-1",
		ResumeBranch: "feature/x",
		Owned:        true,
		HookRawBead:  true,
		Mode:         "ralph",
	}

	persisted := beads.FormatSlingContextDescription(ToContextFields(original, "2026-01-15T10:00:00Z"))
	parsed := beads.ParseSlingContextFields(persisted)
	if parsed == nil {
		t.Fatal("ParseSlingContextFields returned nil")
	}
	got := FromContext(parsed)
	if got.Convoy != original.Convoy {
		t.Errorf("Convoy = %q, want %q", got.Convoy, original.Convoy)
	}
	if got.Owned != original.Owned {
		t.Errorf("Owned = %v, want true", got.Owned)
	}
	if got.Merge != original.Merge {
		t.Errorf("Merge = %q, want %q", got.Merge, original.Merge)
	}
	if got.ResumeBranch != original.ResumeBranch {
		t.Errorf("ResumeBranch = %q, want %q", got.ResumeBranch, original.ResumeBranch)
	}
	if got.Mode != original.Mode {
		t.Errorf("Mode = %q, want %q", got.Mode, original.Mode)
	}
}

func TestFromContextNil(t *testing.T) {
	if got := FromContext(nil); got.BeadID != "" {
		t.Errorf("FromContext(nil) = %+v, want zero Intent", got)
	}
}
