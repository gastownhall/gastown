package opencodeserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/nudge"
)

type WorkerOptions struct {
	TownRoot       string
	GasTownSession string
	Directory      string
	Command        string
	StartupPrompt  string
	Agent          string
	Model          string
	Variant        string
	WorkKey        string
	PollInterval   time.Duration
	StartupTimeout time.Duration
}

func RunWorker(ctx context.Context, opts WorkerOptions) (runErr error) {
	if opts.TownRoot == "" || opts.GasTownSession == "" || opts.Directory == "" {
		return fmt.Errorf("town root, Gas Town session, and directory are required")
	}
	if opts.Command == "" {
		opts.Command = "opencode"
	}
	if opts.Agent == "" {
		opts.Agent = "build"
	}
	if opts.PollInterval <= 0 {
		opts.PollInterval = time.Second
	}
	server, err := StartServer(ctx, ServerOptions{
		Command:        opts.Command,
		Directory:      opts.Directory,
		StartupTimeout: opts.StartupTimeout,
	})
	if err != nil {
		return err
	}
	var releaseLock func()
	defer func() {
		if stopErr := server.Stop(); stopErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("stopping OpenCode server: %w", stopErr))
		}
		if releaseLock != nil {
			releaseLock()
		}
	}()

	releaseLock, err = AcquireSessionLock(opts.TownRoot, opts.GasTownSession)
	if err != nil {
		return err
	}
	if err := nudge.StopPoller(opts.TownRoot, opts.GasTownSession); err != nil {
		return fmt.Errorf("stopping stale nudge poller: %w", err)
	}

	client, err := server.NewClient(opts.Directory)
	if err != nil {
		return err
	}
	session, err := resolveWorkerSession(ctx, client, opts)
	if err != nil {
		return err
	}
	state := State{
		GasTownSession:  opts.GasTownSession,
		OpenCodeSession: session.ID,
		WorkKey:         opts.WorkKey,
		Directory:       opts.Directory,
		Agent:           opts.Agent,
		Model:           opts.Model,
		Variant:         opts.Variant,
		URL:             server.URL(),
		Username:        server.Username(),
		Password:        server.Password(),
		PID:             server.PID(),
		Version:         server.Version(),
		CreatedAt:       time.Now().UTC(),
	}
	if err := SaveState(opts.TownRoot, state); err != nil {
		return err
	}

	watcher, err := nudge.WatcherForSession(opts.TownRoot, opts.GasTownSession)
	if err != nil {
		return fmt.Errorf("starting OpenCode nudge watcher: %w", err)
	}
	defer watcher.Close()
	status, err := client.Status(ctx, session.ID)
	if err != nil {
		return fmt.Errorf("checking initial OpenCode status: %w", err)
	}
	lifecycle := newSessionLifecycle(session.ID, status)
	defer func() {
		if !lifecycle.InFlight() {
			return
		}
		abortCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		abortErr := client.Abort(abortCtx, session.ID)
		cancel()
		if abortErr != nil {
			runErr = errors.Join(runErr, fmt.Errorf("aborting active OpenCode turn: %w", abortErr))
		}
	}()
	// Snapshot status before subscribing so the stream cannot contain a prior
	// turn's buffered busy/idle pair. The stream still opens before any prompt.
	eventStream, err := client.Events(ctx)
	if err != nil {
		return fmt.Errorf("subscribing to OpenCode lifecycle events: %w", err)
	}
	defer eventStream.Close()

	if strings.TrimSpace(opts.StartupPrompt) != "" {
		if !lifecycle.BeginPrompt() {
			return fmt.Errorf("OpenCode session %s is busy before startup prompt", session.ID)
		}
		if err := client.PromptAsync(ctx, session.ID, opts.StartupPrompt, PromptOptions{Agent: opts.Agent, Model: opts.Model, Variant: opts.Variant}); err != nil {
			lifecycle.PromptFailed()
			return fmt.Errorf("submitting OpenCode startup prompt: %w", err)
		}
	}
	fmt.Fprintf(os.Stderr, "OpenCode server worker ready (session %s, OpenCode %s)\n", session.ID, server.Version())

	ticker := time.NewTicker(opts.PollInterval)
	defer ticker.Stop()
	events := eventStream.Events()
	eventErrors := eventStream.Errors()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-server.Done():
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("OpenCode server stopped: %v", server.Err())
		case event, ok := <-events:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				if eventErrors != nil {
					select {
					case streamErr, errorOpen := <-eventErrors:
						if errorOpen && streamErr != nil {
							return fmt.Errorf("OpenCode event stream failed: %w", streamErr)
						}
					default:
					}
				}
				return fmt.Errorf("OpenCode event stream closed")
			}
			if lifecycle.Observe(event) {
				if _, err := deliverQueuedNudgesWithLifecycle(ctx, client, lifecycle, opts.TownRoot, opts.GasTownSession, session.ID, PromptOptions{Agent: opts.Agent, Model: opts.Model, Variant: opts.Variant}); err != nil {
					fmt.Fprintf(os.Stderr, "OpenCode nudge delivery failed: %v\n", err)
				}
			}
		case streamErr, ok := <-eventErrors:
			if !ok {
				eventErrors = nil
				continue
			}
			if streamErr != nil && ctx.Err() == nil {
				return fmt.Errorf("OpenCode event stream failed: %w", streamErr)
			}
		case <-watcher.Events():
			if _, err := deliverQueuedNudgesWithLifecycle(ctx, client, lifecycle, opts.TownRoot, opts.GasTownSession, session.ID, PromptOptions{Agent: opts.Agent, Model: opts.Model, Variant: opts.Variant}); err != nil {
				fmt.Fprintf(os.Stderr, "OpenCode nudge delivery failed: %v\n", err)
			}
		case <-ticker.C:
			if _, err := deliverQueuedNudgesWithLifecycle(ctx, client, lifecycle, opts.TownRoot, opts.GasTownSession, session.ID, PromptOptions{Agent: opts.Agent, Model: opts.Model, Variant: opts.Variant}); err != nil {
				fmt.Fprintf(os.Stderr, "OpenCode nudge delivery failed: %v\n", err)
			}
		}
	}
}

