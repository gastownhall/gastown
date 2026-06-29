package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/deacon"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/style"
)

// MoleculeCurrentOutput represents the JSON output of bd mol current.
type MoleculeCurrentOutput struct {
	MoleculeID    string `json:"molecule_id"`
	MoleculeTitle string `json:"molecule_title"`
	NextStep      *struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Status      string `json:"status"`
	} `json:"next_step"`
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

// showMoleculeExecutionPrompt calls bd mol current and shows the current step
// with execution instructions. This is the core of the Propulsion Principle.
func showMoleculeExecutionPrompt(workDir, moleculeID string) {
	// Call bd mol current with JSON output
	cmd := exec.Command("bd", "mol", "current", moleculeID, "--json")
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Fall back to simple message if bd mol current fails
		fmt.Println(style.Bold.Render("→ PROPULSION PRINCIPLE: Work is on your hook. RUN IT."))
		fmt.Println("  Begin working on this molecule immediately.")
		fmt.Printf("  Check status with: bd mol current %s\n", moleculeID)
		return
	}
	// Handle bd exit 0 bug: empty stdout means not found
	if stdout.Len() == 0 {
		fmt.Println(style.Bold.Render("→ PROPULSION PRINCIPLE: Work is on your hook. RUN IT."))
		fmt.Println("  Begin working on this molecule immediately.")
		return
	}

	// Parse JSON output - it's an array with one element
	var outputs []MoleculeCurrentOutput
	if err := json.Unmarshal(stdout.Bytes(), &outputs); err != nil || len(outputs) == 0 {
		// Fall back to simple message
		fmt.Println(style.Bold.Render("→ PROPULSION PRINCIPLE: Work is on your hook. RUN IT."))
		fmt.Println("  Begin working on this molecule immediately.")
		return
	}
	output := outputs[0]

	// Show molecule progress
	fmt.Printf("**Progress:** %d/%d steps complete\n\n",
		output.Completed, output.Total)

	// Show current step if available
	if output.NextStep != nil {
		step := output.NextStep
		fmt.Printf("%s\n\n", style.Bold.Render("## 🎬 CURRENT STEP: "+step.Title))
		fmt.Printf("**Step ID:** %s\n", step.ID)
		fmt.Printf("**Status:** %s (ready to execute)\n\n", step.Status)

		// Show step description if available
		if step.Description != "" {
			fmt.Println("### Instructions")
			fmt.Println()
			// Indent the description for readability
			lines := strings.Split(step.Description, "\n")
			for _, line := range lines {
				fmt.Printf("%s\n", line)
			}
			fmt.Println()
		}

		// The propulsion directive
		fmt.Println(style.Bold.Render("→ EXECUTE THIS STEP NOW."))
		fmt.Println()
		fmt.Println("When complete:")
		fmt.Printf("  1. Close the step: bd close %s\n", step.ID)
		fmt.Printf("  2. Check for next step: bd mol current %s\n", moleculeID)
		fmt.Println("  3. Continue until molecule complete")
	} else {
		// No next step - molecule may be complete
		fmt.Println(style.Bold.Render("✓ MOLECULE COMPLETE"))
		fmt.Println()
		fmt.Println("All steps are done. You may:")
		fmt.Println("  - Report completion to supervisor")
		fmt.Println("  - Check for new work: bd mol current")
	}
}

// showFormulaSteps renders the formula steps inline in the prime output.
// Agents read these steps instead of materializing them as wisp rows.
// The label parameter customizes the section header (e.g., "Patrol Steps", "Work Steps").
// townRoot and rigName are used to load formula overlays (operator customizations).
// extraVars is an optional list of "key=value" overrides that are substituted into
// step descriptions before rendering, taking precedence over formula defaults.
func showFormulaSteps(formulaName, label, townRoot, rigName string, extraVars ...[]string) {
	content, err := formula.ResolveFormulaContent(formulaName, townRoot, rigName)
	if err != nil {
		style.PrintWarning("could not load formula %s: %v", formulaName, err)
		return
	}

	f, err := formula.Parse(content)
	if err != nil {
		style.PrintWarning("could not parse formula %s: %v", formulaName, err)
		return
	}

	if len(f.Steps) == 0 {
		return
	}

	// Apply formula overlays if townRoot is available.
	applyFormulaOverlays(f, formulaName, townRoot, rigName)

	var vars []string
	if len(extraVars) > 0 {
		vars = extraVars[0]
	}
	varMap := buildFormulaVarMap(f, vars)

	fmt.Println()
	fmt.Printf("**%s** (%d steps from %s):\n", label, len(f.Steps), formulaName)
	for i, step := range f.Steps {
		desc := applyFormulaVars(step.Description, varMap)
		fmt.Printf("  %d. **%s** — %s\n", i+1, step.Title, truncateDescription(desc, 120))
	}
	fmt.Println()
}

