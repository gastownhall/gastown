// Package beads provides a wrapper for the bd (beads) CLI.
package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/util"
)

// Common errors
// ZFC: Only define errors that don't require stderr parsing for decisions.
// ErrNotARepo and ErrSyncConflict were removed - agents should handle these directly.
var (
	ErrNotInstalled = errors.New("bd not installed: run 'pip install beads-cli' or see https://github.com/anthropics/beads")
	ErrNotFound     = errors.New("issue not found")
	ErrFlagTitle    = errors.New("title looks like a CLI flag (starts with '-'); use --title=\"...\" to set flag-like titles intentionally")
)

// bdAllowStale caches whether the installed bd supports --allow-stale.
// The cache is keyed by the resolved bd path so tests and subprocess stubs that
// replace bd on PATH get re-probed instead of reusing stale capability state.
var (
	bdAllowStaleMu     sync.Mutex
	bdAllowStalePath   string
	bdAllowStaleResult bool
)

// ResetBdAllowStaleCacheForTest clears the cached bd --allow-stale capability.
// It exists for tests that swap bd binaries on PATH within a single process.
func ResetBdAllowStaleCacheForTest() {
	bdAllowStaleMu.Lock()
	bdAllowStalePath = ""
	bdAllowStaleResult = false
	bdAllowStaleMu.Unlock()
}

// BdSupportsAllowStale returns true if the installed bd binary accepts --allow-stale.
func BdSupportsAllowStale() bool {
	return BdSupportsAllowStaleWithEnv(nil)
}

// BdSupportsAllowStaleWithEnv returns true if the installed bd binary accepts
// --allow-stale, probing with the provided environment when supplied.
func BdSupportsAllowStaleWithEnv(env []string) bool {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return false
	}

	bdAllowStaleMu.Lock()
	cachedPath := bdAllowStalePath
	cachedResult := bdAllowStaleResult
	bdAllowStaleMu.Unlock()

	if cachedPath == bdPath {
		return cachedResult
	}

	cmd := exec.Command(bdPath, "--allow-stale", "version") //nolint:gosec // G204: bd is a trusted internal tool
	util.SetDetachedProcessGroup(cmd)
	if env != nil {
		cmd.Env = env
	}
	var combinedOut bytes.Buffer
	cmd.Stdout = &combinedOut
	cmd.Stderr = &combinedOut
	_ = cmd.Run()
	// bd v0.60+ exits 0 even on unknown flags, printing the error to stderr.
	// Check output for "unknown flag" to detect lack of support.
	supported := !strings.Contains(combinedOut.String(), "unknown flag")

	bdAllowStaleMu.Lock()
	if bdAllowStalePath != bdPath {
		bdAllowStalePath = bdPath
		bdAllowStaleResult = supported
	}
	result := bdAllowStaleResult
	bdAllowStaleMu.Unlock()
	return result
}

// MaybePrependAllowStale prepends --allow-stale to args if bd supports it.
// Exported for use by other packages that shell out to bd directly.
func MaybePrependAllowStale(args []string) []string {
	if BdSupportsAllowStale() {
		return append([]string{"--allow-stale"}, args...)
	}
	return args
}

// MaybePrependAllowStaleWithEnv prepends --allow-stale to args if bd supports it,
// probing with the provided environment when supplied.
func MaybePrependAllowStaleWithEnv(env []string, args []string) []string {
	if BdSupportsAllowStaleWithEnv(env) {
		return append([]string{"--allow-stale"}, args...)
	}
	return args
}

// InjectFlatForListJSON adds --flat to bd list commands that use --json.
// bd v0.59+ tree-format output ignores --json; --flat is required for JSON.
// Exported for use by other packages that call bd list directly.
func InjectFlatForListJSON(args []string) []string {
	// Only apply to top-level "bd list" commands (args[0] == "list"),
	// not subcommands like "bd dep list" where --flat is unsupported.
	if len(args) == 0 || args[0] != "list" {
		return args
	}
	hasJSON := false
	hasFlat := false
	for _, a := range args[1:] {
		switch {
		case a == "--json":
			hasJSON = true
		case a == "--flat":
			hasFlat = true
		}
	}
	if hasJSON && !hasFlat {
		return append(args, "--flat")
	}
	return args
}

// ExtractIssueID strips the external:prefix:id wrapper from bead IDs.
// bd dep add wraps cross-rig IDs as "external:prefix:id" for routing,
// but consumers need the raw bead ID for display and lookups.
func ExtractIssueID(id string) string {
	if strings.HasPrefix(id, "external:") {
		parts := strings.SplitN(id, ":", 3)
		if len(parts) == 3 {
			return parts[2]
		}
	}
	return id
}

// IsFlagLikeTitle returns true if the title looks like it was accidentally set
// from a CLI flag (e.g., "--help", "--json", "-v"). This catches a common
// mistake where `bd create --title --help` consumes --help as the title value
// instead of showing help. Titles with spaces (e.g., "Fix --help handling")
// are allowed since they're clearly intentional multi-word titles.
func IsFlagLikeTitle(title string) bool {
	if !strings.HasPrefix(title, "-") {
		return false
	}
	// Single-word flag-like strings: "--help", "-h", "--json", "--verbose"
	// Multi-word titles with flags embedded are fine: "Fix --help handling"
	return !strings.Contains(title, " ")
}

