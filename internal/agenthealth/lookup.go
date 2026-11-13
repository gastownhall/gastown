package agenthealth

import (
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
)

// WorkLister is the slice of *beads.Beads this package needs. Narrow on purpose
// so callers can substitute a stub in tests.
type WorkLister interface {
	List(beads.ListOptions) ([]*beads.Issue, error)
}

// HookIdleThreshold returns the configured threshold for how long an agent's
// hooked work may go without advancing before the agent is degraded.
//
// It reuses operational.session.gupp_violation_timeout — the town already has a
// name for "hooked work that isn't progressing" and a knob for it. Keeping the
// threshold in config rather than in a constant is deliberate: what an
// unadvancing hook means depends on how reliably agents run their propulsion
// loop, which is expected to change. Lower the knob as compliance improves.
func HookIdleThreshold(townRoot string) time.Duration {
	return config.LoadOperationalConfig(townRoot).GetSessionConfig().GUPPViolationTimeoutD()
}

// LookupHookedWork returns the beads currently on an agent's hook.
//
// Hooked roots live in either the durable issues table or the ephemeral wisps
// table — a witness's patrol wisp is in the latter — so both are queried and
// merged. An error from either table is returned rather than swallowed, so the
// caller can report StateUnknown instead of a default.
func LookupHookedWork(lister WorkLister, assignee string) ([]Work, error) {
	if lister == nil || assignee == "" {
		return nil, nil
	}

	seen := make(map[string]struct{})
	var out []Work

	for _, ephemeral := range []bool{false, true} {
		issues, err := lister.List(beads.ListOptions{
			Status:    beads.StatusHooked,
			Assignee:  assignee,
			Priority:  -1,
			Ephemeral: ephemeral,
		})
		if err != nil {
			return nil, err
		}
		for _, issue := range issues {
			if issue == nil || issue.ID == "" {
				continue
			}
			if _, dup := seen[issue.ID]; dup {
				continue
			}
			seen[issue.ID] = struct{}{}
			out = append(out, Work{
				ID:        issue.ID,
				CreatedAt: parseBeadTime(issue.CreatedAt),
				UpdatedAt: parseBeadTime(issue.UpdatedAt),
			})
		}
	}

	return out, nil
}

// parseBeadTime accepts the timestamp formats bd emits, returning the zero time
// when the value is absent or unparseable. A zero timestamp is treated by
// Assess as undecidable rather than as infinitely stale.
func parseBeadTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}
