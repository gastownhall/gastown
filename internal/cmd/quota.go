package cmd

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/quota"
	"github.com/steveyegge/gastown/internal/style"
	ttmux "github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/util"
	"github.com/steveyegge/gastown/internal/workspace"
)

// quotaLogger adapts style.PrintWarning to the quota.Logger interface.
type quotaLogger struct{}

func (quotaLogger) Warn(format string, args ...interface{}) {
	style.PrintWarning(format, args...)
}

// Quota command flags
var (
	quotaJSON   bool
	statusProbe bool
)

var quotaCmd = &cobra.Command{
	Use:     "quota",
	GroupID: GroupServices,
	Short:   "Manage account quota rotation",
	RunE:    requireSubcommand,
	Long: `Manage Claude Code account quota rotation for Gas Town.

When sessions hit rate limits, quota commands help detect blocked sessions
and rotate them to available accounts from the pool.

Commands:
  gt quota status            Show account quota status
  gt quota scan              Detect rate-limited sessions
  gt quota rotate            Swap blocked sessions to available accounts
  gt quota clear             Mark account(s) as available again`,
}

var quotaStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show account quota status",
	Long: `Show the quota status of all registered accounts.

Displays which accounts are available, rate-limited, or in cooldown,
along with timestamps for limit detection and estimated reset times.

Use --probe to actively validate each account by making a lightweight
Claude API call. This detects accounts that are rate-limited or have
time-based restrictions (e.g., not available until 12pm PST) even if
no 429 has been seen yet.

Examples:
  gt quota status           # Text output (passive, fast)
  gt quota status --probe   # Active validation per account
  gt quota status --json    # JSON output`,
	RunE: runQuotaStatus,
}

// QuotaStatusItem represents an account in status output.
type QuotaStatusItem struct {
	Handle     string `json:"handle"`
	Email      string `json:"email"`
	Status     string `json:"status"`
	LimitedAt  string `json:"limited_at,omitempty"`
	ResetsAt   string `json:"resets_at,omitempty"`
	LastUsed   string `json:"last_used,omitempty"`
	IsDefault  bool   `json:"is_default"`
	ProbeError string `json:"probe_error,omitempty"`
}

// probeAccount validates an account using a lightweight HTTP probe against the
// Anthropic API. No LLM inference is performed — the probe sends an intentionally
// invalid request body and interprets the HTTP status code to determine auth and
// rate-limit status.
func probeAccount(handle string, configDir string) (probeErr error, resetsAt string) {
	expandedDir := util.ExpandHome(configDir)
	return quota.ProbeAccountHTTP(expandedDir)
}

func runQuotaStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Load accounts
	accountsPath := constants.MayorAccountsPath(townRoot)
	acctCfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		fmt.Println("No accounts configured.")
		fmt.Println("\nTo add an account:")
		fmt.Println("  gt account add <handle>")
		return nil
	}

	if len(acctCfg.Accounts) == 0 {
		fmt.Println("No accounts configured.")
		return nil
	}

	// Load quota state
	mgr := quota.NewManager(townRoot)
	state, err := mgr.Load()
	if err != nil {
		return fmt.Errorf("loading quota state: %w", err)
	}

	// Ensure all accounts are tracked
	mgr.EnsureAccountsTracked(state, acctCfg.Accounts)

	// Auto-clear accounts whose reset time has passed
	if cleared := mgr.ClearExpired(state); cleared > 0 {
		if err := mgr.Save(state); err != nil {
			style.PrintWarning("could not persist expired account clearance: %v", err)
		}
	}

	// Active probe: validate each "available" account with a real API call
	probeErrors := make(map[string]string) // handle -> error message
	stateChanged := false
	if statusProbe {
		for _, handle := range slices.Sorted(maps.Keys(acctCfg.Accounts)) {
			qs := state.Accounts[handle]
			if qs.Status == config.QuotaStatusLimited {
				continue // Already known limited, skip probe
			}
			acct := acctCfg.Accounts[handle]
			fmt.Fprintf(os.Stderr, "  Probing %s (%s)...\n", handle, acct.Email)
			probeErr, resetsAt := probeAccount(handle, acct.ConfigDir)
			if probeErr != nil {
				probeErrors[handle] = probeErr.Error()
				// Mark as limited in state
				now := time.Now().UTC().Format(time.RFC3339)
				state.Accounts[handle] = config.AccountQuotaState{
					Status:    config.QuotaStatusLimited,
					LimitedAt: now,
					ResetsAt:  resetsAt,
					LastUsed:  qs.LastUsed,
				}
				stateChanged = true
			}
		}
		if stateChanged {
			if err := mgr.Save(state); err != nil {
				style.PrintWarning("could not persist probe results: %v", err)
			}
		}
	}

	if quotaJSON {
		return printQuotaStatusJSON(acctCfg, state, probeErrors)
	}
	return printQuotaStatusText(acctCfg, state, probeErrors)
}