// Issue represents a beads issue.
type Issue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	Type        string   `json:"issue_type"`
	CreatedAt   string   `json:"created_at"`
	CreatedBy   string   `json:"created_by,omitempty"`
	UpdatedAt   string   `json:"updated_at"`
	ClosedAt    string   `json:"closed_at,omitempty"`
	Parent      string   `json:"parent,omitempty"`
	Assignee    string   `json:"assignee,omitempty"`
	Children    []string `json:"children,omitempty"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Blocks      []string `json:"blocks,omitempty"`
	BlockedBy   []string `json:"blocked_by,omitempty"`
	Labels      []string `json:"labels,omitempty"`
	Ephemeral   bool     `json:"ephemeral,omitempty"` // Wisp/ephemeral issues, not synced to git

	// Content fields (parsed from bd show --json)
	AcceptanceCriteria string `json:"acceptance_criteria,omitempty"`

	// Agent bead slots (type=agent only)
	HookBead   string `json:"hook_bead,omitempty"`   // Current work attached to agent's hook
	AgentState string `json:"agent_state,omitempty"` // Agent lifecycle state (spawning, working, done, stuck)
	// Note: role_bead field removed - role definitions are now config-based

	// Counts from list output
	DependencyCount int `json:"dependency_count,omitempty"`
	DependentCount  int `json:"dependent_count,omitempty"`
	BlockedByCount  int `json:"blocked_by_count,omitempty"`

	// Detailed dependency info from show output
	Dependencies []IssueDep `json:"dependencies,omitempty"`
	Dependents   []IssueDep `json:"dependents,omitempty"`

	// Arbitrary metadata blob (JSON object). Used for extension points such as
	// delegation state (delegated_from key) and merge-slot state (holder/waiters).
	// Populated by both bd show --json and the in-process store path.
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// HasLabel checks if an issue has a specific label.
func HasLabel(issue *Issue, label string) bool {
	for _, l := range issue.Labels {
		if l == label {
			return true
		}
	}
	return false
}

// HasUncheckedCriteria checks if an issue has acceptance criteria with unchecked items.
// Returns the count of unchecked items (0 means all checked or no criteria).
func HasUncheckedCriteria(issue *Issue) int {
	if issue == nil || issue.AcceptanceCriteria == "" {
		return 0
	}
	count := 0
	for _, line := range strings.Split(issue.AcceptanceCriteria, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- [ ] ") {
			count++
		}
	}
	return count
}

// IsAgentBead checks if an issue is an agent bead by checking for the gt:agent
// label (preferred) or the legacy type == "agent" field. This handles the migration
// from type-based to label-based agent identification (see gt-vja7b).
func IsAgentBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	// Check legacy type field first for backward compatibility
	if issue.Type == "agent" {
		return true
	}
	// Check for gt:agent label (current standard)
	return HasLabel(issue, "gt:agent")
}

// IsProtectedBead checks if a bead has any protection labels that should
// prevent automated status changes (AutoClose, unassign on polecat removal, etc.).
// Protected labels: gt:standing-orders, gt:keep, gt:role, gt:rig.
func IsProtectedBead(issue *Issue) bool {
	if issue == nil {
		return false
	}
	for _, l := range issue.Labels {
		switch l {
		case "gt:standing-orders", "gt:keep", "gt:role", "gt:rig":
			return true
		}
	}
	return false
}

// IssueDep represents a dependency or dependent issue with its relation.
type IssueDep struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Status         string `json:"status"`
	Priority       int    `json:"priority"`
	Type           string `json:"issue_type"`
	DependencyType string `json:"dependency_type,omitempty"`
}

// ListOptions specifies filters for listing issues.
type ListOptions struct {
	Status     string // "open", "closed", "all"
	Type       string // Deprecated: use Label instead. Was "task", "bug", "feature", "epic"; converted to "gt:" prefix.
	Label      string // Label filter (e.g., "gt:agent", "gt:merge-request")
	Priority   int    // 0-4, -1 for no filter
	Parent     string // filter by parent ID
	Assignee   string // filter by assignee (e.g., "gastown/Toast")
	NoAssignee bool   // filter for issues with no assignee
	Limit      int    // Max results (0 = unlimited, overrides bd default of 50)
	Ephemeral  bool   // Search wisps table (ephemeral issues) instead of issues table
}

// CreateOptions specifies options for creating an issue.
type CreateOptions struct {
	Title       string
	Type        string   // Deprecated: use Labels instead. Was "task", "bug", "feature", "epic".
	Label       string   // Deprecated: use Labels instead. Backward-compatible single-label form.
	Labels      []string // Labels to set (e.g., "gt:task", "gt:merge-request")
	Priority    int      // 0-4
	Description string
	Parent      string
	Actor       string // Who is creating this issue (populates created_by)
	Ephemeral   bool   // Create as ephemeral (wisp) - not synced to git
	Rig         string // Target rig database (e.g., "gastown"). When set, passes --repo to bd create.
}

// UpdateOptions specifies options for updating an issue.
type UpdateOptions struct {
	Title        *string
	Status       *string
	Priority     *int
	Description  *string
	Assignee     *string
	AddLabels    []string // Labels to add
	RemoveLabels []string // Labels to remove
	SetLabels    []string // Labels to set (replaces all existing)
}

// Beads wraps bd CLI operations for a working directory.
// When store is non-nil, methods with in-process implementations use the
// beadsdk.Storage directly instead of shelling out to the bd CLI. This
// eliminates ~600ms of subprocess overhead per operation.
type Beads struct {
	workDir    string
	beadsDir   string // Optional BEADS_DIR override for cross-database access
	isolated   bool   // If true, suppress inherited beads env vars (for test isolation)
	serverPort int    // If set, pass --server-port to bd init and GT_DOLT_PORT to env

	// store is an optional in-process beadsdk.Storage. When set, methods
	// bypass the bd subprocess and use the store directly. Follows the
	// pattern in internal/daemon/convoy_manager.go. Callers are responsible
	// for closing the store.
	store beadsdk.Storage

	// Lazy-cached town root for routing resolution.
	// Populated on first call to getTownRoot() to avoid filesystem walk on every operation.
	townRoot     string
	townRootOnce sync.Once
}

// New creates a new Beads wrapper for the given directory.
func New(workDir string) *Beads {
	return &Beads{workDir: workDir}
}

// NewIsolated creates a Beads wrapper for test isolation.
// This suppresses inherited beads env vars (BD_ACTOR, BEADS_DB) to prevent
// tests from accidentally routing to production databases.
func NewIsolated(workDir string) *Beads {
	return &Beads{workDir: workDir, isolated: true}
}

// NewIsolatedWithPort creates a Beads wrapper for test isolation that targets
// a specific Dolt server port. Init() passes --server-port to bd init, and all
// commands get GT_DOLT_PORT in their environment. This prevents tests from
// creating databases on the production Dolt server (port 3307).
func NewIsolatedWithPort(workDir string, serverPort int) *Beads {
	return &Beads{workDir: workDir, isolated: true, serverPort: serverPort}
}

// NewWithBeadsDir creates a Beads wrapper with an explicit BEADS_DIR.
// This is needed when running from a polecat worktree but accessing town-level beads.
func NewWithBeadsDir(workDir, beadsDir string) *Beads {
	return &Beads{workDir: workDir, beadsDir: beadsDir}
}

// getActor returns the BD_ACTOR value for this context.
// Returns empty string when in isolated mode (tests) to prevent
// inherited actors from routing to production databases.
func (b *Beads) getActor() string {
	if b.isolated {
		return ""
	}
	return os.Getenv("BD_ACTOR")
}

// getTownRoot returns the Gas Town root directory, using lazy caching.
// The town root is found by walking up from workDir looking for mayor/town.json.
// Returns empty string if not in a Gas Town project.
// Thread-safe: uses sync.Once to prevent races on concurrent access.
func (b *Beads) getTownRoot() string {
	b.townRootOnce.Do(func() {
		b.townRoot = FindTownRoot(b.workDir)
	})
	return b.townRoot
}

// getResolvedBeadsDir returns the beads directory this wrapper is operating on.
// This follows any redirects and returns the actual beads directory path.
func (b *Beads) getResolvedBeadsDir() string {
	if b.beadsDir != "" {
		return b.beadsDir
	}
	return ResolveBeadsDir(b.workDir)
}

// Init initializes a new beads database in the working directory.
// This uses the same environment isolation as other commands.
// If ServerPort is set (via NewIsolatedWithPort), passes --server-port to bd init
// so the database is created on the test Dolt server.
func (b *Beads) Init(prefix string) error {
	args := []string{"init"}
	if prefix != "" {
		args = append(args, "--prefix", prefix)
	}
	args = append(args, "--quiet")
	if b.serverPort > 0 {
		args = append(args, "--server-port", fmt.Sprintf("%d", b.serverPort))
	}
	_, err := b.run(args...)
	return err
}

// run executes a bd command and returns stdout.
func (b *Beads) run(args ...string) (_ []byte, retErr error) {
	start := time.Now()
	// Declare buffers before defer so the closure captures them after cmd.Run.
	var stdout, stderr bytes.Buffer
	defer func() {
		telemetry.RecordBDCall(context.Background(), args, float64(time.Since(start).Milliseconds()), retErr, stdout.Bytes(), stderr.String())
	}()
	// bd v0.59+ requires --flat for --json to produce JSON output on "list" commands.
	// Without --flat, bd list --json silently returns human-readable tree format,
	// causing all JSON parsing to fail. Inject --flat before --allow-stale prepend
	// (which changes args[0] from "list" to "--allow-stale").
	args = InjectFlatForListJSON(args)

	// Conditionally use --allow-stale to prevent failures when db is temporarily stale
	// (e.g., after daemon is killed during shutdown). Only if bd supports it.
	beadsDir := b.beadsDir
	if beadsDir == "" {
		beadsDir = ResolveBeadsDir(b.workDir)
	}
	runEnv := append(b.buildRunEnv(), "BEADS_DIR="+beadsDir)
	fullArgs := MaybePrependAllowStaleWithEnv(runEnv, args)

	// Always explicitly set BEADS_DIR to prevent inherited env vars from
	// causing prefix mismatches. Use explicit beadsDir if set, otherwise
	// resolve from working directory.
	cmd := exec.Command("bd", fullArgs...) //nolint:gosec // G204: bd is a trusted internal tool
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = b.workDir

	cmd.Env = runEnv
	cmd.Env = append(cmd.Env, telemetry.OTELEnvForSubprocess()...)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	// If bd doesn't support --flat, retry without it. The retry is done here
	// (not in callers like List) so that InjectFlatForListJSON doesn't re-add
	// --flat on the retry path.
	if err != nil && strings.Contains(stderr.String(), "unknown flag: --flat") {
		retryArgs := make([]string, 0, len(fullArgs))
		for _, a := range fullArgs {
			if a != "--flat" {
				retryArgs = append(retryArgs, a)
			}
		}
		stdout.Reset()
		stderr.Reset()
		cmd = exec.Command("bd", retryArgs...) //nolint:gosec // G204: bd is a trusted internal tool
		util.SetDetachedProcessGroup(cmd)
		cmd.Dir = b.workDir
		cmd.Env = runEnv
		cmd.Env = append(cmd.Env, telemetry.OTELEnvForSubprocess()...)
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err = cmd.Run()
	}

	if err != nil {
		return nil, b.wrapError(err, stderr.String(), args)
	}

	// Handle bd exit code 0 bug: when issue not found,
	// bd may exit 0 but write error to stderr with empty stdout.
	// Detect this case and treat as error to avoid JSON parse failures.
	if stdout.Len() == 0 && stderr.Len() > 0 {
		return nil, b.wrapError(fmt.Errorf("command produced no output"), stderr.String(), args)
	}

	return stripStdoutWarnings(stdout.Bytes()), nil
}

// runWithRouting executes a bd command without setting BEADS_DIR, allowing bd's
// native prefix-based routing via routes.jsonl to resolve cross-prefix beads.
// This is needed for slot operations that reference beads with different prefixes
// (e.g., setting an hq-* hook bead on a gt-* agent bead).
// See: sling_helpers.go verifyBeadExists/hookBeadWithRetry for the same pattern.
func (b *Beads) runWithRouting(args ...string) (_ []byte, retErr error) { //nolint:unparam // mirrors run() signature for consistency
	start := time.Now()
	var stdout, stderr bytes.Buffer
	defer func() {
		telemetry.RecordBDCall(context.Background(), args, float64(time.Since(start).Milliseconds()), retErr, stdout.Bytes(), stderr.String())
	}()
	runEnv := b.buildRoutingEnv()
	fullArgs := MaybePrependAllowStaleWithEnv(runEnv, args)

	cmd := exec.Command("bd", fullArgs...) //nolint:gosec // G204: bd is a trusted internal tool
	util.SetDetachedProcessGroup(cmd)
	cmd.Dir = b.workDir

	cmd.Env = runEnv
	cmd.Env = append(cmd.Env, telemetry.OTELEnvForSubprocess()...)

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return nil, b.wrapError(err, stderr.String(), args)
	}

	if stdout.Len() == 0 && stderr.Len() > 0 {
		return nil, b.wrapError(fmt.Errorf("command produced no output"), stderr.String(), args)
	}

	return stripStdoutWarnings(stdout.Bytes()), nil
}

// Run executes a bd command and returns stdout.
// This is a public wrapper around the internal run method for cases where
// callers need to run arbitrary bd commands.
func (b *Beads) Run(args ...string) ([]byte, error) {
	return b.run(args...)
}

// wrapError wraps bd errors with context.
// ZFC: Avoid parsing stderr to make decisions. Transport errors to agents instead.
// Exception: ErrNotInstalled (exec.ErrNotFound) and ErrNotFound (issue lookup) are
// acceptable as they enable basic error handling without decision-making.
func (b *Beads) wrapError(err error, stderr string, args []string) error {
	stderr = strings.TrimSpace(stderr)

	// Check for bd not installed
	if execErr, ok := err.(*exec.Error); ok && errors.Is(execErr.Err, exec.ErrNotFound) {
		return ErrNotInstalled
	}

	// ErrNotFound is widely used for issue lookups - acceptable exception
	// Match various "not found" error patterns from bd
	if strings.Contains(stderr, "not found") || strings.Contains(stderr, "Issue not found") ||
		strings.Contains(stderr, "no issue found") {
		return ErrNotFound
	}

	if stderr != "" {
		return fmt.Errorf("bd %s: %s", strings.Join(args, " "), stderr)
	}
	return fmt.Errorf("bd %s: %w", strings.Join(args, " "), err)
}

// isSubprocessCrash returns true if the error indicates the subprocess crashed
// (e.g., Dolt nil pointer dereference causing SIGSEGV). This is used to detect
// recoverable failures where a fallback strategy should be attempted (GH#1769).
func isSubprocessCrash(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Detect signals from crashed subprocesses (bd panic → SIGSEGV)
	return strings.Contains(errStr, "signal:") ||
		strings.Contains(errStr, "segmentation") ||
		strings.Contains(errStr, "nil pointer") ||
		strings.Contains(errStr, "panic:")
}

// buildRunEnv builds the environment for run() calls.
// In isolated mode: strips all beads-related env vars for test isolation.
// Otherwise: strips inherited BEADS_DIR so the caller can append the correct value.
// Without this, getenv() returns the first occurrence, so an inherited BEADS_DIR
// (e.g., from a parent process or shell context) would shadow the explicit value
// appended by run(). This was the root cause of gt-uygpe / GH #803.
func (b *Beads) buildRunEnv() []string {
	if b.isolated {
		env := filterBeadsEnv(os.Environ())
		if b.serverPort > 0 {
			env = append(env, fmt.Sprintf("GT_DOLT_PORT=%d", b.serverPort))
			env = append(env, fmt.Sprintf("BEADS_DOLT_PORT=%d", b.serverPort))
		}
		return env
	}
	env := stripEnvPrefixes(os.Environ(), "BEADS_DIR=")
	env = overrideDoltEnvFromBeadsDir(env, b.getResolvedBeadsDir())
	return translateDoltPort(env)
}

// buildRoutingEnv builds the environment for runWithRouting() calls.
// Always strips BEADS_DIR so bd uses native routing.
// In isolated mode: also strips BD_ACTOR, BEADS_*, GT_ROOT, HOME.
func (b *Beads) buildRoutingEnv() []string {
	if b.isolated {
		env := filterBeadsEnv(os.Environ())
		if b.serverPort > 0 {
			env = append(env, fmt.Sprintf("GT_DOLT_PORT=%d", b.serverPort))
			env = append(env, fmt.Sprintf("BEADS_DOLT_PORT=%d", b.serverPort))
		}
		return env
	}
	env := stripEnvPrefixes(os.Environ(), "BEADS_DIR=")
	env = overrideDoltEnvFromBeadsDir(env, b.getResolvedBeadsDir())
	return translateDoltPort(env)
}

// filterBeadsEnv removes beads-related environment variables from the given
// environment slice. This ensures test isolation by preventing inherited
// BD_ACTOR, BEADS_DB, GT_ROOT, HOME etc. from routing commands to production databases.
//
// Preserves GT_DOLT_PORT, BEADS_DOLT_PORT, and BEADS_DOLT_SERVER_HOST so that
// isolated-mode tests can reach a test Dolt server on a non-default port/host.
func filterBeadsEnv(environ []string) []string {
	filtered := make([]string, 0, len(environ))
	for _, env := range environ {
		// Preserve Dolt connection env vars needed to reach test/remote Dolt servers.
		// These must be checked before the broad BEADS_ prefix strip below.
		if strings.HasPrefix(env, "BEADS_DOLT_PORT=") ||
			strings.HasPrefix(env, "BEADS_DOLT_SERVER_HOST=") ||
			strings.HasPrefix(env, "GT_DOLT_PORT=") {
			filtered = append(filtered, env)
			continue
		}
		// Skip beads-related env vars that could interfere with test isolation
		// BD_ACTOR, BEADS_* - direct beads config
		// GT_ROOT - causes bd to find global routes file
		// HOME - causes bd to find ~/.beads-planning routing
		if strings.HasPrefix(env, "BD_ACTOR=") ||
			strings.HasPrefix(env, "BEADS_") ||
			strings.HasPrefix(env, "GT_ROOT=") ||
			strings.HasPrefix(env, "HOME=") {
			continue
		}
		filtered = append(filtered, env)
	}
	return filtered
}

// translateDoltPort ensures BEADS_DOLT_PORT and BEADS_DOLT_SERVER_HOST are set
// when their GT_ counterparts are present. Gas Town uses GT_DOLT_PORT and
// GT_DOLT_HOST; beads uses BEADS_DOLT_PORT and BEADS_DOLT_SERVER_HOST. This
// translation prevents bd subprocesses from falling back to localhost:3307
// when a test or daemon has set GT_DOLT_* to alternate values.
func translateDoltPort(env []string) []string {
	var gtPort, gtHost string
	hasBDP, hasBDH := false, false
	for _, e := range env {
		if strings.HasPrefix(e, "GT_DOLT_PORT=") {
			gtPort = strings.TrimPrefix(e, "GT_DOLT_PORT=")
		}
		if strings.HasPrefix(e, "GT_DOLT_HOST=") {
			gtHost = strings.TrimPrefix(e, "GT_DOLT_HOST=")
		}
		if strings.HasPrefix(e, "BEADS_DOLT_PORT=") {
			hasBDP = true
		}
		if strings.HasPrefix(e, "BEADS_DOLT_SERVER_HOST=") {
			hasBDH = true
		}
	}
	if gtPort != "" && !hasBDP {
		env = append(env, "BEADS_DOLT_PORT="+gtPort)
	}
	if gtHost != "" && !hasBDH {
		env = append(env, "BEADS_DOLT_SERVER_HOST="+gtHost)
	}
	return env
}

// overrideDoltEnvFromBeadsDir replaces inherited BEADS_DOLT_* values with the
// authoritative connection data for the selected beads directory when present.
// This prevents a parent shell's stale Dolt port from routing bd commands to
// the wrong server when the command explicitly targets another rig's .beads dir.
func overrideDoltEnvFromBeadsDir(env []string, beadsDir string) []string {
	port, host := doltConnectionFromBeadsDir(beadsDir)
	if port != "" {
		env = stripEnvPrefixes(env, "BEADS_DOLT_PORT=")
		env = append(env, "BEADS_DOLT_PORT="+port)
	}
	if host != "" {
		env = stripEnvPrefixes(env, "BEADS_DOLT_SERVER_HOST=")
		env = append(env, "BEADS_DOLT_SERVER_HOST="+host)
	}
	return env
}

// doltConnectionFromBeadsDir reads the preferred Dolt connection info for a
// beads directory. The per-directory port file is authoritative when present;
// metadata.json is used as a fallback and to supply the server host.
func doltConnectionFromBeadsDir(beadsDir string) (port string, host string) {
	if beadsDir == "" {
		return "", ""
	}

	if data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port")); err == nil {
		port = strings.TrimSpace(string(data))
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "metadata.json"))
	if err != nil {
		return port, ""
	}

	var meta struct {
		DoltServerPort int    `json:"dolt_server_port"`
		DoltServerHost string `json:"dolt_server_host"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return port, ""
	}

	if port == "" && meta.DoltServerPort > 0 {
		port = strconv.Itoa(meta.DoltServerPort)
	}
	host = strings.TrimSpace(meta.DoltServerHost)
	return port, host
}

