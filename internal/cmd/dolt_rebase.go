package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"strconv"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var (
	doltRebaseConfirm    bool
	doltRebaseKeepRecent int
	doltRebaseDryRun     bool
)

var doltRebaseCmd = &cobra.Command{
	Use:   "rebase <database>",
	Short: "Surgical compaction: squash old commits, keep recent ones",
	Long: `Surgically compact a Dolt database using interactive rebase.

Unlike 'gt dolt flatten' (which destroys ALL history), surgical rebase
keeps recent commits individual while squashing old history into one.

Algorithm (based on Dolt's DOLT_REBASE):
  1. Creates anchor branch at root commit
  2. Creates work branch from main
  3. Starts interactive rebase — populates dolt_rebase table
  4. Marks old commits as 'squash', keeps recent N as 'pick'
  5. Executes the rebase plan
  6. Swaps branches: work becomes the new main
  7. Cleans up temporary branches
  8. Runs GC to reclaim space

WARNING: DOLT_REBASE is NOT safe with concurrent writes. If agents are
actively committing to this database, the rebase may fail with a graph-change
error. The Compactor Dog (daemon) has automatic retry logic for this case.
For manual use, re-run the command if it fails due to concurrent writes.
Flatten mode (gt dolt flatten) is safe with concurrent writes.

Use --keep-recent to control how many recent commits to preserve.
Use --dry-run to see the plan without executing it.

Requires --yes-i-am-sure flag as safety interlock.`,
	Args: cobra.ExactArgs(1),
	RunE: runDoltRebase,
}

func init() {
	doltRebaseCmd.Flags().BoolVar(&doltRebaseConfirm, "yes-i-am-sure", false,
		"Required safety flag to confirm compaction")
	doltRebaseCmd.Flags().IntVar(&doltRebaseKeepRecent, "keep-recent", 50,
		"Number of recent commits to keep as individual picks")
	doltRebaseCmd.Flags().BoolVar(&doltRebaseDryRun, "dry-run", false,
		"Show the rebase plan without executing it")
	doltCmd.AddCommand(doltRebaseCmd)
}

