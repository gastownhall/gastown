package cmd

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// slingStatusDecision is the Lifecycle-owned assignment policy: closed,
// garbage titles, dead-agent auto-force, same-target no-op, and deferred.
type slingStatusDecision struct {
	Force  bool
	NoOp   bool
	Merge  string
	Err    error
	ErrMsg string
}

// evaluateSlingStatus is the single assignment-guard implementation.
// sameTarget means this dispatch would land on the current assignee.
// formulaRefresh means new Formula work on that same assignee should proceed
// rather than no-op. explicitForce is the caller's --force, before auto-force.
func evaluateSlingStatus(info *beadInfo, beadID string, explicitForce bool, merge string, sameTarget, formulaRefresh bool) slingStatusDecision {
	if beads.IsFlagLikeTitle(info.Title) {
		return slingStatusDecision{
			ErrMsg: "flag-like title",
			Err:    fmt.Errorf("refusing to sling bead %s: title %q looks like a CLI flag (garbage bead from flag-parsing bug)", beadID, info.Title),
		}
	}
	if info.Status == "closed" || info.Status == "tombstone" {
		return slingStatusDecision{
			ErrMsg: "already " + info.Status,
			Err:    fmt.Errorf("bead %s is %s (work already completed)", beadID, info.Status),
		}
	}

	dec := slingStatusDecision{
		Force: explicitForce,
		Merge: resolveBeadMergeStrategy(merge, info),
	}

	if (info.Status == "pinned" || info.Status == "hooked" || info.Status == "in_progress") && !explicitForce {
		if (info.Status == "hooked" || info.Status == "in_progress") && info.Assignee != "" && isHookedAgentDeadFn(info.Assignee) {
			fmt.Printf("%s Hooked agent %s has no active session, auto-forcing re-sling...\n",
				style.Warning.Render("⚠"), info.Assignee)
			dec.Force = true
		} else if sameTarget && formulaRefresh {
			// Formula-on-bead against the live assignee: keep the hook, apply new work.
		} else if sameTarget {
			fmt.Printf("%s Bead %s is already %s to %s, no-op\n",
				style.Dim.Render("○"), beadID, info.Status, info.Assignee)
			dec.NoOp = true
			return dec
		} else {
			assignee := info.Assignee
			if assignee == "" {
				assignee = "(unknown)"
			}
			dec.ErrMsg = "already " + info.Status
			dec.Err = fmt.Errorf("bead %s is already %s to %s\nUse --force to re-sling", beadID, info.Status, assignee)
			return dec
		}
	}

	if isDeferredBead(info) && !explicitForce {
		dec.ErrMsg = "deferred"
		dec.Err = fmt.Errorf("refusing to sling deferred bead %s: %q\nDeferred work should not consume polecat slots. Use --force to override", beadID, info.Title)
		return dec
	}
	return dec
}