// showFormulaStepsFull renders formula steps with full descriptions.
// Used for polecat work formulas where step details are the primary instructions.
// townRoot and rigName are used to load formula overlays (operator customizations).
// extraVars is an optional list of "key=value" overrides substituted into step descriptions.
func showFormulaStepsFull(formulaName, townRoot, rigName string, extraVars ...[]string) {
	rendered, err := renderFormulaStepsFull(formulaName, townRoot, rigName, extraVars...)
	if err != nil {
		style.PrintWarning("%v", err)
		return
	}
	fmt.Print(rendered)
}

func renderFormulaStepsFull(formulaName, townRoot, rigName string, extraVars ...[]string) (string, error) {
	content, err := formula.ResolveFormulaContent(formulaName, townRoot, rigName)
	if err != nil {
		return "", fmt.Errorf("could not load formula %s: %w", formulaName, err)
	}

	f, err := formula.Parse(content)
	if err != nil {
		return "", fmt.Errorf("could not parse formula %s: %w", formulaName, err)
	}

	if len(f.Steps) == 0 {
		return "", nil
	}

	// Apply formula overlays if townRoot is available.
	applyFormulaOverlays(f, formulaName, townRoot, rigName)

	var vars []string
	if len(extraVars) > 0 {
		vars = extraVars[0]
	}
	varMap := buildFormulaVarMap(f, vars)

	var sb strings.Builder
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "**Formula Checklist** (%d steps from %s):\n\n", len(f.Steps), formulaName)
	for i, step := range f.Steps {
		title := applyFormulaVars(step.Title, varMap)
		fmt.Fprintf(&sb, "### Step %d: %s\n\n", i+1, title)
		if step.Description != "" {
			sb.WriteString(applyFormulaVars(step.Description, varMap))
			sb.WriteString("\n\n")
		}
	}
	return sb.String(), nil
}

// buildFormulaVarMap builds a map of variable name → value for substitution.
// Formula defaults are applied first; extraVars (key=value strings) override them.
func buildFormulaVarMap(f *formula.Formula, extraVars []string) map[string]string {
	m := make(map[string]string, len(f.Vars))
	for k, v := range f.Vars {
		if v.Default != "" {
			m[k] = v.Default
		}
	}
	for _, kv := range extraVars {
		if idx := strings.IndexByte(kv, '='); idx > 0 {
			m[kv[:idx]] = kv[idx+1:]
		}
	}
	return m
}

func attachmentFormulaVars(attachment *beads.AttachmentFields) []string {
	if attachment == nil {
		return nil
	}
	indexes := make(map[string]int)
	vars := make([]string, 0, len(attachment.AttachedVars))
	add := func(variable string) {
		variable = strings.TrimSpace(variable)
		if variable == "" {
			return
		}
		idx := strings.IndexByte(variable, '=')
		if idx <= 0 {
			return
		}
		key := strings.TrimSpace(variable[:idx])
		if idx, ok := indexes[key]; ok {
			vars[idx] = variable
			return
		}
		indexes[key] = len(vars)
		vars = append(vars, variable)
	}
	for _, variable := range strings.Split(attachment.FormulaVars, "\n") {
		add(variable)
	}
	for _, variable := range attachment.AttachedVars {
		add(variable)
	}
	return vars
}

// applyFormulaVars replaces {{key}} placeholders in text with values from varMap.
func applyFormulaVars(text string, varMap map[string]string) string {
	for k, v := range varMap {
		text = strings.ReplaceAll(text, "{{"+k+"}}", v)
	}
	return text
}

// extractFormulaVar extracts a specific key's value from a newline-separated
// key=value string (as stored in AttachmentFields.FormulaVars).
// Returns "" if the key is not found.
func extractFormulaVar(formulaVars, key string) string {
	for _, line := range strings.Split(formulaVars, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok && k == key {
			return v
		}
	}
	return ""
}

