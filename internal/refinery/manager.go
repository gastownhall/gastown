package refinery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
)

// Common errors
var (
	ErrNotRunning     = errors.New("refinery not running")
	ErrAlreadyRunning = errors.New("refinery already running")
	ErrNoQueue        = errors.New("no items in queue")
	ErrForkRig        = errors.New("refinery disabled for fork-backed rig")
)

// ForkRigError reports that refinery startup is disabled because the rig has
// an upstream_url and must use the fork/PR workflow instead of local MQ merge.
type ForkRigError struct {
	RigName     string
	UpstreamURL string
}

func (e *ForkRigError) Error() string {
	if e == nil {
		return ErrForkRig.Error()
	}
	if e.UpstreamURL == "" {
		return fmt.Sprintf("%s %s", ErrForkRig, e.RigName)
	}
	return fmt.Sprintf("%s %s (upstream %s)", ErrForkRig, e.RigName, util.RedactURL(e.UpstreamURL))
}

func (e *ForkRigError) Unwrap() error {
	return ErrForkRig
}

// NewForkRigError returns a typed startup-blocking fork-rig error.
func NewForkRigError(rigName, upstreamURL string) error {
	return &ForkRigError{RigName: rigName, UpstreamURL: upstreamURL}
}

// Manager handles refinery lifecycle and queue operations.
type Manager struct {
	rig     *rig.Rig
	workDir string
	output  io.Writer // Output destination for user-facing messages
}

type scoredIssue struct {
	issue *beads.Issue
	score float64
}

// NewManager creates a new refinery manager for a rig.
func NewManager(r *rig.Rig) *Manager {
	return &Manager{
		rig:     r,
		workDir: r.Path,
		output:  os.Stdout,
	}
}

// SetOutput sets the output writer for user-facing messages.
// This is useful for testing or redirecting output.
func (m *Manager) SetOutput(w io.Writer) {
	m.output = w
}

// SessionName returns the tmux session name for this refinery.
func (m *Manager) SessionName() string {
	return session.RefinerySessionName(session.PrefixFor(m.rig.Name))
}

// IsRunning checks if the refinery session is active and healthy.
// Checks both tmux session existence AND agent process liveness to avoid
// reporting zombie sessions (tmux alive but Claude dead) as "running".
// ZFC: tmux session existence is the source of truth for session state,
// but agent liveness determines if the session is actually functional.
func (m *Manager) IsRunning() (bool, error) {
	t := tmux.NewTmux()
	sessionName := m.SessionName()
	status := t.CheckSessionHealth(sessionName, 0)
	return status == tmux.SessionHealthy, nil
}

// IsHealthy checks if the refinery is running and has been active recently.
// Unlike IsRunning which only checks process liveness, this also detects hung
// sessions where Claude is alive but hasn't produced output in maxInactivity.
// Returns the detailed ZombieStatus for callers that need to distinguish
// between different failure modes.
func (m *Manager) IsHealthy(maxInactivity time.Duration) tmux.ZombieStatus {
	t := tmux.NewTmux()
	return t.CheckSessionHealth(m.SessionName(), maxInactivity)
}

// Status returns information about the refinery session.
// ZFC-compliant: tmux session is the source of truth.
func (m *Manager) Status() (*tmux.SessionInfo, error) {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	running, err := t.HasSession(sessionID)
	if err != nil {
		return nil, fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return nil, ErrNotRunning
	}

	return t.GetSessionInfo(sessionID)
}

// Start starts the refinery.
// If foreground is true, returns an error (foreground mode deprecated).
// Otherwise, spawns a Claude agent in a tmux session to process the merge queue.
// The agentOverride parameter allows specifying an agent alias to use instead of the town default.
// ZFC-compliant: no state file, tmux session is source of truth.
func (m *Manager) Start(foreground bool, agentOverride string) error {
	return m.start(foreground, agentOverride, false)
}

