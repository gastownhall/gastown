package polecat

import "strings"

const (
	WorkstateVerdictWorking       = "WORKING"
	WorkstateVerdictSafeToNuke    = "SAFE_TO_NUKE"
	WorkstateVerdictPendingMR     = "PENDING_MR"
	WorkstateVerdictNeedsRecovery = "NEEDS_RECOVERY"
	WorkstateVerdictNeedsMQSubmit = "NEEDS_MQ_SUBMIT"
)

// WorkstateInput contains the lifecycle, git, and merge-queue facts needed to
// classify a polecat consistently across list, recovery, witness, and capacity.
type WorkstateInput struct {
	State                          State
	HookBead                       string
	ActiveWorkBlocker              string
	ActiveWorkCountsTowardCapacity bool
	CleanupStatus                  CleanupStatus
	IgnoreCleanupStatus            bool
	PushFailed                     bool
	MRFailed                       bool
	Branch                         string
	GitDirty                       bool
	GitDirtyReason                 string
	StashCount                     int
	UnpushedCommits                int
	GitCheckFailed                 bool
	GitCheckFailedReason           string
	ActiveMR                       string
	ActiveMRBlocker                string
	ActiveMRMalformed              bool
	MQCheckRequired                bool
	HasSubmittableWork             bool
	MQNotRequired                  bool
	AssignedBeadTerminal           bool
	MRSubmitted                    bool
	MQLookupFailed                 bool
}

// WorkstateDisposition is the canonical polecat lifecycle decision. It is pure
// policy: callers gather facts, this classifier decides how every subsystem
// should present and count the polecat.
type WorkstateDisposition struct {
	Verdict              string   `json:"verdict"`
	Reason               string   `json:"reason,omitempty"`
	Reusable             bool     `json:"reusable"`
	SafeToNuke           bool     `json:"safe_to_nuke"`
	NeedsRecovery        bool     `json:"needs_recovery"`
	NeedsMQSubmit        bool     `json:"needs_mq_submit"`
	MQStatus             string   `json:"mq_status,omitempty"`
	CountsTowardCapacity bool     `json:"counts_toward_capacity"`
	ReuseStatus          string   `json:"reuse_status,omitempty"`
	Blockers             []string `json:"blockers,omitempty"`
}

// ApplyActiveWork projects shared active/protected work evidence into the
// workstate input fields that block reuse and cleanup.
func (in *WorkstateInput) ApplyActiveWork(evidence ActiveWorkEvidence) {
	if !evidence.BlocksCleanup {
		return
	}
	in.ActiveWorkBlocker = evidence.Blocker
	in.HookBead = evidence.HookBead
	activeAssignedWork := evidence.AssignedIssue != "" || evidence.HookBead != ""
	in.ActiveWorkCountsTowardCapacity = in.ActiveWorkCountsTowardCapacity || (activeAssignedWork && (evidence.Active || evidence.RequiresRestart))
}