// truncateDescription truncates a multi-line description to a single line summary.
func truncateDescription(desc string, maxLen int) string {
	// Take just the first line
	if idx := strings.IndexByte(desc, '\n'); idx >= 0 {
		desc = desc[:idx]
	}
	desc = strings.TrimSpace(desc)
	if len(desc) > maxLen {
		desc = desc[:maxLen-3] + "..."
	}
	if desc == "" {
		desc = "(no description)"
	}
	return desc
}

// outputMoleculeContext checks if the agent is working on a molecule step and shows progress.
func outputMoleculeContext(ctx RoleContext) {
	// Applies to polecats, crew workers, deacon, witness, and refinery
	if ctx.Role != RolePolecat && ctx.Role != RoleCrew && ctx.Role != RoleDeacon && ctx.Role != RoleWitness && ctx.Role != RoleRefinery {
		return
	}

	// For Deacon, use special patrol molecule handling
	if ctx.Role == RoleDeacon {
		outputDeaconPatrolContext(ctx)
		return
	}

	// For Witness, use special patrol molecule handling (auto-bonds on startup)
	if ctx.Role == RoleWitness {
		outputWitnessPatrolContext(ctx)
		return
	}

	// For Refinery, use special patrol molecule handling (auto-bonds on startup)
	if ctx.Role == RoleRefinery {
		outputRefineryPatrolContext(ctx)
		return
	}

	// For polecats with root-only wisps, formula steps are shown inline
	// in outputMoleculeWorkflow() via the attached_formula field.
	// No child-based tracking needed.
}

// outputDeaconPatrolContext shows patrol molecule status for the Deacon.
// Deacon uses wisps (Wisp:true issues in main .beads/) for patrol cycles.
// Deacon is a town-level role, so it uses town root beads (not rig beads).
func outputDeaconPatrolContext(ctx RoleContext) {
	// Check if Deacon is paused - if so, output PAUSED message and skip patrol context
	paused, state, err := deacon.IsPaused(ctx.TownRoot)
	if err == nil && paused {
		outputDeaconPausedMessage(state)
		return
	}

	cfg := PatrolConfig{
		RoleName:      "deacon",
		PatrolMolName: constants.MolDeaconPatrol,
		BeadsDir:      ctx.TownRoot, // Town-level role uses town root beads
		Assignee:      "deacon",
		HeaderEmoji:   "🔄",
		HeaderTitle:   "Patrol Status (Wisp-based)",
		WorkLoopSteps: []string{
			"Work through each patrol step in sequence (see checklist below)",
			"At cycle end:\n   - If context LOW:\n     * Report and loop: `" + cli.Name() + " patrol report --summary \"<brief summary of observations>\"`\n     * This closes the current patrol and starts a new cycle\n   - If context HIGH:\n     * Send handoff: `" + cli.Name() + " handoff -s \"Deacon patrol\" -m \"<observations>\"`\n     * Exit cleanly (daemon respawns fresh session)",
		},
	}
	outputPatrolContext(cfg)
	showFormulaSteps(constants.MolDeaconPatrol, "Patrol Steps", ctx.TownRoot, ctx.Rig)
}

// outputWitnessPatrolContext shows patrol molecule status for the Witness.
// Witness AUTO-BONDS its patrol molecule on startup if one isn't already running.
func outputWitnessPatrolContext(ctx RoleContext) {
	if stopped, reason := IsRigParkedOrDocked(ctx.TownRoot, ctx.Rig); stopped {
		fmt.Printf("\n⏸️  Rig %s is %s — skipping patrol wisp generation.\n", ctx.Rig, reason)
		return
	}
	extraVars := buildWitnessPatrolVars(ctx)
	cfg := PatrolConfig{
		RoleName:      "witness",
		PatrolMolName: constants.MolWitnessPatrol,
		BeadsDir:      ctx.TownRoot,
		Assignee:      ctx.Rig + "/witness",
		HeaderEmoji:   constants.EmojiWitness,
		HeaderTitle:   "Witness Patrol Status",
		ExtraVars:     extraVars,
		WorkLoopSteps: []string{
			"Work through each patrol step in sequence (see checklist below)",
			"At cycle end:\n   - If context LOW:\n     * Report and loop: `" + cli.Name() + " patrol report --summary \"<brief summary of observations>\"`\n     * This closes the current patrol and starts a new cycle\n   - If context HIGH:\n     * Send handoff: `" + cli.Name() + " handoff -s \"Witness patrol\" -m \"<observations>\"`\n     * Exit cleanly (daemon respawns fresh session)",
		},
	}
	outputPatrolContext(cfg)
	showFormulaSteps(constants.MolWitnessPatrol, "Patrol Steps", ctx.TownRoot, ctx.Rig, extraVars)
}