func runDoltRebase(cmd *cobra.Command, args []string) error {
	dbName := args[0]

	if !doltRebaseConfirm && !doltRebaseDryRun {
		return fmt.Errorf("this command rewrites commit history. Pass --yes-i-am-sure to proceed (or --dry-run to preview)")
	}

	if doltRebaseKeepRecent < 0 {
		return fmt.Errorf("--keep-recent must be non-negative (got %d)", doltRebaseKeepRecent)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}
	config := doltserver.DefaultConfig(townRoot)
	if config.IsExternallyManaged() {
		return fmt.Errorf("Dolt server is externally managed (%s) — rebase requires a Town-owned server", config.HostPort())
	}

	running, _, err := doltserver.IsRunning(townRoot)
	if err != nil || !running {
		return fmt.Errorf("Dolt server is not running — start with 'gt dolt start'")
	}

	// wa-d6f: socket-first DSN (TCP fallback) — eliminates TIME_WAIT churn.
	dsn := buildDoltDSNFromConfig(config, dbName, dsnOpts{
		ParseTime:    true,
		Timeout:      "5s",
		ReadTimeout:  "60s",
		WriteTimeout: "300s",
	})

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("connecting to database %s: %w", dbName, err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// Verify database exists.
	var dummy int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&dummy); err != nil {
		return fmt.Errorf("database %q not reachable: %w", dbName, err)
	}

	fmt.Printf("%s Pre-flight checks for %s (surgical rebase)\n", style.Bold.Render("●"), style.Bold.Render(dbName))

	// Count commits.
	var commitCount int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_log", dbName)).Scan(&commitCount); err != nil {
		return fmt.Errorf("counting commits: %w", err)
	}
	fmt.Printf("  Commits: %d\n", commitCount)
	fmt.Printf("  Keep recent: %d\n", doltRebaseKeepRecent)

	// Need at least 3 commits: root (pick) + at least 1 to squash + 1 to keep.
	minCommits := doltRebaseKeepRecent + 2
	if commitCount < minCommits {
		fmt.Printf("  %s Too few commits (%d) for surgical rebase with --keep-recent=%d (need at least %d)\n",
			style.Bold.Render("✓"), commitCount, doltRebaseKeepRecent, minCommits)
		return nil
	}

	// Record pre-flight row counts.
	preCounts, err := flattenGetRowCounts(db, dbName)
	if err != nil {
		return fmt.Errorf("recording row counts: %w", err)
	}
	fmt.Printf("  Tables: %d\n", len(preCounts))
	for table, count := range preCounts {
		fmt.Printf("    %s: %d rows\n", table, count)
	}

	// Get HEAD hash for concurrency check.
	preHead, err := flattenGetHead(db, dbName)
	if err != nil {
		return fmt.Errorf("getting HEAD: %w", err)
	}
	fmt.Printf("  HEAD: %s\n", preHead[:12])

	// Get root commit.
	var rootHash string
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT commit_hash FROM `%s`.dolt_log ORDER BY date ASC LIMIT 1", dbName)).Scan(&rootHash); err != nil {
		return fmt.Errorf("finding root commit: %w", err)
	}
	fmt.Printf("  Root: %s\n", rootHash[:12])

	// USE database for all subsequent operations.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("USE `%s`", dbName)); err != nil {
		return fmt.Errorf("use database: %w", err)
	}

	const baseBranch = "compact-base"
	const workBranch = "compact-work"

	// Clean up any leftover branches from a previous failed run.
	rebaseCleanup(db, baseBranch, workBranch)

	fmt.Printf("\n%s Starting surgical rebase...\n", style.Bold.Render("●"))

	// Step 1: Create anchor branch at root commit.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('%s', '%s')", baseBranch, rootHash)); err != nil {
		return fmt.Errorf("create base branch at root: %w", err)
	}
	fmt.Printf("  Created %s at root %s\n", baseBranch, rootHash[:12])

	// Step 2: Create work branch from main.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('%s', 'main')", workBranch)); err != nil {
		rebaseCleanupBase(db, baseBranch)
		return fmt.Errorf("create work branch from main: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_CHECKOUT('%s')", workBranch)); err != nil {
		rebaseCleanupAll(db, baseBranch, workBranch)
		return fmt.Errorf("checkout work branch: %w", err)
	}
	fmt.Printf("  Created %s from main, checked out\n", workBranch)

	// Step 3: Start interactive rebase — populates dolt_rebase system table.
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_REBASE('--interactive', '%s')", baseBranch)); err != nil {
		rebaseCleanupAll(db, baseBranch, workBranch)
		return fmt.Errorf("start interactive rebase: %w", err)
	}
	fmt.Printf("  Interactive rebase started (dolt_rebase table populated)\n")

	// Step 4: Inspect the rebase plan.
	var totalPlan int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM dolt_rebase").Scan(&totalPlan); err != nil {
		rebaseAbortAndCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("counting rebase entries: %w", err)
	}
	fmt.Printf("  Rebase plan: %d commits\n", totalPlan)

	// Calculate how many to squash: everything except first (must stay pick) and last N.
	// Dolt returns rebase_order as DECIMAL — the MySQL driver delivers it as
	// []uint8 (e.g. "1.00") which cannot be scanned directly into int.
	// Scan as string, parse to float, then truncate to int.
	var minOrderStr, maxOrderStr string
	if err := db.QueryRowContext(ctx, "SELECT MIN(rebase_order), MAX(rebase_order) FROM dolt_rebase").Scan(&minOrderStr, &maxOrderStr); err != nil {
		rebaseAbortAndCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("getting rebase order range: %w", err)
	}
	minOrder, maxOrder, err := parseRebaseOrderRange(minOrderStr, maxOrderStr)
	if err != nil {
		rebaseAbortAndCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("parsing rebase order range: %w", err)
	}

	squashThreshold := maxOrder - doltRebaseKeepRecent
	toSquash := 0
	if squashThreshold > minOrder {
		toSquash = squashThreshold - minOrder
	}

	fmt.Printf("  Squashing: %d old commits (keeping first as pick + last %d)\n",
		toSquash, doltRebaseKeepRecent)

	if toSquash == 0 {
		fmt.Printf("  %s Nothing to squash — all commits are recent\n", style.Bold.Render("✓"))
		rebaseAbortAndCleanup(db, baseBranch, workBranch)
		return nil
	}

	if doltRebaseDryRun {
		// Show the plan.
		fmt.Printf("\n%s Dry-run rebase plan:\n", style.Bold.Render("●"))
		rows, err := db.QueryContext(ctx, "SELECT rebase_order, action, commit_hash, commit_message FROM dolt_rebase ORDER BY rebase_order")
		if err != nil {
			rebaseAbortAndCleanup(db, baseBranch, workBranch)
			return fmt.Errorf("reading rebase plan: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var orderStr string
			var action, hash, msg string
			if err := rows.Scan(&orderStr, &action, &hash, &msg); err != nil {
				continue
			}
			order, err := parseRebaseOrder(orderStr)
			if err != nil {
				continue
			}
			marker := "pick"
			if order > minOrder && order <= squashThreshold {
				marker = "squash"
			}
			if len(msg) > 60 {
				msg = msg[:60] + "..."
			}
			if len(hash) > 8 {
				hash = hash[:8]
			}
			fmt.Printf("  %3d  %-7s  %s  %s\n", order, marker, hash, msg)
		}

		fmt.Printf("\n  Would squash %d commits, keep %d recent + 1 root pick\n",
			toSquash, doltRebaseKeepRecent)
		rebaseAbortAndCleanup(db, baseBranch, workBranch)
		return nil
	}

	// Step 5: Modify the plan — mark old commits as squash.
	// First commit (minOrder) MUST stay 'pick' — squash needs a parent to fold into.
	// Keep last N commits as 'pick'.
	result, err := db.ExecContext(ctx, fmt.Sprintf(
		"UPDATE dolt_rebase SET action = 'squash' WHERE rebase_order > %d AND rebase_order <= %d",
		minOrder, squashThreshold))
	if err != nil {
		rebaseAbortAndCleanup(db, baseBranch, workBranch)
		return fmt.Errorf("updating rebase plan: %w", err)
	}
	affected, _ := result.RowsAffected()
	fmt.Printf("  Marked %d commits as squash\n", affected)

	// Step 6: Execute the rebase plan.
	fmt.Printf("  Executing rebase (this may take a while)...\n")
	if _, err := db.ExecContext(ctx, "CALL DOLT_REBASE('--continue')"); err != nil {
		// Rebase failed — conflicts cause automatic abort.
		rebaseCleanupAll(db, baseBranch, workBranch)
		return fmt.Errorf("rebase execution failed (possible conflicts — automatic abort): %w", err)
	}
	fmt.Printf("  %s Rebase executed successfully\n", style.Bold.Render("✓"))

	// Step 7: Concurrency check — verify main hasn't moved.
	currentHead, err := flattenGetHead(db, dbName)
	if err != nil {
		rebaseCleanupAll(db, baseBranch, workBranch)
		return fmt.Errorf("concurrency check: %w", err)
	}
	if currentHead != preHead {
		rebaseCleanupAll(db, baseBranch, workBranch)
		return fmt.Errorf("ABORT: main HEAD moved during rebase (%s → %s)", shortHash(preHead), shortHash(currentHead))
	}

	// Step 8: Swap branches — make compact-work the new main.
	// We're already on compact-work from the rebase.
	if _, err := db.ExecContext(ctx, "CALL DOLT_BRANCH('-D', 'main')"); err != nil {
		// Can't delete main — leave compact-work in place for manual recovery.
		return fmt.Errorf("delete old main: %w (compact-work branch preserved for manual recovery)", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-m', '%s', 'main')", workBranch)); err != nil {
		return fmt.Errorf("rename work branch to main: %w", err)
	}
	// Delete the base branch.
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
	// Checkout main.
	if _, err := db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')"); err != nil {
		return fmt.Errorf("checkout new main: %w", err)
	}
	fmt.Printf("  Branch swap complete — compact-work is now main\n")

	// Step 9: Verify integrity — row counts must match pre-flight.
	// This runs AFTER the branch swap so we're reading from the new main,
	// not from compact-work (which may have different table visibility during rebase).
	postCounts, err := flattenGetRowCounts(db, dbName)
	if err != nil {
		fmt.Printf("  %s WARNING: could not verify row counts after rebase: %v\n",
			style.Bold.Render("!"), err)
		fmt.Printf("  Branch swap already complete — verify manually with 'gt dolt status'\n")
	} else {
		integrityOK := true
		for table, preCount := range preCounts {
			postCount, ok := postCounts[table]
			if !ok {
				fmt.Printf("  %s INTEGRITY WARNING: table %q missing after rebase (was %d rows)\n",
					style.Bold.Render("!"), table, preCount)
				integrityOK = false
			} else if preCount != postCount {
				fmt.Printf("  %s INTEGRITY WARNING: %q row count changed: pre=%d post=%d\n",
					style.Bold.Render("!"), table, preCount, postCount)
				integrityOK = false
			}
		}
		if integrityOK {
			fmt.Printf("  %s Integrity verified (%d tables match)\n", style.Bold.Render("✓"), len(preCounts))
		} else {
			fmt.Printf("  %s Some integrity checks failed — review above warnings\n", style.Bold.Render("!"))
		}
	}

	// Verify final state.
	var finalCount int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM `%s`.dolt_log", dbName)).Scan(&finalCount); err == nil {
		fmt.Printf("  Final commit count: %d\n", finalCount)
	}

	fmt.Printf("\n%s Surgical rebase complete: %d → %d commits (kept %d recent)\n",
		style.Bold.Render("✓"), commitCount, finalCount, doltRebaseKeepRecent)
	return nil
}

