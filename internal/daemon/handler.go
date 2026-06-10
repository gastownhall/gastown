package daemon

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
	"github.com/steveyegge/gastown/internal/mail"
	"github.com/steveyegge/gastown/internal/plugin"
	"github.com/steveyegge/gastown/internal/tmux"
)

// Dog lifecycle defaults — now config-driven via operational.daemon thresholds.
// These vars are still used as fallbacks and for tests; production code
// should prefer d.daemonCfg() accessors loaded from TownSettings.
var (
	// dogIdleSessionTimeout is how long a dog can be idle with a live tmux
	// session before the session is killed (default 1h).
	// Configurable via operational.daemon.dog_idle_session_timeout.
	dogIdleSessionTimeout = config.DefaultDogIdleSessionTimeout

	// dogIdleRemoveTimeout is how long a dog can be idle before it is removed
	// from the kennel entirely (only when pool is oversized, default 4h).
	// Configurable via operational.daemon.dog_idle_remove_timeout.
	dogIdleRemoveTimeout = config.DefaultDogIdleRemoveTimeout

	// staleWorkingTimeout is how long a dog can be in state=working with no
	// activity updates before it is considered stuck (default 2h).
	// Configurable via operational.daemon.stale_working_timeout.
	staleWorkingTimeout = config.DefaultStaleWorkingTimeout

	// maxDogPoolSize is the target pool size (default 4).
	// Configurable via operational.daemon.max_dog_pool_size.
	maxDogPoolSize = config.DefaultMaxDogPoolSize
)

// handleDogs manages Dog lifecycle: cleanup stuck dogs, reap idle dogs, then dispatch plugins.
// This is the main entry point called from heartbeat.
func (d *Daemon) handleDogs() {
	rigsConfig, err := d.loadRigsConfig()
	if err != nil {
		d.logger.Printf("Handler: failed to load rigs config: %v", err)
		return
	}

	opCfg := d.loadOperationalConfig().GetDaemonConfig()

	mgr := dog.NewManager(d.config.TownRoot, rigsConfig)
	t := tmux.NewTmux()
	sm := dog.NewSessionManager(t, d.config.TownRoot, mgr)

	d.cleanupStuckDogs(mgr, sm)
	d.detectStaleWorkingDogs(mgr, sm, opCfg)
	d.reapIdleDogs(mgr, sm, opCfg)
	d.dispatchPlugins(mgr, sm, rigsConfig)
}

// handleDogsCleanupOnly runs dog lifecycle cleanup (stuck, stale, idle) without
// dispatching new work. Used when pressure checks block new spawns.
func (d *Daemon) handleDogsCleanupOnly() {
	rigsConfig, err := d.loadRigsConfig()
	if err != nil {
		d.logger.Printf("Handler: failed to load rigs config: %v", err)
		return
	}

	opCfg := d.loadOperationalConfig().GetDaemonConfig()

	mgr := dog.NewManager(d.config.TownRoot, rigsConfig)
	t := tmux.NewTmux()
	sm := dog.NewSessionManager(t, d.config.TownRoot, mgr)

	d.cleanupStuckDogs(mgr, sm)
	d.detectStaleWorkingDogs(mgr, sm, opCfg)
	d.reapIdleDogs(mgr, sm, opCfg)
	// Skip dispatchPlugins — under pressure
}

// cleanupStuckDogs finds dogs in state=working whose tmux session is dead OR
// whose agent process has died inside a lingering session (zombie), and clears
// their work so they return to idle for immediate re-dispatch.
//
// Without the agent-liveness check, a zombie (session alive, agent dead, pane
// fell back to a bare shell) is mistaken for a healthy working dog: it is
// skipped here, never re-dispatched (state != idle), and only reclaimed after
// the 2h stale-working timeout — stranding all dog plugin work in the meantime.
func (d *Daemon) cleanupStuckDogs(mgr *dog.Manager, sm *dog.SessionManager) {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs: %v", err)
		return
	}

	for _, dg := range dogs {
		if dg.State != dog.StateWorking {
			continue
		}

		running, err := sm.IsRunning(dg.Name)
		if err != nil {
			d.logger.Printf("Handler: error checking session for dog %s: %v", dg.Name, err)
			continue
		}

		// Healthy working dog: session alive AND agent process alive.
		if running && sm.IsAgentAlive(dg.Name) {
			continue
		}

		// Zombie session (alive but agent dead): kill the stale session so a
		// fresh one can be started cleanly on the next dispatch.
		if running {
			d.logger.Printf("Handler: dog %s is working but agent is dead (zombie session), killing session", dg.Name)
			if err := sm.Stop(dg.Name, true); err != nil {
				d.logger.Printf("Handler: failed to stop zombie session for dog %s: %v", dg.Name, err)
			}
		} else {
			d.logger.Printf("Handler: dog %s is working but session is dead, clearing work", dg.Name)
		}

		if err := mgr.ClearWork(dg.Name); err != nil {
			d.logger.Printf("Handler: failed to clear work for dog %s: %v", dg.Name, err)
		}
	}
}