func printQuotaStatusJSON(acctCfg *config.AccountsConfig, state *config.QuotaState, probeErrors map[string]string) error {
	var items []QuotaStatusItem
	for _, handle := range slices.Sorted(maps.Keys(acctCfg.Accounts)) {
		acct := acctCfg.Accounts[handle]
		qs := state.Accounts[handle]
		status := string(qs.Status)
		if status == "" {
			status = string(config.QuotaStatusAvailable)
		}
		item := QuotaStatusItem{
			Handle:    handle,
			Email:     acct.Email,
			Status:    status,
			LimitedAt: qs.LimitedAt,
			ResetsAt:  qs.ResetsAt,
			LastUsed:  qs.LastUsed,
			IsDefault: handle == acctCfg.Default,
		}
		if pe, ok := probeErrors[handle]; ok {
			item.ProbeError = pe
		}
		items = append(items, item)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}

func printQuotaStatusText(acctCfg *config.AccountsConfig, state *config.QuotaState, probeErrors map[string]string) error {
	available := 0
	limited := 0

	fmt.Println(style.Bold.Render("Account Quota Status"))
	fmt.Println()

	for _, handle := range slices.Sorted(maps.Keys(acctCfg.Accounts)) {
		acct := acctCfg.Accounts[handle]
		qs := state.Accounts[handle]
		status := qs.Status
		if status == "" {
			status = config.QuotaStatusAvailable
		}

		// Handle marker and default indicator
		marker := " "
		if handle == acctCfg.Default {
			marker = "*"
		}

		// Status badge
		var badge string
		switch status {
		case config.QuotaStatusAvailable:
			badge = style.Success.Render("available")
			available++
		case config.QuotaStatusLimited:
			badge = style.Error.Render("limited")
			limited++
			if qs.ResetsAt != "" {
				badge += style.Dim.Render(" (resets " + qs.ResetsAt + ")")
			}
		case config.QuotaStatusCooldown:
			badge = style.Warning.Render("cooldown")
			limited++
		default:
			badge = style.Dim.Render("unknown")
		}

		email := ""
		if acct.Email != "" {
			email = style.Dim.Render(" <" + acct.Email + ">")
		}

		fmt.Printf(" %s %-12s %s%s\n", marker, handle, badge, email)
		if pe, ok := probeErrors[handle]; ok {
			fmt.Printf("   %s %s\n", style.Error.Render("probe:"), pe)
		}
	}

	fmt.Println()
	fmt.Printf(" %s %d available, %d limited\n",
		style.Info.Render("Summary:"), available, limited)

	return nil
}

// Scan command flags
var (
	scanUpdate bool
)

var quotaScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Detect rate-limited sessions",
	Long: `Scan all Gas Town tmux sessions for rate-limit indicators.

Captures recent pane output from each session and checks for rate-limit
messages. Reports which sessions are blocked and which account they use.

Use --update to automatically update quota state with detected limits.

Examples:
  gt quota scan              # Report rate-limited sessions
  gt quota scan --update     # Report and update quota state
  gt quota scan --json       # JSON output`,
	RunE: runQuotaScan,
}

func runQuotaScan(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Load accounts config
	accountsPath := constants.MayorAccountsPath(townRoot)
	acctCfg, loadErr := config.LoadAccountsConfig(accountsPath)
	// acctCfg can be nil if no accounts configured — scan still works

	// Create scanner
	t := ttmux.NewTmux()
	scanner, err := quota.NewScanner(t, nil, acctCfg)
	if err != nil {
		return fmt.Errorf("creating scanner: %w", err)
	}

	results, err := scanner.ScanAll()
	if err != nil {
		return fmt.Errorf("scanning sessions: %w", err)
	}

	// Optionally update quota state
	if scanUpdate && loadErr == nil && acctCfg != nil {
		if err := updateQuotaState(townRoot, results, acctCfg); err != nil {
			return fmt.Errorf("updating quota state: %w", err)
		}
	}

	if quotaJSON {
		return printScanJSON(results)
	}
	return printScanText(results)
}