// rebaseCleanup removes leftover branches from a previous failed rebase.
func rebaseCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Try to get back to main first.
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
}

// rebaseAbortAndCleanup aborts an in-progress rebase then cleans up branches.
//
//nolint:unparam // baseBranch always "compact-base" — API kept flexible for future callers
func rebaseAbortAndCleanup(db *sql.DB, baseBranch, workBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, "CALL DOLT_REBASE('--abort')")
	_, _ = db.ExecContext(ctx, "CALL DOLT_CHECKOUT('main')")
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", workBranch))
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
}

// rebaseCleanupAll cleans up both branches after a failed rebase.
//
//nolint:unparam // baseBranch always "compact-base" — API kept flexible for future callers
func rebaseCleanupAll(db *sql.DB, baseBranch, workBranch string) {
	rebaseCleanup(db, baseBranch, workBranch)
}

// parseRebaseOrder converts a rebase_order value (returned by Dolt as DECIMAL
// string, e.g. "1.00") to an int.
func parseRebaseOrder(s string) (int, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rebase_order %q: %w", s, err)
	}
	return int(math.Round(f)), nil
}

// parseRebaseOrderRange parses min/max rebase_order strings to ints.
func parseRebaseOrderRange(minStr, maxStr string) (int, int, error) {
	minVal, err := parseRebaseOrder(minStr)
	if err != nil {
		return 0, 0, err
	}
	maxVal, err := parseRebaseOrder(maxStr)
	if err != nil {
		return 0, 0, err
	}
	return minVal, maxVal, nil
}

// rebaseCleanupBase cleans up only the base branch (work branch not yet created).
func rebaseCleanupBase(db *sql.DB, baseBranch string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = db.ExecContext(ctx, fmt.Sprintf("CALL DOLT_BRANCH('-D', '%s')", baseBranch))
}