// detectStaleWorkingDogs finds dogs in state=working whose last_active exceeds
// staleWorkingTimeout. These dogs have live tmux sessions sitting idle at a
// prompt — neither cleanupStuckDogs (needs dead session) nor reapIdleDogs
// (needs state=idle) will catch them.
func (d *Daemon) detectStaleWorkingDogs(mgr *dog.Manager, sm *dog.SessionManager, daemonCfg *config.DaemonThresholds) {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs for stale-working check: %v", err)
		return
	}

	threshold := daemonCfg.StaleWorkingTimeoutD()
	now := time.Now()
	for _, dg := range dogs {
		if dg.State != dog.StateWorking {
			continue
		}

		staleDuration := now.Sub(dg.LastActive)
		if staleDuration < threshold {
			continue
		}

		d.logger.Printf("Handler: dog %s stuck in working state (inactive %v, work: %s), clearing",
			dg.Name, staleDuration.Truncate(time.Minute), dg.Work)

		if err := mgr.ClearWork(dg.Name); err != nil {
			d.logger.Printf("Handler: failed to clear work for stale dog %s: %v", dg.Name, err)
			continue
		}

		// Kill the tmux session — it's not doing anything useful.
		running, err := sm.IsRunning(dg.Name)
		if err != nil {
			d.logger.Printf("Handler: error checking session for stale dog %s: %v", dg.Name, err)
			continue
		}
		if running {
			if err := sm.Stop(dg.Name, true); err != nil {
				d.logger.Printf("Handler: failed to stop session for stale dog %s: %v", dg.Name, err)
			}
		}
	}
}

// reapIdleDogs kills tmux sessions for dogs that have been idle too long, and
// removes long-idle dogs from the kennel when the pool is oversized.
func (d *Daemon) reapIdleDogs(mgr *dog.Manager, sm *dog.SessionManager, daemonCfg *config.DaemonThresholds) {
	dogs, err := mgr.List()
	if err != nil {
		d.logger.Printf("Handler: failed to list dogs for reaping: %v", err)
		return
	}

	idleSessionTimeout := daemonCfg.DogIdleSessionTimeoutD()
	idleRemoveTimeout := daemonCfg.DogIdleRemoveTimeoutD()
	poolMax := daemonCfg.MaxDogPoolSizeV()

	now := time.Now()
	poolSize := len(dogs)

	for _, dg := range dogs {
		if dg.State != dog.StateIdle {
			continue
		}

		idleDuration := now.Sub(dg.LastActive)

		// Phase 1: kill stale tmux sessions for idle dogs.
		if idleDuration >= idleSessionTimeout {
			running, err := sm.IsRunning(dg.Name)
			if err != nil {
				d.logger.Printf("Handler: error checking session for idle dog %s: %v", dg.Name, err)
				continue
			}
			if running {
				d.logger.Printf("Handler: reaping idle dog %s session (idle %v)", dg.Name, idleDuration.Truncate(time.Minute))
				if err := sm.Stop(dg.Name, true); err != nil {
					d.logger.Printf("Handler: failed to stop session for idle dog %s: %v", dg.Name, err)
				}
			}
		}

		// Phase 2: remove long-idle dogs when pool is oversized.
		if poolSize > poolMax && idleDuration >= idleRemoveTimeout {
			d.logger.Printf("Handler: removing long-idle dog %s from kennel (idle %v, pool %d/%d)",
				dg.Name, idleDuration.Truncate(time.Minute), poolSize, poolMax)

			// Ensure session is dead before removing.
			running, _ := sm.IsRunning(dg.Name)
			if running {
				_ = sm.Stop(dg.Name, true)
			}

			if err := mgr.Remove(dg.Name); err != nil {
				d.logger.Printf("Handler: failed to remove idle dog %s: %v", dg.Name, err)
				continue
			}
			poolSize--
		}
	}
}