func updateQuotaState(townRoot string, results []quota.ScanResult, acctCfg *config.AccountsConfig) error {
	mgr := quota.NewManager(townRoot)
	return mgr.WithLock(func() error {
		state, err := mgr.Load()
		if err != nil {
			return err
		}
		mgr.EnsureAccountsTracked(state, acctCfg.Accounts)

		now := time.Now().UTC().Format(time.RFC3339)
		for _, r := range results {
			if r.RateLimited && r.AccountHandle != "" {
				existing := state.Accounts[r.AccountHandle]
				state.Accounts[r.AccountHandle] = config.AccountQuotaState{
					Status:    config.QuotaStatusLimited,
					LimitedAt: now,
					ResetsAt:  r.ResetsAt,
					LastUsed:  existing.LastUsed,
				}
			}
		}

		return mgr.SaveUnlocked(state)
	})
}

func printScanJSON(results []quota.ScanResult) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

func printScanText(results []quota.ScanResult) error {
	limited := 0
	nearLimit := 0

	for _, r := range results {
		if r.RateLimited {
			limited++
			account := r.AccountHandle
			if account == "" {
				account = "(unknown)"
			}
			resets := ""
			if r.ResetsAt != "" {
				resets = style.Dim.Render(" resets " + r.ResetsAt)
			}
			fmt.Printf(" %s %-25s %s %s%s\n",
				style.Error.Render("!"),
				r.Session,
				style.Dim.Render("account:"),
				account,
				resets,
			)
		} else if r.NearLimit {
			nearLimit++
			account := r.AccountHandle
			if account == "" {
				account = "(unknown)"
			}
			detail := ""
			if r.MatchedLine != "" {
				detail = style.Dim.Render(fmt.Sprintf(" (%s)", r.MatchedLine))
			}
			fmt.Printf(" %s %-25s %s %s%s\n",
				style.Warning.Render("~"),
				r.Session,
				style.Dim.Render("account:"),
				account,
				detail,
			)
		}
	}

	if limited == 0 && nearLimit == 0 {
		fmt.Printf(" %s No rate-limited sessions detected (%d scanned)\n",
			style.SuccessPrefix, len(results))
	} else {
		fmt.Println()
		parts := []string{}
		if limited > 0 {
			parts = append(parts, fmt.Sprintf("%d limited", limited))
		}
		if nearLimit > 0 {
			parts = append(parts, fmt.Sprintf("%d near-limit", nearLimit))
		}
		fmt.Printf(" %s %s of %d sessions\n",
			style.Warning.Render("Summary:"), strings.Join(parts, ", "), len(results))
	}

	return nil
}

// Rotate command flags
var (
	rotateDryRun bool
	rotateFrom   string
	rotateIdle   bool
)

var quotaRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Swap blocked sessions to available accounts",
	Long: `Rotate rate-limited sessions to available accounts.

Scans all sessions for rate limits, plans account assignments using
least-recently-used ordering, and restarts blocked sessions with fresh accounts.

Use --from to preemptively rotate sessions using a specific account before
it hits its rate limit. This is useful for switching idle sessions while
it's not disruptive.

The rotation process:
  1. Scans all Gas Town sessions for rate-limit indicators
  2. Selects available accounts (LRU order)
  3. Swaps macOS Keychain credentials (same config dir preserved)
  4. Restarts blocked sessions via respawn-pane
  5. Sends /resume to recover conversation context

Examples:
  gt quota rotate                    # Rotate all blocked sessions
  gt quota rotate --from work        # Preemptively rotate sessions on 'work' account
  gt quota rotate --from work --idle # Only rotate idle sessions on 'work' account
  gt quota rotate --dry-run          # Show plan without executing
  gt quota rotate --json             # JSON output`,
	RunE: runQuotaRotate,
}

