package sling

import "context"

// Lifecycle is the deep Slinging interface. One method owns ordering from
// validation through Hook and Bead attachment, including compensation on
// fatal failure after durable artifacts exist.
type Lifecycle interface {
	Execute(ctx context.Context, intent Intent) (*Outcome, error)
}