// dispatchPlugins scans for plugins, evaluates cooldown gates, and dispatches
// eligible plugins to idle dogs.
func (d *Daemon) dispatchPlugins(mgr *dog.Manager, sm *dog.SessionManager, rigsConfig *config.RigsConfig) {
	// Get rig names for scanner
	var rigNames []string
	if rigsConfig != nil {
		for name := range rigsConfig.Rigs {
			rigNames = append(rigNames, name)
		}
	}

	scanner := plugin.NewScanner(d.config.TownRoot, rigNames)
	plugins, err := scanner.DiscoverAll()
	if err != nil {
		d.logger.Printf("Handler: failed to discover plugins: %v", err)
		return
	}

	if len(plugins) == 0 {
		return
	}

	recorder := plugin.NewRecorder(d.config.TownRoot)
	router := mail.NewRouterWithTownRoot(d.config.TownRoot, d.config.TownRoot)

	for _, p := range plugins {
		// Only dispatch plugins with cooldown gates.
		if p.Gate == nil || p.Gate.Type != plugin.GateCooldown {
			continue
		}

		// Evaluate cooldown: skip if plugin ran recently.
		if p.Gate.Duration != "" {
			count, err := recorder.CountRunsSince(p.Name, p.Gate.Duration)
			if err != nil {
				d.logger.Printf("Handler: error checking cooldown for plugin %s: %v", p.Name, err)
				continue
			}
			if count > 0 {
				continue // Still in cooldown
			}
		}

		// Find an idle dog.
		idleDog, err := mgr.GetIdleDog()
		if err != nil {
			d.logger.Printf("Handler: error finding idle dog: %v", err)
			return // No point continuing if we can't list dogs
		}
		if idleDog == nil {
			d.logger.Printf("Handler: no idle dogs available, deferring remaining plugins")
			return
		}

		// Assign work and start session.
		workDesc := fmt.Sprintf("plugin:%s", p.Name)
		if err := mgr.AssignWork(idleDog.Name, workDesc); err != nil {
			d.logger.Printf("Handler: failed to assign work to dog %s: %v", idleDog.Name, err)
			continue
		}

		// Send mail with plugin instructions BEFORE starting the session
		// so the dog finds work in its inbox on first check.
		msg := mail.NewMessage(
			"daemon",
			fmt.Sprintf("deacon/dogs/%s", idleDog.Name),
			fmt.Sprintf("Plugin: %s", p.Name),
			p.FormatMailBody(),
		)
		msg.Type = mail.TypeTask
		msg.Timestamp = time.Now()
		if err := router.Send(msg); err != nil {
			d.logger.Printf("Handler: failed to send mail to dog %s: %v", idleDog.Name, err)
			// Roll back assignment — no point starting a session without instructions.
			if clearErr := mgr.ClearWork(idleDog.Name); clearErr != nil {
				d.logger.Printf("Handler: failed to clear work after mail failure for dog %s: %v", idleDog.Name, clearErr)
			}
			continue
		}

		if err := sm.Start(idleDog.Name, dog.SessionStartOptions{
			WorkDesc: workDesc,
		}); err != nil {
			d.logger.Printf("Handler: failed to start session for dog %s: %v", idleDog.Name, err)
			// Roll back assignment on session start failure.
			if clearErr := mgr.ClearWork(idleDog.Name); clearErr != nil {
				d.logger.Printf("Handler: failed to clear work after start failure for dog %s: %v", idleDog.Name, clearErr)
			}
			continue
		}

		d.logger.Printf("Handler: dispatched plugin %s to dog %s", p.Name, idleDog.Name)

		// Record the dispatch immediately so the cooldown gate is satisfied
		// for the next 1h regardless of what the dog does. Dogs create their
		// own completion beads but don't reliably use the label convention the
		// gate requires, causing infinite re-dispatch loops.
		if _, err := recorder.RecordRun(plugin.PluginRunRecord{
			PluginName: p.Name,
			Result:     plugin.ResultSuccess,
			Body:       fmt.Sprintf("Dispatched to dog %s", idleDog.Name),
		}); err != nil {
			d.logger.Printf("Handler: failed to record dispatch for plugin %s: %v", p.Name, err)
		}
	}
}

// loadRigsConfig loads the rigs configuration from mayor/rigs.json.
func (d *Daemon) loadRigsConfig() (*config.RigsConfig, error) {
	rigsPath := filepath.Join(d.config.TownRoot, "mayor", "rigs.json")
	return config.LoadRigsConfig(rigsPath)
}

// loadOperationalConfig loads operational thresholds from town settings.
// Returns a valid (never nil) config — accessors return defaults for nil fields.
func (d *Daemon) loadOperationalConfig() *config.OperationalConfig {
	return config.LoadOperationalConfig(d.config.TownRoot)
}