func runQuotaRotate(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	// Load accounts config (required for rotation)
	accountsPath := constants.MayorAccountsPath(townRoot)
	acctCfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		return fmt.Errorf("no accounts configured (run 'gt account add' first): %w", err)
	}
	if len(acctCfg.Accounts) < 2 {
		return fmt.Errorf("need at least 2 accounts for rotation (have %d)", len(acctCfg.Accounts))
	}

	// Validate --from account if specified
	if rotateFrom != "" {
		if _, ok := acctCfg.Accounts[rotateFrom]; !ok {
			return fmt.Errorf("account %q not found (available: %s)",
				rotateFrom, strings.Join(accountHandles(acctCfg), ", "))
		}
	}

	// Create scanner and plan rotation
	t := ttmux.NewTmux()
	scanner, err := quota.NewScanner(t, nil, acctCfg)
	if err != nil {
		return fmt.Errorf("creating scanner: %w", err)
	}

	mgr := quota.NewManager(townRoot)
	plan, err := quota.PlanRotation(scanner, mgr, acctCfg, quota.PlanOpts{FromAccount: rotateFrom})
	if err != nil {
		return fmt.Errorf("planning rotation: %w", err)
	}

	// NOTE: We intentionally do NOT persist scan-detected rate limits here.
	// Stale sessions (e.g., parked rigs with old rate-limit messages in the
	// pane) would poison the available account pool, blocking rotation of
	// sessions that actually need it. Account state is updated only after
	// successful rotation execution (LastUsed in executeKeychainRotation).

	if len(plan.LimitedSessions) == 0 {
		if quotaJSON {
			return json.NewEncoder(os.Stdout).Encode([]quota.RotateResult{})
		}
		if rotateFrom != "" {
			fmt.Printf(" %s No sessions found using account %q\n", style.SuccessPrefix, rotateFrom)
		} else {
			fmt.Printf(" %s No rate-limited sessions detected\n", style.SuccessPrefix)
		}
		return nil
	}

	if len(plan.Assignments) == 0 {
		if quotaJSON {
			return json.NewEncoder(os.Stdout).Encode([]quota.RotateResult{})
		}
		if rotateFrom != "" {
			fmt.Printf(" %s %d session(s) on %q but no available accounts to rotate to\n",
				style.WarningPrefix, len(plan.LimitedSessions), rotateFrom)
		} else {
			fmt.Printf(" %s %d sessions rate-limited but no available accounts to rotate to\n",
				style.WarningPrefix, len(plan.LimitedSessions))
		}
		if len(plan.SkippedAccounts) > 0 {
			fmt.Println()
			for handle, reason := range plan.SkippedAccounts {
				fmt.Printf(" %s Skipped %s — %s\n", style.WarningPrefix, handle, reason)
			}
		}
		return nil
	}

	// Count unassigned sessions by reason, before idle filtering changes the assignment count.
	// Three reasons a session may not be assigned:
	//   1. No config dir — session has no CLAUDE_CONFIG_DIR and no known account
	//   2. No available accounts — all accounts are limited or consumed
	noConfigDir := 0
	for _, r := range plan.LimitedSessions {
		if _, assigned := plan.Assignments[r.Session]; !assigned {
			if r.AccountHandle == "" && r.ConfigDir == "" {
				noConfigDir++
			}
		}
	}
	unassignable := len(plan.LimitedSessions) - len(plan.Assignments) - noConfigDir

	// Filter to idle sessions only when --idle is set.
	// This avoids interrupting agents that are actively working.
	skippedBusy := 0
	if rotateIdle {
		for session := range plan.Assignments {
			if !t.IsIdle(session) {
				if !quotaJSON {
					fmt.Printf(" %s %-25s %s\n",
						style.Dim.Render("-"), session,
						style.Dim.Render("skipped (busy)"))
				}
				delete(plan.Assignments, session)
				skippedBusy++
			}
		}
		if len(plan.Assignments) == 0 {
			if quotaJSON {
				return json.NewEncoder(os.Stdout).Encode([]quota.RotateResult{})
			}
			fmt.Printf("\n %s No idle sessions to rotate\n", style.WarningPrefix)
			return nil
		}
	}

	// Sort sessions for deterministic output
	sortedSessions := slices.Sorted(maps.Keys(plan.Assignments))

	// Show plan (text only — skip for JSON consumers)
	if !quotaJSON {
		fmt.Println(style.Bold.Render("Rotation Plan"))
		fmt.Println()
		for _, session := range sortedSessions {
			newAccount := plan.Assignments[session]
			var oldAccount string
			for _, r := range plan.LimitedSessions {
				if r.Session == session {
					oldAccount = r.AccountHandle
					break
				}
			}
			if oldAccount == "" {
				oldAccount = "(unknown)"
			}
			fmt.Printf(" %s %-25s %s → %s\n",
				style.ArrowPrefix, session,
				style.Dim.Render(oldAccount),
				style.Success.Render(newAccount),
			)
		}
		if noConfigDir > 0 {
			fmt.Printf("\n %s %d session(s) skipped (no CLAUDE_CONFIG_DIR)\n",
				style.WarningPrefix, noConfigDir)
		}
		if unassignable > 0 {
			fmt.Printf(" %s %d session(s) cannot be rotated (not enough available accounts)\n",
				style.WarningPrefix, unassignable)
		}
		if len(plan.SkippedAccounts) > 0 {
			fmt.Println()
			for handle, reason := range plan.SkippedAccounts {
				acct := acctCfg.Accounts[handle]
				fmt.Printf(" %s Skipped %s — %s\n", style.WarningPrefix, handle, reason)
				fmt.Printf("   Run: claude /login  (in CLAUDE_CONFIG_DIR=%s)\n", acct.ConfigDir)
			}
		}
	}

	if rotateDryRun {
		if quotaJSON {
			// Return plan as JSON for machine consumers
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(plan)
		}
		fmt.Println()
		fmt.Println(style.Dim.Render(" (dry run — no changes made)"))
		return nil
	}

	// Execute rotation with keychain swap deduplication.
	// Track which config dirs have already been swapped so we only do
	// one keychain operation per config dir, not per session.
	if !quotaJSON {
		fmt.Println()
	}
	swappedConfigDirs := make(map[string]*quota.KeychainCredential)
	var results []quota.RotateResult
	for _, session := range sortedSessions {
		newAccount := plan.Assignments[session]
		result := executeKeychainRotation(t, mgr, acctCfg, session, newAccount, swappedConfigDirs)
		results = append(results, result)

		if !quotaJSON {
			if result.Rotated {
				suffix := ""
				if result.ResumedSession != "" {
					suffix = style.Dim.Render(" (resumed)")
				}
				if result.KeychainSwap {
					suffix += style.Dim.Render(" [keychain]")
				}
				fmt.Printf(" %s %s → %s%s\n", style.SuccessPrefix, result.Session, result.NewAccount, suffix)
			} else if result.Error != "" {
				fmt.Printf(" %s %s: %s\n", style.ErrorPrefix, result.Session, result.Error)
			}
		}
	}

	if quotaJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	}

	return nil
}