// stripEnvPrefixes removes entries matching any of the given prefixes from an
// environment variable slice. Used by runWithRouting to strip BEADS_DIR.
func stripEnvPrefixes(environ []string, prefixes ...string) []string {
	filtered := make([]string, 0, len(environ))
	for _, env := range environ {
		skip := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(env, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			filtered = append(filtered, env)
		}
	}
	return filtered
}

// List returns issues matching the given options.
// When Ephemeral is true, uses "bd query" with ephemeral=true to search the
// wisps table (where ephemeral issues live in beads v0.59+). Without this,
// "bd list" only searches the issues table and misses wisps entirely.
func (b *Beads) List(opts ListOptions) ([]*Issue, error) {
	if b.store != nil && !opts.Ephemeral {
		return b.storeList(opts)
	}
	if opts.Ephemeral {
		return b.listEphemeral(opts)
	}

	args := []string{"list", "--json"}

	if opts.Status != "" {
		args = append(args, "--status="+opts.Status)
	}
	// Prefer Label over Type (Type is deprecated)
	if opts.Label != "" {
		args = append(args, "--label="+opts.Label)
	} else if opts.Type != "" {
		// Deprecated: convert type to label for backward compatibility
		args = append(args, "--label=gt:"+opts.Type)
	}
	if opts.Priority >= 0 {
		args = append(args, fmt.Sprintf("--priority=%d", opts.Priority))
	}
	if opts.Parent != "" {
		args = append(args, "--parent="+opts.Parent)
	}
	if opts.Assignee != "" {
		args = append(args, "--assignee="+opts.Assignee)
	}
	if opts.NoAssignee {
		args = append(args, "--no-assignee")
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	} else {
		// Override bd's default limit of 50 to avoid silent truncation
		args = append(args, "--limit=0")
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	// bd list --json may return plain text (e.g., "No issues found.") instead
	// of an empty JSON array when there are no results. Handle gracefully.
	if len(out) == 0 || !isJSONBytes(out) {
		return nil, nil
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}

	return issues, nil
}

// listEphemeral searches the wisps table using "bd query" with ephemeral=true.
// This is necessary because "bd list" only searches the issues table and does
// not support an --ephemeral flag. Wisps (ephemeral issues like merge-request
// beads) live in a separate table since beads v0.59.
func (b *Beads) listEphemeral(opts ListOptions) ([]*Issue, error) {
	// Build query expression: ephemeral=true AND <filters>
	clauses := []string{"ephemeral=true"}

	if opts.Label != "" {
		clauses = append(clauses, "label="+opts.Label)
	} else if opts.Type != "" {
		clauses = append(clauses, "label=gt:"+opts.Type)
	}
	if opts.Status != "" && opts.Status != "all" {
		clauses = append(clauses, "status="+opts.Status)
	}
	if opts.Priority >= 0 {
		clauses = append(clauses, fmt.Sprintf("priority=%d", opts.Priority))
	}
	if opts.Parent != "" {
		clauses = append(clauses, "parent="+opts.Parent)
	}
	if opts.Assignee != "" {
		clauses = append(clauses, "assignee="+opts.Assignee)
	}

	queryExpr := strings.Join(clauses, " AND ")
	args := []string{"query", "--json", queryExpr}

	if opts.Status == "all" {
		args = append(args, "--all")
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	if len(out) == 0 || !isJSONBytes(out) {
		return nil, nil
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd query output: %w", err)
	}

	return issues, nil
}

// stripStdoutWarnings removes warning/diagnostic lines that bd may emit to stdout.
// bd sometimes prints "warning: ..." lines to stdout instead of stderr, which
// corrupts JSON output. This strips those lines so downstream JSON parsing works.
func stripStdoutWarnings(data []byte) []byte {
	if !bytes.Contains(data, []byte("warning:")) {
		return data
	}

	lines := bytes.Split(data, []byte("\n"))
	var cleaned [][]byte
	stripped := false
	for _, line := range lines {
		if bytes.HasPrefix(bytes.TrimSpace(line), []byte("warning:")) {
			stripped = true
			continue
		}
		cleaned = append(cleaned, line)
	}

	if !stripped {
		return data
	}
	return bytes.Join(cleaned, []byte("\n"))
}

// isJSONBytes returns true if the byte slice starts with [ or { (after whitespace).
// bd list --json may return plain text like "No issues found." instead of JSON
// when there are no results.
func isJSONBytes(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		case '[', '{':
			return true
		default:
			return false
		}
	}
	return false
}

