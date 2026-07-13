package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	convoyops "github.com/steveyegge/gastown/internal/convoy"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/tui/convoy"
	"github.com/steveyegge/gastown/internal/workspace"
)

var convoyIDEntropy io.Reader = rand.Reader

// generateShortID generates a collision-resistant convoy ID suffix using base36.
// 5 chars of base36 gives ~60M possible values (36^5 = 60,466,176).
// Birthday paradox: ~1% collision at ~1,100 IDs — safe for convoy volumes. (#2063)
func generateShortID() string {
	return generateShortIDFromReader(convoyIDEntropy)
}

func generateShortIDFromReader(r io.Reader) string {
	const alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	b := make([]byte, 5)
	_, _ = io.ReadFull(r, b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

// looksLikeIssueID checks if a string looks like a beads issue ID.
// Issue IDs have the format: prefix-id (e.g., gt-abc, bd-xyz, hq-123).
func looksLikeIssueID(s string) bool {
	// Check registry prefixes and legacy fallbacks via centralized helper
	if session.HasKnownPrefix(s) {
		return true
	}
	// Pattern check: 2-3 lowercase letters followed by hyphen.
	// Covers unregistered short rig prefixes (e.g., nx, rpk).
	// Longer prefixes (4+ chars like nrpk) are caught by HasKnownPrefix
	// via the registry — no need to heuristic-match them here.
	hyphenIdx := strings.Index(s, "-")
	if hyphenIdx >= 2 && hyphenIdx <= 3 && len(s) > hyphenIdx+1 {
		prefix := s[:hyphenIdx]
		for _, c := range prefix {
			if c < 'a' || c > 'z' {
				return false
			}
		}
		return true
	}
	return false
}

// Convoy command flags
var (
	convoyMolecule     string
	convoyNotify       string
	convoyOwner        string
	convoyOwned        bool
	convoyMerge        string
	convoyBaseBranch   string
	convoyStatusJSON   bool
	convoyListJSON     bool
	convoyListStatus   string
	convoyListAll      bool
	convoyListTree     bool
	convoyInteractive  bool
	convoyStrandedJSON bool
	convoyCloseReason  string
	convoyCloseNotify  string
	convoyCloseForce   bool
	convoyCheckDryRun  bool
	convoyLandForce    bool
	convoyLandKeep     bool
	convoyLandDryRun   bool
	convoyFromEpic     string
)

const (
	convoyStatusOpen           = "open"
	convoyStatusClosed         = "closed"
	convoyStatusStagedReady    = "staged_ready"
	convoyStatusStagedWarnings = "staged_warnings"

	// trackedStatusUnknown is the sentinel for a tracked dependency whose
	// status could not be resolved — typically a cross-rig bead whose rig DB
	// is missing, parked, or unroutable from the convoy owner's cwd. Distinct
	// from "open" so auto-close does not mistake it for pending work and
	// `gt convoy status` can label it clearly. (gt-bs6 / GH#2786)
	trackedStatusUnknown = "unknown"
)

func normalizeConvoyStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func ensureKnownConvoyStatus(status string) error {
	switch normalizeConvoyStatus(status) {
	case convoyStatusOpen, convoyStatusClosed, convoyStatusStagedReady, convoyStatusStagedWarnings:
		return nil
	default:
		return fmt.Errorf(
			"unsupported convoy status %q (expected %q, %q, %q, or %q)",
			status,
			convoyStatusOpen,
			convoyStatusClosed,
			convoyStatusStagedReady,
			convoyStatusStagedWarnings,
		)
	}
}

// isStagedStatus reports whether the given normalized status is a staged status.
func isStagedStatus(status string) bool {
	return strings.HasPrefix(status, "staged_")
}

func validateConvoyStatusTransition(currentStatus, targetStatus string) error {
	current := normalizeConvoyStatus(currentStatus)
	target := normalizeConvoyStatus(targetStatus)

	if err := ensureKnownConvoyStatus(current); err != nil {
		return err
	}
	if err := ensureKnownConvoyStatus(target); err != nil {
		return err
	}
	if current == target {
		return nil
	}

	// Original open ↔ closed transitions.
	if (current == convoyStatusOpen && target == convoyStatusClosed) ||
		(current == convoyStatusClosed && target == convoyStatusOpen) {
		return nil
	}

	// Staged → open (launch) and staged → closed (cancel) are allowed.
	if isStagedStatus(current) && (target == convoyStatusOpen || target == convoyStatusClosed) {
		return nil
	}

	// Staged ↔ staged transitions (re-stage with different result).
	if isStagedStatus(current) && isStagedStatus(target) {
		return nil
	}

	// REJECT: open → staged_* and closed → staged_* are not allowed.
	// (Falls through to the error below.)

	return fmt.Errorf("illegal convoy status transition %q -> %q", currentStatus, targetStatus)
}

var convoyCmd = &cobra.Command{
	Use:         "convoy",
	GroupID:     GroupWork,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Track batches of work across rigs",
	RunE: func(cmd *cobra.Command, args []string) error {
		if convoyInteractive {
			return runConvoyTUI()
		}
		return requireSubcommand(cmd, args)
	},
	Long: `Manage convoys - the primary unit for tracking batched work.

A convoy is a persistent tracking unit that monitors related issues across
rigs. When you kick off work (even a single issue), a convoy tracks it so
you can see when it lands and what was included.

WHAT IS A CONVOY:
  - Persistent tracking unit with an ID (hq-*)
  - Tracks issues across rigs (frontend+backend, beads+gastown, etc.)
  - Auto-closes when all tracked issues complete → notifies subscribers
  - Can be reopened by adding more issues

WHAT IS A SWARM:
  - Ephemeral: "the workers currently assigned to a convoy's issues"
  - No separate ID - uses the convoy ID
  - Dissolves when work completes

TRACKING SEMANTICS:
  - 'tracks' relation is non-blocking (tracked issues don't block convoy)
  - Cross-prefix capable (convoy in hq-* tracks issues in gt-*, bd-*)
  - Landed: all tracked issues closed → notification sent to subscribers

COMMANDS:
  create    Create a convoy tracking specified issues
  add       Add issues to an existing convoy (reopens if closed)
  close     Close a convoy (verifies all items done, or use --force)
  land      Land an owned convoy (cleanup worktrees, close convoy)
  status    Show convoy progress, tracked issues, and active workers
  list      List convoys (the dashboard view)
  watch     Subscribe to convoy completion notifications
  unwatch   Unsubscribe from convoy completion notifications`,
}

var convoyCreateCmd = &cobra.Command{
	Use:   "create <name> [issues...]",
	Short: "Create a new convoy",
	Long: `Create a new convoy that tracks the specified issues.

The convoy is created in town-level beads (hq-* prefix) and can track
issues across any rig.

The --owner flag specifies who requested the convoy (receives completion
notification by default). If not specified, defaults to created_by.
The --notify flag adds additional subscribers beyond the owner.

The --merge flag sets the merge strategy for all work in the convoy:
  direct  Push branch directly to main (no MR, no refinery)
  mr      Create merge-request bead, refinery processes (default)
  local   Keep on feature branch (for upstream PRs, human review)

Examples:
  gt convoy create "Deploy v2.0" gt-abc bd-xyz
  gt convoy create "Release prep" gt-abc --notify           # defaults to mayor/
  gt convoy create "Release prep" gt-abc --notify ops/      # notify ops/
  gt convoy create "Feature rollout" gt-a gt-b --owner mayor/ --notify ops/
  gt convoy create "Feature rollout" gt-a gt-b gt-c --molecule mol-release
  gt convoy create --owned "Manual deploy" gt-abc           # caller-managed lifecycle
  gt convoy create "Quick fix" gt-abc --merge=direct        # bypass refinery

  # Auto-discover issues from an epic's children:
  gt convoy create --from-epic gt-epic-abc
  gt convoy create --from-epic gt-epic-abc --owned --merge=direct`,
	Args:         cobra.ArbitraryArgs,
	SilenceUsage: true,
	RunE:         runConvoyCreate,
}

var convoyStatusCmd = &cobra.Command{
	Use:   "status [convoy-id]",
	Short: "Show convoy status",
	Long: `Show detailed status for a convoy.

Displays convoy metadata, tracked issues, and completion progress.
Without an ID, shows status of all active convoys.`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyStatus,
}

var convoyListCmd = &cobra.Command{
	Use:   "list",
	Short: "List convoys",
	Long: `List convoys, showing open convoys by default.

Examples:
  gt convoy list              # Open convoys only (default)
  gt convoy list --all        # All convoys (open + closed)
  gt convoy list --status=closed  # Recently landed
  gt convoy list --tree       # Show convoy + child status tree
  gt convoy list --json`,
	SilenceUsage: true,
	RunE:         runConvoyList,
}

var convoyAddCmd = &cobra.Command{
	Use:   "add <convoy-id> <issue-id> [issue-id...]",
	Short: "Add issues to an existing convoy",
	Long: `Add issues to an existing convoy.

If the convoy is closed, it will be automatically reopened.

Examples:
  gt convoy add hq-cv-abc gt-new-issue
  gt convoy add hq-cv-abc gt-issue1 gt-issue2 gt-issue3`,
	Args:         cobra.MinimumNArgs(2),
	SilenceUsage: true,
	RunE:         runConvoyAdd,
}

var convoyCheckCmd = &cobra.Command{
	Use:   "check [convoy-id]",
	Short: "Check and auto-close completed convoys",
	Long: `Check convoys and auto-close any where all tracked issues are complete.

Without arguments, checks all open convoys. With a convoy ID, checks only that convoy.

This handles cross-rig convoy completion: convoys in town beads tracking issues
in rig beads won't auto-close via bd close alone. This command bridges that gap.

Can be run manually or by deacon patrol to ensure convoys close promptly.

Examples:
  gt convoy check              # Check all open convoys
  gt convoy check hq-cv-abc    # Check specific convoy
  gt convoy check --dry-run    # Preview what would close without acting`,
	Args:         cobra.MaximumNArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyCheck,
}

var convoyStrandedCmd = &cobra.Command{
	Use:   "stranded",
	Short: "Find stranded convoys (ready work, stuck, or empty) needing attention",
	Long: `Find convoys that have ready issues but no workers processing them,
stuck convoys (tracked issues but none ready), or empty convoys that need cleanup.

A convoy is "stranded" when:
- Convoy is open AND either:
  - Has tracked issues that are ready but unassigned, OR
  - Has tracked issues but none are ready (stuck — waiting on dependencies/workers), OR
  - Has 0 tracked issues (empty — needs auto-close via convoy check)

Use this to detect convoys that need feeding or cleanup. The Deacon patrol
runs this periodically and dispatches dogs to feed stranded convoys.

Examples:
  gt convoy stranded              # Show stranded convoys
  gt convoy stranded --json       # Machine-readable output for automation`,
	SilenceUsage: true,
	RunE:         runConvoyStranded,
}

var convoyCloseCmd = &cobra.Command{
	Use:   "close <convoy-id>",
	Short: "Close a convoy",
	Long: `Close a convoy, optionally with a reason.

By default, verifies that all tracked issues are closed before allowing the
close. Use --force to close regardless of tracked issue status.

The close is idempotent - closing an already-closed convoy is a no-op.

Examples:
  gt convoy close hq-cv-abc                           # Close (all items must be done)
  gt convoy close hq-cv-abc --force                   # Force close abandoned convoy
  gt convoy close hq-cv-abc --reason="no longer needed" --force
  gt convoy close hq-cv-xyz --notify mayor/`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyClose,
}

var convoyLandCmd = &cobra.Command{
	Use:   "land <convoy-id>",
	Short: "Land an owned convoy (cleanup worktrees, close convoy)",
	Long: `Land an owned convoy, performing caller-side cleanup.

This is the caller-managed equivalent of the witness/refinery merge pipeline.
Use this to explicitly land a convoy when you're satisfied with the results.

The command:
  1. Verifies the convoy has the gt:owned label (refuses non-owned convoys)
  2. Checks all tracked issues are done/closed (use --force to override)
  3. Cleans up polecat worktrees associated with the convoy's tracked issues
  4. Closes the convoy bead with reason "Landed by owner"
  5. Sends completion notifications to owner/notify addresses

Use 'gt convoy close' instead for non-owned convoys.

Examples:
  gt convoy land hq-cv-abc                  # Land owned convoy
  gt convoy land hq-cv-abc --force          # Land even with open issues
  gt convoy land hq-cv-abc --keep-worktrees # Skip worktree cleanup
  gt convoy land hq-cv-abc --dry-run        # Preview what would happen`,
	Args:         cobra.ExactArgs(1),
	SilenceUsage: true,
	RunE:         runConvoyLand,
}

func init() {
	// Create flags
	convoyCreateCmd.Flags().StringVar(&convoyMolecule, "molecule", "", "Associated molecule ID")
	convoyCreateCmd.Flags().StringVar(&convoyOwner, "owner", "", "Owner who requested convoy (gets completion notification)")
	convoyCreateCmd.Flags().StringVar(&convoyNotify, "notify", "", "Additional address to notify on completion (default: mayor/ if flag used without value)")
	convoyCreateCmd.Flags().Lookup("notify").NoOptDefVal = "mayor/"
	convoyCreateCmd.Flags().BoolVar(&convoyOwned, "owned", false, "Mark convoy as caller-managed lifecycle (no automatic witness/refinery registration)")
	convoyCreateCmd.Flags().StringVar(&convoyMerge, "merge", "", "Merge strategy: direct (push to main), mr (merge queue, default), local (keep on branch)")
	convoyCreateCmd.Flags().StringVar(&convoyBaseBranch, "base-branch", "", "Target branch for polecats (e.g., 'feat/extraction-review')")
	convoyCreateCmd.Flags().StringVar(&convoyFromEpic, "from-epic", "", "Auto-discover tracked issues from an epic's slingable children")

	// Status flags
	convoyStatusCmd.Flags().BoolVar(&convoyStatusJSON, "json", false, "Output as JSON")

	// List flags
	convoyListCmd.Flags().BoolVar(&convoyListJSON, "json", false, "Output as JSON")
	convoyListCmd.Flags().StringVar(&convoyListStatus, "status", "", "Filter by status (open, closed)")
	convoyListCmd.Flags().BoolVar(&convoyListAll, "all", false, "Show all convoys (open and closed)")
	convoyListCmd.Flags().BoolVar(&convoyListTree, "tree", false, "Show convoy + child status tree")

	// Interactive TUI flag (on parent command)
	convoyCmd.Flags().BoolVarP(&convoyInteractive, "interactive", "i", false, "Interactive tree view")

	// Check flags
	convoyCheckCmd.Flags().BoolVar(&convoyCheckDryRun, "dry-run", false, "Preview what would close without acting")

	// Stranded flags
	convoyStrandedCmd.Flags().BoolVar(&convoyStrandedJSON, "json", false, "Output as JSON")

	// Close flags
	convoyCloseCmd.Flags().StringVar(&convoyCloseReason, "reason", "", "Reason for closing the convoy")
	convoyCloseCmd.Flags().StringVar(&convoyCloseNotify, "notify", "", "Agent to notify on close (e.g., mayor/)")
	convoyCloseCmd.Flags().BoolVarP(&convoyCloseForce, "force", "f", false, "Close even if tracked issues are still open")

	// Land flags
	convoyLandCmd.Flags().BoolVarP(&convoyLandForce, "force", "f", false, "Land even if tracked issues are not all closed")
	convoyLandCmd.Flags().BoolVar(&convoyLandKeep, "keep-worktrees", false, "Skip worktree cleanup")
	convoyLandCmd.Flags().BoolVar(&convoyLandDryRun, "dry-run", false, "Show what would happen without acting")

	// Add subcommands
	convoyCmd.AddCommand(convoyCreateCmd)
	convoyCmd.AddCommand(convoyStatusCmd)
	convoyCmd.AddCommand(convoyListCmd)
	convoyCmd.AddCommand(convoyAddCmd)
	convoyCmd.AddCommand(convoyCheckCmd)
	convoyCmd.AddCommand(convoyStrandedCmd)
	convoyCmd.AddCommand(convoyCloseCmd)
	convoyCmd.AddCommand(convoyLandCmd)
	convoyCmd.AddCommand(convoyStageCmd)
	convoyCmd.AddCommand(convoyLaunchCmd)

	rootCmd.AddCommand(convoyCmd)
}

// getTownBeadsDir returns the town root directory for bd commands.
// Convoy commands run bd from town root (not .beads/) so bd discovers
// the correct database via its own workspace detection.
func getTownBeadsDir() (string, error) {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "", fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	return townRoot, nil
}

// runBdJSON runs a bd command, captures stdout as JSON output. If the command
// fails, the error includes bd's stderr for diagnostics instead of a bare
// "exit status 1". BEADS_DIR is stripped from the subprocess environment to
// prevent stale overrides from interfering with bd's workspace detection.
func runBdJSON(dir string, args ...string) ([]byte, error) {
	return runBdJSONWithOptions(dir, false, false, args...)
}

func runBdJSONAllowStale(dir string, args ...string) ([]byte, error) {
	return runBdJSONWithOptions(dir, true, false, args...)
}

func runBdJSONWithAutoCommit(dir string, args ...string) ([]byte, error) {
	return runBdJSONWithOptions(dir, false, true, args...)
}

func runBdJSONWithOptions(dir string, allowStale, autoCommit bool, args ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	bdc := BdCmd(args...).Dir(dir).StripBeadsDir().Stderr(&stderr)
	if allowStale {
		bdc.AllowStale()
	}
	if autoCommit {
		bdc.WithAutoCommit()
	}
	cmd := bdc.Build()
	cmd.Dir = dir
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		if errMsg := strings.TrimSpace(stderr.String()); errMsg != "" {
			return nil, fmt.Errorf("bd %s: %s", args[0], errMsg)
		}
		return nil, fmt.Errorf("bd %s: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// bdDepListRawIDs queries the raw dependencies table via bd sql to get
// dependency target IDs. Unlike bd dep list, this does NOT join with the
// issues table, so it works for cross-database dependencies where the
// target issues live in a different Dolt database. See GH #2624.
//
// dir should be the town beads directory (.beads) for HQ queries.
// direction is "down" (issue_id → depends_on_id) or "up" (depends_on_id → issue_id).
// depType filters by dependency type (e.g., "tracks", "blocks"); empty means all types.
//
// Returns deduplicated, unwrapped issue IDs (external:prefix:id → id).
func bdDepListRawIDs(dir, issueID, direction, depType string) ([]string, error) {
	// Bead IDs are system-generated alphanumeric strings with hyphens, dots,
	// and underscores — validate to prevent injection before interpolating below.
	if !isValidBeadID(issueID) {
		return nil, fmt.Errorf("invalid bead ID: %q", issueID)
	}

	var parseKey string
	if direction == "up" {
		parseKey = "issue_id"
	} else {
		parseKey = "depends_on_id"
	}
	if depType != "" && !isValidBeadID(depType) {
		return nil, fmt.Errorf("invalid dep type: %q", depType)
	}

	if ids, err := bdDepListRawIDsViaDolt(dir, issueID, direction, depType); err == nil {
		return ids, nil
	}

	var lastErr error
	for _, legacy := range []bool{false, true} {
		query := rawDepSQLLiteral(issueID, direction, depType, legacy)
		out, err := runBdJSONWithAutoCommit(dir, "sql", query, "--json")
		if err != nil {
			lastErr = err
			continue
		}
		ids, err := parseRawDepRows(out, parseKey)
		if err != nil {
			return nil, fmt.Errorf("parsing dep sql for %s: %w", issueID, err)
		}
		return ids, nil
	}
	return nil, fmt.Errorf("bd sql for deps of %s: %w", issueID, lastErr)
}

func bdDepListRawIDsViaDolt(dir, issueID, direction, depType string) ([]string, error) {
	beadsDir := beads.ResolveBeadsDir(dir)
	cfg, ok := readBeadsRuntimeConfig(beadsDir)
	if !ok || cfg.Database == "" || cfg.Port == 0 {
		return nil, fmt.Errorf("missing server metadata for %s", beadsDir)
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	dsn := fmt.Sprintf("root@tcp(%s)/%s?parseTime=true", net.JoinHostPort(host, strconv.Itoa(cfg.Port)), url.PathEscape(cfg.Database))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	typedQuery, typedArgs := rawDepSQLArgs(issueID, direction, depType, false)
	ids, err := queryRawDepIDs(ctx, db, typedQuery, typedArgs)
	if err == nil {
		return ids, nil
	}
	legacyQuery, legacyArgs := rawDepSQLArgs(issueID, direction, depType, true)
	return queryRawDepIDs(ctx, db, legacyQuery, legacyArgs)
}

func rawDepSQLArgs(issueID, direction, depType string, legacy bool) (string, []any) {
	var query string
	var args []any
	if direction == "up" {
		if legacy {
			query = "SELECT issue_id FROM dependencies WHERE depends_on_id = ?"
			args = append(args, issueID)
		} else {
			query = "SELECT issue_id FROM dependencies WHERE (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external LIKE ? ESCAPE '!')"
			args = append(args, issueID, issueID, "%:"+strings.ReplaceAll(issueID, "_", "!_"))
		}
	} else if legacy {
		query = "SELECT depends_on_id FROM dependencies WHERE issue_id = ?"
		args = append(args, issueID)
	} else {
		query = "SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id FROM dependencies WHERE issue_id = ?"
		args = append(args, issueID)
	}
	if depType != "" {
		query += " AND type = ?"
		args = append(args, depType)
	}
	return query, args
}

func rawDepSQLLiteral(issueID, direction, depType string, legacy bool) string {
	query, args := rawDepSQLArgs(issueID, direction, depType, legacy)
	for _, arg := range args {
		query = strings.Replace(query, "?", "'"+arg.(string)+"'", 1)
	}
	return query
}

func queryRawDepIDs(ctx context.Context, db *sql.DB, query string, args []any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var ids []string
	for rows.Next() {
		var rawID sql.NullString
		if err := rows.Scan(&rawID); err != nil {
			return nil, err
		}
		if !rawID.Valid {
			continue
		}
		id := beads.ExtractIssueID(rawID.String)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

func parseRawDepRows(out []byte, parseKey string) ([]string, error) {
	var rows []map[string]string
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(rows))
	var ids []string
	for _, row := range rows {
		id := beads.ExtractIssueID(row[parseKey])
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func sqlExternalDepTargetClause(issueID string) string {
	// Use an escape character that is not valid in bead IDs so underscores stay literal.
	escapedID := strings.ReplaceAll(issueID, "_", "!_")
	return fmt.Sprintf("depends_on_external LIKE '%%:%s' ESCAPE '!'", escapedID)
}

// isValidBeadID checks that a string is safe for SQL interpolation in dep queries.
// Bead IDs contain only alphanumeric chars, hyphens, dots, and underscores.
func isValidBeadID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

// collectEpicChildren does a BFS walk of an epic's parent-child hierarchy and
// returns all slingable leaf descendants (task, bug, feature, chore).
func collectEpicChildren(epicID string) ([]string, error) {
	epic, err := bdShow(epicID)
	if err != nil {
		return nil, fmt.Errorf("epic '%s' not found: %w", epicID, err)
	}
	if epic.IssueType != "epic" {
		return nil, fmt.Errorf("'%s' is not an epic (type: %s); --from-epic only works with epic beads", epicID, epic.IssueType)
	}

	var issueIDs []string
	visited := make(map[string]bool)
	queue := []string{epicID}
	visited[epicID] = true

	for len(queue) > 0 {
		parentID := queue[0]
		queue = queue[1:]

		children, err := bdListChildren(parentID)
		if err != nil {
			style.PrintWarning("couldn't list children of %s: %v", parentID, err)
			continue
		}

		for _, child := range children {
			if visited[child.ID] {
				continue
			}
			visited[child.ID] = true

			if convoyops.IsSlingableType(child.IssueType) {
				issueIDs = append(issueIDs, child.ID)
			} else {
				// Non-slingable types (sub-epics, decisions) — recurse to find slingable descendants
				queue = append(queue, child.ID)
			}
		}
	}

	if len(issueIDs) == 0 {
		return nil, fmt.Errorf("epic '%s' has no slingable children (task, bug, feature, chore)", epicID)
	}
	return issueIDs, nil
}

func runConvoyCreate(cmd *cobra.Command, args []string) error {
	// Validate --merge flag if provided
	if convoyMerge != "" {
		switch convoyMerge {
		case "direct", "mr", "local":
			// Valid
		default:
			return fmt.Errorf("invalid --merge value %q: must be direct, mr, or local", convoyMerge)
		}
	}

	var name string
	var trackedIssues []string

	if convoyFromEpic != "" {
		// --from-epic mode: auto-discover children
		epicIssues, err := collectEpicChildren(convoyFromEpic)
		if err != nil {
			return err
		}
		trackedIssues = epicIssues

		// Use epic title as convoy name unless a name arg was provided
		if len(args) > 0 {
			name = args[0]
		} else {
			if epic, err := bdShow(convoyFromEpic); err == nil {
				name = epic.Title
			} else {
				name = fmt.Sprintf("From epic %s", convoyFromEpic)
			}
		}
	} else {
		// Standard mode: explicit issue list
		if len(args) == 0 {
			return fmt.Errorf("at least one argument is required\nUsage: gt convoy create <name> <issue-id> [issue-id...]\n       gt convoy create --from-epic <epic-id>")
		}
		name = args[0]
		trackedIssues = args[1:]

		// If first arg looks like an issue ID (has beads prefix), treat all args as issues
		// and auto-generate a name from the first issue's title
		if looksLikeIssueID(name) {
			trackedIssues = args
			if details := getIssueDetails(args[0]); details != nil && details.Title != "" {
				name = details.Title
			} else {
				name = fmt.Sprintf("Tracking %s", args[0])
			}
		}

		if len(trackedIssues) == 0 {
			return fmt.Errorf("at least one issue ID is required\nUsage: gt convoy create <name> <issue-id> [issue-id...]")
		}
	}

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Resolve the actual .beads directory (follows redirects) before calling
	// EnsureCustomTypes/Statuses, which expect a .beads path, not a workspace root.
	resolvedBeads := beads.ResolveBeadsDir(townBeads)

	// Ensure custom types (including 'convoy') are registered in town beads.
	// This handles cases where install didn't complete or beads was initialized manually.
	if err := beads.EnsureCustomTypes(resolvedBeads); err != nil {
		return fmt.Errorf("ensuring custom types: %w", err)
	}

	// Ensure custom statuses (staged_ready, staged_warnings) are registered.
	if err := beads.EnsureCustomStatuses(resolvedBeads); err != nil {
		return fmt.Errorf("ensuring custom statuses: %w", err)
	}

	// Create convoy issue in town beads
	description := fmt.Sprintf("Convoy tracking %d issues", len(trackedIssues))

	// Default owner to creator identity if not specified
	owner := convoyOwner
	if owner == "" {
		owner = detectSender()
	}
	convoyFieldValues := &beads.ConvoyFields{
		Owner:      owner,
		Notify:     convoyNotify,
		Merge:      convoyMerge,
		Molecule:   convoyMolecule,
		BaseBranch: convoyBaseBranch,
	}
	description = beads.SetConvoyFields(&beads.Issue{Description: description}, convoyFieldValues)

	// Guard against flag-like convoy names (gt-e0kx5)
	if beads.IsFlagLikeTitle(name) {
		return fmt.Errorf("refusing to create convoy: name %q looks like a CLI flag", name)
	}

	// Generate convoy ID with cv- prefix
	convoyID := fmt.Sprintf("hq-cv-%s", generateShortID())

	createArgs := []string{
		"create",
		"--type=task",
		"--id=" + convoyID,
		"--title=" + name,
		"--description=" + description,
		"--labels=" + convoyLabels(convoyOwned),
		"--json",
	}
	if beads.NeedsForceForID(convoyID) {
		createArgs = append(createArgs, "--force")
	}

	var stderr bytes.Buffer
	if err := BdCmd(createArgs...).
		WithAutoCommit().
		Dir(townBeads).
		Stderr(&stderr).
		Run(); err != nil {
		return fmt.Errorf("creating convoy: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}

	// Notify address is stored in description (line 166-168) and read from there

	// Add 'tracks' relations for each tracked issue
	trackedCount := 0
	for _, issueID := range trackedIssues {
		if err := addTrackingRelationFn(townBeads, convoyID, issueID); err != nil {
			style.PrintWarning("couldn't track %s: %s", issueID, err)
		} else {
			trackedCount++
		}
	}

	// Output
	fmt.Printf("%s Created convoy 🚚 %s\n\n", style.Bold.Render("✓"), convoyID)
	fmt.Printf("  Name:     %s\n", name)
	if convoyFromEpic != "" {
		fmt.Printf("  Epic:     %s\n", convoyFromEpic)
	}
	fmt.Printf("  Tracking: %d issues\n", trackedCount)
	if convoyFromEpic == "" && len(trackedIssues) > 0 {
		fmt.Printf("  Issues:   %s\n", strings.Join(trackedIssues, ", "))
	}
	if owner != "" {
		fmt.Printf("  Owner:    %s\n", owner)
	}
	if convoyNotify != "" {
		fmt.Printf("  Notify:   %s\n", convoyNotify)
	}
	if convoyMerge != "" {
		fmt.Printf("  Merge:    %s\n", convoyMerge)
	}
	if convoyMolecule != "" {
		fmt.Printf("  Molecule: %s\n", convoyMolecule)
	}
	if convoyBaseBranch != "" {
		fmt.Printf("  Base:     %s\n", convoyBaseBranch)
	}
	if convoyOwned {
		fmt.Printf("  Owned:    %s\n", style.Warning.Render("caller-managed lifecycle"))
	}

	if convoyOwned {
		fmt.Printf("\n  %s\n", style.Dim.Render("Owned convoy: caller manages lifecycle via gt convoy land"))
	} else {
		fmt.Printf("\n  %s\n", style.Dim.Render("Convoy auto-closes when all tracked issues complete"))
	}

	return nil
}

func runConvoyAdd(cmd *cobra.Command, args []string) error {
	convoyID := args[0]
	issuesToAdd := args[1:]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// Validate convoy exists and get its status
	showOut, err := BdCmd("show", convoyID, "--json").
		Dir(townBeads).
		Stderr(io.Discard).
		Output()
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(showOut, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Verify it's actually a convoy type
	if !isConvoyIssue(convoy.Type, convoy.Labels) {
		return fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, convoy.Type)
	}
	if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}

	// If convoy is closed, reopen it
	reopened := false
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		// closed→open is always valid; ensureKnownConvoyStatus above guarantees
		// the current status is known, so no additional transition check needed.
		if err := BdCmd("update", convoyID, "--status=open").
			Dir(townBeads).
			WithAutoCommit().
			Run(); err != nil {
			return fmt.Errorf("couldn't reopen convoy: %w", err)
		}
		if fields := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description}); fields != nil && fields.CompletionNotifiedAt != "" {
			fields.CompletionNotifiedAt = ""
			newDesc := beads.SetConvoyFields(&beads.Issue{Description: convoy.Description}, fields)
			if err := BdCmd("update", convoyID, "--description="+newDesc).
				Dir(townBeads).
				WithAutoCommit().
				Run(); err != nil {
				return fmt.Errorf("couldn't clear convoy completion notification state: %w", err)
			}
		}
		if err := persistTownBeadsJSONL(townBeads); err != nil {
			return fmt.Errorf("couldn't persist reopened convoy to JSONL: %w", err)
		}
		reopened = true
		fmt.Printf("%s Reopened convoy %s\n", style.Bold.Render("↺"), convoyID)
	}

	// Add 'tracks' relations for each issue
	addedCount := 0
	for _, issueID := range issuesToAdd {
		if err := addTrackingRelationFn(townBeads, convoyID, issueID); err != nil {
			style.PrintWarning("couldn't add %s: %s", issueID, err)
		} else {
			addedCount++
		}
	}

	// Output
	if reopened {
		fmt.Println()
	}
	fmt.Printf("%s Added %d issue(s) to convoy 🚚 %s\n", style.Bold.Render("✓"), addedCount, convoyID)
	if addedCount > 0 {
		fmt.Printf("  Issues: %s\n", strings.Join(issuesToAdd[:addedCount], ", "))
	}

	return nil
}

func runConvoyCheck(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// If a specific convoy ID is provided, check only that convoy
	if len(args) == 1 {
		convoyID := args[0]
		return checkSingleConvoy(townBeads, convoyID, convoyCheckDryRun)
	}

	// Check all open convoys
	closed, err := checkAndCloseCompletedConvoys(townBeads, convoyCheckDryRun)
	if err != nil {
		return err
	}

	if len(closed) == 0 {
		fmt.Println("No convoys ready to close.")
	} else {
		if convoyCheckDryRun {
			fmt.Printf("%s Would auto-close %d convoy(s):\n", style.Warning.Render("⚠"), len(closed))
		} else {
			fmt.Printf("%s Auto-closed %d convoy(s):\n", style.Bold.Render("✓"), len(closed))
		}
		for _, c := range closed {
			fmt.Printf("  🚚 %s: %s\n", c.ID, c.Title)
		}
	}

	return nil
}

// closeConvoyIfComplete checks whether all tracked issues in a convoy are resolved
// and closes the convoy if so. Returns (true, nil) if the convoy was closed or
// would be closed (dry-run), (false, nil) if not ready, or (false, err) on failure.
func closeConvoyIfComplete(townBeads, convoyID, title string, tracked []trackedIssueInfo, dryRun bool) (bool, error) {
	// If no tracked issues were resolved, skip auto-close. A 0/0 result means
	// cross-rig tracking resolution failed — not that all issues are done.
	// Treating 0/0 as "complete" caused false 🚚 Convoy landed notifications. (GH#3xxx)
	if len(tracked) == 0 {
		return false, nil
	}

	allClosed := true
	openCount := 0
	unknownCount := 0
	for _, t := range tracked {
		switch t.Status {
		case "closed", "tombstone":
			// counted as complete
		case trackedStatusUnknown:
			// Cross-rig DB unreachable — can't verify completion. Leave convoy
			// open, treat as Info (not a convoy-level failure). (gt-bs6)
			allClosed = false
			unknownCount++
		default:
			allClosed = false
			openCount++
		}
	}

	if !allClosed {
		switch {
		case unknownCount > 0 && openCount > 0:
			fmt.Printf("%s Convoy %s has %d open, %d unknown (cross-rig unreachable) issue(s) remaining\n",
				style.Dim.Render("○"), convoyID, openCount, unknownCount)
		case unknownCount > 0:
			fmt.Printf("%s Convoy %s has %d tracked issue(s) with unknown status (cross-rig unreachable)\n",
				style.Dim.Render("○"), convoyID, unknownCount)
		default:
			fmt.Printf("%s Convoy %s has %d open issue(s) remaining\n", style.Dim.Render("○"), convoyID, openCount)
		}
		return false, nil
	}

	if dryRun {
		fmt.Printf("%s Would auto-close convoy 🚚 %s: %s\n", style.Warning.Render("⚠"), convoyID, title)
		return true, nil
	}

	reason := "All tracked issues completed"
	closeArgs := []string{"close", convoyID, "-r", reason}
	if err := runTownMutationAndExport(townBeads, closeArgs...); err != nil {
		return false, fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("%s Auto-closed convoy 🚚 %s: %s\n", style.Bold.Render("✓"), convoyID, title)
	notifyConvoyCompletion(townBeads, convoyID, title)
	return true, nil
}

// checkSingleConvoy checks a specific convoy and closes it if all tracked issues are complete.
func checkSingleConvoy(townBeads, convoyID string, dryRun bool) error {
	stdout, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Verify it's actually a convoy type
	if !isConvoyIssue(convoy.Type, convoy.Labels) {
		return fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, convoy.Type)
	}
	if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}

	// Check if convoy is already closed
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return persistAndNotifyConvoyCompletion(townBeads, convoyID, convoy.Title)
	}

	// Get tracked issues
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("checking convoy %s: %w", convoyID, err)
	}

	_, err = closeConvoyIfComplete(townBeads, convoyID, convoy.Title, tracked, dryRun)
	return err
}

func runConvoyClose(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	stdout, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Description string   `json:"description"`
		Labels      []string `json:"labels"`
	}
	if err := json.Unmarshal(stdout, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Verify it's actually a convoy type
	if !isConvoyIssue(convoy.Type, convoy.Labels) {
		return fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, convoy.Type)
	}
	if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}

	// Idempotent: if already closed, just report it
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return persistAndNotifyConvoyCompletion(townBeads, convoyID, convoy.Title)
	}
	if err := validateConvoyStatusTransition(convoy.Status, convoyStatusClosed); err != nil {
		return fmt.Errorf("can't close convoy '%s': %w", convoyID, err)
	}

	// Verify all tracked issues are done (unless --force)
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		// If we can't check tracked issues, require --force
		if !convoyCloseForce {
			return fmt.Errorf("couldn't verify tracked issues: %w\n  Use --force to close anyway", err)
		}
		style.PrintWarning("couldn't verify tracked issues: %v", err)
	}

	if len(tracked) > 0 && !convoyCloseForce {
		var openIssues []trackedIssueInfo
		for _, t := range tracked {
			if t.Status != "closed" && t.Status != "tombstone" {
				openIssues = append(openIssues, t)
			}
		}

		if len(openIssues) > 0 {
			fmt.Printf("%s Convoy %s has %d open issue(s):\n\n", style.Warning.Render("⚠"), convoyID, len(openIssues))
			for _, t := range openIssues {
				status := "○"
				if t.Status == "in_progress" || t.Status == "hooked" {
					status = "▶"
				}
				fmt.Printf("    %s %s: %s [%s]\n", status, t.ID, t.Title, t.Status)
			}
			fmt.Printf("\n  Use %s to close anyway.\n", style.Bold.Render("--force"))
			return fmt.Errorf("convoy has %d open issue(s)", len(openIssues))
		}
	}

	// Build close reason
	reason := convoyCloseReason
	if reason == "" {
		if convoyCloseForce {
			reason = "Force closed"
		} else {
			reason = "All tracked issues completed"
		}
	}

	// Close the convoy
	closeArgs := []string{"close", convoyID, "-r", reason}
	if err := runTownMutationAndExport(townBeads, closeArgs...); err != nil {
		return fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("%s Closed convoy 🚚 %s: %s\n", style.Bold.Render("✓"), convoyID, convoy.Title)
	if convoyCloseReason != "" {
		fmt.Printf("  Reason: %s\n", convoyCloseReason)
	}

	// Report cleanup summary
	if len(tracked) > 0 {
		closedCount := 0
		openCount := 0
		for _, t := range tracked {
			if t.Status == "closed" || t.Status == "tombstone" {
				closedCount++
			} else {
				openCount++
			}
		}
		fmt.Printf("  Tracked: %d issue(s) (%d closed", len(tracked), closedCount)
		if openCount > 0 {
			fmt.Printf(", %d still open", openCount)
		}
		fmt.Println(")")
	}

	// Report molecule if present
	convoyFields := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description})
	if convoyFields != nil && convoyFields.Molecule != "" {
		fmt.Printf("  Molecule: %s (not auto-detached)\n", convoyFields.Molecule)
	}

	// Send notification if --notify flag provided
	if convoyCloseNotify != "" {
		sendCloseNotification(convoyCloseNotify, convoyID, convoy.Title, reason)
	} else {
		// Check if convoy has a notify address in description
		notifyConvoyCompletion(townBeads, convoyID, convoy.Title)
	}

	return nil
}

