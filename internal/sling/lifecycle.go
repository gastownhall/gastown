package sling

import "context"

// Lifecycle is the deep Slinging interface. One method owns ordering from
// validation through Hook and Bead attachment, including compensation on
// fatal failure after durable artifacts exist. Rig dispatch, named targets
// (Mayor, Crew, dogs, self), and deferred queue dispatch all cross this
// seam. Callers do not acquire the per-bead flock, run the cross-rig guard,
// or wake the witness themselves.
type Lifecycle interface {
	Execute(ctx context.Context, intent Intent) (*Outcome, error)
}
