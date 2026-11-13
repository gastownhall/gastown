// Package agenthealth judges whether an agent is doing work, not merely whether
// a process exists for it.
//
// The defect this package exists to fix (hq-40k / gt-o57): every health surface
// in town keyed on tmux session existence. That predicate is satisfied by a
// frozen agent, so it inverted the failure modes it was meant to cover — a
// CRASHED witness was respawned (no session, guard did not fire) while a FROZEN
// witness was actively protected by the guard meant to ensure one was running.
// gastown/witness sat unresponsive for three days while the daemon evaluated it
// 1,887 consecutive times and passed it every time.
//
// The rules this package holds to:
//
//  1. The predicate must be something a frozen process cannot satisfy. Here it
//     is the age of the agent's hooked work — a frozen agent cannot advance a
//     bead's updated_at.
//  2. The frozen case must be handled at least as well as the crashed case.
//     StateDegraded, like StateStopped, does not suppress a spawn.
//  3. Where the check cannot decide, it reports StateUnknown — never healthy.
//     Every check in this family that failed did so by asserting the reassuring
//     answer when it could not produce the disconfirming one.
package agenthealth

import (
	"fmt"
	"time"
)

// State is the outcome of a work-progress assessment.
type State string

const (
	// StateStopped means no session exists. The agent is gone and should be
	// (re)spawned. This is the benign, self-correcting failure.
	StateStopped State = "stopped"

	// StateHealthy means a session exists and either its hooked work has
	// advanced within the threshold or it has no hooked work to advance.
	StateHealthy State = "healthy"

	// StateDegraded means a session exists but its hooked work has not advanced
	// past the threshold: alive and not working. This is the failure the old
	// session-existence check protected instead of reporting.
	StateDegraded State = "degraded"

	// StateUnknown means the check could not decide. It is deliberately not
	// healthy: an undecidable check must never assert the reassuring answer.
	StateUnknown State = "unknown"
)

// Work is the minimal view of a hooked bead needed to judge progress.
// Timestamps are zero when the source value was absent or unparseable.
type Work struct {
	ID        string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Progress returns the timestamp at which this work last advanced, preferring
// updated_at and falling back to created_at. A zero return means the bead
// carries no usable timestamp, which is an undecidable input, not a stale one.
func (w Work) Progress() time.Time {
	if !w.UpdatedAt.IsZero() {
		return w.UpdatedAt
	}
	return w.CreatedAt
}

// Input is everything Assess needs. Callers supply the session-liveness answer
// and the hooked-work answer separately, each with its own error, so that a
// failure to determine either produces StateUnknown rather than a default.
type Input struct {
	// SessionAlive reports whether a session exists for the agent.
	SessionAlive bool

	// SessionErr, when non-nil, means liveness could not be determined.
	SessionErr error

	// HookedWork is every bead currently on the agent's hook. Empty means the
	// agent has nothing to be stalled on; nil with a non-nil WorkErr means the
	// hook could not be read.
	HookedWork []Work

	// WorkErr, when non-nil, means the hook could not be read.
	WorkErr error

	// Threshold is how long hooked work may go without advancing before the
	// agent is degraded. It is an explicit input rather than a constant on
	// purpose: what an unadvancing hook MEANS is a property of the town's
	// current propulsion behaviour, not of this detector, and it is expected to
	// change. A non-positive threshold is undecidable, not permissive.
	Threshold time.Duration

	// Now is the reference time. Zero means time.Now().
	Now time.Time
}

// Assessment is the verdict plus the evidence behind it.
type Assessment struct {
	State        State
	SessionAlive bool

	// HookBeadID is the hooked bead whose progress timestamp was used.
	HookBeadID string

	// HookIdle is how long the hook has gone without advancing.
	// Only meaningful when HookIdleKnown is true.
	HookIdle      time.Duration
	HookIdleKnown bool

	// Threshold is the threshold the verdict was measured against.
	Threshold time.Duration

	// Reason is a short human-readable explanation of the verdict.
	Reason string
}

// SuppressesSpawn reports whether this assessment justifies skipping a spawn.
//
// Only StateHealthy does. A degraded or undecidable agent must not be able to
// suppress its own replacement — that suppression is precisely the defect: a
// frozen agent kept its own recovery from firing 1,887 times running.
func (a Assessment) SuppressesSpawn() bool {
	return a.State == StateHealthy
}

// NeedsAttention reports whether a human or patrol should look at this agent.
func (a Assessment) NeedsAttention() bool {
	return a.State == StateDegraded || a.State == StateUnknown
}

// Describe renders the assessment for a status line, e.g. "running, hook idle 21h".
func (a Assessment) Describe() string {
	switch a.State {
	case StateStopped:
		return "stopped"
	case StateDegraded:
		if a.HookIdleKnown {
			return fmt.Sprintf("running, hook idle %s", FormatIdle(a.HookIdle))
		}
		return "running, hook not advancing"
	case StateUnknown:
		return "running, progress unknown"
	default:
		return "running"
	}
}

// Assess judges an agent from session liveness and hooked-work progress.
func Assess(in Input) Assessment {
	now := in.Now
	if now.IsZero() {
		now = time.Now()
	}

	a := Assessment{
		SessionAlive: in.SessionAlive,
		Threshold:    in.Threshold,
	}

	// Liveness undecidable — say so rather than guessing either way.
	if in.SessionErr != nil {
		a.State = StateUnknown
		a.SessionAlive = false
		a.Reason = fmt.Sprintf("session liveness could not be determined: %v", in.SessionErr)
		return a
	}

	// No session: the crashed case. Unchanged behaviour — spawn proceeds.
	if !in.SessionAlive {
		a.State = StateStopped
		a.Reason = "no session"
		return a
	}

	// From here the session exists, which is exactly the point at which the old
	// check stopped and declared health.

	if in.Threshold <= 0 {
		a.State = StateUnknown
		a.Reason = "no hook-idle threshold configured"
		return a
	}

	if in.WorkErr != nil {
		a.State = StateUnknown
		a.Reason = fmt.Sprintf("hooked work could not be read: %v", in.WorkErr)
		return a
	}

	if len(in.HookedWork) == 0 {
		// Nothing on the hook is a decidable answer: there is no work to stall
		// on. This is not the undecidable case.
		a.State = StateHealthy
		a.Reason = "no hooked work"
		return a
	}

	// Any advancing hooked bead counts as progress: take the most recent.
	var newest time.Time
	var newestID string
	for _, w := range in.HookedWork {
		ts := w.Progress()
		if ts.IsZero() {
			continue
		}
		if ts.After(newest) {
			newest, newestID = ts, w.ID
		}
	}
	if newest.IsZero() {
		a.State = StateUnknown
		a.Reason = "hooked work carries no usable timestamp"
		return a
	}

	idle := now.Sub(newest)
	if idle < 0 {
		idle = 0
	}
	a.HookBeadID = newestID
	a.HookIdle = idle
	a.HookIdleKnown = true

	if idle >= in.Threshold {
		a.State = StateDegraded
		a.Reason = fmt.Sprintf("hooked work %s has not advanced in %s (threshold %s)",
			newestID, FormatIdle(idle), FormatIdle(in.Threshold))
		return a
	}

	a.State = StateHealthy
	a.Reason = fmt.Sprintf("hooked work %s advanced %s ago", newestID, FormatIdle(idle))
	return a
}

// FormatIdle renders a duration compactly for status output: 45s, 12m, 3h, 4d.
func FormatIdle(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
