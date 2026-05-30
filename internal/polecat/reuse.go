package polecat

import "errors"

// ErrPolecatNeedsRecovery marks an idle-looking polecat that must not be reset
// or advertised as reusable until its preserved work is recovered or submitted.
var ErrPolecatNeedsRecovery = errors.New("polecat needs recovery before reuse")

// SlotReuseInput is the shared input for deciding whether a polecat slot can be
// advertised as open and destructively reused for new work.
type SlotReuseInput struct {
	State                State
	HookBead             string
	CleanupStatus        CleanupStatus
	IgnoreCleanupStatus  bool
	PushFailed           bool
	MRFailed             bool
	Branch               string
	GitDirty             bool
	GitDirtyReason       string
	StashCount           int
	UnpushedCommits      int
	GitCheckFailed       bool
	GitCheckFailedReason string
	ActiveMR             string
	ActiveMRBlocker      string
	MQCheckRequired      bool
	HasSubmittableWork   bool
	MQNotRequired        bool
	AssignedBeadTerminal bool
	MRSubmitted          bool
	MQLookupFailed       bool
}

// SlotReuseDecision explains whether a polecat can be reused and why not.
type SlotReuseDecision struct {
	Reusable bool
	Reason   string
}

// DecideSlotReuse is the single source of truth for reuse safety. It fails
// closed: unknown cleanup/git state means the slot needs recovery, not reuse.
func DecideSlotReuse(in SlotReuseInput) SlotReuseDecision {
	d := DecideWorkstate(WorkstateInput{
		State:                in.State,
		HookBead:             in.HookBead,
		CleanupStatus:        in.CleanupStatus,
		IgnoreCleanupStatus:  in.IgnoreCleanupStatus,
		PushFailed:           in.PushFailed,
		MRFailed:             in.MRFailed,
		Branch:               in.Branch,
		GitDirty:             in.GitDirty,
		GitDirtyReason:       in.GitDirtyReason,
		StashCount:           in.StashCount,
		UnpushedCommits:      in.UnpushedCommits,
		GitCheckFailed:       in.GitCheckFailed,
		GitCheckFailedReason: in.GitCheckFailedReason,
		ActiveMR:             in.ActiveMR,
		ActiveMRBlocker:      in.ActiveMRBlocker,
		MQCheckRequired:      in.MQCheckRequired,
		HasSubmittableWork:   in.HasSubmittableWork,
		MQNotRequired:        in.MQNotRequired,
		AssignedBeadTerminal: in.AssignedBeadTerminal,
		MRSubmitted:          in.MRSubmitted,
		MQLookupFailed:       in.MQLookupFailed,
	})
	return SlotReuseDecision{Reusable: d.Reusable, Reason: d.Reason}
}
