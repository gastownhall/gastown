package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/flock"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/style"
)

// crossRigEscalationDebounce is the minimum interval between cross-rig prefix
// escalations for the same (rig, prefix) pair. Prevents alert spam when a
// stuck context keeps re-appearing on every dispatch tick.
const crossRigEscalationDebounce = time.Hour

// crossRigEscalationState tracks last-escalation timestamps per (rig, prefix).
// Process-local — debounce resets on daemon restart, which is fine: a new
// process should be allowed to surface the issue once.
var (
	crossRigEscalationMu   sync.Mutex
	crossRigEscalationLast = map[string]time.Time{}
)

// crossRigEscalationKey returns the debounce key for a (rig, prefix) pair.
func crossRigEscalationKey(rig, prefix string) string {
	return rig + "/" + prefix
}

// shouldFireCrossRigEscalation reports whether enough time has elapsed since
// the last escalation for this (rig, prefix) pair to fire a new one. Updates
// the timestamp on a positive answer.
func shouldFireCrossRigEscalation(rig, prefix string, now time.Time) bool {
	crossRigEscalationMu.Lock()
	defer crossRigEscalationMu.Unlock()
	key := crossRigEscalationKey(rig, prefix)
	if last, ok := crossRigEscalationLast[key]; ok && now.Sub(last) < crossRigEscalationDebounce {
		return false
	}
	crossRigEscalationLast[key] = now
	return true
}

// resetCrossRigEscalationStateForTest clears the debounce map. Test-only.
func resetCrossRigEscalationStateForTest() {
	crossRigEscalationMu.Lock()
	defer crossRigEscalationMu.Unlock()
	crossRigEscalationLast = map[string]time.Time{}
}

// fireCrossRigEscalation invokes `gt escalate` with a MEDIUM severity. Best
// effort — escalation failure is logged but does not block the dispatch path.
var fireCrossRigEscalation = func(rig, prefix, beadID string) {
	msg := fmt.Sprintf("cross-rig dispatch refused: rig=%s prefix=%s bead=%s — see gt-el4", rig, prefix, beadID)
	cmd := exec.Command("gt", "escalate", "--severity", "medium", "--reason", "cross-rig-prefix", msg)
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "%s cross-rig escalation failed: %v\n", style.Warning.Render("⚠"), err)
	}
}

// maxDispatchFailures is the maximum number of consecutive dispatch failures
// before a sling context is closed as circuit-broken.
const maxDispatchFailures = 3

type schedulerDispatchPlan struct {
	State       *capacity.SchedulerState
	MaxPolecats int
	BatchSize   int
	SpawnDelay  time.Duration
	Capacity    polecatCapacitySnapshot
	Scheduled   []scheduledBeadInfo
	Ready       []capacity.PendingBead
	Plan        capacity.DispatchPlan
}

func buildSchedulerDispatchPlan(townRoot string, batchOverride int, cleanup bool) (*schedulerDispatchPlan, error) {
	state, err := capacity.LoadState(townRoot)
	if err != nil {
		return nil, fmt.Errorf("loading scheduler state: %w", err)
	}

	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return nil, fmt.Errorf("loading town settings: %w", err)
	}
	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		schedulerCfg = capacity.DefaultSchedulerConfig()
	}

	maxPolecats := schedulerCfg.GetMaxPolecats()
	batchSize := schedulerCfg.GetBatchSize()
	if batchOverride > 0 {
		batchSize = batchOverride
	}
	spawnDelay := schedulerCfg.GetSpawnDelay()

	if cleanup && !state.Paused && maxPolecats > 0 {
		if err := cleanupStaleContexts(townRoot); err != nil {
			return nil, fmt.Errorf("cleaning stale scheduler contexts: %w", err)
		}
	}

	assessments, blockedErr := assessScheduledContexts(townRoot)
	if blockedErr != nil {
		return nil, blockedErr
	}

	snapshot, err := polecatCapacitySnapshotForTownNoCleanup(townRoot)
	if cleanup {
		snapshot, err = polecatCapacitySnapshotForTown(townRoot)
	}
	if err != nil {
		return nil, fmt.Errorf("loading polecat capacity: %w", err)
	}

	ready := readySlingContextsFromAssessments(assessments)
	dispatchPlan := capacity.PlanDispatch(snapshot.Free, batchSize, ready)
	if len(ready) > 0 {
		switch {
		case state.Paused:
			dispatchPlan = capacity.DispatchPlan{Skipped: len(ready), Reason: "paused"}
		case maxPolecats <= 0:
			dispatchPlan = capacity.DispatchPlan{Skipped: len(ready), Reason: "direct-mode"}
		}
	}

	return &schedulerDispatchPlan{
		State:       state,
		MaxPolecats: maxPolecats,
		BatchSize:   batchSize,
		SpawnDelay:  spawnDelay,
		Capacity:    snapshot,
		Scheduled:   scheduledBeadInfosFromAssessments(assessments),
		Ready:       ready,
		Plan:        dispatchPlan,
	}, nil
}