func convoyNotifyFrom(convoyID string) string {
	return "convoy/" + convoyID
}

func convoyMailArgs(addr, subject, body, convoyID string) []string {
	return []string{"mail", "send", addr, "-s", subject, "-m", body, "--from", convoyNotifyFrom(convoyID), "--no-notify"}
}

func convoyNudgeEnv(convoyID string) []string {
	env := filterEnvKey(os.Environ(), "GT_ROLE")
	return append(env, "GT_ROLE="+convoyNotifyFrom(convoyID))
}

// sendCloseNotification sends a notification about convoy closure.
func sendCloseNotification(addr, convoyID, title, reason string) {
	subject := fmt.Sprintf("🚚 Convoy closed: %s", title)
	body := fmt.Sprintf("Convoy %s has been closed.\n\nReason: %s", convoyID, reason)

	mailArgs := convoyMailArgs(addr, subject, body, convoyID)
	mailCmd := exec.Command("gt", mailArgs...)
	if err := mailCmd.Run(); err != nil {
		style.PrintWarning("couldn't send notification: %v", err)
	} else {
		fmt.Printf("  Notified: %s\n", addr)
	}
}

func runConvoyLand(cmd *cobra.Command, args []string) error {
	convoyID := args[0]

	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	stdout, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Type        string   `json:"issue_type"`
		Description string   `json:"description"`
		Labels      []string `json:"labels,omitempty"`
	}
	if err := json.Unmarshal(stdout, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Verify it's a convoy type
	if !isConvoyIssue(convoy.Type, convoy.Labels) {
		return fmt.Errorf("'%s' is not a convoy (type: %s)", convoyID, convoy.Type)
	}

	// Verify the convoy is owned
	if !hasLabel(convoy.Labels, "gt:owned") {
		return fmt.Errorf("convoy '%s' is not an owned convoy\n  Only convoys created with --owned can be landed.\n  Use %s instead for non-owned convoys.",
			convoyID, style.Bold.Render("gt convoy close"))
	}

	// Check if already closed
	if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
		return fmt.Errorf("convoy '%s' has invalid lifecycle state: %w", convoyID, err)
	}
	if normalizeConvoyStatus(convoy.Status) == convoyStatusClosed {
		fmt.Printf("%s Convoy %s is already closed\n", style.Dim.Render("○"), convoyID)
		return persistAndNotifyConvoyCompletion(townBeads, convoyID, convoy.Title)
	}

	// Get tracked issues
	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		if !convoyLandForce {
			return fmt.Errorf("couldn't verify tracked issues: %w\n  Use --force to land anyway", err)
		}
		style.PrintWarning("couldn't verify tracked issues: %v", err)
	}

	// Check if all tracked issues are done
	var openIssues []trackedIssueInfo
	for _, t := range tracked {
		if t.Status != "closed" && t.Status != "tombstone" {
			openIssues = append(openIssues, t)
		}
	}

	if len(openIssues) > 0 && !convoyLandForce {
		fmt.Printf("%s Convoy %s has %d open issue(s):\n\n", style.Warning.Render("⚠"), convoyID, len(openIssues))
		for _, t := range openIssues {
			status := "○"
			if t.Status == "in_progress" || t.Status == "hooked" {
				status = "▶"
			}
			fmt.Printf("    %s %s: %s [%s]\n", status, t.ID, t.Title, t.Status)
		}
		fmt.Printf("\n  Use %s to land anyway.\n", style.Bold.Render("--force"))
		return fmt.Errorf("convoy has %d open issue(s)", len(openIssues))
	}

	if convoyLandDryRun {
		fmt.Printf("%s Dry run — would land convoy 🚚 %s: %s\n\n", style.Warning.Render("⚠"), convoyID, convoy.Title)
		fmt.Printf("  Tracked: %d issue(s) (%d closed, %d open)\n", len(tracked), len(tracked)-len(openIssues), len(openIssues))
		if !convoyLandKeep {
			worktrees := findConvoyWorktrees(tracked)
			fmt.Printf("  Worktrees to clean: %d\n", len(worktrees))
			for _, wt := range worktrees {
				fmt.Printf("    • %s (%s)\n", wt.polecatName, wt.rigName)
			}
		} else {
			fmt.Printf("  Worktrees: skipped (--keep-worktrees)\n")
		}
		fmt.Printf("  Close reason: Landed by owner\n")
		return nil
	}

	// Phase 1: Clean up polecat worktrees
	if !convoyLandKeep {
		worktrees := findConvoyWorktrees(tracked)
		if len(worktrees) > 0 {
			fmt.Printf("  Cleaning up %d worktree(s)...\n", len(worktrees))
			for _, wt := range worktrees {
				if err := removePolecatWorktree(wt); err != nil {
					style.PrintWarning("couldn't remove worktree %s/%s: %v", wt.rigName, wt.polecatName, err)
				} else {
					fmt.Printf("    %s %s/%s\n", style.Dim.Render("✓"), wt.rigName, wt.polecatName)
				}
			}
		}
	}

	// Phase 2: Close the convoy
	reason := "Landed by owner"
	closeArgs := []string{"close", convoyID, "-r", reason}
	if err := runTownMutationAndExport(townBeads, closeArgs...); err != nil {
		return fmt.Errorf("closing convoy: %w", err)
	}

	fmt.Printf("\n%s Landed convoy 🚚 %s: %s\n", style.Bold.Render("✓"), convoyID, convoy.Title)
	fmt.Printf("  Reason: %s\n", reason)
	if len(tracked) > 0 {
		closedCount := len(tracked) - len(openIssues)
		fmt.Printf("  Tracked: %d issue(s) (%d closed", len(tracked), closedCount)
		if len(openIssues) > 0 {
			fmt.Printf(", %d still open", len(openIssues))
		}
		fmt.Println(")")
	}

	// Phase 3: Send completion notifications
	notifyConvoyCompletion(townBeads, convoyID, convoy.Title)

	return nil
}

