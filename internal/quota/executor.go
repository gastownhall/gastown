package quota

import (
	"fmt"
	"sync"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/util"
)

// TmuxExecutor is the interface for tmux mutation operations needed by the Rotator.
// Separating this from scan.go's TmuxClient keeps read-only scanning distinct
// from the write operations required by rotation execution.
type TmuxExecutor interface {
	SetEnvironment(session, key, value string) error
	GetPaneID(session string) (string, error)
	SetRemainOnExit(pane string, on bool) error
	KillPaneProcesses(pane string) error
	ClearHistory(pane string) error
	RespawnPane(pane, command string) error
	AcceptStartupDialogs(session string) error
	AcceptWorkspaceTrustDialog(session string) error
	AcceptBypassPermissionsWarning(session string) error
}

// Logger allows the Rotator to emit non-fatal warnings without depending
// on the CLI style package.
type Logger interface {
	Warn(format string, args ...interface{})
}

// SessionLinker symlinks a session file from its current account into a target
// config directory so that --resume can find it after account rotation.
// Returns a cleanup function (may be nil) and any error.
type SessionLinker func(townRoot, sessionID, targetConfigDir string) (cleanup func(), err error)

// Rotator executes account rotation for rate-limited sessions.
// All dependencies are injected, making it testable without tmux or disk I/O.
type Rotator struct {
	tmuxClient     TmuxClient                           // read-only: GetEnvironment for account resolution
	tmuxExec       TmuxExecutor                         // write: pane lifecycle operations
	mgr            *Manager                             // quota state persistence
	accounts       *config.AccountsConfig               // registered accounts
	restartCommand func(session string) (string, error) // builds the respawn command
	log            Logger                               // non-fatal warning output
	sessionLinker  SessionLinker                        // optional: symlinks session for resume (nil = no resume)
	townRoot       string                               // needed for session discovery
	agentName      string                               // needed for BuildResumeCommand (default "claude")
}

// NewRotator creates a Rotator with all dependencies injected.
// sessionLinker may be nil to disable session resume (fall back to fresh restart).
func NewRotator(
	tmuxClient TmuxClient,
	tmuxExec TmuxExecutor,
	mgr *Manager,
	accounts *config.AccountsConfig,
	restartCmd func(string) (string, error),
	log Logger,
	townRoot string,
	agentName string,
	sessionLinker SessionLinker,
) *Rotator {
	if agentName == "" {
		agentName = "claude"
	}
	return &Rotator{
		tmuxClient:     tmuxClient,
		tmuxExec:       tmuxExec,
		mgr:            mgr,
		accounts:       accounts,
		restartCommand: restartCmd,
		log:            log,
		sessionLinker:  sessionLinker,
		townRoot:       townRoot,
		agentName:      agentName,
	}
}

// Execute performs the rotation plan atomically: the quota file lock is held
// for the entire lifecycle, state is loaded once, all rotations execute
// concurrently (each targets an independent tmux session), and a single save
// is issued at the end. This eliminates the N lock/load/save cycles and the
// TOCTOU race of the previous approach while parallelizing the expensive
// tmux operations (especially KillPaneProcesses which sleeps ~4s per session).
func (r *Rotator) Execute(plan *RotatePlan, sessionOrder []string) []RotateResult {
	var results []RotateResult

	err := r.mgr.WithLock(func() error {
		state, err := r.mgr.Load()
		if err != nil {
			return fmt.Errorf("loading quota state: %w", err)
		}
		r.mgr.EnsureAccountsTracked(state, r.accounts.Accounts)

		// Build work list preserving caller-specified order.
		type work struct {
			idx        int
			session    string
			newAccount string
		}
		var items []work
		for _, session := range sessionOrder {
			newAccount, ok := plan.Assignments[session]
			if !ok {
				continue
			}
			items = append(items, work{idx: len(items), session: session, newAccount: newAccount})
		}

		// Fan out executeOne calls — each targets an independent tmux
		// session/pane so the tmux operations are safe to run concurrently.
		// The only shared resource is state.Accounts, protected by mu.
		indexed := make([]RotateResult, len(items))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, w := range items {
			wg.Add(1)
			go func(w work) {
				defer wg.Done()
				indexed[w.idx] = r.executeOne(state, &mu, w.session, w.newAccount)
			}(w)
		}
		wg.Wait()

		results = indexed
		return r.mgr.SaveUnlocked(state)
	})

	if err != nil {
		// If WithLock itself failed (lock acquisition or final save),
		// report it as a single error result.
		results = append(results, RotateResult{
			Error: fmt.Sprintf("rotation lifecycle: %v", err),
		})
	}

	return results
}