var quotaClearCmd = &cobra.Command{
	Use:   "clear [handle...]",
	Short: "Mark account(s) as available again",
	Long: `Clear the rate-limited status for one or more accounts, marking them available.

When no handles are specified, all limited accounts are cleared.

Examples:
  gt quota clear              # Clear all limited accounts
  gt quota clear work         # Clear a specific account
  gt quota clear work personal`,
	RunE: runQuotaClear,
}

func runQuotaClear(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	mgr := quota.NewManager(townRoot)

	if len(args) == 0 {
		// Clear all limited accounts
		state, err := mgr.Load()
		if err != nil {
			return fmt.Errorf("loading quota state: %w", err)
		}
		cleared := 0
		for handle, acctState := range state.Accounts {
			if acctState.Status == config.QuotaStatusLimited || acctState.Status == config.QuotaStatusCooldown {
				if err := mgr.MarkAvailable(handle); err != nil {
					return fmt.Errorf("clearing %s: %w", handle, err)
				}
				fmt.Printf(" %s %s → available\n", style.SuccessPrefix, handle)
				cleared++
			}
		}
		if cleared == 0 {
			fmt.Printf(" %s No limited accounts to clear\n", style.SuccessPrefix)
		}
		return nil
	}

	for _, handle := range args {
		if err := mgr.MarkAvailable(handle); err != nil {
			return fmt.Errorf("clearing %s: %w", handle, err)
		}
		fmt.Printf(" %s %s → available\n", style.SuccessPrefix, handle)
	}
	return nil
}