func resolveWorkerSession(ctx context.Context, client *Client, opts WorkerOptions) (Session, error) {
	prior, loadErr := LoadState(opts.TownRoot, opts.GasTownSession)
	if loadErr != nil && !os.IsNotExist(loadErr) {
		return Session{}, fmt.Errorf("loading prior OpenCode worker state: %w", loadErr)
	}
	if loadErr == nil && opts.WorkKey != "" && sameDirectory(prior.Directory, opts.Directory) && prior.WorkKey == opts.WorkKey {
		session, getErr := client.GetSession(ctx, prior.OpenCodeSession)
		if getErr == nil {
			if session.Directory != "" && !sameDirectory(session.Directory, opts.Directory) {
				return Session{}, fmt.Errorf("OpenCode session %s directory mismatch: got %q, want %q", session.ID, session.Directory, opts.Directory)
			}
			return session, nil
		}
		if !IsAPIStatus(getErr, http.StatusNotFound) {
			return Session{}, fmt.Errorf("looking up prior OpenCode session %s: %w", prior.OpenCodeSession, getErr)
		}
	}
	session, err := client.CreateSession(ctx, CreateSessionOptions{
		Title:   "Gas Town " + opts.GasTownSession,
		Agent:   opts.Agent,
		Model:   opts.Model,
		Variant: opts.Variant,
	})
	if err != nil {
		return Session{}, fmt.Errorf("creating OpenCode worker session: %w", err)
	}
	return session, nil
}

func sameDirectory(left, right string) bool {
	left = canonicalDirectory(left)
	right = canonicalDirectory(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func canonicalDirectory(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func deliverQueuedNudges(ctx context.Context, client *Client, townRoot, gasTownSession, openCodeSession string, promptOpts ...PromptOptions) (bool, error) {
	lifecycle := newSessionLifecycle(openCodeSession, Status{})
	return deliverQueuedNudgesWithLifecycle(ctx, client, lifecycle, townRoot, gasTownSession, openCodeSession, promptOpts...)
}

func deliverQueuedNudgesWithLifecycle(ctx context.Context, client *Client, lifecycle *sessionLifecycle, townRoot, gasTownSession, openCodeSession string, promptOpts ...PromptOptions) (bool, error) {
	status, err := client.Status(ctx, openCodeSession)
	if err != nil {
		return false, fmt.Errorf("checking OpenCode status: %w", err)
	}
	lifecycle.Reconcile(status)
	if !lifecycle.Ready() {
		return false, nil
	}
	claimed, err := nudge.ClaimOne(townRoot, gasTownSession)
	if err != nil {
		return false, fmt.Errorf("claiming nudges: %w", err)
	}
	drained := claimed.Nudges
	if len(drained) == 0 {
		return false, nil
	}
	if !lifecycle.BeginPrompt() {
		if err := claimed.Release(); err != nil {
			return false, fmt.Errorf("OpenCode session became busy; releasing nudge claim failed: %w", err)
		}
		return false, nil
	}
	prompt := nudge.FormatForInjection(drained)
	var options PromptOptions
	if len(promptOpts) > 0 {
		options = promptOpts[0]
	}
	options.MessageID = claimed.MessageID()
	submitted, err := client.promptAsync(ctx, openCodeSession, prompt, options)
	if err != nil {
		lifecycle.PromptFailed()
		if releaseErr := claimed.Release(); releaseErr != nil {
			return false, fmt.Errorf("submitting OpenCode nudge: %v; releasing claim failed: %w", err, releaseErr)
		}
		return false, fmt.Errorf("submitting OpenCode nudge: %w", err)
	}
	if !submitted {
		lifecycle.AwaitRecoveredPrompt()
	}
	if err := claimed.Commit(); err != nil {
		return true, fmt.Errorf("OpenCode nudge accepted but clearing queue claim failed: %w", err)
	}
	return true, nil
}