// outputRefineryPatrolContext shows patrol molecule status for the Refinery.
// Refinery AUTO-BONDS its patrol molecule on startup if one isn't already running.
func outputRefineryPatrolContext(ctx RoleContext) {
	if stopped, reason := IsRigParkedOrDocked(ctx.TownRoot, ctx.Rig); stopped {
		fmt.Printf("\n⏸️  Rig %s is %s — skipping patrol wisp generation.\n", ctx.Rig, reason)
		return
	}
	if stop, err := refinery.ActiveSafetyStop(ctx.TownRoot, ctx.Rig); err != nil {
		style.PrintWarning("could not check refinery safety stop: %v", err)
		return
	} else if stop != nil {
		fmt.Printf("\nRefinery %s is %s; skipping patrol wisp generation.\n", ctx.Rig, stop.Reason())
		return
	}
	cfg := PatrolConfig{
		RoleName:      "refinery",
		PatrolMolName: constants.MolRefineryPatrol,
		BeadsDir:      ctx.TownRoot,
		Assignee:      ctx.Rig + "/refinery",
		HeaderEmoji:   "🔧",
		HeaderTitle:   "Refinery Patrol Status",
		ExtraVars:     buildRefineryPatrolVars(ctx),
		WorkLoopSteps: []string{
			"Work through each patrol step in sequence (see checklist below)",
			"At cycle end:\n   - If context LOW:\n     * Report and loop: `" + cli.Name() + " patrol report --summary \"<brief summary of observations>\"`\n     * This closes the current patrol and starts a new cycle\n   - If context HIGH:\n     * Send handoff: `" + cli.Name() + " handoff -s \"Refinery patrol\" -m \"<observations>\"`\n     * Exit cleanly (daemon respawns fresh session)",
		},
	}
	outputPatrolContext(cfg)
	showFormulaStepsFull(constants.MolRefineryPatrol, ctx.TownRoot, ctx.Rig, cfg.ExtraVars)
}

// buildWitnessPatrolVars returns --var key=value strings for the witness
// patrol formula. Injects rig name and prefix so the formula can construct
// agent bead IDs without hardcoding the "gt" prefix (gt-48ay).
func buildWitnessPatrolVars(ctx RoleContext) []string {
	var vars []string
	if ctx.TownRoot == "" || ctx.Rig == "" {
		return vars
	}
	vars = append(vars, fmt.Sprintf("rig=%s", ctx.Rig))
	prefix := beads.GetPrefixForRig(ctx.TownRoot, ctx.Rig)
	vars = append(vars, fmt.Sprintf("prefix=%s", prefix))
	return vars
}

