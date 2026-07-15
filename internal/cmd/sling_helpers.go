package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/channelevents"
	"github.com/steveyegge/gastown/internal/cli"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/formula"
	rigpkg "github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/telemetry"
	"github.com/steveyegge/gastown/internal/tmux"
	"github.com/steveyegge/gastown/internal/workspace"
)

// resolveBeadDir returns the directory to run bd commands for a given bead ID.
// Uses prefix-based routing (routes.jsonl) to resolve the correct rig's .beads
// directory and returns its parent as the working directory for bd.
//
// Background: beads v0.62 removed built-in multi-rig routing from bd — all bd
// commands now operate on the local database only. Cross-rig resolution must
// happen in gt before invoking bd, by setting the correct working directory
// (and stripping BEADS_DIR). This function reads routes.jsonl from the town-level
// .beads directory and resolves the bead's prefix to the owning rig.
//
// PR #3166 (steveyegge/gastown) will replace bd shell-outs with the Go module
// Storage API, making this function unnecessary. Until then, this is the
// routing bridge between gt and the routing-free bd CLI.
func resolveBeadDir(beadID string) string {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return "."
	}
	return resolveBeadDirFromTownRoot(townRoot, beadID)
}

func resolveBeadDirFromTownRoot(townRoot, beadID string) string {
	if townRoot == "" {
		return "."
	}
	townBeadsDir := filepath.Join(townRoot, ".beads")
	resolved := beads.ResolveBeadsDirForID(townBeadsDir, beadID)
	// Return the parent of the .beads directory so bd discovers it naturally.
	// For town-level beads this returns townRoot; for rig beads it returns
	// the rig's mayor/rig directory (e.g., gastown/mayor/rig).
	return filepath.Dir(resolved)
}

// resolveBeadDirFromRigsJSON looks up the rig directory from rigs.json using prefix.
func resolveBeadDirFromRigsJSON(townRoot, prefix string) string {
	rigsPath := townRoot + "/mayor/rigs.json"
	data, err := os.ReadFile(rigsPath)
	if err != nil {
		return ""
	}
	var rigsFile struct {
		Rigs map[string]struct {
			Beads struct {
				Prefix string `json:"prefix"`
			} `json:"beads"`
		} `json:"rigs"`
	}
	if err := json.Unmarshal(data, &rigsFile); err != nil {
		return ""
	}
	// prefix includes trailing hyphen (e.g., "bd-"), rigs.json stores without (e.g., "bd")
	trimmedPrefix := strings.TrimSuffix(prefix, "-")
	for rigName, rigConfig := range rigsFile.Rigs {
		if rigConfig.Beads.Prefix == trimmedPrefix {
			// Return mayor/rig path within the rig (where .beads/ lives)
			return townRoot + "/" + rigName + "/mayor/rig"
		}
	}
	return ""
}

// beadInfo holds status and assignee for a bead.
type beadInfo struct {
	Title        string           `json:"title"`
	Status       string           `json:"status"`
	Assignee     string           `json:"assignee"`
	Description  string           `json:"description"`
	Labels       []string         `json:"labels,omitempty"`
	Dependencies []beads.IssueDep `json:"dependencies,omitempty"`
	IssueType    string           `json:"issue_type,omitempty"`
}

// isDeferredBead checks whether a bead should be rejected from slinging because
// it has been deferred. Returns true if the bead has status "deferred" or if its
// description contains deferral keywords like "deferred to post-launch".
func isDeferredBead(info *beadInfo) bool {
	if info.Status == "deferred" {
		return true
	}
	desc := strings.ToLower(info.Description)
	if strings.Contains(desc, "deferred to post-launch") ||
		strings.Contains(desc, "deferred to post launch") ||
		strings.Contains(desc, "status: deferred") {
		return true
	}
	return false
}

func applyWorkflowStepTargetOverride(args []string) ([]string, error) {
	if len(args) != 2 {
		return args, nil
	}
	rigName, isRig := IsRigName(args[1])
	if !isRig {
		return args, nil
	}
	info, err := getBeadInfo(args[0])
	if err != nil {
		return args, nil
	}
	target := workflowStepTargetFromDescription(info.Description, rigName)
	if target == "" || target == args[1] {
		return args, nil
	}
	if err := ValidateTarget(target); err != nil {
		return args, fmt.Errorf("invalid %s for %s: %w", workflowTargetField, args[0], err)
	}
	redirected := append([]string(nil), args...)
	redirected[1] = target
	fmt.Printf("%s Workflow step target: %s\n", style.Dim.Render("→"), target)
	return redirected, nil
}

func workflowStepTargetFromDescription(description, targetRig string) string {
	for _, line := range strings.Split(description, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(key), workflowTargetField) {
			continue
		}
		target := strings.TrimSpace(value)
		if target == "" || target == "rig" {
			return targetRig
		}
		return target
	}
	return ""
}

// isOrphanMolecule reports whether a bead's existing attached molecule(s)
// can be safely burned at sling time without operator confirmation. Used
// to gate the auto-burn path that lets sling self-heal from stale state.
//
// A molecule is treated as orphaned when:
//   - the bead has no assignee but is in an active status (open/in_progress)
//     or stuck in `hooked` with no assignee — the latter covers gh-3697,
//     where one orphan wisp would otherwise wedge every subsequent sling
//     to the rig with "bead already has N attached molecule(s)"; or
//   - the bead has an assignee but that assignee's tmux session is dead.
//
// `closed` and `blocked` deliberately fall through to the refuse path:
// burning molecules off a closed bead would mask completed work, and
// burning off a blocked bead can mask a real dependency.
func isOrphanMolecule(info *beadInfo) bool {
	if info == nil {
		return false
	}
	if info.Assignee == "" {
		switch info.Status {
		case "open", "in_progress", "hooked":
			return true
		}
		return false
	}
	return isHookedAgentDeadFn(info.Assignee)
}

// collectExistingMolecules returns all molecule wisp IDs attached to a bead.
// Checks both dependency bonds (ground truth from bd mol bond) and the
// description's attached_molecule field (metadata pointer). Wisp IDs are
// identified by containing "-wisp-" in their ID.
// Uses Dependencies (structured []IssueDep from bd show --json) rather than
// DependsOn (raw ID list, which is unreliable — see molecule_status.go comments).
func collectExistingMolecules(info *beadInfo) []string {
	seen := make(map[string]bool)
	var molecules []string

	// Check dependency bonds (ground truth - bd mol bond creates these)
	for _, dep := range info.Dependencies {
		if strings.Contains(dep.ID, "-wisp-") && !seen[dep.ID] {
			// Skip molecules already closed/burned — bond is stale
			if dep.Status == "closed" || dep.Status == "tombstone" {
				continue
			}
			seen[dep.ID] = true
			molecules = append(molecules, dep.ID)
		}
	}

	// Also check description's attached_molecule (may differ from bonds)
	issue := &beads.Issue{Description: info.Description}
	fields := beads.ParseAttachmentFields(issue)
	if fields != nil && fields.AttachedMolecule != "" && !seen[fields.AttachedMolecule] {
		seen[fields.AttachedMolecule] = true
		molecules = append(molecules, fields.AttachedMolecule)
	}

	return molecules
}