// executeOne performs rotation for a single session, mutating state in-memory.
// The method is structured in two phases: validation (read-only) then mutation,
// so no tmux state is modified if any pre-check fails.
// The mu parameter protects the shared state.Accounts map; it is only held
// for the brief LastUsed update, not during tmux I/O.
func (r *Rotator) executeOne(state *config.QuotaState, mu *sync.Mutex, session, newAccount string) RotateResult {
	result := RotateResult{
		Session:    session,
		NewAccount: newAccount,
	}

	// --- Validation phase: read-only, no side effects ---

	// 1. Resolve old account from tmux session environment.
	oldConfigDir, err := r.tmuxClient.GetEnvironment(session, "CLAUDE_CONFIG_DIR")
	if err == nil {
		for handle, acct := range r.accounts.Accounts {
			if acct.ConfigDir == oldConfigDir || util.ExpandHome(acct.ConfigDir) == oldConfigDir {
				result.OldAccount = handle
				break
			}
		}
	}

	// 2. Resolve new account config dir.
	newAcct, ok := r.accounts.Accounts[newAccount]
	if !ok {
		result.Error = fmt.Sprintf("account %q not found in config", newAccount)
		return result
	}
	newConfigDir := util.ExpandHome(newAcct.ConfigDir)

	// 3. Read CLAUDE_SESSION_ID from tmux session environment for resume support.
	var sessionID string
	sessionIDEnv := config.GetSessionIDEnvVar(r.agentName)
	if sessionIDEnv != "" {
		sessionID, _ = r.tmuxClient.GetEnvironment(session, sessionIDEnv)
	}

	// 4. Build restart command (always, as fallback).
	respawnCmd, err := r.restartCommand(session)
	if err != nil {
		result.Error = fmt.Sprintf("building restart command: %v", err)
		return result
	}

	// 5. If session ID found + linker available, attempt resume command.
	if sessionID != "" && r.sessionLinker != nil {
		cleanup, linkErr := r.sessionLinker(r.townRoot, sessionID, newConfigDir)
		if linkErr != nil {
			r.log.Warn("could not symlink session for resume in %s: %v (falling back to fresh start)", session, linkErr)
		} else {
			resumeCmd := config.BuildResumeCommand(r.agentName, sessionID)
			if resumeCmd != "" {
				respawnCmd = resumeCmd
				result.ResumedSession = sessionID
			}
			// Cleanup is deferred — the symlink should persist so Claude can read
			// the session file during resume. We don't call cleanup here.
			_ = cleanup
		}
	}

	// 6. Prepend CLAUDE_CONFIG_DIR using OS-aware env syntax.
	respawnCmd = config.PrependEnv(respawnCmd, map[string]string{
		"CLAUDE_CONFIG_DIR": newConfigDir,
	})

	// 7. Validate target pane exists.
	pane, err := r.tmuxExec.GetPaneID(session)
	if err != nil {
		result.Error = fmt.Sprintf("getting pane: %v", err)
		return result
	}

	// --- Mutation phase: all validation passed ---

	// 8. Set new CLAUDE_CONFIG_DIR in tmux session environment.
	if err := r.tmuxExec.SetEnvironment(session, "CLAUDE_CONFIG_DIR", newConfigDir); err != nil {
		result.Error = fmt.Sprintf("setting CLAUDE_CONFIG_DIR: %v", err)
		return result
	}

	// Set remain-on-exit to prevent pane destruction during restart.
	if err := r.tmuxExec.SetRemainOnExit(pane, true); err != nil {
		r.log.Warn("could not set remain-on-exit for %s: %v", session, err)
	}

	// Kill existing processes.
	if err := r.tmuxExec.KillPaneProcesses(pane); err != nil {
		r.log.Warn("could not kill pane processes for %s: %v", session, err)
	}

	// Clear scrollback.
	if err := r.tmuxExec.ClearHistory(pane); err != nil {
		r.log.Warn("could not clear history for %s: %v", session, err)
	}

	// Respawn with new account.
	if err := r.tmuxExec.RespawnPane(pane, respawnCmd); err != nil {
		result.Error = fmt.Sprintf("respawning pane: %v", err)
		return result
	}

	// 9. Accept startup dialogs (non-critical).
	if err := r.tmuxExec.AcceptStartupDialogs(session); err != nil {
		r.log.Warn("could not accept startup dialogs for %s: %v", session, err)
	}

	// 10. Update in-memory quota state (no disk I/O here).
	// Lock only for the map mutation — tmux I/O above runs lock-free.
	mu.Lock()
	existing := state.Accounts[newAccount]
	existing.LastUsed = time.Now().UTC().Format(time.RFC3339)
	state.Accounts[newAccount] = existing
	mu.Unlock()

	result.Rotated = true
	return result
}
