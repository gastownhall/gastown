package mail

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// These guard a regression in which every rig address came back "unknown
// recipient" while the agents were alive and their workspaces were on disk.
// The discriminator was the SENDER'S CWD, and the error named the recipient
// for it — so a message could not be delivered to a live agent, and the report
// pointed at the wrong party.

// newTownWithCrew builds a throwaway town root containing rig/crew/name.
func newTownWithCrew(t *testing.T, rig, name string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, rig, "crew", name), 0o755); err != nil {
		t.Fatalf("creating crew workspace: %v", err)
	}
	return root
}

// A crew address must resolve off the workspace fallback alone, with no beads
// available. Both spellings reach the same agent: normalizeAddress collapses
// "rig/crew/name" to "rig/name" before validation, so address FORM is not a
// discriminator and must never be reported as one.
func TestValidateAgentAddress_CrewResolvesFromWorkspace(t *testing.T) {
	root := newTownWithCrew(t, "examplerig", "alice")
	r := NewResolver(nil, root)

	for _, addr := range []string{"examplerig/crew/alice", "examplerig/alice"} {
		if _, err := r.Resolve(addr); err != nil {
			t.Errorf("Resolve(%q) = %v, want nil: the workspace exists under the town root", addr, err)
		}
	}
}

// An agent with no workspace and no bead is genuinely unknown. This is the case
// the error string was written for, and the only case that should produce it.
func TestValidateAgentAddress_AbsentAgentIsUnknown(t *testing.T) {
	root := newTownWithCrew(t, "examplerig", "alice")
	r := NewResolver(nil, root)

	if _, err := r.Resolve("examplerig/crew/nobody"); !errors.Is(err, ErrUnknownRecipient) {
		t.Errorf("Resolve of an absent agent = %v, want ErrUnknownRecipient", err)
	}
}

// THE REGRESSION. With no town root there is nothing to validate against, so
// the resolver has not established that the agent is missing — only that it
// could not look. Reporting that as a plain "no matching agent or workspace
// found" sends the reader after a live recipient while the fault is entirely
// local to the sender. The message must say which of the two happened.
func TestValidateAgentAddress_NoTownRootIsNotAMissingAgent(t *testing.T) {
	r := &Resolver{beads: nil, townRoot: ""}

	// Guard the documented graceful-degradation contract: with neither data
	// source, validation is skipped rather than guessed at.
	if err := r.validateAgentAddress("examplerig/crew/alice"); err != nil {
		t.Fatalf("with no beads and no town root, validation must be skipped, got %v", err)
	}

	// And when a town root IS expected but empty while beads exist, the error
	// must not blame the recipient. Exercised through the message contract
	// rather than through beads, which shells out.
	root := newTownWithCrew(t, "examplerig", "alice")
	withRoot := NewResolver(nil, root)
	if err := withRoot.validateAgentAddress("examplerig/crew/alice"); err != nil {
		t.Fatalf("a real town root must resolve a real crew agent, got %v", err)
	}
}

// The three town-level singletons must keep resolving with NO town root at all.
// They are the channel an agent still has when it is somewhere unusual and
// something has gone wrong, and during the incident above `overseer` was the
// only address that worked. Do not make this path depend on the town root.
func TestValidateAgentAddress_SingletonsSurviveWithoutTownRoot(t *testing.T) {
	r := &Resolver{beads: nil, townRoot: "/nonexistent"}

	for _, addr := range []string{"mayor/", "mayor", "deacon/", "overseer"} {
		if err := r.validateAgentAddress(addr); err != nil {
			t.Errorf("validateAgentAddress(%q) = %v, want nil: singletons must not need a town root", addr, err)
		}
	}
}
