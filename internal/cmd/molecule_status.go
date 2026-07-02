package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Note: Agent field parsing is now in internal/beads/fields.go (AgentFields, ParseAgentFields)

// buildAgentBeadID constructs the agent bead ID from an agent identity.
// Uses canonical naming: prefix-rig-role-name
// Town-level agents use hq- prefix; rig-level agents use rig's prefix.
// Examples:
//   - "mayor" -> "hq-mayor"
//   - "deacon" -> "hq-deacon"
//   - "gastown/witness" -> "gt-gastown-witness"
//   - "gastown/refinery" -> "gt-gastown-refinery"
//   - "gastown/nux" (polecat) -> "gt-gastown-polecat-nux"
//   - "gastown/crew/max" -> "gt-gastown-crew-max"
//
// If role is unknown, it tries to infer from the identity string.
// townRoot is needed to look up the rig's configured prefix.
func buildAgentBeadID(identity string, role Role, townRoot string) string {
	parts := strings.Split(identity, "/")

	// Helper to get prefix for a rig
	getPrefix := func(rig string) string {
		return config.GetRigPrefix(townRoot, rig)
	}

	// If role is unknown or empty, try to infer from identity
	if role == RoleUnknown || role == Role("") {
		switch {
		case identity == "mayor":
			return beads.MayorBeadIDTown()
		case identity == "deacon":
			return beads.DeaconBeadIDTown()
		case identity == "deacon-boot":
			return beads.DogBeadIDTown("boot")
		case len(parts) == 2 && parts[1] == "witness":
			return beads.WitnessBeadIDWithPrefix(getPrefix(parts[0]), parts[0])
		case len(parts) == 2 && parts[1] == "refinery":
			return beads.RefineryBeadIDWithPrefix(getPrefix(parts[0]), parts[0])
		case len(parts) == 2:
			// Assume rig/name is a polecat
			return beads.PolecatBeadIDWithPrefix(getPrefix(parts[0]), parts[0], parts[1])
		case len(parts) == 3 && parts[1] == "crew":
			// rig/crew/name - crew member
			return beads.CrewBeadIDWithPrefix(getPrefix(parts[0]), parts[0], parts[2])
		case len(parts) == 3 && parts[1] == "polecats":
			// rig/polecats/name - explicit polecat
			return beads.PolecatBeadIDWithPrefix(getPrefix(parts[0]), parts[0], parts[2])
		default:
			return ""
		}
	}

	switch role {
	case RoleMayor:
		return beads.MayorBeadIDTown()
	case RoleDeacon:
		return beads.DeaconBeadIDTown()
	case RoleWitness:
		if len(parts) >= 1 {
			return beads.WitnessBeadIDWithPrefix(getPrefix(parts[0]), parts[0])
		}
		return ""
	case RoleRefinery:
		if len(parts) >= 1 {
			return beads.RefineryBeadIDWithPrefix(getPrefix(parts[0]), parts[0])
		}
		return ""
	case RolePolecat:
		// Handle both 2-part (rig/name) and 3-part (rig/polecats/name) formats
		if len(parts) == 3 && parts[1] == "polecats" {
			return beads.PolecatBeadIDWithPrefix(getPrefix(parts[0]), parts[0], parts[2])
		}
		if len(parts) >= 2 {
			return beads.PolecatBeadIDWithPrefix(getPrefix(parts[0]), parts[0], parts[1])
		}
		return ""
	case RoleCrew:
		if len(parts) >= 3 && parts[1] == "crew" {
			return beads.CrewBeadIDWithPrefix(getPrefix(parts[0]), parts[0], parts[2])
		}
		return ""
	case RoleBoot:
		// Boot is a deacon dog — uses town-level dog bead ID
		return beads.DogBeadIDTown("boot")
	default:
		return ""
	}
}

// MoleculeProgressInfo contains progress information for a molecule instance.
type MoleculeProgressInfo struct {
	RootID       string   `json:"root_id"`
	RootTitle    string   `json:"root_title"`
	MoleculeID   string   `json:"molecule_id,omitempty"`
	TotalSteps   int      `json:"total_steps"`
	DoneSteps    int      `json:"done_steps"`
	InProgress   int      `json:"in_progress_steps"`
	ReadySteps   []string `json:"ready_steps"`
	BlockedSteps []string `json:"blocked_steps"`
	Percent      int      `json:"percent_complete"`
	Complete     bool     `json:"complete"`
}

// MoleculeStatusInfo contains status information for an agent's work.
type MoleculeStatusInfo struct {
	Target           string                `json:"target"`
	Role             string                `json:"role"`
	AgentBeadID      string                `json:"agent_bead_id,omitempty"` // The agent bead if found
	HasWork          bool                  `json:"has_work"`
	PinnedBead       *beads.Issue          `json:"pinned_bead,omitempty"`
	AttachedMolecule string                `json:"attached_molecule,omitempty"`
	AttachedFormula  string                `json:"attached_formula,omitempty"`
	AttachedAt       string                `json:"attached_at,omitempty"`
	AttachedArgs     string                `json:"attached_args,omitempty"`
	AttachedVars     []string              `json:"attached_vars,omitempty"`
	IsWisp           bool                  `json:"is_wisp"`
	Progress         *MoleculeProgressInfo `json:"progress,omitempty"`
	NextAction       string                `json:"next_action,omitempty"`
}