func appendUniqueMolecules(molecules []string, extras ...string) []string {
	seen := make(map[string]bool, len(molecules)+len(extras))
	for _, molecule := range molecules {
		seen[molecule] = true
	}
	for _, molecule := range extras {
		if molecule == "" || seen[molecule] {
			continue
		}
		seen[molecule] = true
		molecules = append(molecules, molecule)
	}
	return molecules
}

func collectExistingMoleculesForBead(info *beadInfo, beadID, townRoot string) ([]string, error) {
	molecules := collectExistingMolecules(info)
	deps, err := collectExistingMoleculeDeps(beadID, townRoot)
	if err != nil {
		return molecules, err
	}
	return appendUniqueMolecules(molecules, deps...), nil
}

func collectExistingMoleculeDeps(beadID, townRoot string) ([]string, error) {
	if beadID == "" {
		return nil, nil
	}
	if !isValidBeadID(beadID) {
		return nil, fmt.Errorf("invalid bead ID: %q", beadID)
	}

	dir := resolveBeadDirFromTownRoot(townRoot, beadID)
	query := fmt.Sprintf(`SELECT DISTINCT wisp_dependencies.issue_id FROM wisp_dependencies JOIN wisps ON wisps.id = wisp_dependencies.issue_id WHERE wisps.issue_type = 'molecule' AND wisps.status NOT IN ('closed', 'tombstone') AND wisp_dependencies.type IN ('blocks', 'conditional-blocks', 'parent-child') AND (wisp_dependencies.depends_on_issue_id = '%[1]s' OR wisp_dependencies.depends_on_wisp_id = '%[1]s' OR wisp_dependencies.depends_on_external = '%[1]s' OR %[2]s)`, beadID, sqlExternalDepTargetClause(beadID))
	out, err := runBdJSON(dir, "sql", query, "--json")
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, nil
	}

	var rows []map[string]string
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("parsing canonical molecule deps for %s: %w", beadID, err)
	}

	seen := make(map[string]bool, len(rows))
	var molecules []string
	for _, row := range rows {
		moleculeID := row["issue_id"]
		if moleculeID == "" || seen[moleculeID] {
			continue
		}
		seen[moleculeID] = true
		molecules = append(molecules, moleculeID)
	}
	return molecules, nil
}

// burnExistingMolecules burns all molecule wisps attached to a bead.
// Order: force-close descendants → detach from bead → remove dep bonds → force-close roots.
// Matches nukeCleanupMolecules pattern. Returns an error if detach fails, since
// proceeding with a stale attached_molecule reference creates harder-to-debug orphans.
func burnExistingMolecules(molecules []string, beadID, townRoot string) error {
	if len(molecules) == 0 {
		return nil
	}
	burnDir := beads.ResolveHookDir(townRoot, beadID, "")

	// Follows the same order as nukeCleanupMolecules, plus dep bond removal:
	//   1. Force-close descendants (children before parents)
	//   2. Detach molecule from bead (clears attached_molecule in description)
	//   3. Remove dependency bonds (prevents "existing molecule(s)" on re-sling)
	//   4. Force-close molecule roots
	// Closing descendants first ensures that if detach succeeds but a later step
	// crashes, we don't leave a detached root with live children.
	bd := beads.New(burnDir)

	// Step 1: Force-close descendant steps before detaching. Uses force variant
	// since burn is a destructive recovery path where prior state may be inconsistent.
	// Best-effort — log but proceed in destructive path.
	for _, molID := range molecules {
		if _, err := forceCloseDescendants(bd, molID); err != nil {
			style.PrintWarning("burn: could not close descendants of %s: %v", molID, err)
		}
	}

	// Step 2: Detach molecule from the base bead using the Go API (with audit logging
	// and advisory locking). This clears attached_molecule/attached_at from the description.
	// Without this, storeFieldsInBead preserves the stale reference because it only
	// overwrites when updates.AttachedMolecule is non-empty.
	if _, err := bd.DetachMoleculeWithAudit(beadID, beads.DetachOptions{
		Operation: "burn",
		Reason:    "force re-sling: burning stale molecules",
	}); err != nil {
		return fmt.Errorf("detaching molecule from %s: %w", beadID, err)
	}

	// Step 3: Remove dependency bonds between the bead and each molecule.
	// DetachMoleculeWithAudit (step 2) only clears the description metadata
	// (attached_molecule/attached_at). The dependency bond from bd mol bond
	// is a separate link that collectExistingMolecules reads via info.Dependencies.
	// Without this, the next sling attempt finds the closed molecule via the
	// bond and refuses with "bead has existing molecule(s)".
	for _, molID := range molecules {
		removeMoleculeBonds(bd, beadID, molID)
	}

	// Step 4: Close descendants, then force-close the orphaned wisp roots.
	// Best-effort — log but proceed in destructive path.
	for _, molID := range molecules {
		if _, err := forceCloseDescendants(bd, molID); err != nil {
			style.PrintWarning("burn: could not close descendants of %s: %v", molID, err)
		}
	}
	if err := bd.ForceCloseWithReason("burned: force re-sling", molecules...); err != nil {
		fmt.Printf("  %s Could not close molecule wisp(s): %v\n",
			style.Dim.Render("Warning:"), err)
		// Close failure is non-fatal — the detach already succeeded, so the bead
		// is clean. Orphaned wisps will be caught by reactive DetectOrphanedMolecules.
	}

	return nil
}

func removeMoleculeBonds(bd *beads.Beads, beadID, molID string) {
	for _, bond := range []struct {
		from string
		to   string
	}{
		{from: molID, to: beadID}, // canonical bd mol bond direction
		{from: beadID, to: molID}, // legacy reverse direction
	} {
		if err := bd.RemoveDependency(bond.from, bond.to); err != nil && !dependencyRemovalMissing(err) {
			fmt.Printf("  %s Could not remove dep bond %s → %s: %v\n",
				style.Dim.Render("Warning:"), bond.from, bond.to, err)
		}
	}
}

func dependencyRemovalMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no dependency") ||
		strings.Contains(msg, "not present")
}