// dispatchScheduledWork is the main dispatch loop for the capacity scheduler.
// Called by both `gt scheduler run` and the daemon heartbeat.
func dispatchScheduledWork(townRoot, actor string, batchOverride int, dryRun bool) (int, error) {
	if dryRun {
		dispatchPlan, err := buildSchedulerDispatchPlan(townRoot, batchOverride, false)
		if err != nil {
			return 0, fmt.Errorf("planning dispatch: %w", err)
		}
		dispatchPlan.Plan = validateDryRunDispatchPlan(townRoot, dispatchPlan.Plan)
		printSchedulerDryRunPlan(dispatchPlan)
		return 0, nil
	}

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
		if isDaemonDispatch() {
			return 0, nil
		}
		return 0, fmt.Errorf("scheduler dispatch already in progress (lock held: %s)", lockFile)
	}
	defer func() { _ = fileLock.Unlock() }()

	dispatchPlan, err := buildSchedulerDispatchPlan(townRoot, batchOverride, true)
	if err != nil {
		return 0, fmt.Errorf("planning dispatch: %w", err)
	}

	if dispatchPlan.State.Paused {
		if !isDaemonDispatch() {
			fmt.Printf("%s Scheduler is paused (by %s), skipping %d ready bead(s)\n",
				style.Dim.Render("⏸"), dispatchPlan.State.PausedBy, len(dispatchPlan.Ready))
		}
		return 0, nil
	}

	// Nothing to dispatch when scheduler is in direct dispatch or disabled mode.
	if dispatchPlan.MaxPolecats <= 0 {
		if !isDaemonDispatch() {
			if len(dispatchPlan.Scheduled) > 0 {
				fmt.Printf("%s %d context bead(s) still open from a previous deferred mode\n",
					style.Warning.Render("⚠"), len(dispatchPlan.Scheduled))
				fmt.Printf("  Use: gt scheduler clear  (close all sling context beads)\n")
				fmt.Printf("  Or:  gt config set scheduler.max_polecats N  (re-enable deferred dispatch)\n")
			} else {
				fmt.Println("No ready beads scheduled for dispatch")
			}
		}
		return 0, nil
	}

	// Wire up the DispatchCycle
	successfulRigs := make(map[string]bool)
	// Track polecat names from dispatch results, keyed by context bead ID.
	polecatNames := make(map[string]string)
	cycle := &capacity.DispatchCycle{
		Validate: func(b capacity.PendingBead) error {
			return validatePendingBeadForDispatch(townRoot, b, true)
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
			// Route to the correct rig's beads dir (GH#3468).
			return beadsForPendingContext(townRoot, b).CloseSlingContext(b.ID, "dispatched")
		},
		OnFailure: func(b capacity.PendingBead, err error) {
			var onSuccessErr *capacity.ErrOnSuccessFailed
			var admissionErr *polecatCapacityAdmissionError
			if errors.As(err, &onSuccessErr) {
				// Polecat launched but context close failed — not a true dispatch failure.
				// Log a distinct warning so operators can distinguish from "polecat never launched".
				fmt.Fprintf(os.Stderr, "%s Dispatch of %s succeeded but context close failed: %v\n",
					style.Warning.Render("⚠"), b.WorkBeadID, err)
				// Last-resort close attempt to prevent double-dispatch on next cycle.
				// OnSuccess already retried 2x; this is a final attempt before circuit-breaking.
				ctxBeads := beadsForPendingContext(townRoot, b)
				if closeErr := ctxBeads.CloseSlingContext(b.ID, "dispatch-close-failed"); closeErr != nil {
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
			} else if errors.As(err, &admissionErr) {
				fmt.Fprintf(os.Stderr, "%s Capacity full while dispatching %s; leaving context queued: %v\n",
					style.Dim.Render("○"), b.WorkBeadID, err)
				return
			} else {
				_ = events.LogFeed(events.TypeSchedulerDispatchFailed, actor,
					events.SchedulerDispatchFailedPayload(b.WorkBeadID, b.TargetRig, err.Error()))
			}
			recordDispatchFailure(beadsForPendingContext(townRoot, b), b, err)
		},
		SpawnDelay: dispatchPlan.SpawnDelay,
	}

	report, err := cycle.RunPlan(dispatchPlan.Plan)
	if err != nil {
		return 0, fmt.Errorf("dispatch cycle failed: %w", err)
	}
	if len(dispatchPlan.Plan.ToDispatch) > 0 && report.Dispatched == 0 && report.Failed == 0 {
		return 0, fmt.Errorf("scheduler dispatch invariant violation: plan had %d dispatchable bead(s) but no dispatch result", len(dispatchPlan.Plan.ToDispatch))
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
	} else if report.Skipped > 0 {
		printDispatchNoOp(report, dispatchPlan.Capacity)
	} else if !isDaemonDispatch() {
		printDispatchNoOp(report, dispatchPlan.Capacity)
	}

	return report.Dispatched, nil
}

