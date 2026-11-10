// Package workflow defines gtb's gated work-item workflow: the item kinds,
// their ordered step tables, and the on-disk markdown artifacts (with
// status-insensitive content hashing) that gate progression between steps.
package workflow

// Kind is the type of a work item, which selects its step table.
type Kind string

// Work item kinds.
const (
	KindTask Kind = "task"
	KindFix  Kind = "fix"
	KindEpic Kind = "epic"
)

// Step is one stage of a work item's workflow.
type Step string

// StepDone is the terminal pseudo-step written by the refinery after merge; it is not part of any step table.
const StepDone Step = "done"