// ListMergeRequests returns merge-request beads from both the issues table
// and the wisps table. MRs are created as ephemeral (wisps) by gt mq submit,
// but bd list only queries the issues table. This method queries the wisps
// table via bd sql --json to get full data including labels and assignee.
func (b *Beads) ListMergeRequests(opts ListOptions) ([]*Issue, error) {
	// 1. Query issues table (bd list) — don't use Ephemeral since bd query
	// can't parse colons in label values like "gt:merge-request".
	opts.Ephemeral = false
	issueResults, err := b.List(opts)
	if err != nil {
		return nil, err
	}

	// Build dedup map from issues
	seen := make(map[string]bool, len(issueResults))
	for _, issue := range issueResults {
		seen[issue.ID] = true
	}

	// 2. Query wisps table via SQL for merge-request wisps with full data
	statusFilter := "w.status = 'open'"
	if opts.Status != "" && strings.EqualFold(opts.Status, "all") {
		statusFilter = "1=1"
	} else if opts.Status != "" {
		statusFilter = fmt.Sprintf("w.status = '%s'", strings.ReplaceAll(strings.ToLower(opts.Status), "'", "''"))
	}

	labelFilter := "l.label = 'gt:merge-request'"
	if opts.Label != "" {
		labelFilter = fmt.Sprintf("l.label = '%s'", strings.ReplaceAll(opts.Label, "'", "''"))
	}

	query := fmt.Sprintf(
		"SELECT w.id, w.title, w.description, w.status, w.priority, w.assignee, "+
			"w.created_at, w.updated_at, w.created_by, "+
			"GROUP_CONCAT(al.label) as labels_csv "+
			"FROM wisps w "+
			"JOIN wisp_labels l ON w.id = l.issue_id "+
			"LEFT JOIN wisp_labels al ON w.id = al.issue_id "+
			"WHERE %s AND %s "+
			"GROUP BY w.id, w.title, w.description, w.status, w.priority, w.assignee, w.created_at, w.updated_at, w.created_by",
		labelFilter, statusFilter)

	sqlOut, sqlErr := b.run("sql", "--json", query)
	if sqlErr == nil && len(sqlOut) > 0 && isJSONBytes(sqlOut) {
		var rows []struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Status      string `json:"status"`
			Priority    int    `json:"priority"`
			Assignee    string `json:"assignee"`
			CreatedAt   string `json:"created_at"`
			UpdatedAt   string `json:"updated_at"`
			CreatedBy   string `json:"created_by"`
			LabelsCSV   string `json:"labels_csv"`
		}
		if jsonErr := json.Unmarshal(sqlOut, &rows); jsonErr == nil {
			for _, row := range rows {
				if seen[row.ID] {
					continue
				}
				issue := &Issue{
					ID:          row.ID,
					Title:       row.Title,
					Description: row.Description,
					Status:      row.Status,
					Priority:    row.Priority,
					Assignee:    row.Assignee,
					CreatedAt:   row.CreatedAt,
					UpdatedAt:   row.UpdatedAt,
					CreatedBy:   row.CreatedBy,
					Ephemeral:   true,
				}
				if row.LabelsCSV != "" {
					issue.Labels = strings.Split(row.LabelsCSV, ",")
				}
				issueResults = append(issueResults, issue)
			}
		}
	}

	return issueResults, nil
}

