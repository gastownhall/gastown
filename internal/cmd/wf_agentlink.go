package cmd

import (
	"fmt"
	"os"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/agentlink/workflowlink"
	"github.com/steveyegge/gastown/internal/workflow"
)

// observeIfJoined wires the agentlink observer into env when the town has
// joined a gtb server (agentlink.LoadToken succeeds) and GTB_AGENTLINK is not
// 0: workflow transitions, evidence and item state writes made by this CLI
// process are then queued in the town's outbox for the daemon to send. An
// unjoined town gets env back unchanged and nothing is written.
func observeIfJoined(env workflow.GateEnv) workflow.GateEnv {
	if env.TownRoot == "" || agentlink.Disabled(os.Getenv) {
		return env
	}
	if _, _, _, err := agentlink.LoadToken(env.TownRoot); err != nil {
		return env
	}
	return workflowlink.Observe(env, agentlink.OpenQueue(env.TownRoot), &workflowlink.ObserveOptions{
		// Never fail the command on a queue error: the daemon's next scan
		// or snapshot reconciles the server. Report it on stderr.
		Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...) },
	})
}
