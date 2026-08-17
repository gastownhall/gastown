package now

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"

	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/mayor"
	"github.com/jonbaldie/gastown/internal/session"
	"github.com/jonbaldie/gastown/internal/tmux"
	"github.com/jonbaldie/gastown/internal/workspace"
)

// Options is the gt now command input.
type Options struct {
	RepoArg        string
	TownFlag       string
	Name           string
	MayorSpec      string
	WorkersSpec    string
	RestartWorkers bool
	NoAttach       bool
	ProvisionOnly  bool
	Executable     string
	Stdout         io.Writer
	Stderr         io.Writer
}

// Result is the user-visible outcome of a successful gt now run.
type Result struct {
	TownRoot      string
	RigName       string
	Mix           string
	DoltPort      int
	AttachSession string
}

// Hooks are cmd-owned operations that gt now must call without importing cmd.
type Hooks struct {
	EnsureDoltReady func() error
	InitBeads       func(townRoot string) error
	InitAgentBeads  func(townRoot string) error
	EnsureDaemon    func(townRoot string) error
}

// Run starts or reuses a Town for a git repository and prepares the Mayor session.
func Run(ctx context.Context, opts Options, hooks Hooks) (Result, error) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if opts.ProvisionOnly {
		townRoot, err := ResolveTownRoot(opts.TownFlag)
		if err != nil {
			return Result{}, err
		}
		return Result{TownRoot: townRoot}, provisionTown(townRoot, hooks)
	}

	repoPath, err := ResolveRepo(opts.RepoArg)
	if err != nil {
		return Result{}, err
	}

	mayorProfile, workersProfile, err := ResolveProfiles(opts.MayorSpec, opts.WorkersSpec)
	if err != nil {
		return Result{}, err
	}

	if err := preflightRepo(repoPath); err != nil {
		return Result{}, err
	}

	townRoot, err := ResolveTownRoot(opts.TownFlag)
	if err != nil {
		return Result{}, err
	}

	if err := refuseTownHQConversion(repoPath, townRoot); err != nil {
		return Result{}, err
	}

	if err := ensureTown(ctx, townRoot, hooks); err != nil {
		return Result{}, err
	}

	port, err := chooseDoltPort(townRoot)
	if err != nil {
		return Result{}, err
	}
	if err := os.Setenv("GT_TOWN_ROOT", townRoot); err != nil {
		return Result{}, fmt.Errorf("setting GT_TOWN_ROOT: %w", err)
	}
	if err := os.Setenv("GT_DOLT_PORT", strconv.Itoa(port)); err != nil {
		return Result{}, fmt.Errorf("setting GT_DOLT_PORT: %w", err)
	}
	if err := persistDoltPort(townRoot, port); err != nil {
		return Result{}, err
	}

	if err := session.InitRegistry(townRoot); err != nil {
		return Result{}, fmt.Errorf("initializing town registry: %w", err)
	}

	var (
		doltErr, rigErr, mixErr error
		rigName                 string
		mayorChanged            bool
	)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		if err := ctx.Err(); err != nil {
			doltErr = err
			return
		}
		doltErr = startDolt(ctx, townRoot, hooks)
	}()
	go func() {
		defer wg.Done()
		if err := ctx.Err(); err != nil {
			rigErr = err
			return
		}
		rigName, rigErr = ensureRig(ctx, townRoot, repoPath, opts.Name)
	}()
	go func() {
		defer wg.Done()
		if err := ctx.Err(); err != nil {
			mixErr = err
			return
		}
		mayorChanged, mixErr = ApplyMix(townRoot, opts.MayorSpec, opts.WorkersSpec, mayorProfile, workersProfile)
	}()
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := errors.Join(doltErr, rigErr, mixErr); err != nil {
		return Result{}, err
	}

	if err := startSessions(ctx, townRoot, mayorChanged, opts, hooks); err != nil {
		return Result{}, err
	}

	if err := startDeferredProvision(opts.Executable, townRoot); err != nil {
		fmt.Fprintf(opts.Stderr, "warning: deferred Town provision did not start: %v\n", err)
	}

	tm := tmux.NewTmux()
	running, err := tm.HasSession(mayor.SessionName())
	if err != nil || !running {
		if err != nil {
			return Result{}, fmt.Errorf("Mayor session is not running: %w", err)
		}
		return Result{}, fmt.Errorf("Mayor session is not running")
	}

	result := Result{
		TownRoot: townRoot,
		RigName:  rigName,
		Mix:      mayorProfile.Format() + " / " + workersProfile.Format(),
		DoltPort: port,
	}
	if !opts.NoAttach {
		result.AttachSession = mayor.SessionName()
	}
	return result, nil
}

func preflightRepo(repoPath string) error {
	repoGit := git.NewGit(repoPath)
	if !repoGit.IsRepo() {
		return fmt.Errorf("not a git repository: %s", repoPath)
	}
	empty, err := repoGit.IsEmpty()
	if err != nil {
		return fmt.Errorf("checking repository: %w", err)
	}
	if empty {
		return fmt.Errorf("repository %s is empty (no commits). Commit at least once before running gt now", repoPath)
	}
	return nil
}

func refuseTownHQConversion(repoPath, townRoot string) error {
	repoIsTown, err := workspace.IsWorkspace(repoPath)
	if err != nil {
		return err
	}
	same := SamePath(repoPath, townRoot)
	if repoIsTown && !same {
		return fmt.Errorf("this directory is a Town HQ (%s); run gt now from a project git repository", repoPath)
	}
	if !same {
		return nil
	}
	if !repoIsTown {
		return fmt.Errorf("refusing to convert this git repository into a Town HQ; pass --town for a separate Town")
	}
	registered, err := repoIsRegisteredRig(townRoot, repoPath)
	if err != nil {
		return fmt.Errorf("checking whether this Town HQ is a registered Rig: %w", err)
	}
	if !registered {
		return fmt.Errorf("this directory is a Town HQ; run gt now from a project git repository")
	}
	return nil
}