func printSchedulerDryRunPlan(dispatchPlan *schedulerDispatchPlan) {
	if dispatchPlan.State.Paused {
		fmt.Printf("Scheduler is paused (by %s); %d ready bead(s) not dispatched\n",
			dispatchPlan.State.PausedBy, len(dispatchPlan.Ready))
		return
	}
	if dispatchPlan.MaxPolecats <= 0 {
		if len(dispatchPlan.Scheduled) == 0 {
			fmt.Println("No ready beads scheduled for dispatch")
			return
		}
		fmt.Printf("Scheduler is in direct dispatch mode (scheduler.max_polecats=%d); %d open context bead(s) will not dispatch\n",
			dispatchPlan.MaxPolecats, len(dispatchPlan.Scheduled))
		return
	}
	printDryRunPlan(dispatchPlan.Plan, dispatchPlan.Capacity, dispatchPlan.BatchSize)
}

func printDispatchNoOp(report capacity.DispatchReport, snapshot polecatCapacitySnapshot) {
	switch report.Reason {
	case "none":
		fmt.Println("No ready beads scheduled for dispatch")
	case "capacity":
		fmt.Printf("\n%s No capacity: %d ready bead(s) waiting (working: %d recovery_blocked: %d reservations: %d reusable_idle: %d pending_mr: %d)\n",
			style.Dim.Render("○"), report.Skipped, snapshot.Working, snapshot.RecoveryBlocked, snapshot.Reservations, snapshot.ReusableIdle, snapshot.PendingMR)
	default:
		fmt.Printf("\n%s No dispatchable beads (reason: %s, skipped: %d)\n",
			style.Dim.Render("○"), report.Reason, report.Skipped)
	}
}