// ListByAssignee returns all issues assigned to a specific assignee.
// The assignee is typically in the format "rig/polecats/polecatName" (e.g., "gastown/polecats/Toast").
func (b *Beads) ListByAssignee(assignee string) ([]*Issue, error) {
	return b.List(ListOptions{
		Status:   "all", // Include both open and closed for state derivation
		Assignee: assignee,
		Priority: -1, // No priority filter
	})
}

// GetAssignedIssue returns the first issue assigned to the given assignee.
// Checks open, in_progress, and hooked statuses (hooked = work on agent's hook).
// Returns nil if no matching issue is assigned.
func (b *Beads) GetAssignedIssue(assignee string) (*Issue, error) {
	// Check all active work statuses: open, in_progress, and hooked
	// "hooked" status is set by gt sling when work is attached to an agent's hook
	for _, status := range []string{"open", "in_progress", StatusHooked} {
		issues, err := b.List(ListOptions{
			Status:   status,
			Assignee: assignee,
			Priority: -1,
		})
		if err != nil {
			return nil, err
		}
		if len(issues) > 0 {
			return issues[0], nil
		}
	}

	return nil, nil
}

// Ready returns issues that are ready to work (not blocked).
func (b *Beads) Ready() ([]*Issue, error) {
	if b.store != nil {
		return b.storeReady()
	}

	out, err := b.run("ready", "--json")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd ready output: %w", err)
	}

	return issues, nil
}