// verifyBeadExists checks that the bead exists using bd show.
// Resolves the rig directory from the bead's prefix for correct dolt access.
// StripBeadsDir prevents inherited BEADS_DIR from overriding the resolved
// directory, which caused rig-prefixed beads to fail (GH#2126).
func verifyBeadExists(beadID string) error {
	out, err := bdShowBeadOutput(beadID)
	if err != nil {
		return fmt.Errorf("bead '%s' not found (bd show failed: %w)", beadID, err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return fmt.Errorf("bead '%s' not found", beadID)
	}
	return nil
}

// verifyBeadExistsInTargetRigDatabase checks the target rig's beads database
// directly instead of following prefix routing. This prevents gt sling from
// spawning polecats or creating molecule/hook side effects for beads that only
// resolve from HQ or another rig database.
func verifyBeadExistsInTargetRigDatabase(beadID, targetRig, townRoot string) error {
	if beadID == "" {
		return nil
	}
	if targetRig == "" {
		return fmt.Errorf("cannot verify bead %s in target rig: target rig is empty; refusing to sling before creating hooks or molecule side effects", beadID)
	}
	if townRoot == "" {
		return fmt.Errorf("cannot verify bead %s in target rig %q: town root is unavailable; refusing to sling before creating hooks or molecule side effects", beadID, targetRig)
	}

	targetBeadsDir, ok := beads.ResolveRepoAliasBeadsDir(townRoot, targetRig)
	if !ok {
		return fmt.Errorf("cannot resolve target rig %q beads database for bead %s; refusing to sling before creating hooks or molecule side effects", targetRig, beadID)
	}
	targetRigDir := filepath.Dir(targetBeadsDir)

	out, err := BdCmd("show", beadID, "--json").
		AllowStale().
		Dir(targetRigDir).
		WithBeadsDir(targetBeadsDir).
		StripBeadsDir().
		Stderr(io.Discard).
		Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		if routedBeadExistsForTargetRig(beadID, targetRig, townRoot) {
			return nil
		}
		return fmt.Errorf("bead %s is not present in target rig %q beads database; refusing to sling before creating hooks or molecule side effects", beadID, targetRig)
	}

	var infos []beadInfo
	if err := json.Unmarshal(out, &infos); err != nil {
		return fmt.Errorf("checking target rig %q database for bead %s: %w", targetRig, beadID, err)
	}
	if len(infos) == 0 {
		if routedBeadExistsForTargetRig(beadID, targetRig, townRoot) {
			return nil
		}
		return fmt.Errorf("bead %s is not present in target rig %q beads database; refusing to sling before creating hooks or molecule side effects", beadID, targetRig)
	}

	return nil
}

func routedBeadExistsForTargetRig(beadID, targetRig, townRoot string) bool {
	prefixRig := beads.GetRigNameForPrefix(townRoot, beads.ExtractPrefix(beadID))
	if prefixRig != targetRig {
		return false
	}
	out, err := bdShowBeadRoutedCmdFromTownRoot(townRoot, beadID).Stderr(io.Discard).Output()
	return err == nil && len(strings.TrimSpace(string(out))) > 0
}

func bdShowBeadOutput(beadID string) ([]byte, error) {
	out, err := bdShowBeadDirectCmd(beadID).Stderr(io.Discard).Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return out, nil
	}
	routedOut, routedErr := bdShowBeadRoutedCmd(beadID).Stderr(io.Discard).Output()
	if routedErr == nil && len(strings.TrimSpace(string(routedOut))) > 0 {
		return routedOut, nil
	}
	return out, err
}

func bdShowBeadOutputFromTownRoot(townRoot, beadID string) ([]byte, error) {
	if townRoot == "" {
		return bdShowBeadOutput(beadID)
	}
	out, err := bdShowBeadDirectCmdFromTownRoot(townRoot, beadID).Stderr(io.Discard).Output()
	if err == nil && len(strings.TrimSpace(string(out))) > 0 {
		return out, nil
	}
	routedOut, routedErr := bdShowBeadRoutedCmdFromTownRoot(townRoot, beadID).Stderr(io.Discard).Output()
	if routedErr == nil && len(strings.TrimSpace(string(routedOut))) > 0 {
		return routedOut, nil
	}
	return out, err
}

func bdShowBeadDirectCmd(beadID string) *bdCmd {
	return BdCmd("show", beadID, "--json").
		AllowStale().
		Dir(resolveBeadDir(beadID)).
		StripBeadsDir()
}

func bdShowBeadDirectCmdFromTownRoot(townRoot, beadID string) *bdCmd {
	return BdCmd("show", beadID, "--json").
		AllowStale().
		Dir(resolveBeadDirFromTownRoot(townRoot, beadID)).
		StripBeadsDir()
}

func bdShowBeadRoutedCmd(beadID string) *bdCmd {
	bdc := BdCmd("show", beadID, "--json").AllowStale()
	if townRoot, err := workspace.FindFromCwdOrError(); err == nil && townRoot != "" {
		return bdShowBeadRoutedCmdFromTownRoot(townRoot, beadID)
	}
	return bdc.Dir(resolveBeadDir(beadID)).StripBeadsDir()
}

func bdShowBeadRoutedCmdFromTownRoot(townRoot, beadID string) *bdCmd {
	return BdCmd("show", beadID, "--json").AllowStale().Dir(townRoot).WithRouting()
}

