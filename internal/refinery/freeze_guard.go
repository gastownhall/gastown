package refinery

import (
	"fmt"
	"strings"
)

// FrozenStatuses are bead statuses that mean "this work is deliberately not to
// proceed". A bead in one of these has been set aside by a decision, and a merge
// of work behind it lands something the town has ruled should not land.
//
// deferred is the operative one: bd defer is how a freeze is expressed in data.
var FrozenStatuses = map[string]string{
	"deferred": "deferred (set aside by decision)",
	"blocked":  "blocked (a dependency or ruling holds it)",
}

// FreezeFinding is a branch whose bead says the work must not proceed.
type FreezeFinding struct {
	// BeadID is the work bead behind the branch.
	BeadID string
	// Status is the bead status that triggered the halt.
	Status string
	// Title is the bead title, for the halt message.
	Title string
	// Explanation renders why this status halts a merge.
	Explanation string
}

func (f FreezeFinding) String() string {
	return fmt.Sprintf(
		"%s is %s, so merging this branch would land work the town has set aside: %q",
		f.BeadID, f.Explanation, f.Title)
}

// beadStatusLookup reads a bead's status and title. Kept as a function value so
// the guard is testable without a beads database.
type beadStatusLookup func(beadID string) (status, title string, err error)

// CheckFrozen reports whether the bead behind a branch says its work must not
// proceed, in which case the merge should HALT for judgement.
//
// WHY THIS EXISTS. On 2026-09-10 the mayor halted four polecats working TV-card
// intake beads under a standing freeze, and then deferred all 637 of those beads
// so the freeze lived in bead status rather than only in a ruling. Dispatch was
// sealed: open 0, hooked 0, and bd ready contained no TV cards. Eleven minutes
// later ap-wvk merged to main anyway, and ap-9o8 followed four minutes after.
//
// The polecats had obeyed. A checkpoint hook committed inside their worktrees
// because core.hooksPath lives in the shared repo config, an auto-save safety net
// committed the rest, and the refinery merged the branches. Nothing in the merge
// path had any notion of a frozen or deferred bead.
//
// THE LESSON THIS ENCODES: every other guard in the town governs SELECTION - the
// ruling, the halt, the bead status, bd ready. None of them governs LANDING. Work
// already sitting on a branch reaches main regardless of what its bead says. A
// guard must be attached to the step that performs the hazardous act, and the
// hazardous act is the merge.
//
// DISPOSITION IS A HALT, NOT A REJECT, matching CheckForReverts: deliberately
// landing set-aside work is a legitimate decision, so a flagged branch stops for
// judgement rather than being refused outright.
func CheckFrozen(beadID string, lookup beadStatusLookup) (*FreezeFinding, error) {
	beadID = strings.TrimSpace(beadID)
	if beadID == "" {
		// No bead behind the branch is not this guard's business to refuse.
		return nil, nil
	}
	status, title, err := lookup(beadID)
	if err != nil {
		// FAIL CLOSED IS WRONG HERE and fail open is also wrong, so say so: the
		// caller decides. Returning an error rather than "not frozen" keeps a
		// lookup failure from silently reading as permission to merge, which is
		// the exact shape of the defect this guard exists to close.
		return nil, fmt.Errorf("cannot determine whether %s is frozen: %w", beadID, err)
	}
	explanation, frozen := FrozenStatuses[strings.ToLower(strings.TrimSpace(status))]
	if !frozen {
		return nil, nil
	}
	return &FreezeFinding{
		BeadID:      beadID,
		Status:      strings.ToLower(strings.TrimSpace(status)),
		Title:       title,
		Explanation: explanation,
	}, nil
}