// MoleculeCurrentInfo contains info about what an agent should be working on.
type MoleculeCurrentInfo struct {
	Identity      string `json:"identity"`
	HandoffID     string `json:"handoff_id,omitempty"`
	HandoffTitle  string `json:"handoff_title,omitempty"`
	MoleculeID    string `json:"molecule_id,omitempty"`
	MoleculeTitle string `json:"molecule_title,omitempty"`
	StepsComplete int    `json:"steps_complete"`
	StepsTotal    int    `json:"steps_total"`
	CurrentStepID string `json:"current_step_id,omitempty"`
	CurrentStep   string `json:"current_step,omitempty"`
	Status        string `json:"status"` // "working", "naked", "complete", "blocked"
}

func runMoleculeProgress(cmd *cobra.Command, args []string) error {
	rootID := args[0]

	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Get the root issue
	root, err := b.Show(rootID)
	if err != nil {
		return fmt.Errorf("getting root issue: %w", err)
	}

	// Find all children of the root issue
	children, err := b.List(beads.ListOptions{
		Parent:   rootID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		return fmt.Errorf("listing children: %w", err)
	}

	if len(children) == 0 {
		return fmt.Errorf("no steps found for %s (not a molecule root?)", rootID)
	}

	// Build progress info
	progress := MoleculeProgressInfo{
		RootID:    rootID,
		RootTitle: root.Title,
	}

	// Try to find molecule ID from first child's description
	for _, child := range children {
		if molID := extractMoleculeID(child.Description); molID != "" {
			progress.MoleculeID = molID
			break
		}
	}

	// Build set of closed issue IDs and collect open step IDs for dependency checking
	closedIDs := make(map[string]bool)
	var openStepIDs []string
	for _, child := range children {
		if child.Status == "closed" {
			closedIDs[child.ID] = true
		} else if child.Status == "open" {
			openStepIDs = append(openStepIDs, child.ID)
		}
	}

	// Fetch full details for open steps to get dependency info.
	// bd list doesn't return dependencies, but bd show does.
	var openStepsMap map[string]*beads.Issue
	if len(openStepIDs) > 0 {
		openStepsMap, err = b.ShowMultiple(openStepIDs)
		if err != nil {
			// Non-fatal: continue without dependency info (all open steps will be "ready")
			openStepsMap = make(map[string]*beads.Issue)
		}
	}

	// Categorize steps
	for _, child := range children {
		progress.TotalSteps++

		switch child.Status {
		case "closed":
			progress.DoneSteps++
		case "in_progress":
			progress.InProgress++
		case "open":
			// Get full step info with dependencies
			step := openStepsMap[child.ID]

			// Check if all dependencies are closed using Dependencies field
			// (from bd show), not DependsOn (which is empty from bd list).
			// Only "blocks" type dependencies block progress - ignore "parent-child".
			allDepsClosed := true
			hasBlockingDeps := false
			var deps []beads.IssueDep
			if step != nil {
				deps = step.Dependencies
			}
			for _, dep := range deps {
				if !isBlockingDepType(dep.DependencyType) {
					continue // Skip parent-child and other non-blocking relationships
				}
				hasBlockingDeps = true
				if !closedIDs[dep.ID] {
					allDepsClosed = false
					break
				}
			}

			if !hasBlockingDeps || allDepsClosed {
				progress.ReadySteps = append(progress.ReadySteps, child.ID)
			} else {
				progress.BlockedSteps = append(progress.BlockedSteps, child.ID)
			}
		}
	}

	// Sort ready steps by sequence number so step 1 comes before step 2, etc.
	sortStepIDsBySequence(progress.ReadySteps)

	// Calculate completion percentage
	if progress.TotalSteps > 0 {
		progress.Percent = (progress.DoneSteps * 100) / progress.TotalSteps
	}
	progress.Complete = progress.DoneSteps == progress.TotalSteps

	// JSON output
	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(progress)
	}

	// Human-readable output
	fmt.Printf("\n%s %s\n\n", style.Bold.Render("🧬 Molecule Progress:"), root.Title)
	fmt.Printf("  Root: %s\n", rootID)
	if progress.MoleculeID != "" {
		fmt.Printf("  Molecule: %s\n", progress.MoleculeID)
	}
	fmt.Println()

	// Progress bar
	barWidth := 20
	filled := (progress.Percent * barWidth) / 100
	bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
	fmt.Printf("  [%s] %d%% (%d/%d)\n\n", bar, progress.Percent, progress.DoneSteps, progress.TotalSteps)

	// Step status
	fmt.Printf("  Done:        %d\n", progress.DoneSteps)
	fmt.Printf("  In Progress: %d\n", progress.InProgress)
	fmt.Printf("  Ready:       %d", len(progress.ReadySteps))
	if len(progress.ReadySteps) > 0 {
		fmt.Printf(" (%s)", strings.Join(progress.ReadySteps, ", "))
	}
	fmt.Println()
	fmt.Printf("  Blocked:     %d\n", len(progress.BlockedSteps))

	if progress.Complete {
		fmt.Printf("\n  %s\n", style.Bold.Render("✓ Molecule complete!"))
	}

	return nil
}

