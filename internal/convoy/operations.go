// Package convoy provides convoy tracking operations: finding tracking convoys,
// checking completion, feeding ready issues, and dispatching via gt sling.
package convoy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/util"
	beadsdk "github.com/steveyegge/beads"
)

// CheckConvoysForIssue finds any convoys tracking the given issue and triggers
// convoy completion checks. If the convoy is not complete, it reactively feeds
// the next ready issue to keep the convoy progressing without waiting for
// polling-based patrol cycles.
//
// The check is idempotent - running it multiple times for the same issue is safe.
// The underlying `gt convoy check` handles already-closed convoys gracefully.
//
// Parameters:
//   - ctx: context for storage operations
//   - store: beads storage for dependency/issue queries (nil skips convoy checks)
//   - townRoot: path to the town root directory
//   - issueID: the issue ID that was just closed
//   - caller: identifier for logging (e.g., "Convoy")
//   - logger: optional logger function (can be nil)
//   - gtPath: resolved path to the gt binary (e.g. from exec.LookPath or daemon config)
//   - resolver: optional StoreResolver for cross-database issue resolution (nil falls back to subprocess)
//
// Returns the convoy IDs that were checked (may be empty if issue isn't tracked).
func CheckConvoysForIssue(ctx context.Context, store beadsdk.Storage, townRoot, issueID, caller string, logger func(format string, args ...interface{}), gtPath string, isRigParked func(string) bool, resolver ...*StoreResolver) []string {
	if logger == nil {
		logger = func(format string, args ...interface{}) {} // no-op
	}
	if isRigParked == nil {
		isRigParked = func(string) bool { return false }
	}
	if store == nil {
		return nil
	}

	// Extract optional resolver (variadic for backward compatibility)
	var res *StoreResolver
	if len(resolver) > 0 {
		res = resolver[0]
	}

	// Find convoys tracking this issue
	convoyIDs := getTrackingConvoys(ctx, store, issueID, logger)
	if len(convoyIDs) == 0 {
		return nil
	}

	logger("%s: %s tracked by %d convoy(s): %v", caller, issueID, len(convoyIDs), convoyIDs)

	// Run convoy check for each tracking convoy
	// Note: gt convoy check is idempotent and handles already-closed convoys
	for _, convoyID := range convoyIDs {
		if isConvoyClosed(ctx, store, convoyID) {
			logger("%s: convoy %s already closed, skipping", caller, convoyID)
			continue
		}

		if isConvoyStaged(ctx, store, convoyID) {
			logger("%s: convoy %s is staged (not yet launched), skipping", caller, convoyID)
			continue
		}

		logger("%s: checking convoy %s", caller, convoyID)
		if err := runConvoyCheck(ctx, townRoot, convoyID, gtPath); err != nil {
			logger("%s: convoy %s check failed: %s", caller, convoyID, util.FirstLine(err.Error()))
		}

		// Continuation feed: if convoy is still open after the completion check,
		// reactively dispatch the next ready issue. This makes convoy feeding
		// event-driven instead of relying on polling-based patrol cycles.
		if !isConvoyClosed(ctx, store, convoyID) {
			feedNextReadyIssue(ctx, store, townRoot, convoyID, caller, logger, gtPath, isRigParked, res)
		}
	}

	return convoyIDs
}

// getTrackingConvoys returns convoy IDs that track the given issue.
// Uses SDK GetDependentsWithMetadata filtered by type "tracks".
func getTrackingConvoys(ctx context.Context, store beadsdk.Storage, issueID string, logger func(format string, args ...interface{})) []string {
	dependents, err := store.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		if logger != nil {
			logger("Convoy: getTrackingConvoys(%s) store error: %v", issueID, err)
		}
		return nil
	}

	convoyIDs := make([]string, 0)
	for _, d := range dependents {
		if string(d.DependencyType) == "tracks" {
			convoyIDs = append(convoyIDs, d.ID)
		}
	}
	return convoyIDs
}

// isConvoyClosed checks if a convoy is already closed.
func isConvoyClosed(ctx context.Context, store beadsdk.Storage, convoyID string) bool {
	issue, err := store.GetIssue(ctx, convoyID)
	if err != nil || issue == nil {
		return false
	}
	return string(issue.Status) == "closed"
}

