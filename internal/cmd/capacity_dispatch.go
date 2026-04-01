package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/style"
)

// maxDispatchFailures is the maximum number of consecutive dispatch failures
// before a sling context is closed as circuit-broken.
const maxDispatchFailures = 3

// dispatchScheduledWork is the main dispatch loop for the capacity scheduler.
// Called by both `gt scheduler run` and the daemon heartbeat.
func dispatchScheduledWork(townRoot, actor string, batchOverride int, dryRun bool) (int, error) {
	// Acquire exclusive lock to prevent concurrent dispatch
	runtimeDir := filepath.Join(townRoot, ".runtime")
	_ = os.MkdirAll(runtimeDir, 0755)
	lockFile := filepath.Join(runtimeDir, "scheduler-dispatch.lock")
	fileLock := flock.New(lockFile)
	locked, err := fileLock.TryLock()
	if err != nil {
		return 0, fmt.Errorf("acquiring dispatch lock: %w", err)
	}
	if !locked {
		return 0, nil
	}
	defer func() { _ = fileLock.Unlock() }()

	// Load scheduler state
	state, err := capacity.LoadState(townRoot)
	if err != nil {
		return 0, fmt.Errorf("loading scheduler state: %w", err)
	}

	if state.Paused {
		if !dryRun {
			fmt.Printf("%s Scheduler is paused (by %s), skipping dispatch\n", style.Dim.Render("⏸"), state.PausedBy)
		}
		return 0, nil
	}

	// Load town settings for scheduler config
	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return 0, fmt.Errorf("loading town settings: %w", err)
	}

	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}

	// Nothing to dispatch when scheduler is in direct dispatch or disabled mode.
	maxPolecats := schedulerCfg.GetMaxPolecats()
	if maxPolecats <= 0 {
		if !dryRun && !isDaemonDispatch() {
			staleBeads, _ := getReadySlingContexts(townRoot)
			if len(staleBeads) > 0 {
				fmt.Printf("%s %d context bead(s) still open from a previous deferred mode\n",
					style.Warning.Render("⚠"), len(staleBeads))
				fmt.Printf("  Use: gt scheduler clear  (close all sling context beads)\n")
				fmt.Printf("  Or:  gt config set scheduler.max_polecats N  (re-enable deferred dispatch)\n")
			}
		}
		return 0, nil
	}

	// Determine limits
	batchSize := schedulerCfg.GetBatchSize()
	if batchOverride > 0 {
		batchSize = batchOverride
	}
	spawnDelay := schedulerCfg.GetSpawnDelay()

	townBeads := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))

	// Clean up invalid/stale contexts before querying for ready beads.
	// Skip during dry-run to avoid mutating state.
	if !dryRun {
		cleanupStaleContexts(townRoot)
	}

	// Wire up the DispatchCycle
	successfulRigs := make(map[string]bool)
	// Track polecat names from dispatch results, keyed by context bead ID.
	polecatNames := make(map[string]string)
	cycle := &capacity.DispatchCycle{
		AvailableCapacity: func() (int, error) {
			active := countWorkingPolecats()
			cap := maxPolecats - active
			if cap <= 0 {
				return 0, nil // No free slots — PlanDispatch treats <= 0 as no capacity
			}
			return cap, nil
		},
		QueryPending: func() ([]capacity.PendingBead, error) {
			return getReadySlingContexts(townRoot)
		},
		Execute: func(b capacity.PendingBead) error {
			result, err := dispatchSingleBead(b, townRoot, actor)
			if err != nil {
				return err
			}
			// Track side effects here (Execute runs exactly once, never retried).
			if result != nil && result.PolecatName != "" {
				polecatNames[b.ID] = result.PolecatName
			}
			if b.TargetRig != "" {
				successfulRigs[b.TargetRig] = true
			}
			_ = events.LogFeed(events.TypeSchedulerDispatch, actor,
				events.SchedulerDispatchPayload(b.WorkBeadID, b.TargetRig, polecatNames[b.ID]))
			return nil
		},
		OnSuccess: func(b capacity.PendingBead) error {
			// OnSuccess may be retried — only do the close here, no side effects.
			return townBeads.CloseSlingContext(b.ID, "dispatched")
		},
		OnFailure: func(b capacity.PendingBead, err error) {
			var onSuccessErr *capacity.ErrOnSuccessFailed
			if errors.As(err, &onSuccessErr) {
				// Polecat launched but context close failed — not a true dispatch failure.
				// Log a distinct warning so operators can distinguish from "polecat never launched".
				fmt.Fprintf(os.Stderr, "%s Dispatch of %s succeeded but context close failed: %v\n",
					style.Warning.Render("⚠"), b.WorkBeadID, err)
				// Last-resort close attempt to prevent double-dispatch on next cycle.
				// OnSuccess already retried 2x; this is a final attempt before circuit-breaking.
				if closeErr := townBeads.CloseSlingContext(b.ID, "dispatch-close-failed"); closeErr != nil {
					fmt.Fprintf(os.Stderr, "%s CRITICAL: last-resort close of %s failed — risk of double-dispatch for %s: %v\n",
						style.Warning.Render("⚠"), b.ID, b.WorkBeadID, closeErr)
				} else {
					// Last-resort close succeeded — context is now closed.
					// Log feed event so dashboards can detect bead DB degradation.
					_ = events.LogFeed(events.TypeSchedulerCloseRetry, actor,
						events.SchedulerDispatchPayload(b.WorkBeadID, b.TargetRig, polecatNames[b.ID]))
					// Skip recordDispatchFailure to avoid writing to a closed context.
					return
				}
			} else {
				_ = events.LogFeed(events.TypeSchedulerDispatchFailed, actor,
					events.SchedulerDispatchFailedPayload(b.WorkBeadID, b.TargetRig, err.Error()))
			}
			recordDispatchFailure(townBeads, b, err)
		},
		BatchSize:  batchSize,
		SpawnDelay: spawnDelay,
	}

	if dryRun {
		plan, planErr := cycle.Plan()
		if planErr != nil {
			return 0, fmt.Errorf("planning dispatch: %w", planErr)
		}
		printDryRunPlan(plan, maxPolecats, batchSize)
		return 0, nil
	}

	report, err := cycle.Run()
	if err != nil {
		return 0, fmt.Errorf("dispatch cycle failed: %w", err)
	}

	// Wake rig agents for each unique rig that had successful dispatches.
	for rig := range successfulRigs {
		wakeRigAgents(rig)
	}

	// Update runtime state with fresh read to avoid clobbering concurrent pause.
	if report.Dispatched > 0 {
		freshState, err := capacity.LoadState(townRoot)
		if err != nil {
			fmt.Printf("%s Could not reload scheduler state: %v\n", style.Dim.Render("Warning:"), err)
		} else {
			freshState.RecordDispatch(report.Dispatched)
			if err := capacity.SaveState(townRoot, freshState); err != nil {
				fmt.Printf("%s Could not save scheduler state: %v\n", style.Dim.Render("Warning:"), err)
			}
		}
	}

	if report.Dispatched > 0 || report.Failed > 0 {
		fmt.Printf("\n%s Dispatched %d, failed %d (reason: %s)\n",
			style.Bold.Render("✓"), report.Dispatched, report.Failed, report.Reason)
	}

	return report.Dispatched, nil
}