// extractMoleculeID extracts the molecule ID from an issue's description.
func extractMoleculeID(description string) string {
	lines := strings.Split(description, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "instantiated_from:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "instantiated_from:"))
		}
	}
	return ""
}

func runMoleculeStatus(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent
	var target string
	var roleCtx RoleContext
	validationRole := RoleUnknown

	if len(args) > 0 {
		// Explicit target provided
		target = normalizeHookShowTarget(args[0])
		callerCtx := detectRole(cwd, townRoot)
		validationRole = callerCtx.Role
	} else {
		// Use cwd-based detection for status display
		// This ensures we show the hook for the agent whose directory we're in,
		// not the agent from the GT_ROLE env var (which might be different if
		// we cd'd into another rig's crew/polecat directory)
		roleCtx = detectRole(cwd, townRoot)
		if roleCtx.Role == RoleUnknown {
			// Fall back to GT_ROLE when cwd doesn't identify an agent
			// (e.g., at rig root like ~/gt/beads instead of ~/gt/beads/witness)
			roleCtx, _ = GetRoleWithContext(cwd, townRoot)
		}
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}
		validationRole = roleCtx.Role
	}
	if err := ensureRoleWorktreeIntegrity(cwd, townRoot, validationRole); err != nil {
		return err
	}

	// Find beads directory.
	// First try CWD-based discovery, then resolve to the correct rig database
	// based on the agent's identity. Without this, CWD at the town root (~/gt)
	// queries the hq database instead of the rig's database where hooked beads
	// actually live. See bd-hook-status-cwd-bug.
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	// Resolve to the agent's rig beads directory if CWD-based discovery
	// found the wrong database. This matches runHookShow's resolution logic.
	if !isTownLevelRole(target) && townRoot != "" {
		workDir = resolveHookLookupWorkDir(workDir, target, townRoot)
	}

	b := beads.New(workDir)

	// Build status info
	status := MoleculeStatusInfo{
		Target: target,
		Role:   string(roleCtx.Role),
	}

	// lookupHookedWork performs the full multi-step hook lookup for target.
	// Called in a retry loop for polecats to handle Dolt propagation lag.
	lookupHookedWork := func() *beads.Issue {
		// Resolve agent bead ID for display purposes only.
		// Agent bead's hook_bead field is no longer maintained (updateAgentHookBead is
		// a no-op since hq-l6mm5), so reading it returns stale data. See GH#2371.
		agentBeadID := buildAgentBeadID(target, roleCtx.Role, townRoot)
		if agentBeadID != "" {
			agentBeadPath := beads.ResolveHookDir(townRoot, agentBeadID, workDir)
			agentB := b
			if agentBeadPath != workDir {
				agentB = beads.New(agentBeadPath)
			}
			agentBead, err := agentB.Show(agentBeadID)
			if err == nil && beads.IsAgentBead(agentBead) {
				status.AgentBeadID = agentBeadID
			}
		}

		// Query for active work using the authoritative source: bead status + assignee.
		hookedBeads, err := listAssignedActiveWork(b, target)
		if err != nil {
			return nil
		}

		// For town-level roles (mayor, deacon), scan all rigs if nothing found locally
		if len(hookedBeads) == 0 && isTownLevelRole(target) {
			hookedBeads = scanAllRigsForHookedBeads(townRoot, target)
		}

		// For rig-level agents (polecats, crew), also search town-level beads.
		// When the Mayor slings an hq-* bead to a polecat, the bead lives in
		// townRoot/.beads, not the rig's .beads database.
		// See: https://github.com/steveyegge/gastown/issues/1438
		if len(hookedBeads) == 0 && !isTownLevelRole(target) && townRoot != "" {
			townB := beads.New(filepath.Join(townRoot, ".beads"))
			if townWork, err := listAssignedActiveWork(townB, target); err == nil && len(townWork) > 0 {
				hookedBeads = townWork
			}
		}

		if len(hookedBeads) > 0 {
			return hookedBeads[0]
		}
		return nil
	}

	// Run the lookup. In polecat context, retry with backoff to handle Dolt
	// propagation lag between the sling write and the nudge arriving here.
	// See: https://github.com/steveyegge/gastown/issues/2389
	var hookBead *beads.Issue
	isPolecat := roleCtx.Role == RolePolecat ||
		(os.Getenv("GT_ROLE") != "" && func() bool {
			r, _, _ := parseRoleString(os.Getenv("GT_ROLE"))
			return r == RolePolecat
		}())

	hookBead = lookupHookedWork()
	if hookBead == nil && isPolecat {
		const maxRetries = 5
		const baseBackoff = 500 * time.Millisecond
		const maxBackoff = 8 * time.Second
		for attempt := 1; attempt <= maxRetries; attempt++ {
			backoff := slingBackoff(attempt, baseBackoff, maxBackoff)
			time.Sleep(backoff)
			hookBead = lookupHookedWork()
			if hookBead != nil {
				break
			}
		}
	}

	if hookBead != nil {
		status.HasWork = true
		status.PinnedBead = hookBead

		// Check for attached molecule
		attachment := beads.ParseAttachmentFields(hookBead)
		if attachment != nil {
			status.AttachedMolecule = attachment.AttachedMolecule
			status.AttachedFormula = attachment.AttachedFormula
			status.AttachedAt = attachment.AttachedAt
			status.AttachedArgs = attachment.AttachedArgs
			status.AttachedVars = attachment.AttachedVars

			status.IsWisp = strings.Contains(hookBead.Description, "wisp: true") ||
				strings.Contains(hookBead.Description, "is_wisp: true")

			if attachment.AttachedMolecule != "" {
				progress, _ := getMoleculeProgressInfo(b, attachment.AttachedMolecule)
				status.Progress = progress
				status.NextAction = determineNextAction(status)
			} else if attachment.AttachedFormula != "" {
				progress, _ := getMoleculeProgressInfo(b, hookBead.ID)
				status.Progress = progress
				status.NextAction = determineNextAction(status)
			}
		}
	}

	// Determine next action if no work is slung
	if !status.HasWork {
		status.NextAction = "Check inbox for work assignments: gt mail inbox"
	} else if status.AttachedMolecule == "" && status.AttachedFormula == "" {
		status.NextAction = "Attach a molecule to start work: gt mol attach <bead-id> <molecule-id>"
	} else if status.AttachedFormula != "" && status.NextAction == "" && status.PinnedBead != nil {
		status.NextAction = "Show the workflow steps: gt prime or bd mol current " + status.PinnedBead.ID
	}

	// JSON output
	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	// Human-readable output
	outputMoleculeStatus(status)
	return nil
}