// isConvoyStaged checks if a convoy is in a staged state (not yet launched).
// Staged convoys have statuses like "staged_ready" or "staged_warnings".
// They should not be fed until they are launched (transitioned to "open").
func isConvoyStaged(ctx context.Context, store beadsdk.Storage, convoyID string) bool {
	issue, err := store.GetIssue(ctx, convoyID)
	if err != nil || issue == nil {
		return false // fail-open: if we can't read, assume not staged
	}
	return strings.HasPrefix(string(issue.Status), "staged_")
}

// runConvoyCheck runs `gt convoy check <convoy-id>` to check a specific convoy.
// This is idempotent and handles already-closed convoys gracefully.
// The context parameter enables cancellation on daemon shutdown.
// gtPath is the resolved path to the gt binary.
func runConvoyCheck(ctx context.Context, townRoot, convoyID, gtPath string) error {
	cmd := exec.CommandContext(ctx, gtPath, "convoy", "check", convoyID)
	cmd.Dir = townRoot
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, stderr.String())
	}

	return nil
}

// trackedIssue holds basic info about an issue tracked by a convoy.
type trackedIssue struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Assignee  string `json:"assignee"`
	Priority  int    `json:"priority"`
	IssueType string `json:"issue_type"`
}

// slingableTypes are bead types that can be dispatched via gt sling.
// Only leaf work items are slingable — containers (epic) and non-work types
// (decision, message, event) are excluded. Unknown/empty types are treated
// as slingable (beads default to "task" when IssueType is empty).
var slingableTypes = map[string]bool{
	"task":    true,
	"bug":     true,
	"feature": true,
	"chore":   true,
	"":        true, // Empty type defaults to task
}

// IsSlingableType reports whether a bead type can be dispatched via gt sling.
// Exported for use by cmd/convoy.go stranded scan path.
func IsSlingableType(issueType string) bool {
	return slingableTypes[issueType]
}

// blockingDepTypes are dependency types that prevent an issue from being
// dispatched. parent-child is intentionally excluded — a child task is
// dispatchable even if its parent epic is open (consistent with molecule
// step behavior in internal/cmd/molecule_step.go).
var blockingDepTypes = map[string]bool{
	"blocks":             true,
	"conditional-blocks": true,
	"waits-for":          true,
	"merge-blocks":       true,
}

// isIssueBlocked checks if an issue has unclosed blocking dependencies.
// Returns true if any blocks, conditional-blocks, waits-for, or merge-blocks
// dependency targets an issue that is not closed/tombstone.
//
// For merge-blocks dependencies, "closed" alone is not sufficient — the
// blocker must have a CloseReason starting with "Merged in " to confirm
// that the code was actually integrated. This prevents dispatching work
// against un-merged code (see #1893).
//
// When a StoreResolver is provided, cross-database dependencies are resolved
// by querying the appropriate rig store for fresh status. Without a resolver,
// this falls back to the hq store's dependency metadata snapshot, which may
// be stale for cross-rig issues (see GH #2624).
func isIssueBlocked(ctx context.Context, store beadsdk.Storage, issueID string, resolver *StoreResolver) bool {
	if store == nil {
		return false // fail-open: no store means we can't check deps
	}

	// Try the resolver first for cross-database accuracy. The resolver looks up
	// deps in the issue's home store (based on prefix routing), which returns
	// current status. Fall back to hq store if resolver is nil or returns nothing.
	var deps []*beadsdk.IssueWithDependencyMetadata
	if resolver != nil {
		deps = resolver.ResolveDepsWithMetadata(ctx, issueID)
	}
	if len(deps) == 0 {
		var err error
		deps, err = store.GetDependenciesWithMetadata(ctx, issueID)
		if err != nil {
			return false // On error, assume not blocked (fail-open)
		}
	}

	// For cross-rig blocking deps, the metadata snapshot status may be stale.
	// Collect blocker IDs whose status we need to verify via the resolver.
	var staleCandidateIDs []string
	var staleCandidateTypes []string

	for _, d := range deps {
		depType := string(d.DependencyType)
		if !blockingDepTypes[depType] {
			continue
		}
		status := string(d.Status)
		if status == "tombstone" {
			continue // always unblocked
		}
		if status == "closed" {
			// For merge-blocks: "closed" alone is not enough — need merge confirmation
			if depType == "merge-blocks" && !strings.HasPrefix(d.CloseReason, "Merged in ") {
				return true // closed but not merged = still blocked
			}
			continue // closed = unblocked for non-merge-blocks
		}
		// Status is not closed/tombstone. If we have a resolver, the dep might
		// actually be closed in its home store but stale in the snapshot.
		if resolver != nil {
			staleCandidateIDs = append(staleCandidateIDs, extractIssueID(d.ID))
			staleCandidateTypes = append(staleCandidateTypes, depType)
		} else {
			return true // not closed = blocked (no resolver to verify)
		}
	}

	// Verify stale candidates via cross-store resolution
	if len(staleCandidateIDs) > 0 {
		freshMap := resolver.ResolveIssues(ctx, staleCandidateIDs)
		for i, id := range staleCandidateIDs {
			fresh, ok := freshMap[id]
			if !ok {
				return true // can't resolve = assume blocked
			}
			freshStatus := string(fresh.Status)
			if freshStatus == "tombstone" {
				continue
			}
			if freshStatus != "closed" {
				return true // confirmed not closed
			}
			// For merge-blocks: check close reason from fresh data
			if staleCandidateTypes[i] == "merge-blocks" && !strings.HasPrefix(fresh.CloseReason, "Merged in ") {
				return true
			}
		}
	}

	return false
}

