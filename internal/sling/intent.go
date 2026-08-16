// Package sling is the deep Slinging module: complete work-dispatch lifecycle
// behind one small interface.
//
// Direct `gt sling` and deferred scheduler dispatch are adapters. They convert
// arguments or durable context into Intent, invoke Lifecycle, and present the
// Outcome. Lifecycle owns validation, cross-rig guard, Convoy reuse, Polecat
// spawn, named-target dispatch (Mayor, Crew, dogs, self), Formula and Molecule
// work, Hook placement, Bead attachment, compensation, and witness wake.
package sling

import (
	"strings"

	"github.com/jonbaldie/gastown/internal/scheduler/capacity"
)

// Intent is complete Sling intent. Every field that scheduling persists and
// dispatch must honor lives here so adapters cannot drop a subset.
type Intent struct {
	BeadID           string
	RigName          string // Polecat/rig dispatch. Empty means named Target (or self).
	Target           string // Named Slinging target: mayor, crew path, dog, self ("" / "."). Unused when RigName is set.
	Formula          string
	Args             string
	Vars             []string
	Merge            string
	Convoy           string // recorded Convoy identity to reuse; empty means none yet
	BaseBranch       string
	ResumeBranch     string
	Account          string
	Agent            string
	Mode             string
	NoMerge          bool
	ReviewOnly       bool
	HookRawBead      bool
	Owned            bool
	NoConvoy         bool
	Force            bool
	NoBoot           bool
	SkipCook         bool
	FormulaFailFatal bool
	CallerContext    string
	TownRoot         string
	BeadsDir         string

	// Named-target adapter fields. Unused for rig/queue dispatch (RigName set).
	DryRun  bool
	Create  bool
	Subject string
	Message string
}

// Outcome is the observable result of one Lifecycle.Execute.
type Outcome struct {
	BeadID           string
	PolecatName      string
	ConvoyID         string
	AttachedMolecule string
	Success          bool
	NoOp             bool // already hooked to a live polecat in the target rig
}

// FromContext reconstructs Intent from durable sling-context fields.
// Execution flags (NoBoot, FormulaFailFatal, TownRoot, …) stay with the adapter.
func FromContext(fields *capacity.SlingContextFields) Intent {
	if fields == nil {
		return Intent{}
	}
	intent := Intent{
		BeadID:       fields.WorkBeadID,
		RigName:      fields.TargetRig,
		Formula:      fields.Formula,
		Args:         fields.Args,
		Merge:        fields.Merge,
		Convoy:       fields.Convoy,
		BaseBranch:   fields.BaseBranch,
		ResumeBranch: fields.ResumeBranch,
		Account:      fields.Account,
		Agent:        fields.Agent,
		Mode:         fields.Mode,
		NoMerge:      fields.NoMerge,
		ReviewOnly:   fields.ReviewOnly,
		HookRawBead:  fields.HookRawBead,
		Owned:        fields.Owned,
	}
	if fields.Vars != "" {
		intent.Vars = splitVars(fields.Vars)
	}
	return intent
}

// ToContextFields serializes Intent into durable sling-context fields.
func ToContextFields(intent Intent, enqueuedAt string) *capacity.SlingContextFields {
	fields := &capacity.SlingContextFields{
		Version:      1,
		WorkBeadID:   intent.BeadID,
		TargetRig:    intent.RigName,
		Formula:      intent.Formula,
		Args:         intent.Args,
		Merge:        intent.Merge,
		Convoy:       intent.Convoy,
		BaseBranch:   intent.BaseBranch,
		ResumeBranch: intent.ResumeBranch,
		NoMerge:      intent.NoMerge,
		ReviewOnly:   intent.ReviewOnly,
		Account:      intent.Account,
		Agent:        intent.Agent,
		HookRawBead:  intent.HookRawBead,
		Owned:        intent.Owned,
		Mode:         intent.Mode,
		EnqueuedAt:   enqueuedAt,
	}
	if len(intent.Vars) > 0 {
		fields.Vars = strings.Join(intent.Vars, "\n")
	}
	return fields
}

func splitVars(vars string) []string {
	if vars == "" {
		return nil
	}
	var result []string
	for _, line := range strings.Split(vars, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