// extractRoleFromIdentity extracts the role name from an agent identity string
// for handoff bead lookup. Handles trailing slashes (e.g. "mayor/" → "mayor")
// and compound paths (e.g. "gastown/crew/jack" → "jack").
func extractRoleFromIdentity(target string) string {
	target = strings.TrimRight(target, "/")
	parts := strings.Split(target, "/")
	return parts[len(parts)-1]
}

// buildAgentIdentity constructs the agent identity string from role context.
// Town-level agents (mayor, deacon) use trailing slash to match the format
// used when setting assignee on hooked beads (see resolveSelfTarget in sling.go).
func buildAgentIdentity(ctx RoleContext) string {
	switch ctx.Role {
	case RoleMayor:
		return "mayor/"
	case RoleDeacon:
		return "deacon/"
	case RoleBoot:
		return "deacon/boot"
	case RoleWitness:
		return ctx.Rig + "/witness"
	case RoleRefinery:
		return ctx.Rig + "/refinery"
	case RolePolecat:
		return ctx.Rig + "/polecats/" + ctx.Polecat
	case RoleCrew:
		return ctx.Rig + "/crew/" + ctx.Polecat
	case RoleDog:
		if ctx.Polecat == "" {
			return ""
		}
		return "deacon/dogs/" + ctx.Polecat
	default:
		return ""
	}
}