// ReadyForMol returns ready steps within a specific molecule.
// Delegates to bd ready --mol which uses beads' canonical blocking semantics
// (blocked_issues_cache), handling all blocking types, transitive propagation,
// and conditional-blocks resolution.
func (b *Beads) ReadyForMol(moleculeID string) ([]*Issue, error) {
	if b.store != nil {
		return b.storeReadyWithFilter(beadsdk.WorkFilter{
			ParentID: &moleculeID,
			Limit:    100,
		})
	}

	out, err := b.run("ready", "--mol", moleculeID, "--json", "-n", "100")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd ready --mol output: %w", err)
	}

	return issues, nil
}

// ReadyWithType returns ready issues filtered by label.
// Uses bd ready --label flag for server-side filtering.
// The issueType is converted to a gt:<type> label (e.g., "molecule" -> "gt:molecule").
func (b *Beads) ReadyWithType(issueType string) ([]*Issue, error) {
	if b.store != nil {
		return b.storeReadyWithFilter(beadsdk.WorkFilter{
			Labels: []string{"gt:" + issueType},
			Limit:  100,
		})
	}

	out, err := b.run("ready", "--json", "--label", "gt:"+issueType, "-n", "100")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd ready output: %w", err)
	}

	return issues, nil
}

// Show returns detailed information about an issue.
func (b *Beads) Show(id string) (*Issue, error) {
	// Route cross-rig queries via routes.jsonl so that rig-level bead IDs
	// (e.g., "gt-abc123") resolve to the correct rig database.
	targetDir := ResolveRoutingTarget(b.getTownRoot(), id, b.getResolvedBeadsDir())
	if targetDir != b.getResolvedBeadsDir() {
		target := NewWithBeadsDir(filepath.Dir(targetDir), targetDir)
		return target.Show(id)
	}

	if b.store != nil {
		return b.storeShow(id)
	}

	out, err := b.run("show", id, "--json")
	if err != nil {
		return nil, err
	}

	// bd show --json returns an array with one element
	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd show output: %w", err)
	}

	if len(issues) == 0 {
		return nil, ErrNotFound
	}

	return issues[0], nil
}

// FindLatestIssueByTitleAndAssignee finds the newest issue matching the given title and assignee.
func (b *Beads) FindLatestIssueByTitleAndAssignee(title, assignee string) (*Issue, error) {
	out, err := b.run("list", "--json", "--limit", "0", "--title", title, "--assignee", assignee)
	if err != nil {
		return nil, fmt.Errorf("bd list: %w", err)
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd list output: %w", err)
	}
	if len(issues) == 0 {
		return nil, ErrNotFound
	}

	var newest *Issue
	for _, issue := range issues {
		if issue.Title != title || issue.Assignee != assignee {
			continue
		}
		if newest == nil || issue.CreatedAt > newest.CreatedAt {
			newest = issue
		}
	}
	if newest == nil {
		return nil, ErrNotFound
	}
	return newest, nil
}

// ShowMultiple fetches multiple issues by ID in a single bd call.
// Returns a map of ID to Issue. Missing IDs are not included in the map.
func (b *Beads) ShowMultiple(ids []string) (map[string]*Issue, error) {
	if len(ids) == 0 {
		return make(map[string]*Issue), nil
	}

	if b.store != nil {
		return b.storeShowMultiple(ids)
	}

	// bd show supports multiple IDs
	args := append([]string{"show", "--json"}, ids...)
	out, err := b.run(args...)
	if err != nil {
		return nil, fmt.Errorf("bd show: %w", err)
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd show output: %w", err)
	}

	result := make(map[string]*Issue, len(issues))
	for _, issue := range issues {
		result[issue.ID] = issue
	}

	return result, nil
}

// Blocked returns issues that are blocked by dependencies.
func (b *Beads) Blocked() ([]*Issue, error) {
	if b.store != nil {
		return b.storeBlocked()
	}

	out, err := b.run("blocked", "--json")
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd blocked output: %w", err)
	}

	return issues, nil
}

// Create creates a new issue and returns it.
// If opts.Actor is empty, it defaults to the BD_ACTOR environment variable.
// This ensures created_by is populated for issue provenance tracking.
func (b *Beads) Create(opts CreateOptions) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(opts.Title) {
		return nil, fmt.Errorf("refusing to create bead: %w (got %q)", ErrFlagTitle, opts.Title)
	}

	if b.store != nil && !opts.Ephemeral {
		return b.storeCreate(opts)
	}

	args := []string{"create", "--json"}

	if opts.Title != "" {
		args = append(args, "--title="+opts.Title)
	}
	// Labels takes precedence; fall back to deprecated single-label/Type fields.
	if len(opts.Labels) > 0 {
		args = append(args, "--labels="+strings.Join(opts.Labels, ","))
	} else if opts.Label != "" {
		args = append(args, "--labels="+opts.Label)
	} else if opts.Type != "" {
		args = append(args, "--labels=gt:"+opts.Type)
	}
	if opts.Priority >= 0 {
		args = append(args, fmt.Sprintf("--priority=%d", opts.Priority))
	}
	if opts.Description != "" {
		args = append(args, "--description="+opts.Description)
	}
	if opts.Parent != "" {
		args = append(args, "--parent="+opts.Parent)
	}
	if opts.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if opts.Rig != "" {
		args = append(args, "--repo="+opts.Rig)
	}
	// Default Actor from BD_ACTOR env var if not specified
	// Uses getActor() to respect isolated mode (tests)
	actor := opts.Actor
	if actor == "" {
		actor = b.getActor()
	}
	if actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// CreateWithID creates an issue with a specific ID.