// printDryRunPlan displays a dry-run dispatch plan.
func printDryRunPlan(plan capacity.DispatchPlan, maxPolecats, batchSize int) {
	if plan.Reason == "none" {
		fmt.Println("No ready beads scheduled for dispatch")
		return
	}

	activePolecats := countActivePolecats()
	capStr := "unlimited"
	if maxPolecats > 0 {
		cap := maxPolecats - activePolecats
		if cap < 0 {
			cap = 0
		}
		capStr = fmt.Sprintf("%d free of %d", cap, maxPolecats)
	}

	totalReady := len(plan.ToDispatch) + plan.Skipped
	if len(plan.ToDispatch) == 0 {
		fmt.Printf("No capacity: %s, %d ready bead(s) waiting\n", capStr, totalReady)
		return
	}

	fmt.Printf("%s Would dispatch %d bead(s) (capacity: %s, batch: %d, ready: %d, reason: %s)\n",
		style.Bold.Render("📋"), len(plan.ToDispatch), capStr, batchSize, totalReady, plan.Reason)
	for _, b := range plan.ToDispatch {
		fmt.Printf("  Would dispatch: %s → %s\n", b.WorkBeadID, b.TargetRig)
	}
}

// cleanupStaleContexts closes invalid and stale sling context beads.
// Called explicitly before the dispatch cycle to separate cleanup from querying.
func cleanupStaleContexts(townRoot string) {
	townBeads := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))

	contexts, err := listAllSlingContexts(townRoot)
	if err != nil {
		return
	}

	// First pass: close invalid and circuit-broken contexts, collect work bead IDs
	// that need status checks for stale detection.
	var staleCheckContexts []*beads.Issue
	var staleCheckFields []*capacity.SlingContextFields
	for _, ctx := range contexts {
		fields := beads.ParseSlingContextFields(ctx.Description)
		if fields == nil {
			_ = townBeads.CloseSlingContext(ctx.ID, "invalid-context")
			continue
		}
		if fields.DispatchFailures >= maxDispatchFailures {
			_ = townBeads.CloseSlingContext(ctx.ID, "circuit-broken")
			continue
		}
		staleCheckContexts = append(staleCheckContexts, ctx)
		staleCheckFields = append(staleCheckFields, fields)
	}

	if len(staleCheckContexts) == 0 {
		return
	}

	// Collect work bead IDs to fetch
	workBeadIDs := make([]string, 0, len(staleCheckFields))
	for _, fields := range staleCheckFields {
		workBeadIDs = append(workBeadIDs, fields.WorkBeadID)
	}

	// Batch-fetch work bead info for only the specific IDs we need
	workBeadInfo := batchFetchBeadInfoByIDs(townRoot, workBeadIDs)

	// Second pass: close contexts whose work beads are stale.
	// Note: in_progress is intentionally excluded — the work bead is being
	// actively worked, and bd ready won't return it, so the dispatch query
	// already prevents re-dispatch. The context stays open until the polecat
	// finishes and the bead transitions to closed/tombstone.
	for i, ctx := range staleCheckContexts {
		fields := staleCheckFields[i]
		info, found := workBeadInfo[fields.WorkBeadID]
		if found && (info.Status == "hooked" || info.Status == "closed" || info.Status == "tombstone") {
			_ = townBeads.CloseSlingContext(ctx.ID, "stale-work-bead")
		}
	}
}

