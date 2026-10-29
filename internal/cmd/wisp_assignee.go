package cmd

import (
	"github.com/steveyegge/gastown/internal/agentaddr"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/style"
)

// propagateAssigneeToSteps copies a dispatched root wisp's resolved address
// down to its child step beads.
//
// The root gets the address the dispatcher resolved, but the steps are created
// by `bd mol wisp` from the formula and come back carrying the bare pool role
// the wisp was cooked under — "dog" rather than "deacon/dogs/alpha". A bare
// pool role names no single agent, so anything looking those steps up by
// assignee cannot find them and they are stranded when the root is closed.
//
// This runs after the root is hooked, so a step is only ever rewritten to an
// address that is already known to be complete. It is best-effort: the root is
// hooked and the work is dispatchable regardless, so a failure here warns
// rather than unwinding the dispatch.
func propagateAssigneeToSteps(workDir, rootID, resolvedAddress string) {
	if workDir == "" || rootID == "" {
		return
	}
	addr, ok := agentaddr.Parse(resolvedAddress)
	if !ok || !addr.IsComplete() {
		// Nothing better to propagate than what the steps already carry.
		return
	}
	canonical := addr.String()

	b := beads.New(workDir)
	children, err := listChildrenAcrossTables(b, rootID)
	if err != nil {
		style.PrintWarning("could not read steps of %s to set their assignee: %v", rootID, err)
		return
	}

	for _, child := range children {
		if child == nil || child.ID == "" {
			continue
		}
		// Leave a step alone when it already names this agent under any
		// spelling; rewriting it would only churn Dolt commits.
		if agentaddr.Equal(child.Assignee, canonical) {
			continue
		}
		// A step assigned to a genuinely different, fully-resolved agent was
		// deliberately routed elsewhere — a formula may fan a step out to
		// another worker. Only replace an assignee that names no single agent.
		if existing, ok := agentaddr.Parse(child.Assignee); ok && existing.IsComplete() {
			continue
		}
		if err := BdCmd("update", child.ID, "--assignee="+canonical).
			Dir(workDir).
			WithAutoCommit().
			Run(); err != nil {
			style.PrintWarning("could not set assignee of step %s to %s: %v", child.ID, canonical, err)
		}
	}
}