// printDryRunPlan displays a dry-run dispatch plan.
func printDryRunPlan(plan capacity.DispatchPlan, snapshot polecatCapacitySnapshot, batchSize int) {
	if plan.Reason == "none" {
		fmt.Println("No ready beads scheduled for dispatch")
		return
	}

	capStr := "unlimited"
	if snapshot.Max > 0 {
		capStr = fmt.Sprintf("%d free of %d (working: %d, recovery_blocked: %d, reservations: %d, reusable_idle: %d, pending_mr: %d)",
			snapshot.Free, snapshot.Max, snapshot.Working, snapshot.RecoveryBlocked, snapshot.Reservations, snapshot.ReusableIdle, snapshot.PendingMR)
	}

	totalReady := len(plan.ToDispatch) + plan.Skipped
	if len(plan.ToDispatch) == 0 {
		switch plan.Reason {
		case "capacity":
			fmt.Printf("No capacity: %s, %d ready bead(s) waiting\n", capStr, totalReady)
		case "validation":
			fmt.Printf("No dispatchable beads: validation failed for %d candidate(s)\n", totalReady)
		default:
			fmt.Printf("No dispatchable beads: reason=%s, %d candidate(s) skipped\n", plan.Reason, totalReady)
		}
		return
	}

	fmt.Printf("%s Would dispatch %d bead(s) (capacity: %s, batch: %d, ready: %d, reason: %s)\n",
		style.Bold.Render("📋"), len(plan.ToDispatch), capStr, batchSize, totalReady, plan.Reason)
	for _, b := range plan.ToDispatch {
		fmt.Printf("  Would dispatch: %s → %s\n", b.WorkBeadID, b.TargetRig)
	}
}

// beadsForContext returns a Beads instance that can operate on a sling context
// bead. Sling contexts live in the target rig's beads dir (GH#3468), so we
// resolve the dir from the context's TargetRig field. Falls back to HQ if
// the target rig is unknown (e.g., invalid context with nil fields).
func beadsForContext(townRoot string, fields *capacity.SlingContextFields) *beads.Beads {
	if fields != nil && fields.TargetRig != "" {
		rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, fields.TargetRig)
		if rigBeadsDir != "" {
			return beads.NewWithBeadsDir(townRoot, rigBeadsDir)
		}
	}
	// Fallback to HQ for contexts without a valid TargetRig
	return beads.NewWithBeadsDir(townRoot, filepath.Join(townRoot, ".beads"))
}

func beadsForPendingContext(townRoot string, b capacity.PendingBead) *beads.Beads {
	if b.ContextBeadsDir != "" {
		workDir := b.ContextWorkDir
		if workDir == "" {
			workDir = filepath.Dir(b.ContextBeadsDir)
		}
		return beads.NewWithBeadsDir(workDir, b.ContextBeadsDir)
	}
	return beadsForContext(townRoot, b.Context)
}

type slingContextRecord struct {
	issue    *beads.Issue
	workDir  string
	beadsDir string
}

type scheduledContextAssessment struct {
	context        slingContextRecord
	fields         *capacity.SlingContextFields
	info           beadStatusInfo
	found          bool
	blocked        bool
	blockedUnknown bool
	ready          bool
}

func beadsForContextRecord(rec slingContextRecord) *beads.Beads {
	return beads.NewWithBeadsDir(rec.workDir, rec.beadsDir)
}

// cleanupStaleContexts closes invalid and stale sling context beads.
// Called explicitly before the dispatch cycle to separate cleanup from querying.
func cleanupStaleContexts(townRoot string) error {
	contexts, err := listAllSlingContextRecords(townRoot)
	if err != nil {
		return err
	}

	// First pass: close invalid and circuit-broken contexts, collect work bead IDs
	// that need status checks for stale detection.
	var staleCheckContexts []slingContextRecord
	var staleCheckFields []*capacity.SlingContextFields
	for _, ctx := range contexts {
		fields := beads.ParseSlingContextFields(ctx.issue.Description)
		if fields == nil {
			_ = beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "invalid-context")
			continue
		}
		if fields.DispatchFailures >= maxDispatchFailures {
			_ = beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "circuit-broken")
			continue
		}
		staleCheckContexts = append(staleCheckContexts, ctx)
		staleCheckFields = append(staleCheckFields, fields)
	}

	if len(staleCheckContexts) == 0 {
		return nil
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
			_ = beadsForContextRecord(ctx).CloseSlingContext(ctx.issue.ID, "stale-work-bead")
		}
	}
	return nil
}