// getBeadInfo returns status and assignee for a bead.
// Resolves the rig directory from the bead's prefix for correct dolt access.
func getBeadInfo(beadID string) (*beadInfo, error) {
	out, err := bdShowBeadOutput(beadID)
	if err != nil {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	return parseBeadInfo(beadID, out)
}

func getBeadInfoFromTownRoot(townRoot, beadID string) (*beadInfo, error) {
	out, err := bdShowBeadOutputFromTownRoot(townRoot, beadID)
	if err != nil {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	return parseBeadInfo(beadID, out)
}

func parseBeadInfo(beadID string, out []byte) (*beadInfo, error) {
	if len(out) == 0 {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	// bd show --json returns an array (issue + dependents), take first element.
	var infos []beadInfo
	if err := json.Unmarshal(out, &infos); err != nil {
		return nil, fmt.Errorf("parsing bead info: %w", err)
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("bead '%s' not found", beadID)
	}
	return &infos[0], nil
}

// beadFieldUpdates holds all the fields that need to be stored in a bead's description.
// This enables a single read-modify-write cycle instead of sequential independent updates,
// eliminating the race condition where concurrent writers could overwrite each other's fields.
type beadFieldUpdates struct {
	Dispatcher       string   // Agent that dispatched the work
	Args             string   // Natural language instructions
	Vars             []string // Formula variables (key=value pairs)
	AttachedMolecule string   // Wisp root ID
	AttachedFormula  string   // Formula name (e.g., "mol-polecat-work") for inline step display
	ClearAttachment  bool     // Clear stale workflow attachment fields before applying updates
	AttachedAt       string   // Assignment timestamp; refreshed when workflow metadata is written
	NoMerge          bool     // Skip merge queue on completion
	ReviewOnly       bool     // Review-only mode: assignee must not merge/commit/push
	Mode             *string  // Execution mode: nil means unchanged, "" clears, "ralph" enables Ralph mode
	ConvoyID         string   // Convoy bead ID (e.g., "hq-cv-abc")
	MergeStrategy    string   // Convoy merge strategy: "direct", "mr", "local"
	ConvoyOwned      bool     // Convoy has gt:owned label (caller-managed lifecycle)
	FormulaVars      string   // Newline-separated key=value pairs for formula template substitution
}

func buildSlingFieldUpdates(
	dispatcher string,
	args string,
	vars []string,
	attachedMolecule string,
	attachedFormula string,
	noMerge bool,
	reviewOnly bool,
	mode string,
	formulaVars string,
	convoyID string,
	mergeStrategy string,
	convoyOwned bool,
) beadFieldUpdates {
	updates := beadFieldUpdates{
		Dispatcher:       dispatcher,
		Args:             args,
		Vars:             vars,
		AttachedMolecule: attachedMolecule,
		AttachedFormula:  attachedFormula,
		NoMerge:          noMerge,
		ReviewOnly:       reviewOnly,
		Mode:             &mode,
		ConvoyID:         convoyID,
		MergeStrategy:    mergeStrategy,
		ConvoyOwned:      convoyOwned,
		FormulaVars:      formulaVars,
	}
	if attachedMolecule != "" || attachedFormula != "" || noMerge || reviewOnly {
		updates.AttachedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return updates
}

// storeFieldsInBead performs a single read-modify-write to update all attachment fields
// in a bead's description atomically. This replaces the sequential storeDispatcherInBead,
// storeArgsInBead, storeAttachedMoleculeInBead, and storeNoMergeInBead calls that each
// independently read-modify-write and could race under concurrent access.
func storeFieldsInBead(beadID string, updates beadFieldUpdates) error {
	return storeFieldsInBeadFromTownRoot("", beadID, updates)
}

func storeFieldsInBeadFromTownRoot(townRoot, beadID string, updates beadFieldUpdates) error {
	logPath := os.Getenv("GT_TEST_ATTACHED_MOLECULE_LOG")

	issue := &beads.Issue{}
	if logPath == "" {
		// Read the bead once
		out, err := bdShowBeadOutputFromTownRoot(townRoot, beadID)
		if err != nil {
			return fmt.Errorf("fetching bead: %w", err)
		}
		if len(out) == 0 {
			return fmt.Errorf("bead not found")
		}

		var issues []beads.Issue
		if err := json.Unmarshal(out, &issues); err != nil {
			return fmt.Errorf("parsing bead: %w", err)
		}
		if len(issues) == 0 {
			return fmt.Errorf("bead not found")
		}
		issue = &issues[0]
	}

	// Get or create attachment fields
	fields := beads.ParseAttachmentFields(issue)
	if fields == nil {
		fields = &beads.AttachmentFields{}
	}

	// Apply all updates in one pass
	if updates.ClearAttachment {
		fields.AttachedMolecule = ""
		fields.AttachedFormula = ""
		fields.AttachedAt = ""
		fields.AttachedVars = nil
		fields.FormulaVars = ""
	}
	if updates.Dispatcher != "" {
		fields.DispatchedBy = updates.Dispatcher
	}
	if updates.Args != "" {
		fields.AttachedArgs = updates.Args
	}
	if len(updates.Vars) > 0 {
		fields.AttachedVars = append([]string(nil), updates.Vars...)
	}
	if updates.AttachedMolecule != "" {
		fields.AttachedMolecule = updates.AttachedMolecule
	}
	if updates.AttachedFormula != "" {
		fields.AttachedFormula = updates.AttachedFormula
	}
	if updates.AttachedAt != "" {
		fields.AttachedAt = updates.AttachedAt
	} else if updates.AttachedMolecule != "" || updates.AttachedFormula != "" || updates.NoMerge || updates.ReviewOnly {
		fields.AttachedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if updates.NoMerge {
		fields.NoMerge = true
	}
	if updates.ReviewOnly {
		fields.ReviewOnly = true
	}
	if updates.Mode != nil {
		fields.Mode = *updates.Mode
	}
	if updates.ConvoyID != "" {
		fields.ConvoyID = updates.ConvoyID
	}
	if updates.MergeStrategy != "" {
		fields.MergeStrategy = updates.MergeStrategy
	}
	if updates.ConvoyOwned {
		fields.ConvoyOwned = true
	}
	if updates.FormulaVars != "" {
		fields.FormulaVars = updates.FormulaVars
	}

	// Write back once
	newDesc := beads.SetAttachmentFields(issue, fields)
	if logPath != "" {
		_ = os.WriteFile(logPath, []byte(newDesc), 0644)
		return nil
	}

	updateDir := resolveBeadDir(beadID)
	if townRoot != "" {
		updateDir = resolveBeadDirFromTownRoot(townRoot, beadID)
	}
	if err := BdCmd("update", beadID, "--description="+newDesc).
		Dir(updateDir).
		StripBeadsDir().
		WithAutoCommit().
		Run(); err != nil {
		return fmt.Errorf("updating bead description: %w", err)
	}

	return nil
}

// injectStartPrompt sends a prompt to the target pane to start working.
// Uses the reliable nudge pattern: literal mode + 500ms debounce + separate Enter.
func injectStartPrompt(pane, beadID, subject, args string) error {
	if pane == "" {
		return fmt.Errorf("no target pane")
	}

	// Skip nudge during tests to prevent agent self-interruption
	if os.Getenv("GT_TEST_NO_NUDGE") != "" {
		return nil
	}

	// Build the prompt to inject
	var prompt string
	if args != "" {
		// Args provided - include them prominently in the prompt
		if subject != "" {
			prompt = fmt.Sprintf("Work slung: %s (%s). Args: %s. Start working now - use these args to guide your execution.", beadID, subject, args)
		} else {
			prompt = fmt.Sprintf("Work slung: %s. Args: %s. Start working now - use these args to guide your execution.", beadID, args)
		}
	} else if subject != "" {
		prompt = fmt.Sprintf("Work slung: %s (%s). Start working on it now - no questions, just begin.", beadID, subject)
	} else {
		prompt = fmt.Sprintf("Work slung: %s. Start working on it now - run `"+cli.Name()+" hook` to see the hook, then begin.", beadID)
	}

	// Use the reliable nudge pattern (same as gt nudge / tmux.NudgeSession)
	t := tmux.NewTmux()
	return t.NudgePane(pane, prompt)
}

// getSessionFromPane extracts session name from a pane target.
// Pane targets can be:
// - "%9" (pane ID) - need to query tmux for session
// - "gt-rig-name:0.0" (session:window.pane) - extract session name
func getSessionFromPane(pane string) string {
	if strings.HasPrefix(pane, "%") {
		// Pane ID format - query tmux for the session
		cmd := tmux.BuildCommand("display-message", "-t", pane, "-p", "#{session_name}")
		out, err := cmd.Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	// Session:window.pane format - extract session name
	if idx := strings.Index(pane, ":"); idx > 0 {
		return pane[:idx]
	}
	return pane
}

// ensureAgentReady waits for an agent to be ready before nudging an existing session.
// Uses a pragmatic approach: wait for the pane to leave a shell, then (Claude-only)
// accept the bypass permissions warning and give it a moment to finish initializing.
func ensureAgentReady(sessionName string) error {
	t := tmux.NewTmux()

	if t.IsAgentRunning(sessionName) {
		// Agent process is detected, but it may have just started (fresh spawn).
		// Check session age — if < 15s old, the agent likely isn't ready for input yet.
		if !isSessionYoung(sessionName, 15*time.Second) {
			return nil
		}
		// Fall through to apply startup delay for young sessions.
	} else {
		// Agent not running yet - wait for it to start (shell → program transition)
		if err := t.WaitForCommand(sessionName, constants.SupportedShells, constants.ClaudeStartTimeout); err != nil {
			return fmt.Errorf("waiting for agent to start: %w", err)
		}
	}

	// Accept startup dialogs (workspace trust + bypass permissions) if they appear
	_ = t.AcceptWorkspaceTrustDialog(sessionName)
	agentName, _ := t.GetEnvironment(sessionName, "GT_AGENT")
	if shouldAcceptPermissionWarning(agentName) {
		_ = t.AcceptBypassPermissionsWarning(sessionName)
	}

	// Use prompt-detection polling instead of fixed sleep.
	// For known presets: uses ReadyPromptPrefix (e.g. "❯ " for Claude) polled every 200ms.
	// For unknown/custom agents: falls back to a 1s fixed delay (mirrors old behavior).
	// Note: uses preset-only resolution (not ResolveRoleAgentConfig) because
	// ensureAgentReady lacks rig/town context — only has the session name.
	effectiveName := agentName
	if effectiveName == "" {
		effectiveName = "claude" // Default sessions without GT_AGENT are Claude
	}
	var rc *config.RuntimeConfig
	if preset := config.GetAgentPreset(config.AgentPreset(effectiveName)); preset != nil {
		rc = config.RuntimeConfigFromPreset(config.AgentPreset(effectiveName))
	} else {
		// Unknown agent — use minimal config: no prompt detection, short fixed delay.
		rc = &config.RuntimeConfig{
			Tmux: &config.RuntimeTmuxConfig{
				ReadyDelayMs: 1000,
			},
		}
	}
	// Ensure a minimum 1s readiness delay for presets without prompt detection.
	// Without this, agents with ReadyPromptPrefix="" and ReadyDelayMs=0
	// (e.g. gemini, cursor) would skip the readiness guard entirely,
	// reintroducing early-input races that this function exists to prevent.
	if rc.Tmux != nil && rc.Tmux.ReadyPromptPrefix == "" && rc.Tmux.ReadyDelayMs < 1000 {
		rc.Tmux.ReadyDelayMs = 1000
	}
	if err := t.WaitForRuntimeReady(sessionName, rc, constants.ClaudeStartTimeout); err != nil {
		// Graceful degradation: warn but proceed (matches original behavior of always continuing)
		fmt.Fprintf(os.Stderr, "Warning: agent readiness detection timed out for %s: %v\n", sessionName, err)
	}

	return nil
}

// isSessionYoung returns true if the tmux session was created less than maxAge ago.
func isSessionYoung(sessionName string, maxAge time.Duration) bool {
	out, err := tmux.BuildCommand("display-message", "-t", sessionName, "-p", "#{session_created}").Output()
	if err != nil {
		return false
	}
	createdUnix, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(createdUnix, 0)) < maxAge
}

// detectCloneRoot finds the root of the current git clone.
func detectCloneRoot() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository")
	}
	return strings.TrimSpace(string(out)), nil
}

// detectActor returns the current agent's actor string for event logging.
func detectActor() string {
	roleInfo, err := GetRole()
	if err != nil {
		return "unknown"
	}
	return roleInfo.ActorString()
}

// agentIDToBeadID converts an agent ID to its corresponding agent bead ID.
// Uses canonical naming: prefix-rig-role-name
// Town-level agents (Mayor, Deacon) use hq- prefix and are stored in town beads.
// Rig-level agents use the rig's configured prefix (default "gt-").
// townRoot is needed to look up the rig's configured prefix.
func agentIDToBeadID(agentID, townRoot string) string {
	// Normalize: strip trailing slash (resolveSelfTarget returns "mayor/" not "mayor")
	agentID = strings.TrimSuffix(agentID, "/")

	// Handle simple cases (town-level agents with hq- prefix)
	if agentID == "mayor" {
		return beads.MayorBeadIDTown()
	}
	if agentID == "deacon" {
		return beads.DeaconBeadIDTown()
	}

	// Parse path-style agent IDs
	parts := strings.Split(agentID, "/")
	if len(parts) < 2 {
		return ""
	}

	rig := parts[0]
	prefix := beads.GetPrefixForRig(townRoot, rig)

	switch {
	case len(parts) == 2 && parts[1] == "witness":
		return beads.WitnessBeadIDWithPrefix(prefix, rig)
	case len(parts) == 2 && parts[1] == "refinery":
		return beads.RefineryBeadIDWithPrefix(prefix, rig)
	case len(parts) == 3 && parts[1] == "crew":
		return beads.CrewBeadIDWithPrefix(prefix, rig, parts[2])
	case len(parts) == 3 && parts[1] == "polecats":
		return beads.PolecatBeadIDWithPrefix(prefix, rig, parts[2])
	case len(parts) == 3 && parts[0] == "deacon" && parts[1] == "dogs":
		// Dogs are town-level agents with hq- prefix
		return beads.DogBeadIDTown(parts[2])
	default:
		return ""
	}
}

// updateAgentHookBead is a no-op. Previously set the hook_bead slot on agent beads
// when work was slung, but this was redundant: the work bead itself tracks
// status=hooked and assignee=<agent>. Agent bead slot writes caused warnings
// in cross-database scenarios and added unnecessary Dolt load.
// Removed per hq-l6mm5: replace bd slot hooks with direct bead tracking.
func updateAgentHookBead(agentID, beadID, workDir, townBeadsDir string) {
	// No-op: work bead status=hooked + assignee is the authoritative source.
	// Agent bead hook_bead slot is no longer maintained.
}

// wakeRigAgents wakes the witness for a rig after polecat dispatch.
// This ensures the witness is ready to monitor. The refinery is nudged
// separately when an MR is actually created (by nudgeRefinery).
func wakeRigAgents(rigName string) {
	// Boot the rig (idempotent - no-op if already running)
	bootCmd := exec.Command("gt", "rig", "boot", rigName)
	_ = bootCmd.Run() // Ignore errors - rig might already be running

	// Verify daemon is running — polecat triggering depends on daemon
	// processing deacon mail. Warn if not running (gt-9wv0).
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" {
		if running, _, _ := daemon.IsRunning(townRoot); !running {
			fmt.Fprintf(os.Stderr, "Warning: daemon is not running. Polecat may not auto-start.\n")
			fmt.Fprintf(os.Stderr, "  Start with: gt daemon start\n")
		}
	}

	// Immediate delivery to witness: send directly to tmux pane.
	// No cooperative queue — idle agents never call Drain(), so queued
	// nudges would be stuck forever. Direct delivery is safe: if the
	// agent is busy, text buffers in tmux and is processed at next prompt.
	witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))
	t := tmux.NewTmux()
	if err := t.NudgeSession(witnessSession, "Polecat dispatched - check for work"); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to nudge witness %s: %v\n", witnessSession, err)
	}
}