// DecideWorkstate returns the canonical disposition for a polecat.
func DecideWorkstate(in WorkstateInput) WorkstateDisposition {
	if in.ActiveMRMalformed {
		blocker := in.ActiveMRBlocker
		if blocker == "" {
			blocker = "active_mr=" + in.ActiveMR + " reconcile_needed=malformed_source"
		}
		return WorkstateDisposition{
			Verdict:              WorkstateVerdictNeedsRecovery,
			Reason:               "active-mr-malformed",
			NeedsRecovery:        true,
			CountsTowardCapacity: in.State != StateIdle || in.ActiveWorkCountsTowardCapacity,
			ReuseStatus:          "idle-recovery-needed",
			Blockers:             []string{blocker},
		}
	}

	if in.State != StateIdle {
		verdict := WorkstateVerdictNeedsRecovery
		needsRecovery := true
		if in.State == StateWorking {
			verdict = WorkstateVerdictWorking
			needsRecovery = false
		}
		return WorkstateDisposition{
			Verdict:              verdict,
			Reason:               "not-idle",
			NeedsRecovery:        needsRecovery,
			CountsTowardCapacity: true,
		}
	}

	d := WorkstateDisposition{Verdict: WorkstateVerdictSafeToNuke}
	block := func(reason, blocker string) {
		if d.Reason == "" {
			d.Reason = reason
		}
		if blocker != "" {
			d.Blockers = append(d.Blockers, blocker)
		}
	}

	if in.ActiveWorkBlocker != "" {
		reason := "active-work"
		if in.HookBead != "" {
			reason = "hook-still-set"
		}
		block(reason, in.ActiveWorkBlocker)
	} else if in.HookBead != "" {
		block("hook-still-set", "has work on hook ("+in.HookBead+")")
	}
	if in.PushFailed {
		block("push-failed", "push_failed=true")
	}
	if in.MRFailed {
		block("mr-failed", "mr_failed=true")
	}
	if !in.IgnoreCleanupStatus && !in.CleanupStatus.IsSafe() {
		reason := "cleanup-" + string(in.CleanupStatus)
		blocker := "cleanup_status=" + string(in.CleanupStatus)
		if in.CleanupStatus == "" {
			reason = "cleanup-unknown"
			blocker = "cleanup_status=<missing>"
		} else if in.CleanupStatus == CleanupUnknown {
			reason = "cleanup-unknown"
		}
		block(reason, blocker)
	}
	if in.GitCheckFailed {
		blocker := in.GitCheckFailedReason
		if blocker == "" {
			blocker = "git_state=unknown"
		}
		block("git-check-failed", blocker)
	}
	if in.GitDirty {
		blocker := in.GitDirtyReason
		if blocker == "" {
			blocker = "git_state=has_uncommitted"
		}
		block("git-dirty", blocker)
	}
	if in.StashCount > 0 {
		block("git-stash", "git_state=has_stash stash_count="+itoa(in.StashCount))
	}
	if in.UnpushedCommits > 0 {
		block("git-unpushed", "git_state=has_unpushed unpushed_commits="+itoa(in.UnpushedCommits))
	}
	activeMRBlocks := in.ActiveMRBlocker != ""
	if activeMRBlocks {
		block("active-mr-open", in.ActiveMRBlocker)
	}

	if len(d.Blockers) > 0 {
		if activeMRBlocks && len(d.Blockers) == 1 {
			d.Verdict = WorkstateVerdictPendingMR
			d.ReuseStatus = "idle-pr-open"
			return d
		}
		d.Verdict = WorkstateVerdictNeedsRecovery
		d.NeedsRecovery = true
		d.CountsTowardCapacity = in.ActiveWorkCountsTowardCapacity
		d.ReuseStatus = "idle-recovery-needed"
		return d
	}

	if in.MQCheckRequired {
		if !in.HasSubmittableWork || in.MQNotRequired {
			d.MQStatus = "not_required"
		} else if in.AssignedBeadTerminal || in.MRSubmitted {
			d.MQStatus = "submitted"
		} else if in.MQLookupFailed {
			d.Verdict = WorkstateVerdictNeedsRecovery
			d.Reason = "mq-lookup-failed"
			d.NeedsRecovery = true
			d.MQStatus = "unknown"
			d.ReuseStatus = "idle-recovery-needed"
			d.Blockers = append(d.Blockers, "mq_status=unknown")
			return d
		} else {
			d.Verdict = WorkstateVerdictNeedsMQSubmit
			d.Reason = "mq-not-submitted"
			d.NeedsRecovery = true
			d.NeedsMQSubmit = true
			d.MQStatus = "not_submitted"
			d.ReuseStatus = "idle-recovery-needed"
			d.Blockers = append(d.Blockers, "mq_status=not_submitted")
			return d
		}
	}

	d.Reusable = true
	d.SafeToNuke = true
	d.Reason = "reusable"
	if strings.HasPrefix(in.Branch, "polecat/") {
		d.ReuseStatus = "idle-preserved"
	} else {
		d.ReuseStatus = "idle-clean"
	}
	return d
}

// CanIgnoreStaleCleanupStatus returns true when a dirty persisted
// cleanup_status is older than the direct predicates proving no work is at risk.
// The status remains unsafe globally; callers must opt into this reconciliation
// path only after gathering live git, hook, work, and active-MR facts.
func CanIgnoreStaleCleanupStatus(status CleanupStatus, workTerminal, hookSafe, activeMRSafe, gitSafe bool) bool {
	if !workTerminal || !hookSafe || !activeMRSafe || !gitSafe {
		return false
	}
	switch status {
	case CleanupUncommitted, CleanupStash, CleanupUnpushed:
		return true
	default:
		return false
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
