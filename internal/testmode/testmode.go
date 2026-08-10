// Package testmode reports whether the current process is a Go test binary.
//
// Gas Town's commands perform real side effects on shared town state: they
// write event files into the town-global ~/gt/events/<channel>/ directories
// and send keystrokes into live agent tmux panes. The test suite runs from
// inside a live workspace, so those side effects reach production unless
// something stops them.
//
// gt-dog is the canonical failure: TestNudgeRefineryNoOpWithoutLog cleared
// the GT_TEST_NUDGE_LOG hook "to exercise the real path", so every
// `go test ./internal/cmd/...` emitted a real MQ_SUBMIT event into the town
// refinery channel. Because the channel is town-global and events are rarely
// consumed, all four rigs' refineries woke on synthetic "test message"
// events forever.
//
// Production code that is about to touch shared town state should call
// Active() and skip the side effect when it returns true. Guards belong on
// the *side effect*, not on the test: a test binary should be inert by
// default and opt in to real behaviour, never the reverse.
package testmode

import (
	"os"
	"testing"
)

// AllowEnv opts a test binary back into real side effects. Set it only for
// tests that deliberately drive real event/tmux behaviour against a
// throwaway town root that cannot reach production.
const AllowEnv = "GT_ALLOW_TEST_SIDE_EFFECTS"

// Active reports whether this process is a Go test binary that has not opted
// into real side effects. It returns false in the shipped gt binary, so
// production behaviour is unchanged.
func Active() bool {
	if !testing.Testing() {
		return false
	}
	return os.Getenv(AllowEnv) == ""
}