// beadStatusInfo holds batch-fetched bead status and title.
type beadStatusInfo struct {
	Status string
	Title  string
}

// batchFetchBeadInfoByIDs returns a map of bead ID → status+title for specific beads.
// Uses `bd show` with multiple IDs per rig directory instead of fetching all beads.
// This avoids the O(minutes) latency of `bd list --all --json --limit=0` on large repos.
func batchFetchBeadInfoByIDs(townRoot string, ids []string) map[string]beadStatusInfo {
	result := make(map[string]beadStatusInfo)
	if len(ids) == 0 {
		return result
	}

	// Group IDs by prefix to route to the correct rig directory
	// Most IDs will have a common prefix (e.g., "gt-", "bcc-", "hq-")
	// For simplicity, try all dirs - bd show will return results only for matching IDs
	for _, dir := range beadsSearchDirs(townRoot) {
		// Use Beads wrapper to get proper BEADS_DIR resolution, --allow-stale,
		// and BEADS_DOLT_PORT translation (matching how all other bd-invoking
		// functions work). Raw exec.Command missed these, causing stale/wrong
		// dolt database queries. See GH#803.
		b := beads.New(dir)
		args := append([]string{"show", "--json"}, ids...)
		out, err := b.Run(args...)
		if err != nil {
			continue
		}
		var items []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Title  string `json:"title"`
		}
		if err := json.Unmarshal(out, &items); err == nil {
			for _, item := range items {
				result[item.ID] = beadStatusInfo{Status: item.Status, Title: item.Title}
			}
		}
	}
	return result
}

