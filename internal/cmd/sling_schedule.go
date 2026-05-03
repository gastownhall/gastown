package cmd

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/scheduler/capacity"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// shouldDeferDispatch checks the town config to decide dispatch mode.
// Returns (true, nil) when max_polecats > 0 (deferred dispatch).
// Returns (false, nil) when max_polecats <= 0 (direct dispatch).
func shouldDeferDispatch() (bool, error) {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return false, nil // No town — direct dispatch
	}

	settingsPath := config.TownSettingsPath(townRoot)
	settings, err := config.LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		return false, fmt.Errorf("loading town settings: %w (dispatch blocked — fix config or use gt config set scheduler.max_polecats -1)", err)
	}

	schedulerCfg := settings.Scheduler
	if schedulerCfg == nil {
		return false, nil // No scheduler config — direct dispatch (default)
	}

	maxPol := schedulerCfg.GetMaxPolecats()
	if maxPol > 0 {
		return true, nil
	}
	return false, nil // -1 or 0 = direct dispatch
}

// ScheduleOptions holds options for scheduling a bead.
type ScheduleOptions struct {
	Formula     string   // Formula to apply at dispatch time (e.g., "mol-polecat-work")
	Args        string   // Natural language args for executor
	Vars        []string // Formula variables (key=value)
	Merge       string   // Merge strategy: direct/mr/local
	BaseBranch  string   // Override base branch for polecat worktree
	NoConvoy    bool     // Skip auto-convoy creation
	Owned       bool     // Mark auto-convoy as caller-managed lifecycle
	DryRun      bool     // Show what would be done without acting
	Force       bool     // Force schedule even if bead is hooked/in_progress
	NoMerge     bool     // Skip merge queue on completion
	ReviewOnly  bool     // Review-only mode: assignee evaluates and reports back, no merge/commit/push
	Account     string   // Claude Code account handle
	Agent       string   // Agent override (e.g., "gemini", "codex")
	HookRawBead bool     // Hook raw bead without default formula
	Ralph       bool     // Ralph Wiggum loop mode
}

// scheduleBead schedules a bead for deferred dispatch via the capacity scheduler.
// Creates a sling context bead to hold scheduling state. The work bead is never modified.
func scheduleBead(beadID, rigName string, opts ScheduleOptions) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return err
	}

	if err := verifyBeadExists(beadID); err != nil {
		return fmt.Errorf("bead '%s' not found", beadID)
	}

	if _, isRig := IsRigName(rigName); !isRig {
		return fmt.Errorf("'%s' is not a known rig", rigName)
	}

	if !opts.Force {
		if err := checkCrossRigGuard(beadID, rigName+"/polecats/_", townRoot); err != nil {
			return err
		}
	}

	info, err := getBeadInfo(beadID)
	if err != nil {
		return fmt.Errorf("checking bead status: %w", err)
	}

	// Idempotency: check for existing open sling context for this work bead.
	// Fail fast on errors to avoid creating duplicate contexts on transient DB failures.
	//
	// Create the sling context in the target rig's beads dir so that the target
	// rig's witness can discover it during patrol. Previously this used the HQ
	// beads dir, which meant non-HQ rig witnesses never saw the context. (GH#3468)
	rigBeadsDir := doltserver.FindRigBeadsDir(townRoot, rigName)
	rigBeads := beads.NewWithBeadsDir(townRoot, rigBeadsDir)
	existingCtx, _, findErr := rigBeads.FindOpenSlingContext(beadID)
	if findErr != nil {
		return fmt.Errorf("checking for existing sling context: %w", findErr)
	}
	if existingCtx != nil {
		fmt.Printf("%s Bead %s is already scheduled (context: %s), no-op\n",
			style.Dim.Render("○"), beadID, existingCtx.ID)
		return nil
	}

	// Guard against scheduling closed/tombstone beads (defense-in-depth, hq-ki2).
	// Mirrors the closed-bead guards in runSling (sling.go) and executeSling
	// (sling_dispatch.go). The daemon's stranded scan can route closed cross-prefix
	// beads through scheduleBead in deferred dispatch mode; without this check, a
	// fresh ghost convoy is created for already-completed work. Not bypassed by
	// --force — if you need to re-dispatch, reopen the bead first.
	if info.Status == "closed" || info.Status == "tombstone" {
		return fmt.Errorf("bead %s is %s (work already completed)", beadID, info.Status)
	}

	if (info.Status == "pinned" || info.Status == "hooked" || info.Status == "in_progress") && !opts.Force {
		return fmt.Errorf("bead %s is already %s to %s\nUse --force to override", beadID, info.Status, info.Assignee)
	}

	if opts.Formula != "" {
		if err := verifyFormulaExists(opts.Formula); err != nil {
			return fmt.Errorf("formula %q not found: %w", opts.Formula, err)
		}
	}

	if opts.DryRun {
		fmt.Printf("Would schedule %s → %s\n", beadID, rigName)
		fmt.Printf("  Would create sling context bead\n")
		if !opts.NoConvoy {
			fmt.Printf("  Would create auto-convoy\n")
		}
		return nil
	}

	// Cook formula after dry-run check to avoid side effects
	if opts.Formula != "" {
		workDir := beads.ResolveHookDir(townRoot, beadID, "")
		if err := CookFormula(opts.Formula, workDir, townRoot); err != nil {
			return fmt.Errorf("formula %q failed to cook: %w", opts.Formula, err)
		}
	}

	// Build sling context fields
	fields := &capacity.SlingContextFields{
		Version:    1,
		WorkBeadID: beadID,
		TargetRig:  rigName,
		EnqueuedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if opts.Formula != "" {
		fields.Formula = opts.Formula
	}
	if opts.Args != "" {
		fields.Args = opts.Args
	}
	if len(opts.Vars) > 0 {
		fields.Vars = strings.Join(opts.Vars, "\n")
	}
	if opts.Merge != "" {
		fields.Merge = opts.Merge
	}
	if opts.BaseBranch != "" {
		fields.BaseBranch = opts.BaseBranch
	}
	fields.NoMerge = opts.NoMerge
	fields.ReviewOnly = opts.ReviewOnly
	if opts.Account != "" {
		fields.Account = opts.Account
	}
	if opts.Agent != "" {
		fields.Agent = opts.Agent
	}
	fields.HookRawBead = opts.HookRawBead
	if opts.Ralph {
		fields.Mode = "ralph"
	}
	fields.Owned = opts.Owned

	// Create sling context bead in the target rig's beads dir so the rig's
	// witness discovers it during patrol. (GH#3468)
	ctxBead, err := rigBeads.CreateSlingContext(info.Title, beadID, fields)
	if err != nil {
		return fmt.Errorf("creating sling context: %w", err)
	}

	// Auto-convoy (unless --no-convoy)
	if !opts.NoConvoy {
		existingConvoy := isTrackedByConvoy(beadID)
		if existingConvoy == "" {
			convoyID, err := createAutoConvoy(beadID, info.Title, opts.Owned, opts.Merge, opts.BaseBranch)
			if err != nil {
				fmt.Printf("%s Could not create auto-convoy: %v\n", style.Dim.Render("Warning:"), err)
			} else {
				fmt.Printf("%s Created convoy %s\n", style.Bold.Render("→"), convoyID)
				// Update the context bead fields with convoy ID
				fields.Convoy = convoyID
				if updateErr := rigBeads.UpdateSlingContextFields(ctxBead.ID, fields); updateErr != nil {
					fmt.Printf("%s Could not update context with convoy: %v\n", style.Dim.Render("Warning:"), updateErr)
				}
			}
		} else {
			fmt.Printf("%s Already tracked by convoy %s\n", style.Dim.Render("○"), existingConvoy)
		}
	}

	actor := detectActor()
	_ = events.LogFeed(events.TypeSchedulerEnqueue, actor, events.SchedulerEnqueuePayload(beadID, rigName))

	fmt.Printf("%s Scheduled %s → %s (context: %s)\n", style.Bold.Render("✓"), beadID, rigName, ctxBead.ID)
	return nil
}

