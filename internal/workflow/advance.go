package workflow

import (
	"fmt"
	"time"
)

// Advance moves item itemID to its next step when its current step's gate
// passes. The current step is the store-derived one (see effectiveStep). An
// epic that passes its last step (verification) moves to StepDone.
//
// A failed gate returns the item and the result with a nil error and records
// nothing. A lost race returns the error from Store.Transition (wrapping
// ErrStepConflict). If the transition is recorded but Items.SetStep fails,
// the transition is kept (evidence is append-only) and the returned item
// reflects the new step alongside the wrapped SetStep error. A successful
// transition clears the item's waiting-for-human reason.
func Advance(env GateEnv, itemID, actor string) (*Item, GateResult, error) {
	it, err := env.Items.Get(itemID)
	if err != nil {
		return nil, GateResult{}, err
	}
	cur, entered, _, err := effectiveStep(env, it)
	if err != nil {
		return nil, GateResult{}, fmt.Errorf("workflow: reading transitions for %s: %w", itemID, err)
	}
	it.Step, it.StepEnteredAt = cur, entered

	res := Gate(env, it)
	if !res.Passed {
		return it, res, nil
	}
	next, ok := Next(it.Kind, cur)
	if !ok && it.Kind == KindEpic {
		// Epics have no merge step: passing verification finishes the epic.
		// Tasks and fixes reach done only through the refinery.
		next, ok = StepDone, true
	}
	if !ok {
		res.Passed = false
		res.Reasons = append(res.Reasons, fmt.Sprintf("%s is the last step", cur))
		return it, res, nil
	}
	if err := env.Store.Transition(itemID, cur, next, actor); err != nil {
		return it, res, err
	}

	tr := Transition{ItemID: itemID, From: cur, To: next, Actor: actor, At: time.Now().UTC()}
	if trs, err := env.Store.Transitions(itemID); err == nil && len(trs) > 0 {
		if last := trs[len(trs)-1]; last.From == cur && last.To == next {
			tr = last
		}
	}
	env.emit(EventTransition, itemID, tr)

	it.Step, it.StepEnteredAt = next, tr.At
	if err := env.Items.SetStep(itemID, next, tr.At); err != nil {
		return it, res, fmt.Errorf("workflow: transition %s %s->%s recorded but set step failed: %w", itemID, cur, next, err)
	}
	// The step the item waited at is behind it: clear any human wait (R21).
	it.WaitingForHuman = ""
	if err := env.Items.SetWaiting(itemID, ""); err != nil {
		return it, res, fmt.Errorf("workflow: transition %s %s->%s recorded but clearing waiting failed: %w", itemID, cur, next, err)
	}
	return it, res, nil
}