// accountHandles returns sorted account handle names for error messages.
func accountHandles(acctCfg *config.AccountsConfig) []string {
	handles := make([]string, 0, len(acctCfg.Accounts))
	for h := range acctCfg.Accounts {
		handles = append(handles, h)
	}
	slices.Sort(handles)
	return handles
}

// executeKeychainRotation performs context-preserving rotation for a single session.
// Instead of changing CLAUDE_CONFIG_DIR (which destroys context), it swaps the
// macOS Keychain OAuth token from an available account into the rate-limited
// account's keychain entry, then respawns with the SAME config dir so /resume works.
//
// swappedConfigDirs tracks which config dirs have already been swapped in this
// rotation batch — multiple sessions sharing a config dir only need one swap.
func executeKeychainRotation(
	t *ttmux.Tmux,
	mgr *quota.Manager,
	acctCfg *config.AccountsConfig,
	session, newAccount string,
	swappedConfigDirs map[string]*quota.KeychainCredential,
) quota.RotateResult {
	result := quota.RotateResult{
		Session:    session,
		NewAccount: newAccount,
	}

	// Read the session's current CLAUDE_CONFIG_DIR, falling back to ~/.claude
	currentConfigDir, err := t.GetEnvironment(session, "CLAUDE_CONFIG_DIR")
	if err != nil || strings.TrimSpace(currentConfigDir) == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			result.Error = fmt.Sprintf("reading CLAUDE_CONFIG_DIR: %v", err)
			return result
		}
		currentConfigDir = home + "/.claude"
	}

	// Resolve old account handle
	for handle, acct := range acctCfg.Accounts {
		if acct.ConfigDir == currentConfigDir || util.ExpandHome(acct.ConfigDir) == currentConfigDir {
			result.OldAccount = handle
			break
		}
	}

	// Get the source (new account) config dir — this is where the fresh token lives
	newAcct, ok := acctCfg.Accounts[newAccount]
	if !ok {
		result.Error = fmt.Sprintf("account %q not found in config", newAccount)
		return result
	}
	sourceConfigDir := util.ExpandHome(newAcct.ConfigDir)

	// Swap keychain credential AND oauthAccount identity (deduplicated per config dir)
	if _, alreadySwapped := swappedConfigDirs[currentConfigDir]; !alreadySwapped {
		backup, err := quota.SwapKeychainCredential(currentConfigDir, sourceConfigDir)
		if err != nil {
			result.Error = fmt.Sprintf("keychain swap failed: %v", err)
			return result
		}
		swappedConfigDirs[currentConfigDir] = backup

		// Also swap the oauthAccount in .claude.json so Claude Code identifies
		// as the new account (correct accountUuid/organizationUuid for rate limits).
		if _, err := quota.SwapOAuthAccount(currentConfigDir, sourceConfigDir); err != nil {
			style.PrintWarning("could not swap oauthAccount for %s: %v", session, err)
		}

		result.KeychainSwap = true
	}

	// Build restart command with --continue to resume previous conversation.
	// ContinueSession omits the beacon prompt and adds --continue, so the
	// agent silently resumes where it left off without a fresh handoff cycle.
	restartCmd, err := buildRestartCommandWithOpts(session, buildRestartCommandOpts{
		ContinueSession: true,
	})
	if err != nil {
		// Session types that can't be restarted (e.g., hq-boot/deacon) still
		// benefit from the keychain swap above — mark as rotated without restart.
		result.Rotated = true
		result.Error = fmt.Sprintf("keychain swapped but could not restart: %v", err)
		return result
	}

	// Keep the SAME config dir — this is what makes /resume work.
	// The keychain swap already replaced the auth token in this dir's keychain entry.
	// Set GT_QUOTA_ACCOUNT so the scanner knows which account's token is actually active
	// (the config dir still maps to the old account).
	restartCmd = config.PrependEnv(restartCmd, map[string]string{
		"CLAUDE_CONFIG_DIR": currentConfigDir,
		"GT_QUOTA_ACCOUNT":  newAccount,
	})

	// Get target pane
	pane, err := t.GetPaneID(session)
	if err != nil {
		result.Error = fmt.Sprintf("getting pane: %v", err)
		return result
	}

	// Set remain-on-exit to prevent pane destruction during restart
	if err := t.SetRemainOnExit(pane, true); err != nil {
		style.PrintWarning("could not set remain-on-exit for %s: %v", session, err)
	}

	// Kill existing processes
	if err := t.KillPaneProcesses(pane); err != nil {
		style.PrintWarning("could not kill pane processes for %s: %v", session, err)
	}

	// Clear scrollback
	if err := t.ClearHistory(pane); err != nil {
		style.PrintWarning("could not clear history for %s: %v", session, err)
	}

	// Respawn with same config dir (fresh token already in keychain)
	if err := t.RespawnPane(pane, restartCmd); err != nil {
		result.Error = fmt.Sprintf("respawning pane: %v", err)
		return result
	}

	// Set GT_QUOTA_ACCOUNT in the tmux session environment so the scanner
	// can resolve the active account. The shell export in restartCmd only
	// affects the process env; this sets it where GetEnvironment reads it.
	if err := t.SetEnvironment(session, "GT_QUOTA_ACCOUNT", newAccount); err != nil {
		style.PrintWarning("could not set GT_QUOTA_ACCOUNT for %s: %v", session, err)
	}

	// Context recovery is handled by --continue in the restart command.
	result.ResumedSession = "continue"

	// Update quota state: mark account as used and record swap mapping
	if err := mgr.WithLock(func() error {
		state, loadErr := mgr.Load()
		if loadErr != nil {
			return loadErr
		}
		existing := state.Accounts[newAccount]
		existing.LastUsed = time.Now().UTC().Format(time.RFC3339)
		state.Accounts[newAccount] = existing

		// Record the swap mapping so SyncSwappedTokens can propagate
		// fresh tokens if the source account re-authenticates later.
		quota.RecordSwap(state, currentConfigDir, newAccount)

		return mgr.SaveUnlocked(state)
	}); err != nil {
		style.PrintWarning("could not update LastUsed for %s: %v", newAccount, err)
	}

	result.Rotated = true
	return result
}