// StartAllowingForkRig starts the refinery even when the rig has upstream_url.
// It bypasses only the fork guard; safety stops and all other gates still apply.
func (m *Manager) StartAllowingForkRig(foreground bool, agentOverride string) error {
	return m.start(foreground, agentOverride, true)
}

func (m *Manager) start(foreground bool, agentOverride string, allowForkRig bool) error {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	if foreground {
		// Foreground mode is deprecated - the Refinery agent handles merge processing
		return fmt.Errorf("foreground mode is deprecated; use background mode (remove --foreground flag)")
	}

	if !allowForkRig {
		if err := m.blockForkRigStart(t); err != nil {
			return err
		}
	}

	townRoot := filepath.Dir(m.rig.Path)
	stop, err := ActiveSafetyStop(townRoot, m.rig.Name)
	if err != nil {
		return fmt.Errorf("checking refinery safety stop: %w", err)
	}
	if stop != nil {
		if running, _ := t.HasSession(sessionID); running {
			_, _ = fmt.Fprintf(m.output, "Refinery %s is safety-stopped; killing leftover session %s.\n", m.rig.Name, sessionID)
			if err := t.KillSessionWithProcesses(sessionID); err != nil {
				return fmt.Errorf("%w: killing leftover refinery session: %v", NewSafetyStoppedError(stop), err)
			}
		}
		return NewSafetyStoppedError(stop)
	}

	// Check if session already exists
	running, _ := t.HasSession(sessionID)
	if running {
		// Session exists - check if agent is actually running (healthy vs zombie)
		if t.IsAgentAlive(sessionID) {
			return ErrAlreadyRunning
		}
		// Zombie - tmux alive but agent dead. Kill and recreate.
		_, _ = fmt.Fprintln(m.output, "⚠ Detected zombie session (tmux alive, agent dead). Recreating...")
		if err := t.KillSession(sessionID); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	// Note: No PID check per ZFC - tmux session is the source of truth

	// Background mode: spawn a Claude agent in a tmux session
	// The Claude agent handles MR processing using git commands and beads

	// Working directory is the refinery worktree (shares .git with mayor/polecats).
	// If the worktree is missing (pruned, deleted, or corrupted), auto-repair it
	// from the shared bare repo (.repo.git) instead of falling back to mayor/rig.
	// Falling back to mayor/rig causes the refinery to operate in the mayor's
	// clone, which can interfere with mayor operations and confuse agents.
	//
	// Rigs using a standard .git clone (e.g. beads) never have a .repo.git bare
	// repo, so the repair path is not applicable for them. Fall back to mayor/rig
	// silently in that case — the fallback is correct and the warning would be noise.
	refineryRigDir := filepath.Join(m.rig.Path, "refinery", "rig")
	if _, err := os.Stat(refineryRigDir); os.IsNotExist(err) {
		bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
		_, bareErr := os.Stat(bareRepoPath)
		standardGitPath := filepath.Join(m.rig.Path, ".git")
		_, standardGitErr := os.Stat(standardGitPath)
		if os.IsNotExist(bareErr) && standardGitErr == nil {
			// Rig uses standard .git layout — worktree repair is not applicable.
			// Fall back to mayor/rig silently; the fallback works correctly here.
			refineryRigDir = filepath.Join(m.rig.Path, "mayor", "rig")
		} else if repairErr := m.repairRefineryWorktree(refineryRigDir); repairErr != nil {
			// Repair failed — fall back to mayor/rig as last resort.
			_, _ = fmt.Fprintf(m.output, "⚠ Could not repair refinery worktree: %v (falling back to mayor/rig)\n", repairErr)
			refineryRigDir = filepath.Join(m.rig.Path, "mayor", "rig")
		}
	}

	// Ensure runtime settings exist in the shared refinery parent directory.
	// Settings are passed to Claude Code via --settings flag.

	// Resolve CLAUDE_CONFIG_DIR from accounts.json so refinery sessions
	// use the correct account. Mirrors the daemon restart path (lifecycle.go).
	accountsPath := constants.MayorAccountsPath(townRoot)
	runtimeConfigDir, _, _ := config.ResolveAccountConfigDir(accountsPath, "")
	if runtimeConfigDir == "" {
		runtimeConfigDir = os.Getenv("CLAUDE_CONFIG_DIR")
	}

	runtimeConfig := config.ResolveRoleAgentConfig("refinery", townRoot, m.rig.Path)
	refinerySettingsDir := config.RoleSettingsDir("refinery", m.rig.Path)
	if err := runtime.EnsureSettingsForRole(refinerySettingsDir, refineryRigDir, "refinery", runtimeConfig); err != nil {
		return fmt.Errorf("ensuring runtime settings: %w", err)
	}

	// Ensure .gitignore has required Gas Town patterns
	if err := rig.EnsureGitignorePatterns(refineryRigDir); err != nil {
		style.PrintWarning("could not update refinery .gitignore: %v", err)
	}

	initialPrompt := session.BuildStartupPrompt(session.BeaconConfig{
		Recipient: session.BeaconRecipient("refinery", "", m.rig.Name),
		Sender:    "deacon",
		Topic:     "patrol",
	}, "Run `gt prime --hook` and begin patrol.")

	command, err := config.BuildStartupCommandFromConfig(config.AgentEnvConfig{
		Role:             "refinery",
		Rig:              m.rig.Name,
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Prompt:           initialPrompt,
		Topic:            "patrol",
		SessionName:      sessionID,
	}, m.rig.Path, initialPrompt, agentOverride)
	if err != nil {
		return fmt.Errorf("building startup command: %w", err)
	}

	// Compute environment BEFORE creating the session so it can be passed to
	// tmux via -e flags. Setting env via SetEnvironment after session creation
	// only affects newly spawned panes — the running pane (and Claude's
	// subprocesses like bd) keeps its original environment (gt-neycp).
	envVars := config.AgentEnv(config.AgentEnvConfig{
		Role:             "refinery",
		Rig:              m.rig.Name,
		TownRoot:         townRoot,
		RuntimeConfigDir: runtimeConfigDir,
		Agent:            agentOverride,
		SessionName:      sessionID,
	})
	envVars = session.MergeRuntimeLivenessEnv(envVars, runtimeConfig)
	envVars["GT_REFINERY"] = "1"

	// Generate the GASTA run ID for this refinery session.
	runID := uuid.New().String()
	envVars["GT_RUN"] = runID

	// Create session with command and env vars via -e flags so the initial
	// shell — and Claude's subprocesses — inherit them from the start.
	// See: https://github.com/anthropics/gastown/issues/280 (race condition fix)
	if err := t.NewSessionWithCommandAndEnv(sessionID, refineryRigDir, command, envVars); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}

	// Apply theme (non-fatal: theming failure doesn't affect operation)
	theme := tmux.ResolveSessionTheme(townRoot, m.rig.Name, "refinery", "")
	_ = t.ConfigureGasTownSession(sessionID, theme, m.rig.Name, "refinery", "refinery")

	// Accept startup dialogs (workspace trust + bypass permissions) if they appear.
	// Must be before WaitForRuntimeReady to avoid race where dialog blocks prompt detection.
	_ = t.AcceptStartupDialogs(sessionID)

	// Wait for Claude to start and show its prompt - fatal if Claude fails to launch
	// WaitForRuntimeReady waits for the runtime to be ready
	if err := t.WaitForRuntimeReady(sessionID, runtimeConfig, constants.ClaudeStartTimeout); err != nil {
		// Kill the zombie session before returning error
		_ = t.KillSessionWithProcesses(sessionID)
		return fmt.Errorf("waiting for refinery to start: %w", err)
	}

	// Start nudge-queue poller (gt-dgf). Claude's UserPromptSubmit hook only
	// drains when the agent submits a prompt. Idle agents never submit, so
	// queued nudges deadlock. The poller breaks the cycle by polling every 10s.
	if _, pollerErr := nudge.StartPoller(townRoot, sessionID); pollerErr != nil {
		log.Printf("warning: could not start nudge poller for %s: %v", sessionID, pollerErr)
	}

	_ = runtime.RunStartupFallback(t, sessionID, "refinery", runtimeConfig)
	_ = runtime.DeliverStartupPromptFallback(t, sessionID, initialPrompt, runtimeConfig, constants.ClaudeStartTimeout)

	// Track PID for defense-in-depth orphan cleanup (non-fatal)
	if err := session.TrackSessionPID(townRoot, sessionID, t); err != nil {
		log.Printf("warning: tracking session PID for %s: %v", sessionID, err)
	}

	// Stream refinery's Claude Code JSONL conversation log to VictoriaLogs (opt-in).
	if os.Getenv("GT_LOG_AGENT_OUTPUT") == "true" && os.Getenv("GT_OTEL_LOGS_URL") != "" {
		if err := session.ActivateAgentLogging(sessionID, refineryRigDir, runID); err != nil {
			log.Printf("warning: agent log watcher setup failed for %s: %v", sessionID, err)
		}
	}

	// Record the agent instantiation event (GASTA root span).
	session.RecordAgentInstantiateFromDir(context.Background(), runID, runtimeConfig.ResolvedAgent,
		"refinery", "refinery", sessionID, m.rig.Name, townRoot, "", refineryRigDir)

	return nil
}