// getMoleculeProgressInfo gets progress info for a molecule instance.
func getMoleculeProgressInfo(b *beads.Beads, moleculeRootID string) (*MoleculeProgressInfo, error) {
	// Get the molecule root issue
	root, err := b.Show(moleculeRootID)
	if err != nil {
		return nil, fmt.Errorf("getting molecule root: %w", err)
	}

	// Find all children of the root issue
	children, err := b.List(beads.ListOptions{
		Parent:   moleculeRootID,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		return nil, fmt.Errorf("listing children: %w", err)
	}

	if len(children) == 0 {
		// No children - might be a simple issue, not a molecule
		return nil, nil
	}

	// Build progress info
	progress := &MoleculeProgressInfo{
		RootID:    moleculeRootID,
		RootTitle: root.Title,
	}

	// Try to find molecule ID from first child's description
	for _, child := range children {
		if molID := extractMoleculeID(child.Description); molID != "" {
			progress.MoleculeID = molID
			break
		}
	}

	// Build set of closed issue IDs and collect open step IDs for dependency checking
	closedIDs := make(map[string]bool)
	var openStepIDs []string
	for _, child := range children {
		if child.Status == "closed" {
			closedIDs[child.ID] = true
		} else if child.Status == "open" {
			openStepIDs = append(openStepIDs, child.ID)
		}
	}

	// Fetch full details for open steps to get dependency info.
	// bd list doesn't return dependencies, but bd show does.
	var openStepsMap map[string]*beads.Issue
	if len(openStepIDs) > 0 {
		openStepsMap, err = b.ShowMultiple(openStepIDs)
		if err != nil {
			// Non-fatal: continue without dependency info (all open steps will be "ready")
			openStepsMap = make(map[string]*beads.Issue)
		}
	}

	// Categorize steps
	for _, child := range children {
		progress.TotalSteps++

		switch child.Status {
		case "closed":
			progress.DoneSteps++
		case "in_progress":
			progress.InProgress++
		case "open":
			// Get full step info with dependencies
			step := openStepsMap[child.ID]

			// Check if all dependencies are closed using Dependencies field
			// (from bd show), not DependsOn (which is empty from bd list).
			// Only "blocks" type dependencies block progress - ignore "parent-child".
			allDepsClosed := true
			hasBlockingDeps := false
			var deps []beads.IssueDep
			if step != nil {
				deps = step.Dependencies
			}
			for _, dep := range deps {
				if !isBlockingDepType(dep.DependencyType) {
					continue // Skip parent-child and other non-blocking relationships
				}
				hasBlockingDeps = true
				if !closedIDs[dep.ID] {
					allDepsClosed = false
					break
				}
			}

			if !hasBlockingDeps || allDepsClosed {
				progress.ReadySteps = append(progress.ReadySteps, child.ID)
			} else {
				progress.BlockedSteps = append(progress.BlockedSteps, child.ID)
			}
		}
	}

	// Sort ready steps by sequence number so step 1 comes before step 2, etc.
	sortStepIDsBySequence(progress.ReadySteps)

	// Calculate completion percentage
	if progress.TotalSteps > 0 {
		progress.Percent = (progress.DoneSteps * 100) / progress.TotalSteps
	}
	progress.Complete = progress.DoneSteps == progress.TotalSteps

	return progress, nil
}

// determineNextAction suggests the next action based on status.
func determineNextAction(status MoleculeStatusInfo) string {
	if status.Progress == nil {
		return ""
	}

	if status.Progress.Complete {
		return "Molecule complete! Close the bead: bd close " + status.PinnedBead.ID
	}

	if status.Progress.InProgress > 0 {
		return "Continue working on in-progress steps"
	}

	if len(status.Progress.ReadySteps) > 0 {
		return fmt.Sprintf("Start next ready step: bd update %s --status=in_progress", status.Progress.ReadySteps[0])
	}

	if len(status.Progress.BlockedSteps) > 0 {
		return "All remaining steps are blocked - waiting on dependencies"
	}

	return ""
}

// outputMoleculeStatus outputs human-readable status.
func outputMoleculeStatus(status MoleculeStatusInfo) {
	// Header with hook icon
	fmt.Printf("\n%s Hook Status: %s\n", style.Bold.Render("🪝"), status.Target)
	if status.Role != "" && status.Role != "unknown" {
		fmt.Printf("Role: %s\n", status.Role)
	}
	fmt.Println()

	if !status.HasWork {
		fmt.Printf("%s\n", style.Dim.Render("Nothing on hook - no work slung"))
		fmt.Printf("\n%s %s\n", style.Bold.Render("Next:"), status.NextAction)
		return
	}

	// Show hooked bead info
	if status.PinnedBead == nil {
		fmt.Printf("%s\n", style.Dim.Render("Work indicated but no bead found"))
		return
	}

	// AUTONOMOUS MODE banner - hooked work triggers autonomous execution
	fmt.Println(style.Bold.Render("🚀 AUTONOMOUS MODE - Work on hook triggers immediate execution"))
	fmt.Println()

	// Check if the hooked bead is already closed (someone closed it externally)
	if status.PinnedBead.Status == "closed" {
		fmt.Printf("%s Hooked bead %s is already closed!\n", style.Bold.Render("⚠"), status.PinnedBead.ID)
		fmt.Printf("   Title: %s\n", status.PinnedBead.Title)
		fmt.Printf("   This work was completed elsewhere. Clear your hook with: gt unsling\n")
		return
	}

	// Check if this is a mail bead - display mail-specific format
	if status.PinnedBead.Type == "message" {
		sender := extractMailSender(status.PinnedBead.Labels)
		fmt.Printf("%s %s (mail)\n", style.Bold.Render("🪝 Hook:"), status.PinnedBead.ID)
		if sender != "" {
			fmt.Printf("   From: %s\n", sender)
		}
		fmt.Printf("   Subject: %s\n", status.PinnedBead.Title)
		fmt.Printf("   Run: gt mail read %s\n", status.PinnedBead.ID)
		return
	}

	fmt.Printf("%s %s: %s\n", style.Bold.Render("🪝 Hooked:"), status.PinnedBead.ID, status.PinnedBead.Title)
	if status.AttachedFormula != "" {
		fmt.Printf("%s %s\n", style.Bold.Render("📐 Formula:"), status.AttachedFormula)
	}
	if len(status.AttachedVars) > 0 {
		fmt.Printf("%s\n", style.Bold.Render("🧩 Vars:"))
		for _, variable := range status.AttachedVars {
			fmt.Printf("   --var %s\n", variable)
		}
	}
	if status.AttachedArgs != "" {
		fmt.Printf("%s %s\n", style.Bold.Render("📋 Args:"), status.AttachedArgs)
	}
	// Show attached molecule
	if status.AttachedMolecule != "" {
		molType := "Molecule"
		if status.IsWisp {
			molType = "Wisp"
		}
		fmt.Printf("%s %s: %s\n", style.Bold.Render("🧬 "+molType+":"), status.AttachedMolecule, "")
		if status.AttachedAt != "" {
			fmt.Printf("   Attached: %s\n", status.AttachedAt)
		}
	} else if status.AttachedFormula == "" {
		fmt.Printf("%s\n", style.Dim.Render("No molecule attached (hooked bead still triggers autonomous work)"))
	}

	// Show progress if available
	if status.Progress != nil {
		fmt.Println()

		// Progress bar
		barWidth := 20
		filled := (status.Progress.Percent * barWidth) / 100
		bar := strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
		fmt.Printf("Progress: [%s] %d%% (%d/%d steps)\n",
			bar, status.Progress.Percent, status.Progress.DoneSteps, status.Progress.TotalSteps)

		// Step breakdown
		fmt.Printf("  Done:        %d\n", status.Progress.DoneSteps)
		fmt.Printf("  In Progress: %d\n", status.Progress.InProgress)
		fmt.Printf("  Ready:       %d", len(status.Progress.ReadySteps))
		if len(status.Progress.ReadySteps) > 0 && len(status.Progress.ReadySteps) <= 3 {
			fmt.Printf(" (%s)", strings.Join(status.Progress.ReadySteps, ", "))
		}
		fmt.Println()
		fmt.Printf("  Blocked:     %d\n", len(status.Progress.BlockedSteps))

		if status.Progress.Complete {
			fmt.Printf("\n%s\n", style.Bold.Render("✓ Molecule complete!"))
		}
	}

	// Git divergence warning and recent trail (gt-7w6cq)
	showGitDivergenceWarning()
	showRecentTrailSummary()

	// Next action hint
	if status.NextAction != "" {
		fmt.Printf("\n%s %s\n", style.Bold.Render("Next:"), status.NextAction)
	}
}

// showGitDivergenceWarning fetches from origin and checks if the current branch
// has diverged from its remote tracking branch, showing a warning if so.
func showGitDivergenceWarning() {
	g := git.NewGit(".")
	if !g.IsRepo() {
		return
	}

	branch, err := g.CurrentBranch()
	if err != nil || branch == "" {
		return
	}

	// Fetch quietly to get fresh remote refs. Non-fatal if it fails
	// (e.g., offline, no remote).
	_ = g.Fetch("origin")

	remote := "origin/" + branch
	ahead, aErr := g.CommitsAhead(remote, "HEAD")
	behind, bErr := g.CountCommitsBehind(remote)

	// Also check divergence from origin/main as a fallback — polecats
	// work on feature branches that may not have a remote tracking branch,
	// but we still want to warn if they're behind main.
	if aErr != nil || bErr != nil {
		// No tracking branch for current branch; check against origin/main
		ahead, aErr = g.CommitsAhead("origin/main", "HEAD")
		behind, bErr = g.CountCommitsBehind("origin/main")
		if aErr != nil || bErr != nil {
			return // Can't determine divergence at all — skip silently
		}
		remote = "origin/main"
	}

	if ahead == 0 && behind == 0 {
		return // In sync
	}

	fmt.Println()
	if ahead > 0 && behind > 0 {
		fmt.Printf("%s Branch diverged: %d ahead, %d behind %s\n",
			style.Warning.Render("⚠"), ahead, behind, remote)
		fmt.Printf("  Run 'git pull --rebase' before starting work\n")
	} else if behind > 0 {
		fmt.Printf("%s Branch is %d commits behind %s\n",
			style.Warning.Render("⚠"), behind, remote)
		fmt.Printf("  Run 'git pull' to update\n")
	} else {
		fmt.Printf("%s Branch is %d commits ahead of %s (unpushed work)\n",
			style.Dim.Render("ℹ"), ahead, remote)
	}
}

// showRecentTrailSummary shows a compact summary of recent agent activity.
// Leverages git log and beads to show what happened since last activity.
func showRecentTrailSummary() {
	g := git.NewGit(".")
	if !g.IsRepo() {
		return
	}

	// Get recent commits (last 24h) — summarize by author
	since := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	gitArgs := []string{
		"log",
		"--format=%an",
		"--since=" + since,
		"-n50",
		"--all",
	}
	gitCmd := exec.Command("git", gitArgs...)
	output, err := gitCmd.Output()
	if err != nil {
		return
	}

	// Count commits per author
	authorCounts := make(map[string]int)
	totalCommits := 0
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		authorCounts[line]++
		totalCommits++
	}

	if totalCommits == 0 {
		return
	}

	// Build compact author summary (e.g., "3 commits by darcy, 2 by nux")
	type authorCount struct {
		name  string
		count int
	}
	var authors []authorCount
	for name, count := range authorCounts {
		authors = append(authors, authorCount{name, count})
	}
	// Sort by count descending
	for i := 0; i < len(authors); i++ {
		for j := i + 1; j < len(authors); j++ {
			if authors[j].count > authors[i].count {
				authors[i], authors[j] = authors[j], authors[i]
			}
		}
	}

	var parts []string
	for i, a := range authors {
		if i >= 3 {
			remaining := len(authors) - 3
			parts = append(parts, fmt.Sprintf("+%d others", remaining))
			break
		}
		parts = append(parts, fmt.Sprintf("%d by %s", a.count, a.name))
	}

	fmt.Printf("\n%s Recent (24h): %d commits (%s)\n",
		style.Dim.Render("📍"), totalCommits, strings.Join(parts, ", "))
}

