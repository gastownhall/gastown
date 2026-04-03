//go:build !darwin

package quota

import "context"

// ProbeStatus represents the outcome of an account probe.
type ProbeStatus int

const (
	ProbeOK           ProbeStatus = iota
	ProbeRateLimited
	ProbeAuthError
	ProbeNoToken
	ProbeNetworkError
)

// ProbeResult holds the outcome of a non-LLM API probe.
type ProbeResult struct {
	Status   ProbeStatus
	ResetsAt string
	Err      error
}

func (r *ProbeResult) OK() bool { return r.Status == ProbeOK }

func ProbeAccount(_ string) *ProbeResult {
	return &ProbeResult{Status: ProbeOK}
}

func ProbeAccountWithContext(_ context.Context, _ string) *ProbeResult {
	return &ProbeResult{Status: ProbeOK}
}