// ForkRigStartError returns ErrForkRig when the rig config has upstream_url.
// The derived config guard is the single runtime policy for fork-backed rigs.
func (m *Manager) ForkRigStartError() error {
	cfg, err := rig.LoadRigConfig(m.rig.Path)
	if err != nil || cfg == nil || strings.TrimSpace(cfg.UpstreamURL) == "" {
		return nil
	}
	return NewForkRigError(m.rig.Name, cfg.UpstreamURL)
}

// BlockForkRigStart applies the fork-rig startup guard and kills any leftover
// refinery session that would otherwise keep processing a fork-backed rig.
func (m *Manager) BlockForkRigStart() error {
	return m.blockForkRigStart(tmux.NewTmux())
}

func (m *Manager) blockForkRigStart(t *tmux.Tmux) error {
	err := m.ForkRigStartError()
	if err == nil {
		return nil
	}
	sessionID := m.SessionName()
	if running, _ := t.HasSession(sessionID); running {
		_, _ = fmt.Fprintf(m.output, "Refinery %s is disabled for fork-backed rig; killing leftover session %s.\n", m.rig.Name, sessionID)
		if killErr := t.KillSessionWithProcesses(sessionID); killErr != nil {
			return fmt.Errorf("%w: killing leftover refinery session: %v", err, killErr)
		}
	}
	return err
}