func runMoleculeCurrent(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting current directory: %w", err)
	}

	// Find town root
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}
	if townRoot == "" {
		return fmt.Errorf("not in a Gas Town workspace")
	}

	// Determine target agent identity
	var target string
	var roleCtx RoleContext

	if len(args) > 0 {
		// Explicit target provided
		target = args[0]
	} else {
		// Use cwd-based detection for status display
		// This ensures we show the hook for the agent whose directory we're in,
		// not the agent from the GT_ROLE env var (which might be different if
		// we cd'd into another rig's crew/polecat directory)
		roleCtx = detectRole(cwd, townRoot)
		if roleCtx.Role == RoleUnknown {
			// Fall back to GT_ROLE when cwd doesn't identify an agent
			// (e.g., at rig root like ~/gt/beads instead of ~/gt/beads/witness)
			roleCtx, _ = GetRoleWithContext(cwd, townRoot)
		}
		target = buildAgentIdentity(roleCtx)
		if target == "" {
			return fmt.Errorf("cannot determine agent identity (role: %s)", roleCtx.Role)
		}
	}

	// Find beads directory
	workDir, err := findLocalBeadsDir()
	if err != nil {
		return fmt.Errorf("not in a beads workspace: %w", err)
	}

	b := beads.New(workDir)

	// Extract role from target for handoff bead lookup
	role := extractRoleFromIdentity(target)

	// Find handoff bead for this identity
	handoff, err := b.FindHandoffBead(role)
	if err != nil {
		return fmt.Errorf("finding handoff bead: %w", err)
	}

	// Build current info
	info := MoleculeCurrentInfo{
		Identity: target,
	}

	if handoff == nil {
		info.Status = "naked"
		return outputMoleculeCurrent(info)
	}

	info.HandoffID = handoff.ID
	info.HandoffTitle = handoff.Title

	// Check for attached molecule
	attachment := beads.ParseAttachmentFields(handoff)
	if attachment == nil || attachment.AttachedMolecule == "" {
		info.Status = "naked"
		return outputMoleculeCurrent(info)
	}

	info.MoleculeID = attachment.AttachedMolecule

	// Get the molecule root to find its title and children
	molRoot, err := b.Show(attachment.AttachedMolecule)
	if err != nil {
		// Molecule not found - might be a template ID, still report what we have
		info.Status = "working"
		return outputMoleculeCurrent(info)
	}

	info.MoleculeTitle = molRoot.Title

	// Find all children (steps) of the molecule root
	children, err := b.List(beads.ListOptions{
		Parent:   attachment.AttachedMolecule,
		Status:   "all",
		Priority: -1,
	})
	if err != nil {
		// No steps - just an issue, not a molecule instance
		info.Status = "working"
		return outputMoleculeCurrent(info)
	}

	info.StepsTotal = len(children)

	// Build set of closed issue IDs and collect open step IDs for dependency checking
	closedIDs := make(map[string]bool)
	var inProgressSteps []*beads.Issue
	var openStepIDs []string

	for _, child := range children {
		switch child.Status {
		case "closed":
			info.StepsComplete++
			closedIDs[child.ID] = true
		case "in_progress":
			inProgressSteps = append(inProgressSteps, child)
		case "open":
			openStepIDs = append(openStepIDs, child.ID)
		}
	}

	// Fetch full details for open steps to get dependency info.
	// bd list doesn't return dependencies, but bd show does.
	var openStepsMap map[string]*beads.Issue
	if len(openStepIDs) > 0 {
		openStepsMap, _ = b.ShowMultiple(openStepIDs)
		if openStepsMap == nil {
			openStepsMap = make(map[string]*beads.Issue)
		}
	}

	// Find ready steps (open with all deps closed)
	var readySteps []*beads.Issue
	for _, stepID := range openStepIDs {
		step := openStepsMap[stepID]
		if step == nil {
			continue
		}

		// Check dependencies using Dependencies field (from bd show),
		// not DependsOn (which is empty from bd list).
		allDepsClosed := true
		hasBlockingDeps := false
		for _, dep := range step.Dependencies {
			if !isBlockingDepType(dep.DependencyType) {
				continue // Skip parent-child and other non-blocking relationships
			}
			hasBlockingDeps = true
			if !closedIDs[dep.ID] {
				allDepsClosed = false
				break
			}
		}
		if !hasBlockingDeps || allDepsClosed {
			readySteps = append(readySteps, step)
		}
	}

	// Sort ready steps by sequence number so step 1 comes before step 2, etc.
	sortStepsBySequence(readySteps)

	// Determine current step and status
	if info.StepsComplete == info.StepsTotal && info.StepsTotal > 0 {
		info.Status = "complete"
	} else if len(inProgressSteps) > 0 {
		// First in-progress step is the current one
		info.Status = "working"
		info.CurrentStepID = inProgressSteps[0].ID
		info.CurrentStep = inProgressSteps[0].Title
	} else if len(readySteps) > 0 {
		// First ready step is the next to work on
		info.Status = "working"
		info.CurrentStepID = readySteps[0].ID
		info.CurrentStep = readySteps[0].Title
	} else if info.StepsTotal > 0 {
		// Has steps but none ready or in-progress -> blocked
		info.Status = "blocked"
	} else {
		info.Status = "working"
	}

	return outputMoleculeCurrent(info)
}