// convoyWorktreeInfo holds info about a polecat worktree to clean up.
type convoyWorktreeInfo struct {
	rigName     string // e.g., "gastown"
	polecatName string // e.g., "rictus"
	townRoot    string // workspace root
}

// findConvoyWorktrees discovers polecat worktrees associated with a convoy's tracked issues.
// It matches tracked issue assignees to polecat worktrees across all rigs.
func findConvoyWorktrees(tracked []trackedIssueInfo) []convoyWorktreeInfo {
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return nil
	}

	rigsConfigPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsConfig, err := config.LoadRigsConfig(rigsConfigPath)
	if err != nil {
		return nil
	}

	// Collect all assignees from tracked issues
	assignees := make(map[string]bool)
	for _, t := range tracked {
		if t.Assignee != "" {
			assignees[t.Assignee] = true
		}
	}

	if len(assignees) == 0 {
		return nil
	}

	var worktrees []convoyWorktreeInfo

	for rigName := range rigsConfig.Rigs {
		rigPath := filepath.Join(townRoot, rigName)
		polecatsDir := filepath.Join(rigPath, "polecats")

		entries, err := os.ReadDir(polecatsDir)
		if err != nil {
			continue
		}

		for _, entry := range entries {
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}

			// Check if this polecat's assignee matches any tracked issue assignee
			// Assignees have format: rig/polecats/name
			polecatAssignee := fmt.Sprintf("%s/polecats/%s", rigName, entry.Name())
			if assignees[polecatAssignee] {
				worktrees = append(worktrees, convoyWorktreeInfo{
					rigName:     rigName,
					polecatName: entry.Name(),
					townRoot:    townRoot,
				})
			}
		}
	}

	return worktrees
}