// beadStatusInfo holds batch-fetched bead status, title, and labels.
type beadStatusInfo struct {
	Status string
	Title  string
	Labels []string
}

func beadStatusInfoFromBeadInfo(info *beadInfo) beadStatusInfo {
	if info == nil {
		return beadStatusInfo{}
	}
	return beadStatusInfo{
		Status: info.Status,
		Title:  info.Title,
		Labels: info.Labels,
	}
}

// batchFetchBeadInfoByIDs returns a map of bead ID → status+title+labels for specific beads.
// Uses `bd show` with multiple IDs per rig directory instead of fetching all beads.
// This avoids the O(minutes) latency of `bd list --all --json --limit=0` on large repos.
func batchFetchBeadInfoByIDs(townRoot string, ids []string) map[string]beadStatusInfo {
	result := make(map[string]beadStatusInfo)
	if len(ids) == 0 {
		return result
	}

	requestedIDs := uniqueNonEmptyIDs(ids)
	idsByBeadsDir := groupBeadIDsByResolvedBeadsDir(townRoot, requestedIDs)
	for beadsDir, groupedIDs := range idsByBeadsDir {
		// Use Beads wrapper to get proper BEADS_DIR resolution, --allow-stale,
		// and BEADS_DOLT_PORT translation (matching how all other bd-invoking
		// functions work). Route IDs directly instead of trying every beads dir;
		// scheduler status/list/run sit on operator hot paths, and repeated bd show
		// fanout dominates latency in large towns.
		b := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
		args := append([]string{"show", "--json"}, groupedIDs...)
		out, err := b.Run(args...)
		if err != nil {
			continue
		}
		var items []struct {
			ID     string   `json:"id"`
			Status string   `json:"status"`
			Title  string   `json:"title"`
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal(out, &items); err != nil {
			continue
		}
		for _, item := range items {
			result[item.ID] = beadStatusInfo{
				Status: item.Status,
				Title:  item.Title,
				Labels: item.Labels,
			}
		}
	}

	for _, id := range requestedIDs {
		if _, found := result[id]; found {
			continue
		}
		info, err := getBeadInfoFromTownRoot(townRoot, id)
		if err != nil {
			continue
		}
		result[id] = beadStatusInfoFromBeadInfo(info)
	}
	return result
}

func uniqueNonEmptyIDs(ids []string) []string {
	result := make([]string, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		result = append(result, id)
	}
	return result
}

func groupBeadIDsByResolvedBeadsDir(townRoot string, ids []string) map[string][]string {
	townBeadsDir := filepath.Join(townRoot, ".beads")
	idsByBeadsDir := make(map[string][]string)
	seen := make(map[string]bool)
	for _, id := range ids {
		if id == "" {
			continue
		}
		beadsDir := beads.ResolveBeadsDirForID(townBeadsDir, id)
		key := beadsDir + "\x00" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		idsByBeadsDir[beadsDir] = append(idsByBeadsDir[beadsDir], id)
	}
	return idsByBeadsDir
}

func assessScheduledContexts(townRoot string) ([]scheduledContextAssessment, error) {
	contexts, err := listAllSlingContextRecords(townRoot)
	if err != nil {
		return nil, err
	}
	if len(contexts) == 0 {
		return nil, nil
	}

	candidates := make([]scheduledContextAssessment, 0, len(contexts))
	workBeadIDs := make([]string, 0, len(contexts))
	for _, ctx := range contexts {
		fields := beads.ParseSlingContextFields(ctx.issue.Description)
		if fields == nil || fields.WorkBeadID == "" || fields.TargetRig == "" {
			continue
		}
		if fields.DispatchFailures >= maxDispatchFailures {
			continue
		}
		candidates = append(candidates, scheduledContextAssessment{context: ctx, fields: fields})
		workBeadIDs = append(workBeadIDs, fields.WorkBeadID)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		fi := candidates[i].fields
		fj := candidates[j].fields
		if fi.EnqueuedAt != fj.EnqueuedAt {
			return fi.EnqueuedAt < fj.EnqueuedAt
		}
		return candidates[i].context.issue.ID < candidates[j].context.issue.ID
	})

	workBeadInfo := batchFetchBeadInfoByIDs(townRoot, workBeadIDs)
	blockedWorkIDs, blockedUnknownIDs, blockedErr := listBlockedWorkBeadIDStates(townRoot, workBeadIDs)

	seenWork := make(map[string]bool)
	assessments := make([]scheduledContextAssessment, 0, len(candidates))
	for _, candidate := range candidates {
		workBeadID := candidate.fields.WorkBeadID
		if seenWork[workBeadID] {
			continue
		}
		seenWork[workBeadID] = true

		info, found := workBeadInfo[workBeadID]
		candidate.info = info
		candidate.found = found
		candidate.blocked = blockedWorkIDs[workBeadID]
		candidate.blockedUnknown = blockedUnknownIDs[workBeadID]
		candidate.ready = isScheduledWorkBeadReady(workBeadID, info, found, blockedWorkIDs, blockedUnknownIDs)
		assessments = append(assessments, candidate)
	}

	return assessments, blockedErr
}

