package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/formula"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

// Root-only wisps (b8f79dc8) carry no child step rows: the steps live in the
// embedded formula and are rendered inline at prime time. `gt mol progress` and
// `gt mol dag` used to read child rows only, so on a root-only wisp they failed
// with "no steps found ... (not a molecule root?)" — which reads as corruption
// when the wisp is in fact perfectly well formed. The helpers here recover the
// step list from the attached formula so both commands describe what is really
// there.

// rootOnlySteps returns the steps of the formula behind a childless molecule
// root, along with the formula's name. It returns an empty name when the issue
// names no formula (a genuinely stepless issue), so callers can fall back to
// their own error.
func rootOnlySteps(root *beads.Issue) ([]formula.Step, string, error) {
	if root == nil {
		return nil, "", nil
	}

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return nil, "", fmt.Errorf("finding workspace: %w", err)
	}
	rigName := rigNameForFormula(townRoot)

	attachment := beads.ParseAttachmentFields(root)
	formulaName := rootOnlyFormulaName(root, attachment, townRoot, rigName)
	if formulaName == "" {
		return nil, "", nil
	}

	f, varMap, err := resolveFormulaForRendering(formulaName, townRoot, rigName, attachmentFormulaVars(attachment))
	if err != nil {
		return nil, formulaName, err
	}

	steps := make([]formula.Step, 0, len(f.Steps))
	for _, step := range f.Steps {
		step.Title = applyFormulaVars(step.Title, varMap)
		steps = append(steps, step)
	}
	return steps, formulaName, nil
}

// rootOnlyFormulaName resolves which formula a root-only wisp came from.
//
// Dog and patrol wisps carry an explicit `attached_formula:` field. Wisps
// created by `bd mol wisp <formula>` (the sling path) do not — their only link
// back to the formula is the root title, which bd sets to the formula name. The
// title is accepted only when it actually resolves to a formula, so an ordinary
// epic whose title happens to look like one is not misread.
func rootOnlyFormulaName(root *beads.Issue, attachment *beads.AttachmentFields, townRoot, rigName string) string {
	if attachment != nil && attachment.AttachedFormula != "" {
		return attachment.AttachedFormula
	}
	title := strings.TrimSpace(root.Title)
	if title == "" || strings.ContainsAny(title, " \t/") {
		return ""
	}
	if _, err := formula.ResolveFormulaContent(title, townRoot, rigName); err != nil {
		return ""
	}
	return title
}