// repairRefineryWorktree recreates a missing refinery/rig worktree from the
// shared bare repo (.repo.git). The refinery worktree is created during
// `gt rig add` but can be lost if `git worktree prune` runs, the directory
// is deleted, or the .git file becomes corrupted. This self-heals on startup
// instead of requiring manual intervention.
func (m *Manager) repairRefineryWorktree(refineryRigDir string) error {
	bareRepoPath := filepath.Join(m.rig.Path, ".repo.git")
	if _, err := os.Stat(bareRepoPath); os.IsNotExist(err) {
		return fmt.Errorf("bare repo not found at %s", bareRepoPath)
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(refineryRigDir), 0755); err != nil {
		return fmt.Errorf("creating refinery dir: %w", err)
	}

	// Prune stale worktree entries so git doesn't reject the add
	bareGit := git.NewGitWithDir(bareRepoPath, "")
	_ = bareGit.WorktreePrune()

	// Create worktree on the rig's default branch
	defaultBranch := m.rig.DefaultBranch()
	if err := bareGit.WorktreeAddExisting(refineryRigDir, defaultBranch); err != nil {
		return fmt.Errorf("git worktree add: %w", err)
	}

	// Configure hooks path (matches rig add behavior)
	refineryGit := git.NewGit(refineryRigDir)
	if err := refineryGit.ConfigureHooksPath(); err != nil {
		// Non-fatal: worktree is usable without hooks
		_, _ = fmt.Fprintf(m.output, "⚠ Could not configure hooks for repaired worktree: %v\n", err)
	}

	_, _ = fmt.Fprintf(m.output, "✓ Auto-repaired missing refinery worktree at %s\n", refineryRigDir)
	return nil
}