// This is useful for agent beads, role beads, and other beads that need
// deterministic IDs rather than auto-generated ones.
func (b *Beads) CreateWithID(id string, opts CreateOptions) (*Issue, error) {
	// Guard against flag-like titles (gt-e0kx5: --help garbage beads)
	if IsFlagLikeTitle(opts.Title) {
		return nil, fmt.Errorf("refusing to create bead: %w (got %q)", ErrFlagTitle, opts.Title)
	}

	args := []string{"create", "--json", "--id=" + id}
	if NeedsForceForID(id) {
		args = append(args, "--force")
	}

	if opts.Title != "" {
		args = append(args, "--title="+opts.Title)
	}
	// Labels takes precedence; fall back to deprecated single-label/Type fields.
	if len(opts.Labels) > 0 {
		args = append(args, "--labels="+strings.Join(opts.Labels, ","))
	} else if opts.Label != "" {
		args = append(args, "--labels="+opts.Label)
	} else if opts.Type != "" {
		args = append(args, "--labels=gt:"+opts.Type)
	}
	if opts.Priority >= 0 {
		args = append(args, fmt.Sprintf("--priority=%d", opts.Priority))
	}
	if opts.Description != "" {
		args = append(args, "--description="+opts.Description)
	}
	if opts.Parent != "" {
		args = append(args, "--parent="+opts.Parent)
	}
	// Default Actor from BD_ACTOR env var if not specified
	// Uses getActor() to respect isolated mode (tests)
	actor := opts.Actor
	if actor == "" {
		actor = b.getActor()
	}
	if actor != "" {
		args = append(args, "--actor="+actor)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issue Issue
	if err := json.Unmarshal(out, &issue); err != nil {
		return nil, fmt.Errorf("parsing bd create output: %w", err)
	}

	return &issue, nil
}

// SearchOptions specifies options for searching issues.
type SearchOptions struct {
	Query        string // Text query to search titles and descriptions
	Status       string // "open", "closed", "all"
	Label        string // Label filter (e.g., "gt:bug")
	Limit        int    // Max results (0 = default)
	DescContains string // Filter by description substring
}

// Search searches issues by text query across title, description, and ID.
func (b *Beads) Search(opts SearchOptions) ([]*Issue, error) {
	if b.store != nil {
		return b.storeSearch(opts)
	}

	args := []string{"search", "--json"}

	if opts.Query != "" {
		args = append(args, opts.Query)
	}
	if opts.Status != "" {
		args = append(args, "--status="+opts.Status)
	}
	if opts.Label != "" {
		args = append(args, "--label="+opts.Label)
	}
	if opts.Limit > 0 {
		args = append(args, fmt.Sprintf("--limit=%d", opts.Limit))
	}
	if opts.DescContains != "" {
		args = append(args, "--desc-contains="+opts.DescContains)
	}

	out, err := b.run(args...)
	if err != nil {
		return nil, err
	}

	var issues []*Issue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing bd search output: %w", err)
	}

	return issues, nil
}

// FindOpenBugsByTitle searches for existing open bugs with titles similar to the given title.
// Used for duplicate detection before filing new test-failure bugs.
// Returns matching issues sorted by relevance (best match first).
func (b *Beads) FindOpenBugsByTitle(title string) ([]*Issue, error) {
	// Extract key terms from the title for searching.
	// Test failure titles typically contain the test name or error description.
	issues, err := b.Search(SearchOptions{
		Query:  title,
		Status: "open",
		Label:  "gt:bug",
		Limit:  10,
	})
	if err != nil {
		return nil, fmt.Errorf("searching for duplicate bugs: %w", err)
	}

	return issues, nil
}

// CreateIfNoDuplicate creates a new bug only if no existing open bug has a similar title.
// If a duplicate is found, it returns the existing issue and a nil error.
// The returned bool is true if a new issue was created, false if an existing duplicate was found.
func (b *Beads) CreateIfNoDuplicate(opts CreateOptions) (*Issue, bool, error) {
	if opts.Title == "" {
		return nil, false, fmt.Errorf("title is required for duplicate detection")
	}

	// Search for existing open bugs with similar titles
	existing, err := b.FindOpenBugsByTitle(opts.Title)
	if err != nil {
		// If search fails, fall through to create (fail-open)
		issue, createErr := b.Create(opts)
		if createErr != nil {
			return nil, false, createErr
		}
		return issue, true, nil
	}

	// Check for title similarity using normalized comparison
	normalizedTitle := normalizeBugTitle(opts.Title)
	for _, issue := range existing {
		if normalizeBugTitle(issue.Title) == normalizedTitle {
			// Exact normalized match — this is a duplicate
			return issue, false, nil
		}
	}

	// No duplicate found, create the new bug
	issue, err := b.Create(opts)
	if err != nil {
		return nil, false, err
	}
	return issue, true, nil
}

// normalizeBugTitle normalizes a bug title for duplicate comparison.
// Strips common prefixes, whitespace, and case differences so that
// "Pre-existing failure: test_foo fails" matches "pre-existing failure: test_foo fails".
func normalizeBugTitle(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	// Strip common prefixes that the refinery adds
	for _, prefix := range []string{"pre-existing failure: ", "pre-existing: ", "test failure: "} {
		t = strings.TrimPrefix(t, prefix)
	}
	return t
}

// Update updates an existing issue.
func (b *Beads) Update(id string, opts UpdateOptions) error {
	if b.store != nil {
		return b.storeUpdate(id, opts)
	}

	args := []string{"update", id}

	if opts.Title != nil {
		args = append(args, "--title="+*opts.Title)
	}
	if opts.Status != nil {
		args = append(args, "--status="+*opts.Status)
	}
	if opts.Priority != nil {
		args = append(args, fmt.Sprintf("--priority=%d", *opts.Priority))
	}
	if opts.Description != nil {
		args = append(args, "--description="+*opts.Description)
	}
	if opts.Assignee != nil {
		args = append(args, "--assignee="+*opts.Assignee)
	}
	// Label operations: set-labels replaces all, otherwise use add/remove
	if len(opts.SetLabels) > 0 {
		for _, label := range opts.SetLabels {
			args = append(args, "--set-labels="+label)
		}
	} else {
		for _, label := range opts.AddLabels {
			args = append(args, "--add-label="+label)
		}
		for _, label := range opts.RemoveLabels {
			args = append(args, "--remove-label="+label)
		}
	}

	_, err := b.run(args...)
	return err
}