// getReadySlingContexts queries for sling context beads whose work beads are ready.
// This is a pure query — no destructive side effects. Call cleanupStaleContexts()
// before this function to handle invalid/stale contexts.
//
// Sling contexts are queried from HQ only (authoritative). Work bead readiness
// is checked across all rig dirs since work beads live in rig-local DBs.
func getReadySlingContexts(townRoot string) ([]capacity.PendingBead, error) {
	// 1. List all open sling context beads from HQ (authoritative)
	allContexts, err := listAllSlingContexts(townRoot)
	if err != nil {
		return nil, fmt.Errorf("listing sling contexts: %w", err)
	}

	if len(allContexts) == 0 {
		return nil, nil
	}

	// 2. Build readyWorkIDs set from bd ready across all dirs
	// (work beads live in rig-local DBs, so we need to check all dirs)
	readyWorkIDs, readyErr := listReadyWorkBeadIDsWithError(townRoot)
	if readyErr != nil {
		return nil, readyErr
	}

	// 3. Build PendingBead list — pure filtering, no mutations.
	// Sort by EnqueuedAt for deterministic deduplication: when concurrent
	// scheduleBead calls create multiple contexts for the same work bead,
	// the oldest context always wins.
	sort.Slice(allContexts, func(i, j int) bool {
		fi := beads.ParseSlingContextFields(allContexts[i].Description)
		fj := beads.ParseSlingContextFields(allContexts[j].Description)
		if fi == nil || fj == nil {
			return fi != nil // valid contexts sort before invalid
		}
		if fi.EnqueuedAt != fj.EnqueuedAt {
			return fi.EnqueuedAt < fj.EnqueuedAt
		}
		return allContexts[i].ID < allContexts[j].ID // deterministic tiebreaker
	})

	seenWork := make(map[string]bool)
	var result []capacity.PendingBead
	for _, ctx := range allContexts {
		fields := beads.ParseSlingContextFields(ctx.Description)
		if fields == nil {
			continue // Skip invalid — cleanupStaleContexts handles these
		}

		// Circuit breaker filter
		if fields.DispatchFailures >= maxDispatchFailures {
			continue
		}

		// Only include if work bead is ready (unblocked)
		if !readyWorkIDs[fields.WorkBeadID] {
			continue
		}

		// Deduplicate: one dispatch per work bead (oldest context wins)
		if seenWork[fields.WorkBeadID] {
			continue
		}
		seenWork[fields.WorkBeadID] = true

		result = append(result, capacity.PendingBead{
			ID:          ctx.ID,
			WorkBeadID:  fields.WorkBeadID,
			Title:       ctx.Title,
			TargetRig:   fields.TargetRig,
			Description: ctx.Description,
			Labels:      ctx.Labels,
			Context:     fields,
		})
	}

	return result, nil
}

// dispatchSingleBead dispatches one scheduled bead via executeSling.
// Context fields are already parsed (from PendingBead.Context).
// Returns the SlingResult (including PolecatName) on success.
func dispatchSingleBead(b capacity.PendingBead, townRoot, _ string) (*SlingResult, error) {
	if b.Context == nil {
		return nil, fmt.Errorf("missing sling context for %s", b.ID)
	}

	dp := capacity.ReconstructFromContext(b.Context)
	params := SlingParams{
		BeadID:           dp.BeadID,
		RigName:          dp.RigName,
		FormulaName:      dp.FormulaName,
		Args:             dp.Args,
		Vars:             dp.Vars,
		Merge:            dp.Merge,
		BaseBranch:       dp.BaseBranch,
		NoMerge:          dp.NoMerge,
		ReviewOnly:       dp.ReviewOnly,
		Account:          dp.Account,
		Agent:            dp.Agent,
		HookRawBead:      dp.HookRawBead,
		Mode:             dp.Mode,
		FormulaFailFatal: true,
		CallerContext:    "scheduler-dispatch",
		NoConvoy:         true,
		NoBoot:           true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(townRoot, ".beads"),
	}

	fmt.Printf("  Dispatching %s → %s...\n", b.WorkBeadID, b.TargetRig)
	result, err := executeSling(params)
	if err != nil {
		return nil, fmt.Errorf("sling failed: %w", err)
	}

	return result, nil
}