// nudgeWitness wakes the witness after polecat completion (gt-a6gp).
// Replaces POLECAT_DONE mail — nudges are free (no Dolt commit).
// Uses immediate delivery: sends directly to the tmux pane.
func nudgeWitness(rigName, message string) {
	witnessSession := session.WitnessSessionName(session.PrefixFor(rigName))

	// Test hook: log nudge for test observability
	if logPath := os.Getenv("GT_TEST_NUDGE_LOG"); logPath != "" {
		entry := fmt.Sprintf("nudge:%s:%s\n", witnessSession, message)
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			_, _ = f.WriteString(entry)
			_ = f.Close()
		}
		return // Don't actually nudge tmux in tests
	}

	// Emit a file event so the witness's await-event unblocks instantly.
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" {
		_, _ = channelevents.EmitToTown(townRoot, "witness", "POLECAT_DONE", []string{
			"source=polecat",
			"message=" + message,
		})
	}

	t := tmux.NewTmux()
	if err := t.NudgeSession(witnessSession, message); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to nudge witness %s: %v\n", witnessSession, err)
	}
}

// nudgeRefinery wakes the refinery after an MR is created.
// Uses immediate delivery: sends directly to the tmux pane.
// No cooperative queue — idle agents never call Drain(), so queued
// nudges would be stuck forever. Direct delivery is safe: if the
// agent is busy, text buffers in tmux and is processed at next prompt.
func nudgeRefinery(rigName, message string) {
	refinerySession := session.RefinerySessionName(session.PrefixFor(rigName))

	// Test hook: log nudge for test observability (same pattern as GT_TEST_ATTACHED_MOLECULE_LOG)
	if logPath := os.Getenv("GT_TEST_NUDGE_LOG"); logPath != "" {
		entry := fmt.Sprintf("nudge:%s:%s\n", refinerySession, message)
		f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err == nil {
			_, _ = f.WriteString(entry)
			_ = f.Close()
		}
		return // Don't actually nudge tmux in tests
	}

	// Emit a file event so the refinery's await-event unblocks instantly.
	// This is the programmatic bridge between mq submit and the event system.
	townRoot, _ := workspace.FindFromCwd()
	if townRoot != "" {
		_, _ = channelevents.EmitToTown(townRoot, "refinery", "MQ_SUBMIT", []string{
			"source=sling",
			"message=" + message,
		})
	}

	t := tmux.NewTmux()
	if err := t.NudgeSession(refinerySession, message); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to nudge refinery %s: %v\n", refinerySession, err)
	}
}