// Close closes one or more issues.
// If a runtime session ID is set in the environment, it is passed to bd close
// for work attribution tracking (see decision 009-session-events-architecture.md).
func (b *Beads) Close(ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	if b.store != nil {
		return b.storeClose("", runtime.SessionIDFromEnv(), ids...)
	}

	args := append([]string{"close"}, ids...)

	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	_, err := b.run(args...)
	return err
}

// CloseWithReason closes one or more issues with a reason.
// If a runtime session ID is set in the environment, it is passed to bd close
// for work attribution tracking (see decision 009-session-events-architecture.md).
func (b *Beads) CloseWithReason(reason string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	if b.store != nil {
		return b.storeClose(reason, runtime.SessionIDFromEnv(), ids...)
	}

	args := append([]string{"close"}, ids...)
	args = append(args, "--reason="+reason)

	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	_, err := b.run(args...)
	return err
}

// ForceCloseWithReason closes one or more issues with --force, bypassing
// dependency checks. Used by gt done where the polecat is about to be nuked
// and open molecule wisps should not block issue closure.
func (b *Beads) ForceCloseWithReason(reason string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	// In-process store close doesn't enforce dependency checks (no --force
	// needed). Note: this means the store path bypasses the dependency
	// validation that the CLI's --force flag overrides. Callers relying on
	// ForceCloseWithReason (e.g., gt done nuking polecat wisps) are already
	// accepting that deps may remain dangling, so this is intentional.
	if b.store != nil {
		return b.storeClose(reason, runtime.SessionIDFromEnv(), ids...)
	}

	args := append([]string{"close"}, ids...)
	args = append(args, "--reason="+reason, "--force")

	// Pass session ID for work attribution if available
	if sessionID := runtime.SessionIDFromEnv(); sessionID != "" {
		args = append(args, "--session="+sessionID)
	}

	_, err := b.run(args...)
	return err
}

// Release moves an in_progress issue back to open status.
// This is used to recover stuck steps when a worker dies mid-task.
// It clears the assignee so the step can be claimed by another worker.
func (b *Beads) Release(id string) error {
	return b.ReleaseWithReason(id, "")
}

// ReleaseWithReason moves an in_progress issue back to open status with a reason.
// The reason is added as a note to the issue for tracking purposes.
func (b *Beads) ReleaseWithReason(id, reason string) error {
	if b.store != nil {
		updates := map[string]interface{}{
			"status":   "open",
			"assignee": "",
		}
		if reason != "" {
			updates["notes"] = "Released: " + reason
		}
		ctx, cancel := storeCtx()
		defer cancel()
		return b.store.UpdateIssue(ctx, id, updates, b.getActor())
	}

	args := []string{"update", id, "--status=open", "--assignee="}

	// Add reason as a note if provided
	if reason != "" {
		args = append(args, "--notes=Released: "+reason)
	}

	_, err := b.run(args...)
	return err
}

// AddDependency adds a dependency: issue depends on dependsOn.
func (b *Beads) AddDependency(issue, dependsOn string) error {
	if b.store != nil {
		return b.storeAddDependency(issue, dependsOn)
	}

	_, err := b.run("dep", "add", issue, dependsOn)
	return err
}

// RemoveDependency removes a dependency.
func (b *Beads) RemoveDependency(issue, dependsOn string) error {
	if b.store != nil {
		return b.storeRemoveDependency(issue, dependsOn)
	}

	_, err := b.run("dep", "remove", issue, dependsOn)
	return err
}

// Stats returns repository statistics.
func (b *Beads) Stats() (string, error) {
	out, err := b.run("stats")
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// IsBeadsRepo checks if the working directory is a beads repository.
// ZFC: Check file existence directly instead of parsing bd errors.
func (b *Beads) IsBeadsRepo() bool {
	beadsDir := ResolveBeadsDir(b.workDir)
	info, err := os.Stat(beadsDir)
	return err == nil && info.IsDir()
}

// primeContent is the Gas Town PRIME.md content that provides essential context
// for crew workers. This is the fallback if the SessionStart hook fails.
const primeContent = `# Gas Town Worker Context

> **Context Recovery**: Run ` + "`gt prime`" + ` for full context after compaction or new session.

## The Propulsion Principle (GUPP)

**If you find work on your hook, YOU RUN IT.**

No confirmation. No waiting. No announcements. The hook having work IS the assignment.
This is physics, not politeness. Gas Town is a steam engine - you are a piston.

**Failure mode we're preventing:**
- Agent starts with work on hook
- Agent announces itself and waits for human to say "ok go"
- Human is AFK / trusting the engine to run
- Work sits idle. The whole system stalls.

## Startup Protocol

1. Check your hook: ` + "`gt mol status`" + `
2. If work is hooked → EXECUTE (no announcement, no waiting)
3. If hook empty → Check mail: ` + "`gt mail inbox`" + `
4. Still nothing? Wait for user instructions

## Key Commands

- ` + "`gt prime`" + ` - Get full role context (run after compaction)
- ` + "`gt mol status`" + ` - Check your hooked work
- ` + "`gt mail inbox`" + ` - Check for messages
- ` + "`bd ready`" + ` - Find available work (no blockers)

## Session Close Protocol

Before signaling completion:
1. git status (check what changed)
2. git add <files> (stage code changes)
3. git commit -m "..." (commit code)
4. git push (push to remote)
5. ` + "`gt done`" + ` (submit to merge queue and exit)

**Polecats MUST call ` + "`gt done`" + ` - this submits work and exits the session.**
`

// ProvisionPrimeMD writes the Gas Town PRIME.md file to the specified beads directory.
// This provides essential Gas Town context (GUPP, startup protocol) as a fallback
// if the SessionStart hook fails. The PRIME.md is read by bd prime.
//
// The beadsDir should be the actual beads directory (after following any redirect).
// Returns nil if PRIME.md already exists (idempotent).
func ProvisionPrimeMD(beadsDir string) error {
	primePath := filepath.Join(beadsDir, "PRIME.md")

	// Check if already exists - don't overwrite customizations
	if _, err := os.Stat(primePath); err == nil {
		return nil // Already exists, don't overwrite
	}

	// Create .beads directory if it doesn't exist
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		return fmt.Errorf("creating beads dir: %w", err)
	}

	// Write PRIME.md
	if err := os.WriteFile(primePath, []byte(primeContent), 0644); err != nil {
		return fmt.Errorf("writing PRIME.md: %w", err)
	}

	return nil
}

// ProvisionPrimeMDForWorktree provisions PRIME.md for a worktree by following its redirect.
// This is the main entry point for crew/polecat provisioning.
func ProvisionPrimeMDForWorktree(worktreePath string) error {
	// Resolve the beads directory (follows redirect chain)
	beadsDir := ResolveBeadsDir(worktreePath)

	// Provision PRIME.md in the target directory
	return ProvisionPrimeMD(beadsDir)
}