// isDaemonDispatch returns true when dispatch is triggered by the daemon heartbeat.
func isDaemonDispatch() bool {
	return os.Getenv("GT_DAEMON") == "1"
}

// recordDispatchFailure increments the dispatch failure counter on the sling context bead.
func recordDispatchFailure(townBeads *beads.Beads, b capacity.PendingBead, dispatchErr error) {
	if b.Context == nil {
		return
	}

	b.Context.DispatchFailures++
	b.Context.LastFailure = dispatchErr.Error()

	if err := townBeads.UpdateSlingContextFields(b.ID, b.Context); err != nil {
		fmt.Printf("  %s Failed to record dispatch failure for %s: %v\n",
			style.Warning.Render("⚠"), b.ID, err)
	}

	if b.Context.DispatchFailures >= maxDispatchFailures {
		if err := townBeads.CloseSlingContext(b.ID, "circuit-broken"); err != nil {
			fmt.Printf("  %s Failed to close circuit-broken context %s: %v\n",
				style.Warning.Render("⚠"), b.ID, err)
		}
		fmt.Printf("  %s Context %s (work: %s) failed %d times, circuit-broken\n",
			style.Warning.Render("⚠"), b.ID, b.WorkBeadID, b.Context.DispatchFailures)
	}
}

// listAllSlingContexts returns all open sling context beads from HQ.
// Sling contexts are always created in the town-root DB (HQ is authoritative),
// so we query HQ only. This avoids partial-failure scenarios where a rig dir
// succeeds but HQ fails, silently returning incomplete results.
// Used by scheduler list/status/clear and cleanupStaleContexts.
// Does NOT filter by readiness or circuit breaker.
func listAllSlingContexts(townRoot string) ([]*beads.Issue, error) {
	townBeads := beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
	return townBeads.ListOpenSlingContexts()
}

// listReadyWorkBeadIDsWithError returns a set of work bead IDs that are unblocked.
// Returns an error only when ALL dirs fail (partial success is acceptable).
func listReadyWorkBeadIDsWithError(townRoot string) (map[string]bool, error) {
	readyIDs := make(map[string]bool)
	dirs := beadsSearchDirs(townRoot)
	failCount := 0
	var lastErr error
	for _, dir := range dirs {
		// Use Beads wrapper to get proper BEADS_DIR resolution, --allow-stale,
		// and BEADS_DOLT_PORT translation. Raw exec.Command missed these,
		// causing the scheduler to query stale/wrong dolt databases and return
		// empty readyWorkIDs. See GH#803.
		b := beads.New(dir)
		readyOut, err := b.Run("ready", "--json", "--limit=0")
		if err != nil {
			failCount++
			lastErr = err
			fmt.Fprintf(os.Stderr, "%s Warning: bd ready failed for %s: %v\n",
				style.Dim.Render("⚠"), dir, err)
			continue
		}
		var readyBeads []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(readyOut, &readyBeads); err == nil {
			for _, b := range readyBeads {
				readyIDs[b.ID] = true
			}
		}
	}
	if failCount == len(dirs) && failCount > 0 {
		return nil, fmt.Errorf("all %d bd ready queries failed (last: %w)", failCount, lastErr)
	}
	return readyIDs, nil
}

// listReadyWorkBeadIDs returns a set of work bead IDs that are unblocked.
// Convenience wrapper that ignores errors (used by listScheduledBeads for display).
func listReadyWorkBeadIDs(townRoot string) map[string]bool {
	ids, _ := listReadyWorkBeadIDsWithError(townRoot)
	if ids == nil {
		return make(map[string]bool)
	}
	return ids
}