// removePolecatWorktree removes a polecat worktree via gt polecat remove.
func removePolecatWorktree(wt convoyWorktreeInfo) error {
	// gt polecat remove accepts rig/polecat format
	target := fmt.Sprintf("%s/%s", wt.rigName, wt.polecatName)
	cmd := exec.Command("gt", "polecat", "remove", target, "--force")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		return err
	}
	return nil
}

// strandedConvoyInfo holds info about a stranded convoy.
type strandedConvoyInfo struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	TrackedCount int      `json:"tracked_count"`
	ReadyCount   int      `json:"ready_count"`
	ReadyIssues  []string `json:"ready_issues"`
	CreatedAt    string   `json:"created_at,omitempty"`
	BaseBranch   string   `json:"base_branch,omitempty"`
}

// readyIssueInfo holds info about a ready (stranded) issue.
type readyIssueInfo struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Priority string `json:"priority"`
}

func runConvoyStranded(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	stranded, err := findStrandedConvoys(townBeads)
	if err != nil {
		return err
	}

	if convoyStrandedJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(stranded)
	}

	if len(stranded) == 0 {
		fmt.Println("No stranded convoys found.")
		return nil
	}

	fmt.Printf("%s Found %d stranded convoy(s):\n\n", style.Warning.Render("⚠"), len(stranded))
	for _, s := range stranded {
		fmt.Printf("  🚚 %s: %s\n", s.ID, s.Title)
		if s.ReadyCount == 0 && s.TrackedCount == 0 {
			fmt.Printf("     Empty convoy (0 tracked issues) — needs cleanup\n")
		} else if s.ReadyCount == 0 && s.TrackedCount > 0 {
			fmt.Printf("     %d tracked issues, 0 ready — needs agent review\n", s.TrackedCount)
		} else {
			fmt.Printf("     Ready issues: %d (of %d tracked)\n", s.ReadyCount, s.TrackedCount)
			for _, issueID := range s.ReadyIssues {
				fmt.Printf("       • %s\n", issueID)
			}
		}
		fmt.Println()
	}

	// Separate feed advice, needs-attention convoys, and cleanup advice.
	var feedable, needsAttention, empty []strandedConvoyInfo
	for _, s := range stranded {
		if s.ReadyCount > 0 {
			feedable = append(feedable, s)
		} else if s.TrackedCount > 0 {
			needsAttention = append(needsAttention, s)
		} else {
			empty = append(empty, s)
		}
	}

	if len(feedable) > 0 {
		fmt.Println("To feed stranded convoys, run:")
		for _, s := range feedable {
			fmt.Printf("  gt sling mol-convoy-feed deacon/dogs --var convoy=%s\n", s.ID)
		}
	}
	if len(needsAttention) > 0 {
		if len(feedable) > 0 {
			fmt.Println()
		}
		fmt.Println("Needs agent review (tracked issues exist but none are ready):")
		for _, s := range needsAttention {
			fmt.Printf("  🚚 %s (%d tracked, 0 ready)\n", s.ID, s.TrackedCount)
		}
	}
	if len(empty) > 0 {
		if len(feedable) > 0 || len(needsAttention) > 0 {
			fmt.Println()
		}
		fmt.Println("To close empty convoys, run:")
		for _, s := range empty {
			fmt.Printf("  gt convoy check %s\n", s.ID)
		}
	}
	fmt.Println()
	fmt.Println(style.Dim.Render("  Note: Pool dispatch auto-creates dogs if pool is under capacity."))

	return nil
}