// isPolecatTarget checks if the target string refers to a polecat.
// Returns true if the target format is "rig/polecats/name".
// This is used to determine if we should respawn a dead polecat
// instead of failing when slinging work.
func isPolecatTarget(target string) bool {
	parts := strings.Split(target, "/")
	return len(parts) >= 3 && parts[1] == "polecats"
}

// FormulaOnBeadResult contains the result of instantiating a formula on a bead.
type FormulaOnBeadResult struct {
	WispRootID  string   // The wisp root ID (compound root after bonding)
	BeadToHook  string   // The bead ID to hook (BASE bead, not wisp - lifecycle fix)
	FormulaVars []string // Vars used to instantiate/render the formula
}

func formulaBeadBdCmd(beadID, formulaWorkDir, townRoot string, args ...string) *bdCmd {
	targetBeadsDir := beads.ResolveBeadsDirForID(filepath.Join(townRoot, ".beads"), beadID)
	return BdCmd(args...).Dir(formulaWorkDir).WithBeadsDir(targetBeadsDir).WithGTRoot(townRoot)
}

// InstantiateFormulaOnBead bonds a formula directly to a bead.
// This is the formula-on-bead pattern used by issue #288 for auto-applying mol-polecat-work.
//
// Parameters:
//   - formulaName: the formula to instantiate (e.g., "mol-polecat-work")
//   - beadID: the base bead to bond the wisp to
//   - title: the bead title (used for --var feature=<title>)
//   - hookWorkDir: working directory for bd commands (polecat's worktree)
//   - townRoot: the town root directory
//   - skipCook: if true, skip cooking (for batch mode optimization where cook happens once)
//   - extraVars: additional --var values supplied by the user
//
// Returns the spawned molecule root ID while leaving the base bead as the hook target.
func InstantiateFormulaOnBead(ctx context.Context, formulaName, beadID, title, hookWorkDir, townRoot string, skipCook bool, extraVars []string) (_ *FormulaOnBeadResult, retErr error) {
	defer func() { telemetry.RecordFormulaInstantiate(ctx, formulaName, beadID, retErr) }()
	// Route bd mutations to the correct beads context for the target bead.
	formulaWorkDir := beads.ResolveHookDir(townRoot, beadID, hookWorkDir)

	// Step 1: Cook the formula (ensures proto exists)
	// If cook fails, retry with the embedded formula extracted to a temp file.
	// This handles non-gastown rigs that don't have formulas provisioned on disk.
	// See gt-oir.
	resolvedFormula := formulaName
	var formulaCleanup func()
	if !skipCook {
		if err := formulaBeadBdCmd(beadID, formulaWorkDir, townRoot, "cook", formulaName).
			WithAutoCommit().
			Run(); err != nil {
			// Retry with embedded formula
			resolvedFormula, formulaCleanup = resolveFormulaToTempFile(formulaName)
			if formulaCleanup != nil {
				defer formulaCleanup()
			}
			if resolvedFormula != formulaName {
				if retryErr := formulaBeadBdCmd(beadID, formulaWorkDir, townRoot, "cook", resolvedFormula).
					WithAutoCommit().
					Run(); retryErr != nil {
					telemetry.RecordMolCook(ctx, formulaName, retryErr)
					return nil, fmt.Errorf("cooking formula %s: %w (embedded retry: %v)", formulaName, err, retryErr)
				}
			} else {
				telemetry.RecordMolCook(ctx, formulaName, err)
				return nil, fmt.Errorf("cooking formula %s: %w", formulaName, err)
			}
		}
		telemetry.RecordMolCook(ctx, formulaName, nil)
	}

	formulaVars := formulaVarsForBead(formulaName, beadID, title, extraVars)
	wispRootID, err := bondFormulaDirect(resolvedFormula, formulaName, beadID, formulaWorkDir, townRoot, formulaVars)
	if err != nil {
		return nil, fmt.Errorf("bonding formula %s to bead %s: %w", formulaName, beadID, err)
	}
	telemetry.RecordMolWisp(ctx, formulaName, wispRootID, beadID, nil)

	return &FormulaOnBeadResult{
		WispRootID:  wispRootID,
		BeadToHook:  beadID, // Hook the BASE bead (lifecycle fix: wisp is attached_molecule)
		FormulaVars: append([]string(nil), formulaVars...),
	}, nil
}

func formulaVarsForBead(formulaName, beadID, title string, extraVars []string) []string {
	formulaVars := []string{
		fmt.Sprintf("feature=%s", title),
		fmt.Sprintf("issue=%s", beadID),
	}
	formulaVars = append(formulaVars, extraVars...)
	return ensureFormulaRequiredVars(formulaName, formulaVars)
}

// bondFormulaDirect attaches a formula to a bead through bd's canonical bond path.
func bondFormulaDirect(bondTarget, formulaName, beadID, formulaWorkDir, townRoot string, vars []string) (string, error) {
	bondArgs := []string{"mol", "bond", bondTarget, beadID, "--json", "--ephemeral"}
	for _, variable := range vars {
		bondArgs = append(bondArgs, "--var", variable)
	}
	bondOut, err := formulaBeadBdCmd(beadID, formulaWorkDir, townRoot, bondArgs...).
		WithAutoCommit().
		Output()
	if err != nil {
		return "", fmt.Errorf("%w (args: %s)", err, strings.Join(bondArgs, " "))
	}

	rootID := parseBondSpawnRootID(bondOut, formulaName, beadID, "")
	if rootID == "" {
		return "", fmt.Errorf("direct bond output missing spawned root id (output: %s)", trimJSONForError(bondOut))
	}
	return rootID, nil
}

