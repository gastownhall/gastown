package capacity

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrOnSuccessFailed wraps dispatch-succeeded-but-cleanup-failed errors.
// Used to distinguish "polecat launched, context close failed" from
// "polecat never launched" in the OnFailure callback.
type ErrOnSuccessFailed struct{ Err error }

func (e *ErrOnSuccessFailed) Error() string {
	return "dispatch succeeded but OnSuccess failed: " + e.Err.Error()
}
func (e *ErrOnSuccessFailed) Unwrap() error { return e.Err }

// ErrCrossRigPrefix is returned when a bead's ID prefix does not match the
// target rig's registered prefix. This protects against cross-rig dispatch
// where, e.g., an `hq-` bead would be handed to a rig polecat whose DB only
// resolves `gt-` prefixes (gt-el4 / gastownhall/gastown#3800).
var ErrCrossRigPrefix = errors.New("cross-rig prefix dispatch refused")

// BeadIDPrefix returns the prefix of a bead ID — the substring before the
// first '-'. Returns "" if the ID has no dash.
//
// Examples: "gt-abc" -> "gt", "hq-uejt" -> "hq", "wisp-xyz" -> "wisp".
func BeadIDPrefix(beadID string) string {
	idx := strings.Index(beadID, "-")
	if idx < 0 {
		return ""
	}
	return beadID[:idx]
}

// AcceptsPrefix reports whether a bead ID's prefix matches the target rig's
// registered prefix. Empty rigPrefix means "unknown / accept" (the dispatcher
// degrades open rather than refusing dispatch when rig config is unavailable).
func AcceptsPrefix(rigPrefix, beadID string) bool {
	if rigPrefix == "" {
		return true
	}
	return BeadIDPrefix(beadID) == rigPrefix
}

// DispatchCycle is a capacity-controlled dispatch orchestrator.
// The core loop is generic — all domain logic is injected via callbacks.
type DispatchCycle struct {
	// AvailableCapacity returns the number of free dispatch slots.
	// Positive = that many slots available. Zero or negative = no capacity.
	AvailableCapacity func() (int, error)

	// QueryPending returns work items eligible for dispatch.
	// The implementation handles querying, readiness checks, and filtering.
	QueryPending func() ([]PendingBead, error)

	// Validate is an optional pre-dispatch hook called before Execute. A
	// non-nil return value short-circuits dispatch for that bead — Execute is
	// not called and OnFailure is invoked with the error. Used for fast
	// invariant checks (e.g., cross-rig prefix guard) that should not consume
	// failure quota or trigger expensive dispatch machinery.
	Validate func(PendingBead) error

	// Execute dispatches a single item. Called for each planned item.
	Execute func(PendingBead) error

	// OnSuccess is called after successful dispatch.
	OnSuccess func(PendingBead) error

	// OnFailure is called after failed dispatch.
	OnFailure func(PendingBead, error)

	// BatchSize caps items dispatched per cycle.
	BatchSize int

	// SpawnDelay between dispatches.
	SpawnDelay time.Duration
}

// DispatchReport summarizes the result of one dispatch cycle.
type DispatchReport struct {
	Dispatched int
	Failed     int
	Skipped    int
	Reason     string // "capacity" | "batch" | "ready" | "none"
}

// Plan returns the dispatch plan without executing. Used for dry-run.
func (c *DispatchCycle) Plan() (DispatchPlan, error) {
	cap, err := c.AvailableCapacity()
	if err != nil {
		return DispatchPlan{}, fmt.Errorf("checking capacity: %w", err)
	}

	pending, err := c.QueryPending()
	if err != nil {
		return DispatchPlan{}, fmt.Errorf("querying pending: %w", err)
	}

	return PlanDispatch(cap, c.BatchSize, pending), nil
}

// onSuccessRetries is the number of times to retry OnSuccess before giving up.
const onSuccessRetries = 2

// Run executes one dispatch cycle: query → plan → execute → report.
func (c *DispatchCycle) Run() (DispatchReport, error) {
	plan, err := c.Plan()
	if err != nil {
		return DispatchReport{}, err
	}
	return c.RunPlan(plan)
}

// RunPlan executes a precomputed dispatch plan. Used when callers need one
// authoritative plan for rendering, validation, and dispatch.
func (c *DispatchCycle) RunPlan(plan DispatchPlan) (DispatchReport, error) {
	report := DispatchReport{
		Skipped: plan.Skipped,
		Reason:  plan.Reason,
	}

	for i, b := range plan.ToDispatch {
		if c.Validate != nil {
			if err := c.Validate(b); err != nil {
				report.Failed++
				if c.OnFailure != nil {
					c.OnFailure(b, err)
				}
				continue
			}
		}

		if err := c.Execute(b); err != nil {
			report.Failed++
			if c.OnFailure != nil {
				c.OnFailure(b, err)
			}
			continue
		}

		// OnSuccess must succeed (e.g., closing the sling context) to prevent
		// re-dispatch on the next cycle. Retry before giving up.
		if c.OnSuccess != nil {
			var successErr error
			for attempt := 0; attempt <= onSuccessRetries; attempt++ {
				successErr = c.OnSuccess(b)
				if successErr == nil {
					break
				}
				if attempt < onSuccessRetries {
					time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
				}
			}
			if successErr != nil {
				// OnSuccess failed after retries — do NOT count as dispatched.
				// The dispatch ran but we couldn't close the context, so treat
				// it as a failure to prevent double-dispatch on the next cycle.
				report.Failed++
				if c.OnFailure != nil {
					c.OnFailure(b, &ErrOnSuccessFailed{Err: successErr})
				}
				continue
			}
		}

		report.Dispatched++

		// Inter-spawn delay (skip after last item)
		if c.SpawnDelay > 0 && i < len(plan.ToDispatch)-1 {
			time.Sleep(c.SpawnDelay)
		}
	}

	return report, nil
}