// rigNameForFormula resolves the rig whose formula overlays apply to the current
// directory. An empty result means "town-level formulas only", which is the
// correct fallback outside a rig.
func rigNameForFormula(townRoot string) string {
	if townRoot == "" {
		return ""
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return detectRigFromPath(townRoot, cwd)
}

// rootOnlyProgress builds progress info for a root-only wisp. Per-step state is
// not tracked (there are no step rows to close), so completion is derived from
// the root wisp itself: a closed root means the whole checklist is done.
func rootOnlyProgress(root *beads.Issue, steps []formula.Step, formulaName string) *MoleculeProgressInfo {
	progress := &MoleculeProgressInfo{
		RootID:     root.ID,
		RootTitle:  root.Title,
		RootOnly:   true,
		Formula:    formulaName,
		TotalSteps: len(steps),
	}
	// ReadySteps/BlockedSteps stay empty: without step rows there is no per-step
	// readiness to report, and listing every step as "ready" would be a lie.
	// Steps carries the declared checklist instead.
	for _, step := range steps {
		progress.Steps = append(progress.Steps, stepLabel(step))
	}
	if root.Status == "closed" {
		progress.DoneSteps = len(steps)
		progress.Percent = 100
		progress.Complete = true
	}
	return progress
}

// outputRootOnlyProgress prints the human-readable form of a root-only wisp's
// checklist. It states plainly that step state is not tracked, so an operator
// does not read "0/10 done" as stalled work.
func outputRootOnlyProgress(progress *MoleculeProgressInfo, steps []formula.Step) {
	noun := "Root"
	if strings.Contains(progress.RootID, "-wisp-") {
		noun = "Root wisp"
	}

	fmt.Printf("\n%s %s\n\n", moleculeProgressHeading(), progress.RootTitle)
	fmt.Printf("  Root:    %s (steps read inline from the formula)\n", progress.RootID)
	fmt.Printf("  Formula: %s\n", progress.Formula)
	fmt.Println()

	if progress.Complete {
		fmt.Printf("  %s is closed — all %d step(s) done.\n\n", noun, progress.TotalSteps)
	} else {
		fmt.Printf("  There are no step rows to close, so per-step progress is not tracked in\n")
		fmt.Printf("  the database. The %s closes when the agent finishes.\n\n", strings.ToLower(noun))
	}

	fmt.Printf("  Checklist (%d):\n", len(steps))
	for i, step := range steps {
		fmt.Printf("    %2d. %s\n", i+1, stepLabel(step))
	}
	fmt.Println()
	fmt.Printf("  Full step text: gt prime (as the assigned agent)\n")
	fmt.Printf("                  gt formula show %s\n", progress.Formula)
}

// stepLabel renders a formula step as "id: title", falling back to whichever
// half is present.
func stepLabel(step formula.Step) string {
	switch {
	case step.ID != "" && step.Title != "":
		return step.ID + ": " + step.Title
	case step.Title != "":
		return step.Title
	default:
		return step.ID
	}
}

// dagStatusInline marks a DAG node whose state is not tracked in the database
// because its molecule is a root-only wisp.
const dagStatusInline = "inline"

// buildFormulaDAG builds a DAG from a formula's declared steps, for root-only
// wisps that have no step rows to read. Node status is "inline" because the
// database holds no per-step state.
func buildFormulaDAG(root *beads.Issue, steps []formula.Step, formulaName string) *DAGInfo {
	dag := &DAGInfo{
		RootID:    root.ID,
		RootTitle: root.Title,
		RootOnly:  true,
		Formula:   formulaName,
		Nodes:     make(map[string]*DAGNode),
	}

	known := make(map[string]bool, len(steps))
	for _, step := range steps {
		known[step.ID] = true
	}

	for _, step := range steps {
		node := &DAGNode{
			ID:       step.ID,
			Title:    step.Title,
			Status:   dagStatusInline,
			Parallel: step.Parallel,
		}
		for _, need := range step.Needs {
			// Skip needs that point outside this formula: they would leave a
			// permanent in-degree and strand every dependent in computeTiers.
			if known[need] {
				node.Dependencies = append(node.Dependencies, need)
			}
		}
		dag.Nodes[step.ID] = node
		dag.TotalNodes++
	}

	for id, node := range dag.Nodes {
		for _, depID := range node.Dependencies {
			if depNode, ok := dag.Nodes[depID]; ok {
				depNode.Dependents = append(depNode.Dependents, id)
			}
		}
	}

	computeTiers(dag)
	dag.CriticalPath = findCriticalPath(dag)
	return dag
}

// rootOnlyStepsHint is appended to the "no steps found" error when the issue has
// no attached formula either, pointing at the two shapes that do have steps.
const rootOnlyStepsHint = " — not a molecule root, and no formula is attached"

// noStepsError builds the error returned when an issue has neither child steps
// nor an attached formula.
func noStepsError(rootID string) error {
	return fmt.Errorf("no steps found for %s%s", rootID, rootOnlyStepsHint)
}

// moleculeProgressHeading is the shared heading for `gt mol progress` output.
func moleculeProgressHeading() string {
	return style.Bold.Render("🧬 Molecule Progress:")
}

// formulaLoadWarning formats the non-fatal notice shown when a root-only wisp
// names a formula that cannot be loaded.
func formulaLoadWarning(formulaName string, err error) string {
	return strings.TrimSpace(fmt.Sprintf("attached formula %s could not be loaded: %v", formulaName, err))
}