// findStrandedConvoys finds convoys with ready work but no workers,
// or empty convoys (0 tracked issues) that need cleanup.
func findStrandedConvoys(townBeads string) ([]strandedConvoyInfo, error) {
	stranded := []strandedConvoyInfo{} // Initialize as empty slice for proper JSON encoding

	convoys, err := listConvoyIssues(townBeads, "open", false)
	if err != nil {
		return nil, fmt.Errorf("listing convoys: %w", err)
	}

	// Check each convoy for stranded state
	for _, convoy := range convoys {
		// Extract base_branch from convoy description fields
		var baseBranch string
		if cf := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description}); cf != nil {
			baseBranch = cf.BaseBranch
		}

		tracked, err := getTrackedIssues(townBeads, convoy.ID)
		if err != nil {
			// Write to stderr explicitly — stdout may be consumed as JSON
			// by the daemon's JSON parser (fixes #2142).
			fmt.Fprintf(os.Stderr, "⚠ Warning: skipping convoy %s: %v\n", convoy.ID, err)
			continue
		}
		// Empty convoys (0 tracked issues) are stranded — they need
		// attention (auto-close via convoy check or manual cleanup).
		if len(tracked) == 0 {
			stranded = append(stranded, strandedConvoyInfo{
				ID:           convoy.ID,
				Title:        convoy.Title,
				TrackedCount: 0,
				ReadyCount:   0,
				ReadyIssues:  []string{},
				CreatedAt:    convoy.CreatedAt,
				BaseBranch:   baseBranch,
			})
			continue
		}

		// Find ready issues (open, not blocked, no live assignee, slingable).
		// Town-level beads (hq- prefix with path=".") are excluded because
		// they can't be dispatched via gt sling -- they're handled by the deacon.
		// Non-slingable types (epics, convoys, etc.) are also excluded.

		// Batch-check scheduling status for all tracked issues (single DB query).
		var trackedIDs []string
		for _, t := range tracked {
			trackedIDs = append(trackedIDs, t.ID)
		}
		scheduledSet := areScheduled(trackedIDs)

		var readyIssues []string
		for _, t := range tracked {
			if isReadyIssue(t, scheduledSet) {
				if !isSlingableBead(townBeads, t.ID) {
					continue
				}
				if !convoyops.IsSlingableType(t.IssueType) {
					continue
				}
				readyIssues = append(readyIssues, t.ID)
			}
		}

		if len(readyIssues) > 0 {
			stranded = append(stranded, strandedConvoyInfo{
				ID:           convoy.ID,
				Title:        convoy.Title,
				TrackedCount: len(tracked),
				ReadyCount:   len(readyIssues),
				ReadyIssues:  readyIssues,
				CreatedAt:    convoy.CreatedAt,
				BaseBranch:   baseBranch,
			})
		} else {
			// Has tracked issues but none are ready — include in stranded
			// list so callers can distinguish from truly empty convoys.
			stranded = append(stranded, strandedConvoyInfo{
				ID:           convoy.ID,
				Title:        convoy.Title,
				TrackedCount: len(tracked),
				ReadyCount:   0,
				ReadyIssues:  []string{},
				CreatedAt:    convoy.CreatedAt,
				BaseBranch:   baseBranch,
			})
		}
	}

	return stranded, nil
}

// isReadyIssue checks if an issue is ready for dispatch (stranded).
// An issue is ready if:
// - status = "open" AND (no assignee OR assignee session is dead)
// - OR status = "in_progress"/"hooked" AND assignee session is dead (orphaned molecule)
// - AND not blocked (cross-rig-aware from issue details)
// scheduledSet is a pre-computed set of bead IDs with open sling contexts (from areScheduled).
func isReadyIssue(t trackedIssueInfo, scheduledSet map[string]bool) bool {
	status := strings.TrimSpace(t.Status)

	// Unresolved issues are not safe to dispatch.
	if status == "" || status == trackedStatusUnknown {
		return false
	}

	// Closed issues are never ready
	if status == "closed" || status == "tombstone" {
		return false
	}

	// Must not be blocked
	if t.Blocked {
		return false
	}

	// Scheduled beads are not stranded — they're waiting for dispatch capacity.
	if scheduledSet[t.ID] {
		return false
	}

	// Open issues with no assignee are trivially ready
	if status == "open" && t.Assignee == "" {
		return true
	}

	// For issues with an assignee (or non-open status with molecule attached),
	// check if the worker session is still alive
	if t.Assignee == "" {
		// Non-open status but no assignee is an edge case (shouldn't happen
		// normally, but could occur if molecule detached improperly)
		return true
	}

	// Has assignee - check if session is alive
	// Use the shared assigneeToSessionName from rig.go
	sessionName, _ := assigneeToSessionName(t.Assignee)
	if sessionName == "" {
		return true // Can't determine session = treat as ready
	}

	// Check if tmux session exists
	checkCmd := tmux.BuildCommand("has-session", "-t", sessionName)
	if err := checkCmd.Run(); err != nil {
		// Session doesn't exist = orphaned molecule or dead worker
		// This is the key fix: issues with in_progress/hooked status but
		// dead workers are now correctly detected as stranded
		return true
	}

	return false // Session exists = worker is active
}

// isSlingableBead reports whether a bead can be dispatched via gt sling.
// Town-level beads (hq- prefix with path=".") and beads with unknown
// prefixes are not slingable — they're handled by the deacon/mayor.
func isSlingableBead(townRoot, beadID string) bool {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return true // No prefix info, assume slingable
	}
	return beads.GetRigNameForPrefix(townRoot, prefix) != ""
}