// outputMoleculeCurrent outputs the current info in the appropriate format.
func outputMoleculeCurrent(info MoleculeCurrentInfo) error {
	if moleculeJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(info)
	}

	// Human-readable output matching spec format
	fmt.Printf("Identity: %s\n", info.Identity)

	if info.HandoffID != "" {
		fmt.Printf("Handoff:  %s (%s)\n", info.HandoffID, info.HandoffTitle)
	} else {
		fmt.Printf("Handoff:  %s\n", style.Dim.Render("(none)"))
	}

	if info.MoleculeID != "" {
		if info.MoleculeTitle != "" {
			fmt.Printf("Molecule: %s (%s)\n", info.MoleculeID, info.MoleculeTitle)
		} else {
			fmt.Printf("Molecule: %s\n", info.MoleculeID)
		}
	} else {
		fmt.Printf("Molecule: %s\n", style.Dim.Render("(none attached)"))
	}

	if info.StepsTotal > 0 {
		fmt.Printf("Progress: %d/%d steps complete\n", info.StepsComplete, info.StepsTotal)
	}

	if info.CurrentStepID != "" {
		fmt.Printf("Current:  %s - %s\n", info.CurrentStepID, info.CurrentStep)
	} else if info.Status == "naked" {
		fmt.Printf("Status:   %s\n", style.Dim.Render("naked - awaiting work assignment"))
	} else if info.Status == "complete" {
		fmt.Printf("Status:   %s\n", style.Bold.Render("complete - molecule finished"))
	} else if info.Status == "blocked" {
		fmt.Printf("Status:   %s\n", style.Dim.Render("blocked - waiting on dependencies"))
	}

	return nil
}

