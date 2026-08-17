package now

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/deacon"
	"github.com/jonbaldie/gastown/internal/formula"
	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/mayor"
	"github.com/jonbaldie/gastown/internal/refinery"
	"github.com/jonbaldie/gastown/internal/rig"
	"github.com/jonbaldie/gastown/internal/skills"
	"github.com/jonbaldie/gastown/internal/templates"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/util"
	"github.com/jonbaldie/gastown/internal/witness"
	"github.com/jonbaldie/gastown/internal/workspace"
)

func startSessions(ctx context.Context, townRoot string, mayorChanged bool, opts Options, hooks Hooks) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := config.EnsureDaemonPatrolConfig(townRoot); err != nil {
		return fmt.Errorf("ensuring daemon config: %w", err)
	}

	if hooks.EnsureDaemon != nil {
		if err := hooks.EnsureDaemon(townRoot); err != nil {
			return fmt.Errorf("starting daemon: %w", err)
		}
	}

	var wg sync.WaitGroup
	var firstErr error
	var mu sync.Mutex
	setErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	restartMayorRoles := mayorChanged && opts.MayorSpec != ""

	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr := mayor.NewManager(townRoot)
		setErr(reuseOrStartSession(ctx, mayor.SessionName(), restartMayorRoles, mgr.Stop, func() error {
			return mgr.StartImmediate("")
		}, mayor.ErrNotRunning, mayor.ErrAlreadyRunning, mayor.ErrACPActive))
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		mgr := deacon.NewManager(townRoot)
		setErr(reuseOrStartSession(ctx, deacon.SessionName(), restartMayorRoles, mgr.Stop, func() error {
			return mgr.StartImmediate("")
		}, deacon.ErrNotRunning, deacon.ErrAlreadyRunning))
	}()

	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	if opts.RestartWorkers {
		return restartWorkers(townRoot)
	}
	return nil
}

func reuseOrStartSession(ctx context.Context, name string, restart bool, stop, start func() error, skipStop error, skipStart ...error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	has, err := tmux.NewTmux().HasSession(name)
	if err != nil {
		return fmt.Errorf("checking %s session: %w", name, err)
	}
	if has && restart {
		if err := stop(); err != nil && !errors.Is(err, skipStop) {
			return fmt.Errorf("stopping %s: %w", name, err)
		}
		has = false
	}
	if has {
		return nil
	}
	if err := start(); err != nil && !ignoreStartConflict(err, skipStart...) {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	return nil
}

func ignoreStartConflict(err error, skipStart ...error) bool {
	if errors.Is(err, tmux.ErrSessionExists) {
		return true
	}
	for _, skip := range skipStart {
		if errors.Is(err, skip) {
			return true
		}
	}
	return false
}

func restartWorkers(townRoot string) error {
	rigsPath := constants.MayorRigsPath(townRoot)
	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		return fmt.Errorf("loading rigs for --restart-workers: %w", err)
	}
	mgr := rig.NewManager(townRoot, rigsConfig, git.NewGit(townRoot))
	var errs []error
	for name := range rigsConfig.Rigs {
		r, err := mgr.GetRig(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("loading rig %s: %w", name, err))
			continue
		}
		wit := witness.NewManager(r)
		if err := wit.Stop(); err != nil && !errors.Is(err, witness.ErrNotRunning) {
			errs = append(errs, fmt.Errorf("stopping witness for %s: %w", name, err))
		} else if err := wit.Start(false, "", nil); err != nil && !errors.Is(err, witness.ErrAlreadyRunning) {
			errs = append(errs, fmt.Errorf("restarting witness for %s: %w", name, err))
		}
		ref := refinery.NewManager(r)
		if err := ref.Stop(); err != nil && !errors.Is(err, refinery.ErrNotRunning) {
			errs = append(errs, fmt.Errorf("stopping refinery for %s: %w", name, err))
		} else if err := ref.Start(false, ""); err != nil && !errors.Is(err, refinery.ErrAlreadyRunning) {
			errs = append(errs, fmt.Errorf("restarting refinery for %s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func startDeferredProvision(executable, townRoot string) error {
	if executable == "" {
		return fmt.Errorf("gt executable path is empty")
	}
	cmd := exec.Command(executable, "now", "--provision-only", "--town", townRoot)
	cmd.Dir = townRoot
	cmd.Env = append(os.Environ(), "GT_TOWN_ROOT="+townRoot)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	util.SetDetachedProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting deferred provision: %w", err)
	}
	return nil
}

func provisionTown(townRoot string, hooks Hooks) error {
	ok, err := workspace.IsWorkspace(townRoot)
	if err != nil {
		return fmt.Errorf("checking Town HQ: %w", err)
	}
	if !ok {
		return fmt.Errorf("not a Gas Town HQ: %s", townRoot)
	}
	var errs []error
	count, err := formula.ProvisionFormulas(townRoot)
	if err != nil {
		errs = append(errs, fmt.Errorf("provisioning formulas: %w", err))
	} else if count > 0 {
		fmt.Printf("provisioned %d formulas\n", count)
	}
	if err := templates.ProvisionCommands(townRoot); err != nil {
		errs = append(errs, fmt.Errorf("provisioning slash commands: %w", err))
	}
	if err := skills.ProvisionFor(townRoot, "claude"); err != nil {
		errs = append(errs, fmt.Errorf("provisioning skills: %w", err))
	}
	if hooks.InitAgentBeads != nil {
		if err := hooks.InitAgentBeads(townRoot); err != nil {
			errs = append(errs, fmt.Errorf("initializing agent beads: %w", err))
		}
	}
	return errors.Join(errs...)
}