// checkAndCloseCompletedConvoys finds open convoys where all tracked issues are closed
// and auto-closes them. Returns the list of convoys that were closed (or would be closed in dry-run mode).
// If dryRun is true, no changes are made and the function returns what would have been closed.
func checkAndCloseCompletedConvoys(townBeads string, dryRun bool) ([]struct{ ID, Title string }, error) {
	var closed []struct{ ID, Title string }

	convoys, err := listConvoyIssues(townBeads, "open", false)
	if err != nil {
		return nil, fmt.Errorf("listing convoys: %w", err)
	}

	// Check each convoy
	for _, convoy := range convoys {
		if err := ensureKnownConvoyStatus(convoy.Status); err != nil {
			style.PrintWarning("skipping convoy %s: invalid lifecycle state: %v", convoy.ID, err)
			continue
		}
		tracked, err := getTrackedIssues(townBeads, convoy.ID)
		if err != nil {
			style.PrintWarning("skipping convoy %s: %v", convoy.ID, err)
			continue
		}
		ready, err := closeConvoyIfComplete(townBeads, convoy.ID, convoy.Title, tracked, dryRun)
		if err != nil {
			style.PrintWarning("couldn't close convoy %s: %v", convoy.ID, err)
			continue
		}
		if ready {
			closed = append(closed, struct{ ID, Title string }{convoy.ID, convoy.Title})
		}
	}

	return closed, nil
}

// persistTownBeadsJSONL writes the current town Beads state to the JSONL file
// used by bd's fallback import path. Convoy close/check suppresses bd's implicit
// auto-export for normal command hygiene, but close state must survive a later
// Dolt rebuild from .beads/issues.jsonl.
func persistTownBeadsJSONL(townBeads string) error {
	beadsDir := beads.ResolveBeadsDir(townBeads)
	if beadsDir == "" {
		return fmt.Errorf("could not resolve town .beads directory")
	}
	issuesPath := filepath.Join(beadsDir, "issues.jsonl")
	return BdCmd("export", "-o", issuesPath).Dir(townBeads).Run()
}

func runTownMutationAndExport(townBeads string, args ...string) error {
	if err := BdCmd(args...).Dir(townBeads).WithAutoCommit().Run(); err != nil {
		return err
	}
	return persistTownBeadsJSONL(townBeads)
}

func persistAndNotifyConvoyCompletion(townBeads, convoyID, title string) error {
	if err := persistTownBeadsJSONL(townBeads); err != nil {
		return fmt.Errorf("persisting convoy close to JSONL: %w", err)
	}
	notifyConvoyCompletion(townBeads, convoyID, title)
	return nil
}

// notifyConvoyCompletion sends notifications to owner, any notify addresses, and mayor/.
func notifyConvoyCompletion(townBeads, convoyID, title string) {
	stdout, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return
	}

	var convoys []struct {
		Description string `json:"description"`
		CreatedAt   string `json:"created_at"`
	}
	if err := json.Unmarshal(stdout, &convoys); err != nil || len(convoys) == 0 {
		return
	}

	// ZFC: Use typed accessor instead of parsing description text
	fields := beads.ParseConvoyFields(&beads.Issue{Description: convoys[0].Description})
	if fields == nil {
		fields = &beads.ConvoyFields{}
	}
	if fields.CompletionNotifiedAt != "" {
		return
	}

	// Compute duration since convoy was created.
	var durationStr string
	if t, err := time.Parse(time.RFC3339, convoys[0].CreatedAt); err == nil {
		d := time.Since(t).Round(time.Minute)
		durationStr = formatWorkerAge(d)
	}

	// Count tracked issues (best-effort; 0 on error is fine for display).
	trackedIDs, _ := bdDepListRawIDs(townBeads, convoyID, "down", "tracks")
	issueCount := len(trackedIDs)

	// Build enriched body for mayor notification.
	mayorBody := fmt.Sprintf("Convoy %s has completed. All tracked issues are now closed.", convoyID)
	if issueCount > 0 || durationStr != "" {
		mayorBody += "\n"
		if issueCount > 0 {
			mayorBody += fmt.Sprintf("\nIssues: %d", issueCount)
		}
		if durationStr != "" {
			mayorBody += fmt.Sprintf("\nDuration: %s", durationStr)
		}
	}

	// Track notified addresses to avoid duplicate mayor/ notification.
	notifiedAddrs := make(map[string]bool)

	for _, addr := range fields.NotificationAddresses() {
		notifiedAddrs[addr] = true
		mailArgs := convoyMailArgs(addr,
			fmt.Sprintf("🚚 Convoy landed: %s", title),
			fmt.Sprintf("Convoy %s has completed.\n\nAll tracked issues are now closed.", convoyID),
			convoyID)
		mailCmd := exec.Command("gt", mailArgs...)
		if err := mailCmd.Run(); err != nil {
			style.PrintWarning("could not notify %s: %v", addr, err)
		}
	}

	// Send nudge notifications to nudge watchers.
	for _, addr := range fields.NudgeNotificationAddresses() {
		nudgeMsg := fmt.Sprintf("🚚 Convoy landed: %s — Convoy %s has completed. All tracked issues are now closed.", title, convoyID)
		nudgeCmd := exec.Command("gt", "nudge", addr, "-m", nudgeMsg)
		nudgeCmd.Env = convoyNudgeEnv(convoyID)
		if err := nudgeCmd.Run(); err != nil {
			style.PrintWarning("could not nudge %s: %v", addr, err)
		}
	}

	// Always notify mayor/ for strategic visibility, unless already notified above.
	if !notifiedAddrs["mayor/"] {
		mailArgs := convoyMailArgs("mayor/", fmt.Sprintf("Convoy complete: %s", title), mayorBody, convoyID)
		mailCmd := exec.Command("gt", mailArgs...)
		if err := mailCmd.Run(); err != nil {
			style.PrintWarning("could not notify mayor/ of convoy completion: %v", err)
		}
	}

	// Push notification to active Mayor session if configured.
	notifyMayorSession(townBeads, convoyID, title)

	fields.CompletionNotifiedAt = time.Now().UTC().Format(time.RFC3339)
	newDesc := beads.SetConvoyFields(&beads.Issue{Description: convoys[0].Description}, fields)
	if err := runTownMutationAndExport(townBeads, "update", convoyID, "--description="+newDesc); err != nil {
		style.PrintWarning("could not record convoy completion notification state for %s: %v", convoyID, err)
		return
	}
}

// notifyMayorSession pushes a convoy completion notification into the active
// Mayor session via nudge, if convoy.notify_on_complete is enabled.
func notifyMayorSession(townBeads, convoyID, title string) {
	settingsPath := config.TownSettingsPath(townBeads)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return
	}
	if settings.Convoy == nil || !settings.Convoy.NotifyOnComplete {
		return
	}

	nudgeMsg := fmt.Sprintf("🚚 Convoy landed: %s — Convoy %s has completed. All tracked issues are now closed.", title, convoyID)
	nudgeCmd := exec.Command("gt", "nudge", "mayor", "-m", nudgeMsg)
	nudgeCmd.Env = convoyNudgeEnv(convoyID)
	if err := nudgeCmd.Run(); err != nil {
		style.PrintWarning("could not nudge Mayor session: %v", err)
	}
}