// feedNextReadyIssue finds the next ready issue in a convoy and dispatches it
// via gt sling. A ready issue is one that is open, with no assignee, and not
// blocked by unclosed dependencies. This provides reactive (event-driven)
// convoy feeding instead of waiting for polling-based patrol cycles.
//
// Only one issue is dispatched per call. When that issue completes, the
// next close event triggers another feed cycle.
// gtPath is the resolved path to the gt binary.
func feedNextReadyIssue(ctx context.Context, store beadsdk.Storage, townRoot, convoyID, caller string, logger func(format string, args ...interface{}), gtPath string, isRigParked func(string) bool, resolver *StoreResolver) {
	tracked := getConvoyTrackedIssues(ctx, store, convoyID, townRoot, resolver)
	if len(tracked) == 0 {
		return
	}

	// Extract base_branch from convoy description fields
	var baseBranch string
	if convoy, err := store.GetIssue(ctx, convoyID); err == nil && convoy != nil {
		if cf := beads.ParseConvoyFields(&beads.Issue{Description: convoy.Description}); cf != nil {
			baseBranch = cf.BaseBranch
		}
	}

	// Sort by priority (lower = higher) then by ID for deterministic tie-breaking.
	sort.Slice(tracked, func(i, j int) bool {
		if tracked[i].Priority != tracked[j].Priority {
			return tracked[i].Priority < tracked[j].Priority
		}
		return tracked[i].ID < tracked[j].ID
	})

	// Find the first ready issue (open, no assignee, not blocked).
	for _, issue := range tracked {
		if issue.Status != "open" || issue.Assignee != "" {
			continue
		}

		// Filter non-slingable types: only leaf work items (task, bug,
		// feature, chore) can be dispatched. Epics, convoys, and other
		// container types are skipped.
		if !IsSlingableType(issue.IssueType) {
			logger("%s: convoy %s: %s has non-slingable type %q, skipping", caller, convoyID, issue.ID, issue.IssueType)
			continue
		}

		// Check blocking dependencies: blocks and conditional-blocks with
		// non-closed targets prevent dispatch. parent-child is NOT treated
		// as blocking (consistent with molecule step behavior).
		if isIssueBlocked(ctx, store, issue.ID, resolver) {
			logger("%s: convoy %s: %s is blocked, skipping", caller, convoyID, issue.ID)
			continue
		}

		// Determine target rig from issue prefix
		rig := rigForIssue(townRoot, issue.ID)
		if rig == "" {
			logger("%s: convoy %s: cannot determine rig for issue %s, skipping", caller, convoyID, issue.ID)
			continue
		}

		if isRigParked(rig) {
			logger("%s: convoy %s: rig %s is parked, skipping %s", caller, convoyID, rig, issue.ID)
			continue
		}

		logger("%s: convoy %s: feeding next ready issue %s to %s", caller, convoyID, issue.ID, rig)
		if err := dispatchIssue(ctx, townRoot, issue.ID, rig, gtPath, baseBranch); err != nil {
			logger("%s: convoy %s: dispatch %s failed: %s", caller, convoyID, issue.ID, util.FirstLine(err.Error()))
			continue // Try next issue on dispatch failure
		}
		return // Successfully dispatched one issue
	}

	logger("%s: convoy %s: no ready issues to feed", caller, convoyID)
}