// Watch command flags
var (
	watchInterval time.Duration
	watchDryRun   bool
)

var quotaWatchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Monitor sessions and rotate proactively before hard 429",
	Long: `Continuously monitor sessions for approaching rate limits and rotate proactively.

Polls all Gas Town sessions on the specified interval, checking for both
hard rate limits and near-limit warning signals via pane pattern matching.

When a session is detected as approaching its limit, rotation is triggered
before the hard 429 hits.

Examples:
  gt quota watch                      # Watch with default 5m interval
  gt quota watch --interval 2m        # Custom interval
  gt quota watch --dry-run            # Show detections without rotating`,
	RunE: runQuotaWatch,
}

func runQuotaWatch(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}

	accountsPath := constants.MayorAccountsPath(townRoot)
	acctCfg, err := config.LoadAccountsConfig(accountsPath)
	if err != nil {
		return fmt.Errorf("no accounts configured: %w", err)
	}
	if len(acctCfg.Accounts) < 2 {
		return fmt.Errorf("need at least 2 accounts for rotation (have %d)", len(acctCfg.Accounts))
	}

	fmt.Printf(" %s Watching for near-limit signals (interval: %s)\n",
		style.Info.Render("Watch:"), watchInterval)
	if watchDryRun {
		fmt.Println(style.Dim.Render(" (dry run — detections only, no rotation)"))
	}
	fmt.Println()

	// Handle graceful shutdown on SIGTERM/SIGINT
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	ticker := time.NewTicker(watchInterval)
	defer ticker.Stop()

	// Run immediately on start, then on each tick
	for {
		runWatchCycle(townRoot, acctCfg)

		select {
		case <-sigCh:
			fmt.Printf("\n %s Shutting down watch\n", style.Info.Render("Watch:"))
			return nil
		case <-ticker.C:
		}
	}
}

