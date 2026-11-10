package daemon

import (
	"context"
	"errors"
	"os"
	"sync"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/agentlink/workflowlink"
)

// GtbVersion is reported to the gtb server in Hello. internal/cmd sets it
// before starting the daemon (the daemon cannot import cmd).
var GtbVersion = "dev"

// agentlinkState holds the link goroutine's lifecycle.
type agentlinkState struct {
	agentlinkWG sync.WaitGroup
}

// startAgentLink starts the machine link (agentlink.Run, via run) in a
// goroutine bound to the daemon's context when the town has joined a gtb
// server and GTB_AGENTLINK is not 0. It returns whether it started. A town
// without a token is left alone. The token is never logged.
func (d *Daemon) startAgentLink(run func(context.Context, agentlink.LoopConfig) error) bool {
	if agentlink.Disabled(os.Getenv) {
		d.logger.Printf("agentlink: disabled by %s=0", agentlink.DisableEnv)
		return false
	}
	town := d.config.TownRoot
	serverURL, machineID, token, err := agentlink.LoadToken(town)
	if err != nil {
		if agentlink.HasToken(town) {
			d.logger.Printf("agentlink: not starting: %v", err)
		}
		return false
	}
	exe, err := os.Executable()
	if err != nil {
		d.logger.Printf("agentlink: not starting: finding gtb executable: %v", err)
		return false
	}
	q := agentlink.OpenQueue(town)
	tw := workflowlink.NewTown(town, d.logger.Printf)
	col := tw.Collector(q)
	cfg := agentlink.LoopConfig{
		TownRoot: town, ServerURL: serverURL, MachineID: machineID, Token: token,
		GtbVersion: GtbVersion,
		Collect:    col.Collect,
		Scan:       col.Scan,
		Apply:      tw.Applier(q, exe),
		Logf:       d.logger.Printf,
	}
	d.logger.Printf("agentlink: starting link to %s", serverURL)
	d.agentlinkWG.Add(1)
	go func() {
		defer d.agentlinkWG.Done()
		if err := run(d.ctx, cfg); err != nil && !errors.Is(err, agentlink.ErrTokenRejected) {
			d.logger.Printf("agentlink: stopped: %v", err)
		}
	}()
	return true
}