// getConvoyTrackedIssues returns issues tracked by a convoy with fresh status.
// Uses SDK GetDependenciesWithMetadata filtered by tracks, then GetIssuesByIDs for current status.
// When a StoreResolver is provided, cross-rig beads are resolved via direct store queries.
// Otherwise falls back to bd show subprocess via fetchCrossRigBeadStatus.
func getConvoyTrackedIssues(ctx context.Context, store beadsdk.Storage, convoyID, townRoot string, resolver *StoreResolver) []trackedIssue {
	deps, err := store.GetDependenciesWithMetadata(ctx, convoyID)
	if err != nil || len(deps) == 0 {
		return nil
	}

	// Filter by tracks type and collect IDs
	var ids []string
	type depMeta struct {
		status    string
		assignee  string
		priority  int
		issueType string
	}
	metaByID := make(map[string]depMeta)
	for _, d := range deps {
		if string(d.DependencyType) == "tracks" {
			id := extractIssueID(d.ID)
			ids = append(ids, id)
			metaByID[id] = depMeta{
				status:    string(d.Status),
				assignee:  d.Assignee,
				priority:  d.Priority,
				issueType: string(d.IssueType),
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}

	// Refresh status via GetIssuesByIDs for cross-rig accuracy
	freshIssues, err := store.GetIssuesByIDs(ctx, ids)
	if err != nil {
		freshIssues = nil
	}

	freshMap := make(map[string]*beadsdk.Issue)
	for _, iss := range freshIssues {
		if iss != nil {
			freshMap[iss.ID] = iss
		}
	}

	// Cross-rig resolution: for beads not found in the local store (e.g., ds-*
	// beads when convoys live in hq), resolve via the StoreResolver which
	// queries the appropriate rig store directly. Falls back to bd show
	// subprocess if no resolver is available. See GH #2624.
	var missingIDs []string
	for _, id := range ids {
		if _, ok := freshMap[id]; !ok {
			missingIDs = append(missingIDs, id)
		}
	}
	if len(missingIDs) > 0 {
		if resolver != nil {
			// Direct store queries — faster, no subprocess, no bd dependency
			crossRigFresh := resolver.ResolveIssues(ctx, missingIDs)
			for id, fresh := range crossRigFresh {
				freshMap[id] = fresh
			}
		} else if townRoot != "" {
			// Legacy fallback: subprocess bd show per rig
			crossRigFresh := fetchCrossRigBeadStatus(townRoot, missingIDs)
			for id, fresh := range crossRigFresh {
				freshMap[id] = fresh
			}
		}
	}

	result := make([]trackedIssue, 0, len(ids))
	for _, id := range ids {
		t := trackedIssue{ID: id}
		if fresh := freshMap[id]; fresh != nil {
			t.Status = string(fresh.Status)
			t.Assignee = fresh.Assignee
			t.Priority = fresh.Priority
			t.IssueType = string(fresh.IssueType)
		} else if meta, ok := metaByID[id]; ok {
			t.Status = meta.status
			t.Assignee = meta.assignee
			t.Priority = meta.priority
			t.IssueType = meta.issueType
		}
		result = append(result, t)
	}

	return result
}

// extractIssueID strips the external:prefix:id wrapper from bead IDs.
func extractIssueID(id string) string {
	if strings.HasPrefix(id, "external:") {
		parts := strings.SplitN(id, ":", 3)
		if len(parts) == 3 {
			return parts[2]
		}
	}
	return id
}

// rigForIssue determines the rig name for an issue based on its ID prefix.
// Uses the beads routes to map prefixes to rigs.
func rigForIssue(townRoot, issueID string) string {
	prefix := beads.ExtractPrefix(issueID)
	if prefix == "" {
		return ""
	}
	return beads.GetRigNameForPrefix(townRoot, prefix)
}

// fetchCrossRigBeadStatus fetches fresh status for beads that live in other rigs.
// Groups IDs by prefix, resolves each prefix to its rig directory via routes,
// and runs `bd show --json <ids>` per rig. Pattern from batchFetchBeadInfoByIDs
// in capacity_dispatch.go.
func fetchCrossRigBeadStatus(townRoot string, ids []string) map[string]*beadsdk.Issue {
	result := make(map[string]*beadsdk.Issue)
	if len(ids) == 0 {
		return result
	}

	// Group IDs by prefix
	byPrefix := make(map[string][]string)
	for _, id := range ids {
		prefix := beads.ExtractPrefix(id)
		if prefix != "" {
			byPrefix[prefix] = append(byPrefix[prefix], id)
		}
	}

	for prefix, prefixIDs := range byPrefix {
		rigPath := beads.GetRigPathForPrefix(townRoot, prefix)
		if rigPath == "" {
			continue
		}

		args := append([]string{"show", "--json"}, prefixIDs...)
		cmd := beads.Spawn(args...)
		cmd.Dir = rigPath
		util.SetDetachedProcessGroup(cmd)
		out, err := cmd.Output()
		if err != nil {
			continue
		}

		var items []struct {
			ID       string `json:"id"`
			Status   string `json:"status"`
			Assignee string `json:"assignee"`
			Priority int    `json:"priority"`
			Type     string `json:"issue_type"`
		}
		if err := json.Unmarshal(out, &items); err != nil {
			continue
		}
		for _, item := range items {
			result[item.ID] = &beadsdk.Issue{
				ID:        item.ID,
				Status:    beadsdk.Status(item.Status),
				Assignee:  item.Assignee,
				Priority:  item.Priority,
				IssueType: beadsdk.IssueType(item.Type),
			}
		}
	}

	return result
}

// FireCrossRigDepNotifications checks if any issues in other rigs were unblocked
// by the closure of closedIssueID. For each affected rig, sends a nudge to that
// rig's witness so it can react to the resolved dependency.
//
// Cross-rig deps are stored in the dependent issue's store as "external:<prefix>:<id>".
// To find them, this function queries each rig store using the external-wrapped form
// of the closed issue ID.
//
// This is best-effort: failures are silently logged. The closed issue's own store
// is skipped (same-rig deps don't need cross-rig notification).
func FireCrossRigDepNotifications(ctx context.Context, closedIssueID, townRoot string, stores map[string]beadsdk.Storage, logger func(format string, args ...interface{})) {
	if logger == nil {
		logger = func(format string, args ...interface{}) {}
	}
	if len(stores) == 0 || closedIssueID == "" || townRoot == "" {
		return
	}

	// Determine the home store of the closed issue so we can skip it.
	closedPrefix := beads.ExtractPrefix(closedIssueID)
	if closedPrefix == "" {
		return
	}
	closedRig := beads.GetRigNameForPrefix(townRoot, closedPrefix)
	closedStoreKey := closedRig
	if closedStoreKey == "" {
		closedStoreKey = "hq"
	}

	// Build the external-wrapped form used when storing cross-rig dep records:
	// "external:<prefix-without-trailing-dash>:<id>"
	externalID := fmt.Sprintf("external:%s:%s", strings.TrimSuffix(closedPrefix, "-"), closedIssueID)

	// Track which rigs have already been notified to avoid duplicate nudges.
	notifiedRigs := make(map[string]bool)

	for storeName, store := range stores {
		if storeName == closedStoreKey {
			continue // skip the closed issue's own store
		}

		dependents, err := store.GetDependentsWithMetadata(ctx, externalID)
		if err != nil || len(dependents) == 0 {
			continue
		}

		for _, dep := range dependents {
			if dep == nil {
				continue
			}
			depType := string(dep.DependencyType)
			if !blockingDepTypes[depType] {
				continue
			}

			// Determine the rig for the dependent issue.
			depID := extractIssueID(dep.ID)
			depPrefix := beads.ExtractPrefix(depID)
			if depPrefix == "" {
				continue
			}
			depRig := beads.GetRigNameForPrefix(townRoot, depPrefix)
			if depRig == "" || depRig == closedRig {
				continue
			}
			if notifiedRigs[depRig] {
				continue
			}
			notifiedRigs[depRig] = true

			depTitle := dep.Title
			logger("CrossRig: %s closed, unblocking %s (%s) — nudging %s/witness", closedIssueID, depID, depRig, depRig)

			msg := fmt.Sprintf("Dependency resolved: %s — External dependency %s has closed. Unblocked: %s (%s). This issue may now proceed.",
				closedIssueID, closedIssueID, depID, depTitle)
			nudgeCmd := exec.Command("gt", "nudge", depRig+"/witness", "-m", msg)
			nudgeCmd.Dir = townRoot
			if err := nudgeCmd.Run(); err != nil {
				logger("CrossRig: nudge %s/witness failed: %v", depRig, err)
			}
		}
	}
}

// dispatchIssue dispatches an issue to a rig via gt sling.
// The context parameter enables cancellation on daemon shutdown.
// gtPath is the resolved path to the gt binary.
func dispatchIssue(ctx context.Context, townRoot, issueID, rig, gtPath, baseBranch string) error {
	args := []string{"sling", issueID, rig, "--no-boot"}
	if baseBranch != "" {
		args = append(args, "--base-branch="+baseBranch)
	}
	cmd := exec.CommandContext(ctx, gtPath, args...)
	cmd.Dir = townRoot
	util.SetProcessGroup(cmd)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}

	return nil
}
