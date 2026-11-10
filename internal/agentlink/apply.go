// This file holds the request-handling contract between the agent loop and
// whatever applies requests on the machine (internal/agentlink/workflowlink
// in production).
package agentlink

import "context"

// Result error codes the machine reports.
const (
	ErrCodeExpired     = "expired"
	ErrCodeUnsupported = "unsupported"
	ErrCodeStaleSHA    = "stale_sha"
	ErrCodeInvalid     = "invalid_params"
	ErrCodeNotFound    = "not_found"
	ErrCodeFailed      = "failed"
)

// Applier applies one server request on the machine and returns its result.
// The loop has already dropped expired and already-applied requests; Apply
// must still never panic on unknown types (answer ErrCodeUnsupported).
type Applier interface {
	Apply(ctx context.Context, r Request) Result
}

// ApplierFunc adapts a function to Applier.
type ApplierFunc func(ctx context.Context, r Request) Result

// Apply implements Applier.
func (f ApplierFunc) Apply(ctx context.Context, r Request) Result { return f(ctx, r) }

// Fail returns a failed Result for key with code.
func Fail(key, code string) Result { return Result{Key: key, OK: false, Error: code} }

// DisableEnv is the environment variable that turns the machine link off:
// GTB_AGENTLINK=0 stops the daemon loop and the CLI event hooks.
const DisableEnv = "GTB_AGENTLINK"

// Disabled reports whether GTB_AGENTLINK=0 (or false/off) is set.
func Disabled(getenv func(string) string) bool {
	switch getenv(DisableEnv) {
	case "0", "false", "off", "no":
		return true
	}
	return false
}