// Stop stops the refinery.
// ZFC-compliant: tmux session is the source of truth.
func (m *Manager) Stop() error {
	t := tmux.NewTmux()
	sessionID := m.SessionName()

	// Check if tmux session exists
	running, _ := t.HasSession(sessionID)
	if !running {
		return ErrNotRunning
	}

	// Kill the tmux session
	return t.KillSession(sessionID)
}

// Queue returns the current merge queue.
// Uses beads merge-request issues as the source of truth (not git branches).
// ZFC-compliant: beads is the source of truth, no state file.
func (m *Manager) Queue() ([]QueueItem, error) {
	// Query beads for open merge-request issues
	// BeadsPath() returns the git-synced beads location
	b := beads.New(m.rig.BeadsPath())
	issues, err := b.ListMergeRequests(beads.ListOptions{
		Label:    "gt:merge-request",
		Status:   "open",
		Priority: -1, // No priority filter
		Rig:      m.rig.Name,
	})
	if err != nil {
		return nil, fmt.Errorf("querying merge queue from beads: %w", err)
	}

	// Score and sort issues by priority score (highest first)
	now := time.Now()
	scored := make([]scoredIssue, 0, len(issues))
	for _, issue := range issues {
		// Defensive filter: bd status filters can drift; queue must only include open MRs.
		if issue == nil || issue.Status != "open" {
			continue
		}

		// Filter by rig — wisps are shared across all rigs (GH#2718).
		fields := beads.ParseMRFields(issue)
		if fields != nil && fields.Rig != "" && !strings.EqualFold(fields.Rig, m.rig.Name) {
			continue
		}

		score := m.calculateIssueScore(issue, now)
		scored = append(scored, scoredIssue{issue: issue, score: score})
	}

	sort.Slice(scored, func(i, j int) bool {
		return compareScoredIssues(scored[i], scored[j])
	})

	// Convert scored issues to queue items
	var items []QueueItem
	pos := 1
	for _, s := range scored {
		mr := m.issueToMR(s.issue)
		if mr != nil {
			items = append(items, QueueItem{
				Position: pos,
				MR:       mr,
				Age:      formatAge(mr.CreatedAt),
			})
			pos++
		}
	}

	return items, nil
}

func compareScoredIssues(a, b scoredIssue) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	if a.issue == nil || b.issue == nil {
		return a.issue != nil
	}
	return a.issue.ID < b.issue.ID
}

// calculateIssueScore computes the priority score for an MR issue.
// Higher scores mean higher priority (process first).
func (m *Manager) calculateIssueScore(issue *beads.Issue, now time.Time) float64 {
	fields := beads.ParseMRFields(issue)

	// Parse MR creation time
	mrCreatedAt := parseTime(issue.CreatedAt)
	if mrCreatedAt.IsZero() {
		mrCreatedAt = now // Fallback
	}

	// Build score input
	input := ScoreInput{
		Priority:    issue.Priority,
		MRCreatedAt: mrCreatedAt,
		Now:         now,
	}

	// Add fields from MR metadata if available
	if fields != nil {
		input.RetryCount = fields.RetryCount

		// Parse convoy created at if available
		if fields.ConvoyCreatedAt != "" {
			if convoyTime := parseTime(fields.ConvoyCreatedAt); !convoyTime.IsZero() {
				input.ConvoyCreatedAt = &convoyTime
			}
		}
	}

	return ScoreMRWithDefaults(input)
}