// getReadySlingContexts queries for sling context beads whose work beads are ready.
// This is a pure query — no destructive side effects. Call cleanupStaleContexts()
// before this function to handle invalid/stale contexts.
//
// Sling contexts are scanned from HQ and rig DBs. Work bead readiness is checked
// by source ID so context location and source readiness cannot diverge.
func getReadySlingContexts(townRoot string) ([]capacity.PendingBead, error) {
	assessments, blockedErr := assessScheduledContexts(townRoot)
	if blockedErr != nil {
		return nil, blockedErr
	}
	return readySlingContextsFromAssessments(assessments), nil
}

func readySlingContextsFromAssessments(assessments []scheduledContextAssessment) []capacity.PendingBead {
	var result []capacity.PendingBead
	for _, assessment := range assessments {
		if !assessment.ready {
			continue
		}
		workLabels := assessment.info.Labels
		if capacity.IsMessagingBead(workLabels) {
			fmt.Fprintf(os.Stderr, "%s dispatch_skip reason=messaging_label bead=%s labels=%v\n",
				style.Dim.Render("○"), assessment.fields.WorkBeadID, workLabels)
			continue
		}

		result = append(result, capacity.PendingBead{
			ID:              assessment.context.issue.ID,
			WorkBeadID:      assessment.fields.WorkBeadID,
			Title:           assessment.info.Title,
			TargetRig:       assessment.fields.TargetRig,
			Description:     assessment.context.issue.Description,
			Labels:          workLabels,
			Context:         assessment.fields,
			ContextWorkDir:  assessment.context.workDir,
			ContextBeadsDir: assessment.context.beadsDir,
		})
	}

	return result
}

// dispatchSingleBead dispatches one scheduled bead via executeSling.
// Context fields are already parsed (from PendingBead.Context).
// Returns the SlingResult (including PolecatName) on success.
func dispatchSingleBead(b capacity.PendingBead, townRoot, _ string) (*SlingResult, error) {
	if b.Context == nil {
		return nil, fmt.Errorf("missing sling context for %s", b.ID)
	}

	dp := capacity.ReconstructFromContext(b.Context)
	targetBeadsDir := filepath.Join(townRoot, ".beads")
	if dp.RigName != "" {
		resolved, ok := beads.ResolveRepoAliasBeadsDir(townRoot, dp.RigName)
		if !ok {
			return nil, fmt.Errorf("cannot resolve target rig %q beads database for %s", dp.RigName, b.WorkBeadID)
		}
		targetBeadsDir = resolved
	}
	params := SlingParams{
		BeadID:           dp.BeadID,
		RigName:          dp.RigName,
		FormulaName:      dp.FormulaName,
		Args:             dp.Args,
		Vars:             dp.Vars,
		Merge:            dp.Merge,
		BaseBranch:       dp.BaseBranch,
		ResumeBranch:     dp.ResumeBranch,
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
		BeadsDir:         targetBeadsDir,
	}

	fmt.Printf("  Dispatching %s → %s...\n", b.WorkBeadID, b.TargetRig)
	result, err := executeSling(params)
	if err != nil {
		return nil, fmt.Errorf("sling failed: %w", err)
	}

	return result, nil
}