// buildRefineryPatrolVars loads rig MQ settings and returns --var key=value
// strings for the refinery patrol formula.
func buildRefineryPatrolVars(ctx RoleContext) []string {
	var vars []string
	if ctx.TownRoot == "" || ctx.Rig == "" {
		return vars
	}
	rigPath := filepath.Join(ctx.TownRoot, ctx.Rig)

	// Always inject target_branch from rig config — this is independent of
	// merge queue settings and must not be gated behind MQ existence.
	// Without this, rigs with no settings/config.json or no merge_queue
	// section get the formula default ("main") instead of their configured
	// default_branch.
	defaultBranch := "main"
	rigCfg, err := rig.LoadRigConfig(rigPath)
	if err == nil && rigCfg != nil && rigCfg.DefaultBranch != "" {
		defaultBranch = rigCfg.DefaultBranch
	}
	vars = append(vars, fmt.Sprintf("target_branch=%s", defaultBranch))

	// MQ-specific vars: try settings/config.json first (legacy format), then
	// fall back to the layered rig config (bead labels / wisp layer).
	settingsPath := filepath.Join(rigPath, "settings", "config.json")
	settings, sErr := config.LoadRigSettings(settingsPath)
	if sErr == nil && settings != nil && settings.MergeQueue != nil {
		mq := settings.MergeQueue
		vars = append(vars, fmt.Sprintf("integration_branch_refinery_enabled=%t", mq.IsRefineryIntegrationEnabled()))
		vars = append(vars, fmt.Sprintf("integration_branch_auto_land=%t", mq.IsIntegrationBranchAutoLandEnabled()))
		vars = append(vars, fmt.Sprintf("run_tests=%t", mq.IsRunTestsEnabled()))
		if mq.SetupCommand != "" {
			vars = append(vars, fmt.Sprintf("setup_command=%s", mq.SetupCommand))
		}
		if mq.TypecheckCommand != "" {
			vars = append(vars, fmt.Sprintf("typecheck_command=%s", mq.TypecheckCommand))
		}
		if mq.LintCommand != "" {
			vars = append(vars, fmt.Sprintf("lint_command=%s", mq.LintCommand))
		}
		if mq.TestCommand != "" {
			vars = append(vars, fmt.Sprintf("test_command=%s", mq.TestCommand))
		}
		if mq.BuildCommand != "" {
			vars = append(vars, fmt.Sprintf("build_command=%s", mq.BuildCommand))
		}
		vars = append(vars, fmt.Sprintf("delete_merged_branches=%t", mq.IsDeleteMergedBranchesEnabled()))
		vars = append(vars, fmt.Sprintf("judgment_enabled=%t", mq.IsJudgmentEnabled()))
		vars = append(vars, fmt.Sprintf("review_depth=%s", mq.GetReviewDepth()))
		if mq.MergeStrategy != "" {
			vars = append(vars, fmt.Sprintf("merge_strategy=%s", mq.MergeStrategy))
		}
		vars = append(vars, fmt.Sprintf("require_review=%t", mq.IsRequireReviewEnabled()))
		return vars
	}

	// Fallback: read command vars from rig identity bead labels.
	// This is the path for rigs using `gt rig config set --global` (bead layer).
	// We use native bd routing (no explicit BEADS_DIR) to avoid dolt database
	// name mismatches that occur when bypassing the routing system.
	if rigCfg != nil && rigCfg.Beads != nil && rigCfg.Beads.Prefix != "" {
		rigBeadID := beads.RigBeadIDWithPrefix(rigCfg.Beads.Prefix, ctx.Rig)
		bd := beads.New(ctx.TownRoot)
		if issue, err := bd.Show(rigBeadID); err == nil {
			labelMap := make(map[string]string, len(issue.Labels))
			for _, label := range issue.Labels {
				if idx := strings.IndexByte(label, ':'); idx > 0 {
					labelMap[label[:idx]] = label[idx+1:]
				}
			}
			for _, key := range []string{"integration_branch_refinery_enabled", "integration_branch_auto_land", "run_tests", "delete_merged_branches", "setup_command", "typecheck_command", "lint_command", "test_command", "build_command", "merge_strategy", "require_review"} {
				if val := labelMap[key]; val != "" {
					vars = append(vars, fmt.Sprintf("%s=%s", key, val))
				}
			}
		}
	}
	return vars
}

// applyFormulaOverlays loads and applies overlays to a parsed formula.
// It emits warnings for stale step IDs and, in --explain mode, shows which overlays are active.
func applyFormulaOverlays(f *formula.Formula, formulaName, townRoot, rigName string) {
	if townRoot == "" {
		return
	}

	overlay, err := formula.LoadFormulaOverlay(formulaName, townRoot, rigName)
	if err != nil {
		style.PrintWarning("could not load overlay for %s: %v", formulaName, err)
		return
	}
	if overlay == nil {
		explain(true, fmt.Sprintf("Formula overlay: no overlay found for %s", formulaName))
		return
	}

	explain(true, fmt.Sprintf("Formula overlay: applying %d override(s) for %s (rig=%s)", len(overlay.StepOverrides), formulaName, rigName))
	for _, so := range overlay.StepOverrides {
		explain(true, fmt.Sprintf("  overlay: step_id=%s mode=%s", so.StepID, so.Mode))
	}

	warnings := formula.ApplyOverlays(f, overlay)
	for _, w := range warnings {
		style.PrintWarning("formula overlay: %s", w)
	}
}