// issueToMR converts a beads issue to a MergeRequest.
func (m *Manager) issueToMR(issue *beads.Issue) *MergeRequest {
	if issue == nil {
		return nil
	}

	// Get configured default branch for this rig
	defaultBranch := m.rig.DefaultBranch()

	fields := beads.ParseMRFields(issue)
	if fields == nil {
		// No MR fields in description, construct from title/ID
		return &MergeRequest{
			ID:           issue.ID,
			IssueID:      issue.ID,
			Status:       mrStatusFromIssue(issue),
			CreatedAt:    parseTime(issue.CreatedAt),
			TargetBranch: defaultBranch,
		}
	}

	// Default target to rig's default branch if not specified
	target := fields.Target
	if target == "" {
		target = defaultBranch
	}

	return &MergeRequest{
		ID:           issue.ID,
		Branch:       fields.Branch,
		Worker:       fields.Worker,
		AgentBead:    fields.AgentBead,
		IssueID:      fields.SourceIssue,
		TargetBranch: target,
		MergeCommit:  fields.MergeCommit,
		Status:       mrStatusFromIssue(issue),
		CloseReason:  CloseReason(fields.CloseReason),
		CreatedAt:    parseTime(issue.CreatedAt),
	}
}

func mrStatusFromIssue(issue *beads.Issue) MRStatus {
	if issue == nil {
		return MROpen
	}
	status := beads.IssueStatus(strings.TrimSpace(issue.Status))
	if status.IsTerminal() {
		return MRClosed
	}
	if status == "in_progress" {
		return MRInProgress
	}
	return MROpen
}

// parseTime parses a time string, returning zero time on error.
func parseTime(s string) time.Time {
	// Try RFC3339 first (most common)
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		// Try date-only format as fallback
		t, _ = time.Parse("2006-01-02", s)
	}
	return t
}