// parseBondSpawnRootID extracts the spawned molecule root from bd mol bond JSON.
// Handles both legacy output (root_id) and polymorphic output (result_id + id_mapping).
func parseBondSpawnRootID(bondOut []byte, formulaName, beadID, fallbackID string) string {
	rootID, _ := parseBondSpawnRootIDWithStatus(bondOut, formulaName, beadID, fallbackID)
	return rootID
}

func parseBondSpawnRootIDWithStatus(bondOut []byte, formulaName, beadID, fallbackID string) (string, bool) {
	var bondResult struct {
		RootID    string            `json:"root_id"`
		ResultID  string            `json:"result_id"`
		NewEpicID string            `json:"new_epic_id"`
		IDMapping map[string]string `json:"id_mapping"`
	}
	if err := json.Unmarshal(bondOut, &bondResult); err != nil {
		return fallbackID, false
	}

	if len(bondResult.IDMapping) > 0 {
		if mappedID := bondResult.IDMapping[formulaName]; mappedID != "" {
			return mappedID, true
		}
		if !strings.HasPrefix(formulaName, "mol-") {
			if mappedID := bondResult.IDMapping["mol-"+formulaName]; mappedID != "" {
				return mappedID, true
			}
		}
		var onlySpawned string
		for _, mappedID := range bondResult.IDMapping {
			if mappedID == "" || mappedID == beadID {
				continue
			}
			if onlySpawned != "" && onlySpawned != mappedID {
				onlySpawned = ""
				break
			}
			onlySpawned = mappedID
		}
		if onlySpawned != "" {
			return onlySpawned, true
		}
	}

	for _, candidate := range []string{bondResult.RootID, bondResult.ResultID, bondResult.NewEpicID} {
		if candidate != "" && candidate != beadID {
			return candidate, true
		}
	}
	return fallbackID, true
}

// ensureFormulaRequiredVars appends missing required vars for formulas that enforce
// strict var presence on direct bond paths.
func ensureFormulaRequiredVars(formulaName string, vars []string) []string {
	// Currently only mol-polecat-work has strict required vars on bond.
	if formulaName != "mol-polecat-work" && formulaName != "polecat-work" {
		return vars
	}

	seen := make(map[string]bool, len(vars))
	for _, variable := range vars {
		if eq := strings.Index(variable, "="); eq > 0 {
			seen[variable[:eq]] = true
		}
	}

	requiredDefaults := []struct {
		Key   string
		Value string
	}{
		{"base_branch", "main"},
		{"setup_command", ""},
		{"typecheck_command", ""},
		{"lint_command", ""},
		{"test_command", ""},
		{"build_command", ""},
	}
	for _, item := range requiredDefaults {
		if !seen[item.Key] {
			vars = append(vars, item.Key+"="+item.Value)
		}
	}
	return vars
}

// CookFormula cooks a formula to ensure its proto exists.
// This is useful for batch mode where we cook once before processing multiple beads.
// townRoot is required for GT_ROOT so bd can find town-level formulas.
// Falls back to embedded formula extraction if bd can't find the formula on disk.
func CookFormula(formulaName, workDir, townRoot string) error {
	err := BdCmd("cook", formulaName).
		Dir(workDir).
		WithAutoCommit().
		WithGTRoot(townRoot).
		Run()
	if err == nil {
		return nil
	}
	// Retry with embedded formula extracted to temp file
	resolved, cleanup := resolveFormulaToTempFile(formulaName)
	if cleanup != nil {
		defer cleanup()
	}
	if resolved == formulaName {
		return err // No embedded fallback available
	}
	return BdCmd("cook", resolved).
		Dir(workDir).
		WithAutoCommit().
		WithGTRoot(townRoot).
		Run()
}

// resolveFormulaToTempFile extracts an embedded formula to a temp file.
// Returns the temp file path and a cleanup function, or the original name
// if extraction fails. Used as a fallback when bd can't find the formula on disk.
func resolveFormulaToTempFile(formulaName string) (resolved string, cleanup func()) {
	content, err := formula.GetEmbeddedFormulaContent(formulaName)
	if err != nil {
		return formulaName, nil
	}

	tmpFile, err := os.CreateTemp("", "gt-formula-*.formula.toml")
	if err != nil {
		return formulaName, nil
	}
	if _, err := tmpFile.Write(content); err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		return formulaName, nil
	}
	tmpFile.Close()

	return tmpFile.Name(), func() { os.Remove(tmpFile.Name()) }
}

// isHookedAgentDeadFn is a seam for tests. Production uses isHookedAgentDead.
var isHookedAgentDeadFn = isHookedAgentDead

// isHookedAgentDead checks if the tmux session for a hooked assignee is dead.
// Used by sling to auto-force re-sling when the previous agent has no active session (gt-pqf9x).
// Returns true if the session is confirmed dead. Returns false if alive or if we
// can't determine liveness (conservative: don't auto-force on uncertainty).
func isHookedAgentDead(assignee string) bool {
	sessionName, _ := assigneeToSessionName(assignee)
	if sessionName == "" {
		return false // Unknown format, can't determine
	}
	t := tmux.NewTmux()
	alive, err := t.HasSession(sessionName)
	if err != nil {
		return false // tmux not available or error, be conservative
	}
	return !alive
}

// hookBeadWithRetry hooks a bead to a target agent with exponential backoff retry
// and post-hook verification. This ensures the hook sticks even under Dolt concurrency.
// Fails fast on configuration/initialization errors (gt-2ra).
// See: https://github.com/steveyegge/gastown/issues/148
func hookBeadWithRetry(beadID, targetAgent, hookDir string) error {
	return hookBeadWithRetryWithTownRoot(beadID, targetAgent, hookDir, "")
}

func hookBeadWithRetryWithTownRoot(beadID, targetAgent, hookDir, townRoot string) error {
	const maxRetries = 10
	const baseBackoff = 500 * time.Millisecond
	const maxBackoff = 30 * time.Second
	skipVerify := os.Getenv("GT_TEST_SKIP_HOOK_VERIFY") != ""

	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		out, err := BdCmd("update", beadID, "--status=hooked", "--assignee="+targetAgent).
			Dir(hookDir).
			WithAutoCommit().
			CombinedOutput()
		if err != nil {
			if len(out) > 0 {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			lastErr = err
			// Fail fast on config/init errors — retrying won't help (gt-2ra)
			if isSlingConfigError(err) {
				return fmt.Errorf("hooking bead failed (non-retryable Dolt/beads failure — not retrying): %w\nSafe next action: run `gt dolt status` and `bd show %s` to verify whether a durable hook exists before re-slinging", err, beadID)
			}
			if attempt < maxRetries {
				backoff := slingBackoff(attempt, baseBackoff, maxBackoff)
				fmt.Printf("%s Hook attempt %d failed, retrying in %v...\n", style.Warning.Render("⚠"), attempt, backoff)
				time.Sleep(backoff)
				continue
			}
			return fmt.Errorf("hooking bead after %d attempts: %w", maxRetries, err)
		}

		if skipVerify {
			break
		}

		verifyInfo, verifyErr := getBeadInfoFromTownRoot(townRoot, beadID)
		if verifyErr != nil {
			lastErr = fmt.Errorf("verifying hook: %w", verifyErr)
			if attempt < maxRetries {
				backoff := slingBackoff(attempt, baseBackoff, maxBackoff)
				fmt.Printf("%s Hook verification failed, retrying in %v...\n", style.Warning.Render("⚠"), backoff)
				time.Sleep(backoff)
				continue
			}
			return fmt.Errorf("verifying hook after %d attempts: %w", maxRetries, lastErr)
		}

		if verifyInfo.Status != "hooked" || verifyInfo.Assignee != targetAgent {
			lastErr = fmt.Errorf("hook did not stick: status=%s, assignee=%s (expected hooked, %s)",
				verifyInfo.Status, verifyInfo.Assignee, targetAgent)
			if attempt < maxRetries {
				backoff := slingBackoff(attempt, baseBackoff, maxBackoff)
				fmt.Printf("%s %v, retrying in %v...\n", style.Warning.Render("⚠"), lastErr, backoff)
				time.Sleep(backoff)
				continue
			}
			return fmt.Errorf("hook failed after %d attempts: %w", maxRetries, lastErr)
		}

		break
	}

	return nil
}