// isTownLevelRole returns true if the agent ID is a town-level role.
// Town-level roles (Mayor, Deacon) operate from the town root and may have
// pinned beads in any rig's beads directory.
// Accepts both "mayor" and "mayor/" formats for compatibility.
func isTownLevelRole(agentID string) bool {
	return agentID == "mayor" || agentID == "mayor/" ||
		agentID == "deacon" || agentID == "deacon/" ||
		agentID == "deacon/boot" || agentID == "deacon-boot"
}

// extractMailSender extracts the sender from mail bead labels.
// Mail beads have a "from:X" label containing the sender address.
func extractMailSender(labels []string) string {
	for _, label := range labels {
		if strings.HasPrefix(label, "from:") {
			return strings.TrimPrefix(label, "from:")
		}
	}
	return ""
}

// scanAllRigsForHookedBeads scans all registered rigs for hooked beads
// assigned to the target agent. Used for town-level roles that may have
// work hooked in any rig.
func scanAllRigsForHookedBeads(townRoot, target string) []*beads.Issue {
	// Load routes from town beads
	townBeadsDir := filepath.Join(townRoot, ".beads")
	routes, err := beads.LoadRoutes(townBeadsDir)
	if err != nil {
		return nil
	}

	// Scan each rig's beads directory
	for _, route := range routes {
		// Handle both absolute and relative paths in routes.jsonl
		// Go's filepath.Join doesn't replace with absolute paths like Python
		var rigBeadsDir string
		if filepath.IsAbs(route.Path) {
			rigBeadsDir = route.Path
		} else {
			rigBeadsDir = filepath.Join(townRoot, route.Path)
		}
		if _, err := os.Stat(rigBeadsDir); os.IsNotExist(err) {
			continue
		}

		b := beads.New(rigBeadsDir)
		hookedBeads, err := listAssignedActiveWork(b, target)
		if err != nil {
			continue
		}

		if len(hookedBeads) > 0 {
			return hookedBeads
		}
	}

	return nil
}