func validateDryRunDispatchPlan(townRoot string, plan capacity.DispatchPlan) capacity.DispatchPlan {
	if len(plan.ToDispatch) == 0 {
		return plan
	}
	validated := make([]capacity.PendingBead, 0, len(plan.ToDispatch))
	for _, b := range plan.ToDispatch {
		if err := validatePendingBeadForDispatch(townRoot, b, false); err != nil {
			fmt.Fprintf(os.Stderr, "%s dry-run_skip reason=validation bead=%s target_rig=%s: %v\n",
				style.Dim.Render("○"), b.WorkBeadID, b.TargetRig, err)
			plan.Skipped++
			continue
		}
		if _, err := getBeadInfoFromTownRoot(townRoot, b.WorkBeadID); err != nil {
			fmt.Fprintf(os.Stderr, "%s dry-run_skip reason=bead_lookup bead=%s target_rig=%s: %v\n",
				style.Dim.Render("○"), b.WorkBeadID, b.TargetRig, err)
			plan.Skipped++
			continue
		}
		if b.TargetRig != "" {
			if err := verifyBeadExistsInTargetRigDatabase(b.WorkBeadID, b.TargetRig, townRoot); err != nil {
				fmt.Fprintf(os.Stderr, "%s dry-run_skip reason=target_db bead=%s target_rig=%s: %v\n",
					style.Dim.Render("○"), b.WorkBeadID, b.TargetRig, err)
				plan.Skipped++
				continue
			}
		}
		validated = append(validated, b)
	}
	plan.ToDispatch = validated
	if len(plan.ToDispatch) == 0 && plan.Reason != "none" {
		plan.Reason = "validation"
	}
	return plan
}

func validatePendingBeadForDispatch(townRoot string, b capacity.PendingBead, escalate bool) error {
	// Cross-rig prefix guard (gt-el4). A bead whose ID prefix does not match the
	// target rig's registered prefix must not be dispatched — the polecat would
	// land in a rig DB that cannot resolve the bead and hang in prime.
	if b.TargetRig == "" {
		return nil
	}
	rigPath := filepath.Join(townRoot, b.TargetRig)
	rigPrefix := rigBeadsPrefix(townRoot, rigPath, b.TargetRig)
	if capacity.AcceptsPrefix(rigPrefix, b.WorkBeadID) {
		return nil
	}
	gotPrefix := capacity.BeadIDPrefix(b.WorkBeadID)
	fmt.Fprintf(os.Stderr,
		"%s dispatch_refused reason=cross_rig_prefix bead=%s target_rig=%s rig_prefix=%s bead_prefix=%s\n",
		style.Warning.Render("⚠"), b.WorkBeadID, b.TargetRig, rigPrefix, gotPrefix)
	if escalate && shouldFireCrossRigEscalation(b.TargetRig, gotPrefix, time.Now()) {
		fireCrossRigEscalation(b.TargetRig, gotPrefix, b.WorkBeadID)
	}
	return capacity.ErrCrossRigPrefix
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

// listAllSlingContexts returns all open sling context beads across all rig
// beads dirs. Sling contexts are created in the target rig's beads dir
// (GH#3468), so we scan HQ plus all rig dirs.
// Used by scheduler list/status/clear, cleanupStaleContexts, and areScheduled.
// Does NOT filter by readiness or circuit breaker.
//
// Deduplicates by context ID: different search dirs can resolve to the same
// underlying beads DB (e.g., when a rig's top-level .beads is a redirect to
// mayor/rig/.beads), and both paths would otherwise return the same contexts.
func listAllSlingContexts(townRoot string) ([]*beads.Issue, error) {
	records, err := listAllSlingContextRecords(townRoot)
	if err != nil {
		return nil, err
	}
	all := make([]*beads.Issue, 0, len(records))
	for _, rec := range records {
		all = append(all, rec.issue)
	}
	return all, nil
}

func listAllSlingContextRecords(townRoot string) ([]slingContextRecord, error) {
	var records []slingContextRecord
	seen := make(map[string]bool)
	dirs, err := beadsSearchDirs(townRoot)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		beadsDir := beads.ResolveBeadsDir(dir)
		b := beads.NewWithBeadsDir(dir, beadsDir)
		contexts, err := b.ListOpenSlingContexts()
		if err != nil {
			return nil, fmt.Errorf("listing sling contexts in %s: %w", beadsDir, err)
		}
		for _, ctx := range contexts {
			key := beadsDir + "\x00" + ctx.ID
			if seen[key] {
				continue
			}
			seen[key] = true
			records = append(records, slingContextRecord{issue: ctx, workDir: dir, beadsDir: beadsDir})
		}
	}
	return records, nil
}