// runBatchSchedule schedules multiple beads for deferred dispatch.
// Returns error when all schedule attempts fail.
func runBatchSchedule(beadIDs []string, rigName, townRoot string) error {
	if slingDryRun {
		fmt.Printf("%s Would schedule %d beads to rig '%s':\n", style.Bold.Render("📋"), len(beadIDs), rigName)
		for _, beadID := range beadIDs {
			fmt.Printf("  Would schedule: %s → %s\n", beadID, rigName)
		}
		return nil
	}

	fmt.Printf("%s Scheduling %d beads to rig '%s'...\n", style.Bold.Render("📋"), len(beadIDs), rigName)

	successCount := 0
	for _, beadID := range beadIDs {
		formula := resolveFormula(slingFormula, slingHookRawBead, townRoot, rigName)
		err := scheduleBead(beadID, rigName, ScheduleOptions{
			Formula:     formula,
			Args:        slingArgs,
			Vars:        slingVars,
			NoConvoy:    slingNoConvoy,
			Owned:       slingOwned,
			Merge:       slingMerge,
			BaseBranch:  slingBaseBranch,
			DryRun:      false,
			Force:       slingForce,
			NoMerge:     slingNoMerge,
			Account:     slingAccount,
			Agent:       slingAgent,
			HookRawBead: slingHookRawBead,
			Ralph:       slingRalph,
		})
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Dim.Render("✗"), beadID, err)
			continue
		}
		successCount++
	}

	fmt.Printf("\n%s Scheduled %d/%d beads\n", style.Bold.Render("📊"), successCount, len(beadIDs))
	if successCount == 0 {
		return fmt.Errorf("all %d schedule attempts failed", len(beadIDs))
	}
	return nil
}

// resolveRigForBead determines the rig that owns a bead from its ID prefix.
func resolveRigForBead(townRoot, beadID string) string {
	prefix := beads.ExtractPrefix(beadID)
	if prefix == "" {
		return ""
	}
	return beads.GetRigNameForPrefix(townRoot, prefix)
}

