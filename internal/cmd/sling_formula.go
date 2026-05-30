package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/events"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

type wispCreateJSON struct {
	NewEpicID string `json:"new_epic_id"`
	RootID    string `json:"root_id"`
	ResultID  string `json:"result_id"`
}

func parseWispIDFromJSON(jsonOutput []byte) (string, error) {
	var result wispCreateJSON
	if err := json.Unmarshal(jsonOutput, &result); err != nil {
		return "", fmt.Errorf("parsing wisp JSON: %w (output: %s)", err, trimJSONForError(jsonOutput))
	}

	switch {
	case result.NewEpicID != "":
		return result.NewEpicID, nil
	case result.RootID != "":
		return result.RootID, nil
	case result.ResultID != "":
		return result.ResultID, nil
	default:
		return "", fmt.Errorf("wisp JSON missing id field (expected one of new_epic_id, root_id, result_id); output: %s", trimJSONForError(jsonOutput))
	}
}

func trimJSONForError(jsonOutput []byte) string {
	s := strings.TrimSpace(string(jsonOutput))
	const maxLen = 500
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// verifyFormulaExists checks that the formula exists using bd formula show.
// Formulas are TOML files (.formula.toml).
// Requests stale-read compatibility for consistency with verifyBeadExists.
func verifyFormulaExists(formulaName string) error {
	// Try bd formula show (handles all formula file formats)
	// Use Output() instead of Run() to detect bd exit 0 bug:
	// when formula not found, bd may exit 0 but produce empty stdout.
	// Stderr discarded — first attempt may fail expectedly (retry with mol- prefix).
	if out, err := BdCmd("formula", "show", formulaName).
		AllowStale().
		Stderr(io.Discard).Output(); err == nil && len(out) > 0 {
		return nil
	}

	// Try with mol- prefix
	if out, err := BdCmd("formula", "show", "mol-"+formulaName).
		AllowStale().
		Stderr(io.Discard).Output(); err == nil && len(out) > 0 {
		return nil
	}

	return fmt.Errorf("formula '%s' not found (check 'bd formula list')", formulaName)
}

// findHookedFormulaSingleton returns the existing hooked bead for an assignee
// when that bead already carries the same attached_formula metadata.
func findHookedFormulaSingleton(workDir, targetAgent, formulaName string) (*beads.Issue, error) {
	if workDir == "" || targetAgent == "" || formulaName == "" {
		return nil, nil
	}

	b := beads.New(workDir)
	hookedBeads, err := b.List(beads.ListOptions{
		Status:   beads.StatusHooked,
		Assignee: targetAgent,
		Priority: -1,
	})
	if err != nil {
		return nil, err
	}

	for _, bead := range hookedBeads {
		fields := beads.ParseAttachmentFields(bead)
		if fields != nil && fields.AttachedFormula == formulaName {
			return bead, nil
		}
	}

	return nil, nil
}

var findHookedFormulaSingletonFn = findHookedFormulaSingleton

// runSlingFormula handles standalone formula slinging.
// Flow: cook → wisp → attach to hook → nudge
func runSlingFormula(ctx context.Context, args []string) error {
	formulaName := args[0]

	// Get town root early - needed for BEADS_DIR when running bd commands
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding town root: %w", err)
	}
	townBeadsDir := filepath.Join(townRoot, ".beads")

	// Resolve target using shared dispatch logic
	var target string
	if len(args) > 1 {
		target = args[1]
	}
	var admission *polecatAdmissionHandle
	if !slingDryRun && target != "" {
		admissionRig := ""
		if rigName, isRig := IsRigName(target); isRig {
			admissionRig = rigName
		}
		if admissionRig != "" {
			admission, _, err = acquirePolecatAdmissionFn(townRoot, admissionRig, formulaName, "formula")
			if err != nil {
				return err
			}
			defer admission.Release()
		}
	}
	resolved, err := resolveTarget(target, ResolveTargetOptions{
		DryRun:               slingDryRun,
		Force:                slingForce,
		Create:               slingCreate,
		Account:              slingAccount,
		Agent:                slingAgent,
		NoBoot:               slingNoBoot,
		WorkDesc:             formulaName,
		TownRoot:             townRoot,
		SkipPolecatAdmission: admission != nil,
	})
	if err != nil {
		return err
	}
	targetAgent := resolved.Agent
	targetPane := resolved.Pane
	formulaWorkDir := resolved.WorkDir
	delayedDogInfo := resolved.DelayedDogInfo
	isSelfSling := resolved.IsSelfSling

	fmt.Printf("%s Slinging formula %s to %s...\n", style.Bold.Render("🎯"), formulaName, targetAgent)

	rollbackSpawned := func(beadID string) {
		if resolved.NewPolecatInfo == nil {
			return
		}
		fmt.Printf("%s Rolling back spawned polecat %s...\n", style.Warning.Render("⚠"), resolved.NewPolecatInfo.PolecatName)
		rollbackSlingArtifactsFn(resolved.NewPolecatInfo, beadID, formulaWorkDir, "")
	}

	// Resolve working directory for bd commands (routes to correct rig beads)
	// Fall back to townRoot (HQ beads) if no specific rig directory was determined
	if formulaWorkDir == "" {
		formulaWorkDir = townRoot
	}

	if slingDryRun {
		existing, err := findHookedFormulaSingletonFn(formulaWorkDir, targetAgent, formulaName)
		if err != nil {
			return fmt.Errorf("checking existing hooked formulas for %s: %w", targetAgent, err)
		}
		if existing != nil && !slingForce {
			fmt.Printf("Would reuse existing formula %s on %s via %s\n", formulaName, targetAgent, existing.ID)
			return nil
		}

		fmt.Printf("Would cook formula: %s\n", formulaName)
		fmt.Printf("Would create wisp and pin to: %s\n", targetAgent)
		for _, v := range slingVars {
			fmt.Printf("  --var %s\n", v)
		}
		fmt.Printf("Would nudge pane: %s\n", targetPane)
		return nil
	}

	// Serialize standalone formula slings per assignee so same-formula retries
	// and handoffs cannot create duplicate hooked wisps for one target.
	assigneeUnlock, assigneeLockErr := tryAcquireSlingAssigneeLock(townRoot, targetAgent)
	if assigneeLockErr != nil {
		return fmt.Errorf("serializing formula sling for %s: %w", targetAgent, assigneeLockErr)
	}
	defer assigneeUnlock()

	existing, err := findHookedFormulaSingletonFn(formulaWorkDir, targetAgent, formulaName)
	if err != nil {
		return fmt.Errorf("checking existing hooked formulas for %s: %w", targetAgent, err)
	}
	if existing != nil && !slingForce {
		fmt.Printf("%s Formula %s already hooked to %s via %s, no-op\n",
			style.Dim.Render("○"), formulaName, targetAgent, existing.ID)
		return nil
	}
	if admission == nil && strings.Contains(targetAgent, "/polecats/") {
		parts := strings.Split(targetAgent, "/")
		if len(parts) >= 3 {
			admission, _, err = acquirePolecatAdmissionFn(townRoot, parts[0], formulaName, "formula")
			if err != nil {
				return err
			}
			defer admission.Release()
		}
	}

	// Step 1: Cook the formula (ensures proto exists)
	fmt.Printf("  Cooking formula...\n")
	if err := BdCmd("cook", formulaName).
		Dir(formulaWorkDir).
		WithGTRoot(townRoot).
		Run(); err != nil {
		telemetry.RecordMolCook(ctx, formulaName, err)
		rollbackSpawned("")
		return fmt.Errorf("cooking formula: %w", err)
	}
	telemetry.RecordMolCook(ctx, formulaName, nil)

	// Step 2: Create wisp instance (ephemeral)
	fmt.Printf("  Creating wisp...\n")
	wispArgs := []string{"mol", "wisp", formulaName}
	for _, v := range slingVars {
		wispArgs = append(wispArgs, "--var", v)
	}
	wispArgs = append(wispArgs, "--json")

	wispOut, err := BdCmd(wispArgs...).
		Dir(formulaWorkDir).
		WithAutoCommit().
		WithGTRoot(townRoot).
		Output()
	if err != nil {
		rollbackSpawned("")
		return fmt.Errorf("creating wisp: %w", err)
	}

	// Parse wisp output to get the root ID
	wispRootID, err := parseWispIDFromJSON(wispOut)
	if err != nil {
		telemetry.RecordMolWisp(ctx, formulaName, "", "", err)
		rollbackSpawned("")
		return fmt.Errorf("parsing wisp output: %w", err)
	}
	telemetry.RecordMolWisp(ctx, formulaName, wispRootID, "", nil)

	fmt.Printf("%s Wisp created: %s\n", style.Bold.Render("✓"), wispRootID)

	// Step 3: Hook the wisp bead with retry and verification.
	// See: https://github.com/steveyegge/gastown/issues/148.
	hookDir := beads.ResolveHookDir(townRoot, wispRootID, "")
	if err := hookBeadWithRetryFn(wispRootID, targetAgent, hookDir); err != nil {
		return err
	}
	fmt.Printf("%s Attached to hook (status=hooked)\n", style.Bold.Render("✓"))

	// Log sling event to activity feed (formula slinging)
	actor := detectActor()
	payload := events.SlingPayload(wispRootID, targetAgent)
	payload["formula"] = formulaName
	_ = events.LogFeed(events.TypeSling, actor, payload)

	// Update agent bead's hook_bead field (ZFC: agents track their current work)
	// Note: formula slinging uses town root as workDir (no polecat-specific path)
	updateAgentHookBead(targetAgent, wispRootID, "", townBeadsDir)

	// Store all attachment fields in a single read-modify-write cycle.
	// NOTE: For standalone formula sling, the wisp IS the work - do NOT store
	// attached_molecule as a self-reference (the wisp's own ID pointing to itself
	// is meaningless). attached_molecule is only meaningful when a formula-on-bead
	// creates a wisp that's bonded to a separate base bead.
	fieldUpdates := beadFieldUpdates{
		Dispatcher:      actor,
		Args:            slingArgs,
		Vars:            append([]string(nil), slingVars...),
		AttachedFormula: formulaName,
		FormulaVars:     strings.Join(slingVars, "\n"),
	}
	if err := storeFieldsInBead(wispRootID, fieldUpdates); err != nil {
		fmt.Printf("%s Could not store fields in bead: %v\n", style.Dim.Render("Warning:"), err)
	} else if slingArgs != "" {
		fmt.Printf("%s Args stored in bead (durable)\n", style.Bold.Render("✓"))
	}

	// Start delayed dog session now that hook is set
	// This ensures dog sees the hook when gt prime runs on session start
	if delayedDogInfo != nil {
		pane, err := delayedDogInfo.StartDelayedSession()
		if err != nil {
			return fmt.Errorf("starting delayed dog session: %w", err)
		}
		targetPane = pane
	}

	// Start spawned polecat session now that hook is set.
	// This ensures polecat sees the wisp when gt prime runs on session start.
	if resolved.NewPolecatInfo != nil {
		pane, err := resolved.NewPolecatInfo.StartSession()
		if err != nil {
			// Rollback: unhook wisp, delete Dolt branch, clean up polecat worktree/agent bead
			rollbackSlingArtifactsFn(resolved.NewPolecatInfo, wispRootID, "", "")
			return fmt.Errorf("starting polecat session: %w", err)
		}
		targetPane = pane
	}

	// Step 4: Nudge to start (graceful if no tmux)
	// Skip for self-sling - agent is currently processing the sling command and will see
	// the hooked work on next turn. Nudging would inject text while agent is busy.
	if isSelfSling {
		fmt.Printf("%s Self-sling: work hooked, will process on next turn\n", style.Dim.Render("○"))
		return nil
	}
	if targetPane == "" {
		fmt.Printf("%s No pane to nudge (agent will discover work via gt prime)\n", style.Dim.Render("○"))
		return nil
	}

	// Skip nudge during tests to prevent agent self-interruption
	if os.Getenv("GT_TEST_NO_NUDGE") != "" {
		return nil
	}

	var prompt string
	if slingArgs != "" {
		prompt = fmt.Sprintf("Formula %s slung. Args: %s. Run `"+cli.Name()+" hook` to see your hook, then execute using these args.", formulaName, slingArgs)
	} else {
		prompt = fmt.Sprintf("Formula %s slung. Run `"+cli.Name()+" hook` to see your hook, then execute the steps.", formulaName)
	}
	t := tmux.NewTmux()

	// Dog sessions need a nudge sent to their session (not to the bare pane ID
	// from StartDelayedSession, which is ambiguous on platforms where tmux pane
	// IDs are not globally unique). Use NudgeSession which qualifies the target
	// with the session name. (gt-etc)
	if delayedDogInfo != nil {
		dogSession := fmt.Sprintf("hq-dog-%s", delayedDogInfo.DogName)
		if err := t.NudgeSession(dogSession, prompt); err != nil {
			fmt.Printf("%s Could not nudge dog %s: %v (will discover work via gt prime)\n",
				style.Dim.Render("○"), delayedDogInfo.DogName, err)
		} else {
			fmt.Printf("%s Nudged dog %s\n", style.Bold.Render("▶"), delayedDogInfo.DogName)
		}
		return nil
	}

	if err := t.NudgePane(targetPane, prompt); err != nil {
		// Graceful fallback for no-tmux mode
		fmt.Printf("%s Could not nudge (no tmux?): %v\n", style.Dim.Render("○"), err)
		fmt.Printf("  Agent will discover work via gt prime / bd show\n")
	} else {
		fmt.Printf("%s Nudged to start\n", style.Bold.Render("▶"))
	}

	return nil
}