func runWatchCycle(townRoot string, acctCfg *config.AccountsConfig) {
	t := ttmux.NewTmux()
	scanner, err := quota.NewScanner(t, nil, acctCfg)
	if err != nil {
		style.PrintWarning("creating scanner: %v", err)
		return
	}

	// Enable near-limit detection via pane patterns
	if err := scanner.WithWarningPatterns(nil); err != nil {
		style.PrintWarning("setting warning patterns: %v", err)
		return
	}

	mgr := quota.NewManager(townRoot)

	// Sync swapped tokens: if a source account re-authenticated since the
	// last rotation, propagate the fresh token to all target keychain entries.
	if state, err := mgr.Load(); err == nil && len(state.ActiveSwaps) > 0 {
		resolved := quota.ResolveSwapSourceDirs(state.ActiveSwaps, acctCfg.Accounts)
		if n := quota.SyncSwappedTokens(resolved); n > 0 {
			now := time.Now().Format("15:04:05")
			fmt.Printf(" [%s] %s synced %d swapped keychain(s)\n",
				style.Dim.Render(now),
				style.Info.Render("Sync:"),
				n)
		}
	}

	plan, err := quota.PlanRotation(scanner, mgr, acctCfg, quota.PlanOpts{IncludeNearLimit: true})
	if err != nil {
		style.PrintWarning("planning rotation: %v", err)
		return
	}

	// Report findings
	now := time.Now().Format("15:04:05")
	totalTargets := len(plan.LimitedSessions) + len(plan.NearLimitSessions)
	if totalTargets == 0 {
		fmt.Printf(" [%s] %s\n", style.Dim.Render(now), style.Dim.Render("all clear"))
		return
	}

	for _, r := range plan.LimitedSessions {
		fmt.Printf(" [%s] %s %-25s %s\n",
			style.Dim.Render(now),
			style.Error.Render("LIMITED"),
			r.Session,
			style.Dim.Render(r.AccountHandle))
	}
	for _, r := range plan.NearLimitSessions {
		detail := ""
		if r.MatchedLine != "" {
			detail = fmt.Sprintf(" (%s)", r.MatchedLine)
		}
		fmt.Printf(" [%s] %s %-25s %s%s\n",
			style.Dim.Render(now),
			style.Warning.Render("NEAR"),
			r.Session,
			style.Dim.Render(r.AccountHandle),
			style.Dim.Render(detail))
	}

	if watchDryRun || len(plan.Assignments) == 0 {
		return
	}

	// Execute rotation
	swappedConfigDirs := make(map[string]*quota.KeychainCredential)
	for _, session := range slices.Sorted(maps.Keys(plan.Assignments)) {
		newAccount := plan.Assignments[session]
		result := executeKeychainRotation(t, mgr, acctCfg, session, newAccount, swappedConfigDirs)
		if result.Rotated {
			fmt.Printf(" [%s] %s %s → %s\n",
				style.Dim.Render(now),
				style.SuccessPrefix,
				result.Session,
				style.Success.Render(result.NewAccount))
		} else if result.Error != "" {
			fmt.Printf(" [%s] %s %s: %s\n",
				style.Dim.Render(now),
				style.ErrorPrefix,
				result.Session,
				result.Error)
		}
	}
}

func init() {
	quotaStatusCmd.Flags().BoolVar(&quotaJSON, "json", false, "Output as JSON")
	quotaStatusCmd.Flags().BoolVar(&statusProbe, "probe", false, "Actively validate each account with a lightweight API call")

	quotaScanCmd.Flags().BoolVar(&quotaJSON, "json", false, "Output as JSON")
	quotaScanCmd.Flags().BoolVar(&scanUpdate, "update", false, "Update quota state with detected limits")

	quotaRotateCmd.Flags().BoolVar(&rotateDryRun, "dry-run", false, "Show plan without executing")
	quotaRotateCmd.Flags().BoolVar(&quotaJSON, "json", false, "Output as JSON")
	quotaRotateCmd.Flags().StringVar(&rotateFrom, "from", "", "Preemptively rotate sessions using this account")
	quotaRotateCmd.Flags().BoolVar(&rotateIdle, "idle", false, "Only rotate sessions at the idle prompt (skip busy agents)")

	quotaWatchCmd.Flags().DurationVar(&watchInterval, "interval", 5*time.Minute, "Poll interval")
	quotaWatchCmd.Flags().BoolVar(&watchDryRun, "dry-run", false, "Show detections without executing rotation")

	quotaCmd.AddCommand(quotaStatusCmd)
	quotaCmd.AddCommand(quotaScanCmd)
	quotaCmd.AddCommand(quotaRotateCmd)
	quotaCmd.AddCommand(quotaClearCmd)
	quotaCmd.AddCommand(quotaWatchCmd)

	rootCmd.AddCommand(quotaCmd)
}