// formatAge formats a duration since the given time.
func formatAge(t time.Time) string {
	d := time.Since(t)

	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// Common errors for MR operations
var (
	ErrMRNotFound  = errors.New("merge request not found")
	ErrMRNotFailed = errors.New("merge request has not failed")
)

// GetMR returns a merge request by ID.
// ZFC-compliant: delegates to FindMR which uses beads as source of truth.
// Deprecated: Use FindMR directly for more flexible matching.
func (m *Manager) GetMR(id string) (*MergeRequest, error) {
	return m.FindMR(id)
}

// FindMR finds a merge request by ID or branch name in the queue.
func (m *Manager) FindMR(idOrBranch string) (*MergeRequest, error) {
	queue, err := m.Queue()
	if err != nil {
		return nil, err
	}

	for _, item := range queue {
		// Match by ID
		if item.MR.ID == idOrBranch {
			return item.MR, nil
		}
		// Match by branch name (with or without polecat/ prefix)
		if item.MR.Branch == idOrBranch {
			return item.MR, nil
		}
		if constants.BranchPolecatPrefix+idOrBranch == item.MR.Branch {
			return item.MR, nil
		}
		// Match by ID prefix (partial match for convenience)
		if strings.HasPrefix(item.MR.ID, idOrBranch) {
			return item.MR, nil
		}
	}

	return nil, ErrMRNotFound
}

func (m *Manager) findMRForTerminalCleanup(idOrBranch string, b *beads.Beads) (*MergeRequest, error) {
	mr, err := m.FindMR(idOrBranch)
	if err == nil {
		return mr, nil
	}
	if !errors.Is(err, ErrMRNotFound) {
		return nil, err
	}

	issue, showErr := b.Show(idOrBranch)
	if showErr != nil {
		return nil, err
	}
	if issue == nil || !beads.HasLabel(issue, "gt:merge-request") {
		return nil, err
	}
	return m.issueToMR(issue), nil
}

// Retry is deprecated - the Refinery agent handles retry logic autonomously.
// ZFC-compliant: no state file, agent uses beads issue status.
// The agent will automatically retry failed MRs in its patrol cycle.
func (m *Manager) Retry(_ string, _ bool) error {
	_, _ = fmt.Fprintln(m.output, "Note: Retry is deprecated. The Refinery agent handles retries autonomously via beads.")
	return nil
}

// RegisterMR is deprecated - MRs are registered via beads merge-request issues.
// ZFC-compliant: beads is the source of truth, not state file.
// Use 'gt mr create' or create a merge-request type bead directly.
func (m *Manager) RegisterMR(_ *MergeRequest) error {
	return fmt.Errorf("RegisterMR is deprecated: use beads to create merge-request issues")
}

// RejectMR manually rejects a merge request.
// It closes the MR with rejected status and optionally notifies the worker.
// Returns the rejected MR for display purposes.
func (m *Manager) RejectMR(idOrBranch string, reason string, notify bool) (*MergeRequest, error) {
	b := beads.New(m.rig.BeadsPath())
	mr, err := m.findMRForTerminalCleanup(idOrBranch, b)
	if err != nil {
		return nil, err
	}

	closeResult, err := closeTerminalMR(b, mr.ID, terminalMRCloseOptions{
		Reason:        "rejected: " + reason,
		AgentBeadHint: mr.AgentBead,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to close MR bead: %w", err)
	}
	if closeResult.AgentBead != "" {
		mr.AgentBead = closeResult.AgentBead
	}
	if closeResult.AgentActiveMRClearErr != nil {
		_, _ = fmt.Fprintf(m.output, "Warning: failed to clear agent bead %s active_mr: %v\n", closeResult.AgentBead, closeResult.AgentActiveMRClearErr)
	}

	// Update in-memory state for return value
	if closeResult.Closed {
		if err := mr.Close(CloseReasonRejected); err != nil {
			// Non-fatal: bead is already closed, just log
			_, _ = fmt.Fprintf(m.output, "Warning: failed to update MR state: %v\n", err)
		}
	} else if closeResult.AlreadyTerminal {
		mr.Status = MRClosed
	} else {
		return nil, fmt.Errorf("failed to close MR bead: MR %s status is not open or terminal", mr.ID)
	}
	mr.Error = reason

	// Optionally notify worker
	if notify && !closeResult.AlreadyTerminal {
		m.notifyWorkerRejected(mr, reason)
	}

	return mr, nil
}

// PostMergeResult holds the result of a post-merge cleanup operation.
type PostMergeResult struct {
	MR                  *MergeRequest
	MRClosed            bool
	SourceIssueClosed   bool
	SourceIssueID       string
	SourceIssueNotFound bool // true if source issue doesn't exist (already closed or invalid)
}

// PostMerge performs post-merge cleanup for a successfully merged MR.
// It closes the MR bead and its source issue. Branch deletion is handled
// by the caller since the Manager doesn't have git access.
func (m *Manager) PostMerge(idOrBranch string) (*PostMergeResult, error) {
	b := beads.New(m.rig.BeadsPath())
	mr, err := m.findMRForTerminalCleanup(idOrBranch, b)
	if err != nil {
		return nil, err
	}

	result := &PostMergeResult{
		MR:            mr,
		SourceIssueID: mr.IssueID,
	}

	// Close the MR bead
	closeResult, err := closeTerminalMR(b, mr.ID, terminalMRCloseOptions{
		Reason:        string(CloseReasonMerged),
		MergeCommit:   mr.MergeCommit,
		AgentBeadHint: mr.AgentBead,
	})
	if err != nil {
		return result, fmt.Errorf("closing MR bead: %w", err)
	}
	if closeResult.AgentBead != "" {
		mr.AgentBead = closeResult.AgentBead
	}
	if closeResult.AgentActiveMRClearErr != nil {
		_, _ = fmt.Fprintf(m.output, "Warning: failed to clear agent bead %s active_mr: %v\n", closeResult.AgentBead, closeResult.AgentActiveMRClearErr)
	}
	if closeResult.AlreadyTerminal {
		_, _ = fmt.Fprintf(m.output, "  %s MR already closed\n", style.Dim.Render("—"))
		result.MRClosed = true
		if mr.CloseReason != CloseReasonMerged {
			if mr.CloseReason == "" {
				return result, fmt.Errorf("post-merge retry for already-closed MR %s requires close_reason=%s", mr.ID, CloseReasonMerged)
			}
			return result, fmt.Errorf("post-merge retry for already-closed MR %s has close_reason=%s", mr.ID, mr.CloseReason)
		}
	} else if closeResult.Closed {
		if closeErr := mr.Close(CloseReasonMerged); closeErr != nil {
			_, _ = fmt.Fprintf(m.output, "Warning: failed to update MR state: %v\n", closeErr)
		}
		result.MRClosed = true
	} else {
		return result, fmt.Errorf("closing MR bead: MR %s status is not open or terminal", mr.ID)
	}

	// Close the source issue with reason and --force to bypass dependency checks.
	// The source issue may have an attached molecule (wisp) whose open steps
	// would block a normal bd close. ForceCloseWithReason bypasses this,
	// matching how gt done handles closures for the no-MR path.
	if mr.IssueID != "" {
		closeReason := fmt.Sprintf("Merged in %s", mr.ID)
		if mr.MergeCommit != "" {
			closeReason = fmt.Sprintf("%s\ntarget_branch: %s\ncommit_sha: %s", closeReason, mr.TargetBranch, mr.MergeCommit)
		}
		if err := b.ForceCloseWithReason(closeReason, mr.IssueID); err != nil {
			// Check if already closed (by polecat's gt done) — that's fine
			if issue, showErr := b.Show(mr.IssueID); showErr == nil && beads.IssueStatus(issue.Status).IsTerminal() {
				_, _ = fmt.Fprintf(m.output, "  %s source issue already closed: %s\n", style.Dim.Render("○"), mr.IssueID)
				result.SourceIssueClosed = true
			} else {
				_, _ = fmt.Fprintf(m.output, "  %s source issue close: %v\n", style.Dim.Render("○"), err)
				result.SourceIssueNotFound = true
			}
		} else {
			result.SourceIssueClosed = true
		}
	}

	return result, nil
}

// notifyWorkerRejected sends a rejection notification to a polecat.
func (m *Manager) notifyWorkerRejected(mr *MergeRequest, reason string) {
	// Nudge polecat about rejection instead of sending permanent mail.
	polecatName := strings.TrimPrefix(mr.Worker, "polecats/")
	target := fmt.Sprintf("%s/%s", m.rig.Name, polecatName)
	nudgeMsg := fmt.Sprintf("MR rejected: branch=%s issue=%s reason=%s — review feedback and resubmit with 'gt done'",
		mr.Branch, mr.IssueID, reason)
	nudgeCmd := exec.Command("gt", "nudge", target, nudgeMsg)
	util.SetDetachedProcessGroup(nudgeCmd)
	nudgeCmd.Dir = m.workDir
	if err := nudgeCmd.Run(); err != nil {
		log.Printf("warning: nudging worker about rejection for %s: %v", mr.IssueID, err)
	}
}

// Town root is computed in Start() as filepath.Dir(m.rig.Path) and passed
// through to callers — no filesystem-inference function needed (ZFC gt-qago).