type blockedWorkQuery func(beadsDir string, groupedIDs []string) ([]byte, error)

func runBlockedWorkQuery(beadsDir string, _ []string) ([]byte, error) {
	// Use Beads wrapper to get proper BEADS_DIR resolution, --allow-stale,
	// and BEADS_DOLT_PORT translation.
	b := beads.NewWithBeadsDir(filepath.Dir(beadsDir), beadsDir)
	return b.Run("blocked", "--json")
}

func listBlockedWorkBeadIDStates(townRoot string, workBeadIDs []string) (map[string]bool, map[string]bool, error) {
	return listBlockedWorkBeadIDStatesWithRunner(townRoot, workBeadIDs, runBlockedWorkQuery)
}

func listBlockedWorkBeadIDStatesWithRunner(townRoot string, workBeadIDs []string, query blockedWorkQuery) (map[string]bool, map[string]bool, error) {
	blockedIDs := make(map[string]bool)
	blockedUnknownIDs := make(map[string]bool)
	idsByBeadsDir := groupBeadIDsByResolvedBeadsDir(townRoot, workBeadIDs)
	failCount := 0
	var lastErr error
	for beadsDir, groupedIDs := range idsByBeadsDir {
		blockedOut, err := query(beadsDir, groupedIDs)
		if err != nil {
			failCount++
			lastErr = err
			markBlockedUnknown(blockedUnknownIDs, groupedIDs)
			fmt.Fprintf(os.Stderr, "%s Warning: bd blocked failed for %s: %v\n",
				style.Dim.Render("⚠"), filepath.Dir(beadsDir), err)
			continue
		}
		var blockedBeads []struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(blockedOut, &blockedBeads); err != nil {
			failCount++
			lastErr = err
			markBlockedUnknown(blockedUnknownIDs, groupedIDs)
			fmt.Fprintf(os.Stderr, "%s Warning: parsing bd blocked failed for %s: %v\n",
				style.Dim.Render("⚠"), filepath.Dir(beadsDir), err)
			continue
		}
		for _, b := range blockedBeads {
			blockedIDs[b.ID] = true
		}
	}
	if failCount == len(idsByBeadsDir) && failCount > 0 {
		return blockedIDs, blockedUnknownIDs, fmt.Errorf("all %d bd blocked queries failed (last: %w)", failCount, lastErr)
	}
	return blockedIDs, blockedUnknownIDs, nil
}

func markBlockedUnknown(blockedUnknownIDs map[string]bool, ids []string) {
	for _, id := range ids {
		if id != "" {
			blockedUnknownIDs[id] = true
		}
	}
}

func isScheduledWorkBeadReady(workBeadID string, info beadStatusInfo, found bool, blockedWorkIDs, blockedUnknownIDs map[string]bool) bool {
	if !found || blockedWorkIDs[workBeadID] || blockedUnknownIDs[workBeadID] {
		return false
	}
	return info.Status == "open"
}