var hookBeadWithRetryFn = hookBeadWithRetry
var hookBeadWithRetryWithTownRootFn = hookBeadWithRetryWithTownRoot

// slingBackoff calculates exponential backoff with ±25% jitter for a given attempt (1-indexed).
// Formula: base * 2^(attempt-1) * (1 ± 25% random), capped at max.
func slingBackoff(attempt int, base, max time.Duration) time.Duration { //nolint:unparam // base is parameterized for testability
	backoff := base
	for i := 1; i < attempt; i++ {
		backoff *= 2
		if backoff > max {
			backoff = max
			break
		}
	}
	// Apply ±25% jitter
	jitter := 1.0 + (rand.Float64()-0.5)*0.5 // range [0.75, 1.25]
	result := time.Duration(float64(backoff) * jitter)
	if result > max {
		result = max
	}
	return result
}

// isSlingConfigError returns true if the error indicates a configuration or
// initialization problem rather than a transient failure. Config errors should
// NOT be retried because they will fail identically on every attempt (gt-2ra).
func isSlingConfigError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not initialized") ||
		strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "table not found") ||
		strings.Contains(msg, "issue_prefix") ||
		strings.Contains(msg, "no database") ||
		strings.Contains(msg, "database not found") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "circuit breaker") ||
		strings.Contains(msg, "server appears down") ||
		strings.Contains(msg, "server down") ||
		strings.Contains(msg, "server is not running") ||
		strings.Contains(msg, "server may not be running")
}

// loadRigCommandVars reads rig settings and returns --var key=value strings
// for all configured build pipeline commands (setup, typecheck, lint, test, build)
// and the default branch (base_branch). Only non-empty values are included.
//
// Settings are resolved in priority order:
//  1. Repository defaults: <rig>/mayor/rig/.gastown/settings.json (committed to git)
//  2. Rig-local overrides: <rig>/settings/config.json (operator tuning)
//  3. User --var flags (handled by caller, not here)
func loadRigCommandVars(townRoot, rig string) []string {
	if townRoot == "" || rig == "" {
		return nil
	}
	var vars []string

	// Load default_branch from rig root config.json (single source of truth per 5ee9abcc).
	// This sets base_branch for formula instantiation so polecats fork from the right branch.
	rigCfg, err := rigpkg.LoadRigConfig(filepath.Join(townRoot, rig))
	if err == nil && rigCfg != nil && rigCfg.DefaultBranch != "" {
		vars = append(vars, fmt.Sprintf("base_branch=%s", rigCfg.DefaultBranch))
	}

	// Load repo-sourced settings (floor — committed to git, always present after clone)
	var repoMQ *config.MergeQueueConfig
	repoRoot := filepath.Join(townRoot, rig, "mayor", "rig")
	repoSettings, _ := config.LoadRepoSettings(repoRoot)
	if repoSettings != nil {
		repoMQ = repoSettings.MergeQueue
	}

	// Load rig-local settings (override — operator tuning)
	var localMQ *config.MergeQueueConfig
	settingsPath := filepath.Join(townRoot, rig, "settings", "config.json")
	localSettings, err := config.LoadRigSettings(settingsPath)
	if err == nil && localSettings != nil {
		localMQ = localSettings.MergeQueue
	}

	// Merge: repo defaults + local overrides
	mq := config.MergeSettingsCommand(repoMQ, localMQ)
	if mq == nil {
		return vars
	}

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
	if mq.MergeStrategy != "" {
		vars = append(vars, fmt.Sprintf("merge_strategy=%s", mq.MergeStrategy))
	}
	if mq.IsRequireReviewEnabled() {
		vars = append(vars, "require_review=true")
	}
	return vars
}

// shouldAcceptPermissionWarning checks if the agent emits a bypass-permissions
// warning on startup that needs to be acknowledged via tmux.
func shouldAcceptPermissionWarning(agentName string) bool {
	if agentName == "" {
		agentName = "claude" // Default sessions without GT_AGENT are Claude
	}
	preset := config.GetAgentPresetByName(agentName)
	if preset == nil {
		return false
	}
	return preset.EmitsPermissionWarning
}

// updateAgentMode updates the mode field on the agent bead.
// This is needed so the stuck detector can read the mode from agent fields
// and apply appropriate thresholds (ralphcats get longer leash).
func updateAgentMode(agentID, mode, workDir, townBeadsDir string) {
	_ = townBeadsDir // Not used - BEADS_DIR breaks redirect mechanism

	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return
	}
	if workDir == "" {
		workDir = townRoot
	}

	agentBeadID := agentIDToBeadID(agentID, townRoot)
	if agentBeadID == "" {
		return
	}

	agentWorkDir := beads.ResolveHookDir(townRoot, agentBeadID, workDir)
	bd := beads.New(agentWorkDir)
	if err := bd.UpdateAgentDescriptionFields(agentBeadID, beads.AgentFieldUpdates{Mode: &mode}); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: couldn't set agent %s mode: %v\n", agentBeadID, err)
	}
}

// lookupPriorAttempt checks if there are existing open MRs for the given issue.
// If found, returns formula variables with the prior branch name so the new
// polecat can cherry-pick or reference prior work instead of starting from scratch.
// Returns nil if no prior attempt exists. (GH#gt-zqvj)
func lookupPriorAttempt(beadsDir, issueID string) []string {
	bd := beads.New(beadsDir)
	mrs, err := bd.FindOpenMRsForIssue(issueID)
	if err != nil || len(mrs) == 0 {
		return nil
	}

	// Use the most recent MR (last in list) as the prior attempt.
	prior := mrs[len(mrs)-1]
	fields := beads.ParseMRFields(prior)
	if fields == nil || fields.Branch == "" {
		return nil
	}

	vars := []string{
		fmt.Sprintf("prior_branch=%s", fields.Branch),
	}
	if fields.CloseReason != "" {
		vars = append(vars, fmt.Sprintf("prior_failure=%s", fields.CloseReason))
	}
	return vars
}