func runConvoyStatus(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	// If no ID provided, show all active convoys
	if len(args) == 0 {
		return showAllConvoyStatus(townBeads)
	}

	convoyID := args[0]

	// Check if it's a numeric shortcut (e.g., "1" instead of "hq-cv-xyz")
	if n, err := strconv.Atoi(convoyID); err == nil && n > 0 {
		resolved, err := resolveConvoyNumber(townBeads, n)
		if err != nil {
			return err
		}
		convoyID = resolved
	}

	// Get convoy details
	showOut, err := runBdJSON(townBeads, "show", convoyID, "--json")
	if err != nil {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	// Parse convoy data
	var convoys []struct {
		ID          string   `json:"id"`
		Title       string   `json:"title"`
		Status      string   `json:"status"`
		Description string   `json:"description"`
		CreatedAt   string   `json:"created_at"`
		ClosedAt    string   `json:"closed_at,omitempty"`
		DependsOn   []string `json:"depends_on,omitempty"`
		Labels      []string `json:"labels,omitempty"`
	}
	if err := json.Unmarshal(showOut, &convoys); err != nil {
		return fmt.Errorf("parsing convoy data: %w", err)
	}

	if len(convoys) == 0 {
		return fmt.Errorf("convoy '%s' not found", convoyID)
	}

	convoy := convoys[0]

	// Check if convoy is owned (caller-managed lifecycle)
	isOwned := hasLabel(convoy.Labels, "gt:owned")

	tracked, err := getTrackedIssues(townBeads, convoyID)
	if err != nil {
		return fmt.Errorf("getting tracked issues for %s: %w", convoyID, err)
	}

	// Count completed
	completed := 0
	for _, t := range tracked {
		if t.Status == "closed" {
			completed++
		}
	}

	if convoyStatusJSON {
		lifecycle := "system-managed"
		if isOwned {
			lifecycle = "caller-managed"
		}
		type jsonStatus struct {
			ID            string             `json:"id"`
			Title         string             `json:"title"`
			Status        string             `json:"status"`
			Owned         bool               `json:"owned"`
			Lifecycle     string             `json:"lifecycle"`
			MergeStrategy string             `json:"merge_strategy,omitempty"`
			Tracked       []trackedIssueInfo `json:"tracked"`
			Completed     int                `json:"completed"`
			Total         int                `json:"total"`
		}
		out := jsonStatus{
			ID:            convoy.ID,
			Title:         convoy.Title,
			Status:        convoy.Status,
			Owned:         isOwned,
			Lifecycle:     lifecycle,
			MergeStrategy: convoyMergeFromFields(convoy.Description),
			Tracked:       tracked,
			Completed:     completed,
			Total:         len(tracked),
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	// Human-readable output
	fmt.Printf("🚚 %s %s\n\n", style.Bold.Render(convoy.ID+":"), convoy.Title)
	fmt.Printf("  Status:    %s\n", formatConvoyStatus(convoy.Status))
	fmt.Printf("  Owned:     %s\n", formatYesNo(isOwned))
	if isOwned {
		fmt.Printf("  Lifecycle: %s\n", style.Warning.Render("caller-managed"))
	} else {
		fmt.Printf("  Lifecycle: %s\n", "system-managed")
	}
	merge := convoyMergeFromFields(convoy.Description)
	if merge != "" {
		fmt.Printf("  Merge:     %s\n", merge)
	}
	fmt.Printf("  Progress:  %d/%d completed\n", completed, len(tracked))
	fmt.Printf("  Created:   %s\n", convoy.CreatedAt)
	if convoy.ClosedAt != "" {
		fmt.Printf("  Closed:    %s\n", convoy.ClosedAt)
	}

	if len(tracked) > 0 {
		fmt.Printf("\n  %s\n", style.Bold.Render("Tracked Issues:"))
		for _, t := range tracked {
			// Status symbol: ✓ closed, ▶ in_progress/hooked, ? unknown (cross-rig unreachable), ○ other
			status := "○"
			switch t.Status {
			case "closed":
				status = "✓"
			case "in_progress", "hooked":
				status = "▶"
			case trackedStatusUnknown:
				status = "?"
			}

			// Show assignee in brackets (extract short name from path like gastown/polecats/goose -> goose)
			bracketContent := t.IssueType
			if t.Assignee != "" {
				parts := strings.Split(t.Assignee, "/")
				bracketContent = parts[len(parts)-1] // Last part of path
			} else if bracketContent == "" {
				bracketContent = "unassigned"
			}

			line := fmt.Sprintf("    %s %s: %s [%s]", status, t.ID, t.Title, bracketContent)
			if t.Worker != "" {
				workerDisplay := "@" + t.Worker
				if t.WorkerAge != "" {
					workerDisplay += fmt.Sprintf(" (%s)", t.WorkerAge)
				}
				line += fmt.Sprintf("  %s", style.Dim.Render(workerDisplay))
			}
			fmt.Println(line)
		}
	}

	// Hint for owned convoys when all issues are complete
	if isOwned && completed == len(tracked) && len(tracked) > 0 && normalizeConvoyStatus(convoy.Status) == convoyStatusOpen {
		fmt.Printf("\n  %s\n", style.Dim.Render("All issues complete. Land with: gt convoy land "+convoyID))
	}

	return nil
}

func showAllConvoyStatus(townBeads string) error {
	convoys, err := listConvoyIssues(townBeads, "open", false)
	if err != nil {
		return fmt.Errorf("listing convoys: %w", err)
	}

	if len(convoys) == 0 {
		fmt.Println("No active convoys.")
		fmt.Println("Create a convoy with: gt convoy create <name> [issues...]")
		return nil
	}

	if convoyStatusJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(convoys)
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Active Convoys"))
	for _, c := range convoys {
		ownedTag := ""
		if hasLabel(c.Labels, "gt:owned") {
			ownedTag = " " + style.Warning.Render("[owned]")
		}
		fmt.Printf("  🚚 %s: %s%s\n", c.ID, c.Title, ownedTag)
	}
	fmt.Printf("\nUse 'gt convoy status <id>' for detailed status.\n")

	return nil
}

func runConvoyList(cmd *cobra.Command, args []string) error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	convoys, err := listConvoyIssues(townBeads, convoyListStatus, convoyListAll)
	if err != nil {
		return fmt.Errorf("listing convoys: %w", err)
	}

	if convoyListJSON {
		// Enrich each convoy with tracked issues and completion counts
		type convoyListEntry struct {
			ID        string             `json:"id"`
			Title     string             `json:"title"`
			Status    string             `json:"status"`
			CreatedAt string             `json:"created_at"`
			Tracked   []trackedIssueInfo `json:"tracked"`
			Completed int                `json:"completed"`
			Total     int                `json:"total"`
		}
		enriched := make([]convoyListEntry, 0, len(convoys))
		for _, c := range convoys {
			tracked, err := getTrackedIssues(townBeads, c.ID)
			if err != nil {
				style.PrintWarning("skipping convoy %s: %v", c.ID, err)
				continue
			}
			if tracked == nil {
				tracked = []trackedIssueInfo{} // Ensure JSON [] not null
			}
			completed := 0
			for _, t := range tracked {
				if t.Status == "closed" {
					completed++
				}
			}
			enriched = append(enriched, convoyListEntry{
				ID:        c.ID,
				Title:     c.Title,
				Status:    c.Status,
				CreatedAt: c.CreatedAt,
				Tracked:   tracked,
				Completed: completed,
				Total:     len(tracked),
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(enriched)
	}

	if len(convoys) == 0 {
		fmt.Println("No convoys found.")
		fmt.Println("Create a convoy with: gt convoy create <name> [issues...]")
		return nil
	}

	// Tree view: show convoys with their child issues
	if convoyListTree {
		return printConvoyTree(townBeads, convoys)
	}

	fmt.Printf("%s\n\n", style.Bold.Render("Convoys"))
	for i, c := range convoys {
		status := formatConvoyStatus(c.Status)
		ownedTag := ""
		if hasLabel(c.Labels, "gt:owned") {
			ownedTag = " " + style.Warning.Render("[owned]")
		}
		fmt.Printf("  %d. 🚚 %s: %s %s%s\n", i+1, c.ID, c.Title, status, ownedTag)
	}
	fmt.Printf("\nUse 'gt convoy status <id>' or 'gt convoy status <n>' for detailed view.\n")

	return nil
}

// printConvoyTree displays convoys with their child issues in a tree format.
func printConvoyTree(townBeads string, convoys []convoyListIssue) error {
	for _, c := range convoys {
		// Get tracked issues for this convoy
		tracked, err := getTrackedIssues(townBeads, c.ID)
		if err != nil {
			style.PrintWarning("skipping convoy %s: %v", c.ID, err)
			continue
		}

		// Count completed
		completed := 0
		for _, t := range tracked {
			if t.Status == "closed" {
				completed++
			}
		}

		// Print convoy header with progress
		total := len(tracked)
		progress := ""
		if total > 0 {
			progress = fmt.Sprintf(" (%d/%d)", completed, total)
		}
		ownedTag := ""
		if hasLabel(c.Labels, "gt:owned") {
			ownedTag = " " + style.Warning.Render("[owned]")
		}
		fmt.Printf("🚚 %s: %s%s%s\n", c.ID, c.Title, progress, ownedTag)

		// Print tracked issues as tree children
		for i, t := range tracked {
			// Determine tree connector
			isLast := i == len(tracked)-1
			connector := "├──"
			if isLast {
				connector = "└──"
			}

			// Status symbol: ✓ closed, ▶ in_progress/hooked, ○ other
			status := "○"
			switch t.Status {
			case "closed":
				status = "✓"
			case "in_progress", "hooked":
				status = "▶"
			}

			fmt.Printf("%s %s %s: %s\n", connector, status, t.ID, t.Title)
		}

		// Add blank line between convoys
		fmt.Println()
	}

	return nil
}

// hasLabel checks if a label exists in a list of labels.
func hasLabel(labels []string, target string) bool { //nolint:unparam // target is always "gt:owned" today but the API is intentionally general
	for _, l := range labels {
		if l == target {
			return true
		}
	}
	return false
}

type convoyListIssue struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"`
	CreatedAt   string   `json:"created_at"`
	Description string   `json:"description"`
	IssueType   string   `json:"issue_type"`
	Labels      []string `json:"labels"`
}

func isConvoyIssue(issueType string, labels []string) bool {
	return issueType == "convoy" || hasLabel(labels, "gt:convoy")
}

func convoyLabels(owned bool) string {
	if owned {
		return "gt:convoy,gt:owned"
	}
	return "gt:convoy"
}

func listConvoyIssues(townBeads, status string, all bool, extraLabels ...string) ([]convoyListIssue, error) {
	args := []string{"list", "--label=gt:convoy", "--json", "--limit=0"}
	for _, label := range extraLabels {
		args = append(args, "--label="+label)
	}
	if status != "" {
		args = append(args, "--status="+status)
	} else if all {
		args = append(args, "--all")
	}

	args = beads.InjectFlatForListJSON(args)
	convoys, err := readConvoyIssues(townBeads, args...)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(convoys))
	for _, convoy := range convoys {
		seen[convoy.ID] = true
	}

	legacyArgs := []string{"list", "--json", "--limit=0"}
	if status != "" {
		legacyArgs = append(legacyArgs, "--status="+status)
	} else if all {
		legacyArgs = append(legacyArgs, "--all")
	}
	legacyArgs = beads.InjectFlatForListJSON(legacyArgs)
	legacy, err := readConvoyIssues(townBeads, legacyArgs...)
	if err != nil {
		return nil, err
	}
	for _, issue := range legacy {
		if seen[issue.ID] || issue.IssueType != "convoy" || !hasAllLabels(issue.Labels, extraLabels) {
			continue
		}
		convoys = append(convoys, issue)
		seen[issue.ID] = true
	}
	return convoys, nil
}

func readConvoyIssues(townBeads string, args ...string) ([]convoyListIssue, error) {
	out, err := runBdJSON(townBeads, args...)
	if err != nil {
		return nil, err
	}
	var issues []convoyListIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

func hasAllLabels(labels, required []string) bool {
	for _, label := range required {
		if !hasLabel(labels, label) {
			return false
		}
	}
	return true
}

// convoyMergeFromFields extracts the merge strategy from a convoy description
// using the typed ConvoyFields accessor.
// Returns the strategy string ("direct", "mr", "local") or empty string if not set.
func convoyMergeFromFields(description string) string {
	fields := beads.ParseConvoyFields(&beads.Issue{Description: description})
	if fields == nil {
		return ""
	}
	return fields.Merge
}

// formatYesNo returns "yes" or "no" for a boolean value.
func formatYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func formatConvoyStatus(status string) string {
	switch status {
	case "open":
		return style.Warning.Render("●")
	case "closed":
		return style.Success.Render("✓")
	case "in_progress":
		return style.Info.Render("→")
	default:
		return status
	}
}

// trackedIssueInfo holds info about an issue being tracked by a convoy.
type trackedIssueInfo struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Type      string   `json:"dependency_type"`
	IssueType string   `json:"issue_type"`
	Blocked   bool     `json:"blocked,omitempty"`    // True if issue currently has blockers
	Assignee  string   `json:"assignee,omitempty"`   // Assigned agent (e.g., gastown/polecats/goose)
	Labels    []string `json:"labels,omitempty"`     // Bead labels (propagated from trackedDependency)
	Worker    string   `json:"worker,omitempty"`     // Worker currently assigned (e.g., gastown/nux)
	WorkerAge string   `json:"worker_age,omitempty"` // How long worker has been on this issue
}

// trackedDependency is dep-list data enriched with fresh issue details.
type trackedDependency struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Status         string   `json:"status"`
	IssueType      string   `json:"issue_type"`
	Assignee       string   `json:"assignee"`
	DependencyType string   `json:"dependency_type"`
	Labels         []string `json:"labels"`
	Blocked        bool     `json:"-"`
}

func applyFreshIssueDetails(dep *trackedDependency, details *issueDetails) {
	dep.Status = strings.TrimSpace(details.Status)
	if dep.Status == "" {
		dep.Status = trackedStatusUnknown
	}
	dep.Blocked = details.IsBlocked()
	if dep.Title == "" {
		dep.Title = details.Title
	}
	if dep.Assignee == "" {
		dep.Assignee = details.Assignee
	}
	if dep.IssueType == "" {
		dep.IssueType = details.IssueType
	}
	// Always refresh labels unconditionally — bd dep list may return stale
	// labels from dependency records, but bd show returns current bead labels.
	// This ensures isReadyIssue sees accurate queue labels (gt:queued,
	// gt:queue-dispatched) for cross-rig beads. Assigning even when fresh
	// labels are empty clears stale queue labels that would otherwise
	// suppress stranded issue detection.
	dep.Labels = details.Labels
}

// getTrackedIssues gets issues tracked by a convoy with fresh cross-rig details.
// Returns issue details including status, type, and worker info.
//
// Prefers raw SQL query against the dependencies table (bdDepListRawIDs) which
// avoids the JOIN with the issues table that silently drops cross-database
// dependencies (see GH #2624, #2832). Falls back to bd dep list and bd show
// for older bd versions that don't support bd sql.
// Then fetches fresh issue details via bd show with prefix routing.
func getTrackedIssues(townBeads, convoyID string) ([]trackedIssueInfo, error) {
	// Prefer raw SQL — works for cross-database deps where tracked beads
	// live in different Dolt databases. Falls back to bd dep list if bd sql
	// is not available (older bd versions).
	trackedIDs, err := bdDepListRawIDs(townBeads, convoyID, "down", "tracks")
	if err != nil {
		// bd sql not supported (older bd) — fall back to bd dep list.
		trackedIDs, err = bdDepListTracked(townBeads, convoyID)
		if err != nil {
			return nil, fmt.Errorf("querying tracked issues for %s: %w", convoyID, err)
		}
	}

	// Fallback: when dep queries return empty (common for cross-database deps
	// on older bd where the JOIN fails), try parsing from bd show output.
	if len(trackedIDs) == 0 {
		trackedIDs, err = bdShowTrackedDeps(townBeads, convoyID)
		if err != nil {
			return nil, fmt.Errorf("fallback show for tracked deps of %s: %w", convoyID, err)
		}
	}

	if len(trackedIDs) == 0 {
		return nil, nil
	}

	// Fetch fresh issue details via bd show (uses prefix routing for cross-rig).
	freshDetails := getIssueDetailsBatch(trackedIDs)

	// Build tracked dependency structs from fresh details. When fresh details
	// are missing (cross-rig DB unreachable, missing, parked, or unroutable
	// from town root), mark the dep with trackedStatusUnknown so callers can
	// distinguish it from a legitimately open bead. (gt-bs6)
	var deps []trackedDependency
	for _, id := range trackedIDs {
		dep := trackedDependency{
			ID:             id,
			DependencyType: "tracks",
		}
		if details, ok := freshDetails[id]; ok {
			applyFreshIssueDetails(&dep, details)
		} else {
			dep.Status = trackedStatusUnknown
		}
		deps = append(deps, dep)
	}

	// Collect non-closed issue IDs for worker lookup
	openIssueIDs := make([]string, 0, len(deps))
	for _, dep := range deps {
		if dep.Status != "closed" {
			openIssueIDs = append(openIssueIDs, dep.ID)
		}
	}
	workersMap := getWorkersForIssues(openIssueIDs)

	// Build result
	var tracked []trackedIssueInfo
	for _, dep := range deps {
		info := trackedIssueInfo{
			ID:        dep.ID,
			Title:     dep.Title,
			Status:    dep.Status,
			Type:      dep.DependencyType,
			IssueType: dep.IssueType,
			Blocked:   dep.Blocked,
			Assignee:  dep.Assignee,
			Labels:    dep.Labels,
		}

		// Add worker info if available
		if worker, ok := workersMap[dep.ID]; ok {
			info.Worker = worker.Worker
			info.WorkerAge = worker.Age
		}

		tracked = append(tracked, info)
	}

	return tracked, nil
}

// bdDepListTracked runs `bd dep list <convoyID> --direction=down --type=tracks --json`
// and returns the tracked issue IDs (unwrapped from external: prefixes).
// Uses --allow-stale for consistency with sling's other bd calls (verifyBeadExists,
// bdShowBead) — without it, a jsonl write that straddles a second boundary causes
// "database out of sync" errors in CI and fast-turnaround production workflows.
func bdDepListTracked(dir, convoyID string) ([]string, error) {
	out, err := runBdJSONAllowStale(dir, "dep", "list", convoyID, "--direction=down", "--type=tracks", "--json")
	if err != nil {
		return nil, err
	}

	var results []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing dep list for %s: %w", convoyID, err)
	}

	seen := make(map[string]bool, len(results))
	var ids []string
	for _, r := range results {
		id := beads.ExtractIssueID(r.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// bdShowTrackedDeps falls back to `bd show <convoyID> --json` and extracts
// tracked dependency IDs from the convoy's dependencies array.
// This handles cross-database dependencies where bd dep list returns empty.
func bdShowTrackedDeps(dir, convoyID string) ([]string, error) {
	out, err := runBdJSON(dir, "show", convoyID, "--json")
	if err != nil {
		return nil, err
	}

	var results []struct {
		Dependencies []issueDependency `json:"dependencies"`
	}
	if err := json.Unmarshal(out, &results); err != nil {
		return nil, fmt.Errorf("parsing show for %s: %w", convoyID, err)
	}
	if len(results) == 0 {
		return nil, nil
	}

	seen := make(map[string]bool)
	var ids []string
	for _, dep := range results[0].Dependencies {
		if dep.DependencyType != "tracks" {
			continue
		}
		id := beads.ExtractIssueID(dep.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids, nil
}

type issueDependency struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	DependencyType string `json:"dependency_type"`
}

// issueDetails holds basic issue info.
type issueDetails struct {
	ID             string
	Title          string
	Status         string
	IssueType      string
	Assignee       string
	Labels         []string
	BlockedBy      []string
	BlockedByCount int
	Dependencies   []issueDependency
}

func (d issueDetails) IsBlocked() bool {
	if d.BlockedByCount > 0 || len(d.BlockedBy) > 0 {
		return true
	}

	// bd show can omit blocked_by_count; fall back to live dependency edges.
	for _, dep := range d.Dependencies {
		if dep.DependencyType == "blocks" && dep.Status != "closed" && dep.Status != "tombstone" {
			return true
		}
	}

	return false
}

// getIssueDetailsBatch fetches details through the central routed beads lookup.
// Returns a map from issue ID to details. Missing/invalid issues are omitted from the map.
func getIssueDetailsBatch(issueIDs []string) map[string]*issueDetails {
	result := make(map[string]*issueDetails, len(issueIDs))
	if len(issueIDs) == 0 {
		return result
	}

	client := convoyIssueClient()
	if client == nil {
		return result
	}

	issues, err := client.ShowMultiple(issueIDs)
	for id, issue := range issues {
		if details := issueToDetails(issue); details != nil {
			result[id] = details
		}
	}
	if err == nil {
		return result
	}

	// If a grouped batch fails because one ID is missing or stale, keep the
	// previous best-effort behavior and recover any IDs that still resolve.
	for _, id := range issueIDs {
		if result[id] != nil {
			continue
		}
		if details := getIssueDetailsWithClient(client, id); details != nil {
			result[id] = details
		}
	}

	return result
}

// getIssueDetails fetches issue details through the central routed beads lookup.
func getIssueDetails(issueID string) *issueDetails {
	client := convoyIssueClient()
	if client == nil {
		return nil
	}
	return getIssueDetailsWithClient(client, issueID)
}

func convoyIssueClient() *beads.Beads {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return nil
	}
	return beads.New(townRoot)
}

func getIssueDetailsWithClient(client *beads.Beads, issueID string) *issueDetails {
	issue, err := client.Show(issueID)
	if err != nil {
		return nil
	}
	return issueToDetails(issue)
}

func issueToDetails(issue *beads.Issue) *issueDetails {
	if issue == nil {
		return nil
	}

	deps := make([]issueDependency, 0, len(issue.Dependencies))
	for _, dep := range issue.Dependencies {
		deps = append(deps, issueDependency{
			ID:             dep.ID,
			Status:         dep.Status,
			DependencyType: dep.DependencyType,
		})
	}

	return &issueDetails{
		ID:             issue.ID,
		Title:          issue.Title,
		Status:         issue.Status,
		IssueType:      issue.Type,
		Assignee:       issue.Assignee,
		Labels:         issue.Labels,
		BlockedBy:      issue.BlockedBy,
		BlockedByCount: issue.BlockedByCount,
		Dependencies:   deps,
	}
}

// workerInfo holds info about a worker assigned to an issue.
type workerInfo struct {
	Worker string // Agent identity (e.g., gastown/nux)
	Age    string // How long assigned (e.g., "12m")
}

// getWorkersForIssues finds workers currently assigned to the given issues.
// Returns a map from issue ID to worker info.
//
// Optimized to batch queries per rig (O(R) instead of O(N×R)) and
// parallelize across rigs.
func getWorkersForIssues(issueIDs []string) map[string]*workerInfo {
	result := make(map[string]*workerInfo)
	if len(issueIDs) == 0 {
		return result
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		return result
	}

	// Build a set of target issue IDs for fast lookup
	targetIDs := make(map[string]bool, len(issueIDs))
	for _, id := range issueIDs {
		targetIDs[id] = true
	}

	// Discover rigs with beads directories
	rigDirs, _ := filepath.Glob(filepath.Join(townRoot, "*", "polecats"))
	var beadsDirs []string
	for _, polecatsDir := range rigDirs {
		rigDir := filepath.Dir(polecatsDir)
		beadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			beadsDirs = append(beadsDirs, filepath.Join(rigDir, "mayor", "rig"))
		}
	}

	if len(beadsDirs) == 0 {
		return result
	}

	// Query all rigs in parallel using bd list
	type rigResult struct {
		agents []struct {
			ID           string `json:"id"`
			HookBead     string `json:"hook_bead"`
			LastActivity string `json:"last_activity"`
		}
	}

	resultChan := make(chan rigResult, len(beadsDirs))
	var wg sync.WaitGroup

	for _, dir := range beadsDirs {
		wg.Add(1)
		go func(workDir string) {
			defer wg.Done()

			out, err := BdCmd("list", "--label=gt:agent", "--status=open", "--json", "--limit=0", "--flat").
				Dir(workDir).
				StripBeadsDir().
				Stderr(io.Discard).
				Output()
			if err != nil {
				resultChan <- rigResult{}
				return
			}

			var rr rigResult
			if err := json.Unmarshal(out, &rr.agents); err != nil {
				resultChan <- rigResult{}
				return
			}
			resultChan <- rr
		}(dir)
	}

	// Wait for all queries to complete
	go func() {
		wg.Wait()
		close(resultChan)
	}()

	// Collect results from all rigs, filtering by target issue IDs
	for rr := range resultChan {
		for _, agent := range rr.agents {
			// Only include agents working on issues we care about
			if !targetIDs[agent.HookBead] {
				continue
			}

			// Skip if we already found a worker for this issue
			if _, ok := result[agent.HookBead]; ok {
				continue
			}

			// Parse agent ID to get worker identity
			workerID := parseWorkerFromAgentBead(agent.ID)
			if workerID == "" {
				continue
			}

			// Calculate age from last_activity
			age := ""
			if agent.LastActivity != "" {
				if t, err := time.Parse(time.RFC3339, agent.LastActivity); err == nil {
					age = formatWorkerAge(time.Since(t))
				}
			}

			result[agent.HookBead] = &workerInfo{
				Worker: workerID,
				Age:    age,
			}
		}
	}

	return result
}

// parseWorkerFromAgentBead extracts worker identity from agent bead ID.
// Input: "gt-gastown-polecat-nux" -> Output: "gastown/polecat/nux"
// Input: "gt-beads-crew-amber" -> Output: "beads/crew/amber"
func parseWorkerFromAgentBead(agentID string) string {
	rig, role, name, ok := beads.ParseAgentBeadID(agentID)
	if !ok {
		return ""
	}

	// Build path from parsed components
	if rig == "" {
		// Town-level
		if name != "" {
			return role + "/" + name
		}
		return role
	}
	if name != "" {
		return rig + "/" + role + "/" + name
	}
	return rig + "/" + role
}

// formatWorkerAge formats a duration as a short string (e.g., "5m", "2h", "1d")
func formatWorkerAge(d time.Duration) string {
	if d < time.Minute {
		return "<1m"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// runConvoyTUI launches the interactive convoy TUI.
func runConvoyTUI() error {
	townBeads, err := getTownBeadsDir()
	if err != nil {
		return err
	}

	m := convoy.New(townBeads)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// resolveConvoyNumber converts a numeric shortcut (1, 2, 3...) to a convoy ID.
// Numbers correspond to the order shown in 'gt convoy list'.
func resolveConvoyNumber(townBeads string, n int) (string, error) {
	convoys, err := listConvoyIssues(townBeads, "", false)
	if err != nil {
		return "", fmt.Errorf("listing convoys: %w", err)
	}

	if n < 1 || n > len(convoys) {
		return "", fmt.Errorf("convoy %d not found (have %d convoys)", n, len(convoys))
	}

	return convoys[n-1].ID, nil
}