// resolveFormula determines the formula name from user flags and rig settings.
// Resolution order:
//  1. Explicit --formula flag
//  2. Rig property layers (wisp → bead → system default "mol-polecat-work")
//  3. Rig settings file (workflow.default_formula in settings/config.json)
//  4. Hardcoded fallback "mol-polecat-work"
//
// The property layers are the primary mechanism, supporting:
//
//	gt rig config set <rig> default_formula mol-evolve         # wisp layer
//	gt rig config set <rig> default_formula mol-evolve --global # bead layer
func resolveFormula(explicit string, hookRawBead bool, townRoot, rigName string) string {
	if hookRawBead {
		return ""
	}
	if explicit != "" {
		return explicit
	}
	// Check rig property layers: wisp → bead → system default (issue gt-y18).
	if townRoot != "" && rigName != "" {
		r := &rig.Rig{
			Name: rigName,
			Path: filepath.Join(townRoot, rigName),
		}
		if df := r.GetStringConfig("default_formula"); df != "" {
			return df
		}
	}
	// Fallback: check rig settings file (legacy path, issue gt-boc).
	if townRoot != "" && rigName != "" {
		rigPath := filepath.Join(townRoot, rigName)
		if df := config.GetDefaultFormula(rigPath); df != "" {
			return df
		}
	}
	return "mol-polecat-work"
}

// slingContextTTL is the maximum age of a sling context before it's considered
// stale and ignored by areScheduled(). This prevents orphaned sling contexts
// (from failed spawns or throttled dispatches) from permanently blocking tasks.
// See GH#2279.
const slingContextTTL = 30 * time.Minute

// areScheduled returns a set of bead IDs that have open sling contexts.
// Scans all rig beads dirs since sling contexts are created in the target
// rig's beads dir (GH#3468). On error, fails closed: treats ALL requested
// beads as scheduled to prevent false stranded detection and duplicate
// scheduling attempts.
//
// Sling contexts older than slingContextTTL are ignored — they are likely
// orphans from failed spawn attempts (GH#2279).
func areScheduled(beadIDs []string) map[string]bool {
	result := make(map[string]bool)
	if len(beadIDs) == 0 {
		return result
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil || townRoot == "" {
		// Can't determine town root — fail closed (treat all as scheduled)
		for _, id := range beadIDs {
			result[id] = true
		}
		return result
	}

	// Scan all rig beads dirs (sling contexts live in target rig's DB). (GH#3468)
	contexts := listAllSlingContexts(townRoot)

	// Build lookup of work bead IDs from open contexts, skipping stale ones.
	scheduledWorkBeads := make(map[string]bool)
	now := time.Now()
	for _, ctx := range contexts {
		// Skip stale sling contexts (GH#2279): contexts older than the TTL
		// are likely orphans from failed spawn attempts. Ignoring them allows
		// the task to appear as "ready" again for re-dispatch.
		if ctx.CreatedAt != "" {
			if created, err := time.Parse(time.RFC3339, ctx.CreatedAt); err == nil {
				if now.Sub(created) > slingContextTTL {
					continue
				}
			}
		}
		fields := beads.ParseSlingContextFields(ctx.Description)
		if fields != nil {
			scheduledWorkBeads[fields.WorkBeadID] = true
		}
	}

	// Filter to just the requested IDs
	for _, id := range beadIDs {
		if scheduledWorkBeads[id] {
			result[id] = true
		}
	}
	return result
}

// isScheduled checks if a single bead has an open sling context.
// For batch checks in loops, use areScheduled() instead.
func isScheduled(beadID string) bool {
	scheduled := areScheduled([]string{beadID})
	return scheduled[beadID]
}

// detectSchedulerIDType determines what kind of ID was passed for scheduling.
// Returns "convoy", "epic", or "task".
func detectSchedulerIDType(id string) (string, error) {
	// Fast path: hq-cv-* is always a convoy
	if strings.HasPrefix(id, "hq-cv-") {
		return "convoy", nil
	}

	info, err := getBeadInfo(id)
	if err != nil {
		return "", fmt.Errorf("cannot resolve bead '%s': %w", id, err)
	}

	switch info.IssueType {
	case "epic":
		return "epic", nil
	case "convoy":
		return "convoy", nil
	}

	for _, label := range info.Labels {
		switch label {
		case "gt:epic":
			return "epic", nil
		case "gt:convoy":
			return "convoy", nil
		}
	}

	return "task", nil
}

// schedulerTaskOnlyFlagNames lists flags that only apply to task bead scheduling,
// not convoy or epic mode.
var schedulerTaskOnlyFlagNames = []string{
	"account", "agent", "ralph", "args", "var",
	"merge", "base-branch", "no-convoy", "owned", "no-merge",
}

// validateNoTaskOnlySchedulerFlags checks that no task-only flags were set.
func validateNoTaskOnlySchedulerFlags(cmd *cobra.Command, mode string) error {
	var used []string
	for _, name := range schedulerTaskOnlyFlagNames {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			used = append(used, "--"+name)
		}
	}
	if len(used) > 0 {
		return fmt.Errorf("%s mode does not support: %s\nThese flags only apply to task bead scheduling",
			mode, strings.Join(used, ", "))
	}
	return nil
}
