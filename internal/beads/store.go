// Package beads: in-process beadsdk.Storage integration.
//
// When a beadsdk.Storage is set on a Beads instance (via NewWithStore or
// SetStore), methods bypass the bd subprocess and use the store directly.
// This eliminates ~600ms per operation and the ~30ms CPU overhead of process
// spawning. Follows the pattern established by internal/daemon/convoy_manager.go.
package beads

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// SetStore configures an in-process beadsdk.Storage for this Beads instance.
// When set, methods that have in-process implementations will use the store
// directly instead of shelling out to the bd CLI. Methods without in-process
// implementations will still fall back to the subprocess.
//
// Callers are responsible for closing the store when done.
func (b *Beads) SetStore(store beadsdk.Storage) {
	b.store = store
}

// Store returns the in-process beadsdk.Storage, or nil if not set.
func (b *Beads) Store() beadsdk.Storage {
	return b.store
}

// NewWithStore creates a new Beads wrapper backed by an in-process store.
// The store is used for direct SDK calls, bypassing bd subprocess spawning.
// Callers are responsible for closing the store when done.
func NewWithStore(workDir string, store beadsdk.Storage) *Beads {
	return &Beads{workDir: workDir, store: store}
}

// NewWithBeadsDirAndStore creates a Beads wrapper with an explicit BEADS_DIR
// and an in-process store. Used for cross-database access from polecat worktrees.
func NewWithBeadsDirAndStore(workDir, beadsDir string, store beadsdk.Storage) *Beads {
	return &Beads{workDir: workDir, beadsDir: beadsDir, store: store}
}

// OpenStore opens a beadsdk.Storage for the resolved beads directory.
// This is a convenience for short-lived gt commands that open, use, and close
// a store within a single invocation. For long-lived processes (daemon), prefer
// keeping persistent stores via SetStore.
//
// Returns the store and a cleanup function. Always call cleanup when done:
//
//	store, cleanup, err := b.OpenStore(ctx)
//	if err != nil { /* fall back to subprocess */ }
//	defer cleanup()
func (b *Beads) OpenStore(ctx context.Context) (beadsdk.Storage, func(), error) {
	beadsDir := b.beadsDir
	if beadsDir == "" {
		beadsDir = ResolveBeadsDir(b.workDir)
	}
	if beadsDir == "" {
		return nil, nil, fmt.Errorf("no beads directory found")
	}

	store, err := beadsdk.OpenFromConfig(ctx, beadsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("opening beads store: %w", err)
	}

	cleanup := func() {
		_ = store.Close()
	}
	return store, cleanup, nil
}

// storeCtx returns a context with a standard timeout for store operations.
func storeCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// sdkIssueToIssue converts a beadsdk Issue (types.Issue) to the gastown
// beads.Issue type used throughout the gt codebase. This handles the type
// differences between the two representations:
//   - time.Time → string (RFC3339)
//   - types.Status → string
//   - types.IssueType → string
//   - Labels populated from the SDK issue
func sdkIssueToIssue(si *beadsdk.Issue) *Issue {
	if si == nil {
		return nil
	}

	issue := &Issue{
		ID:                 si.ID,
		Title:              si.Title,
		Description:        si.Description,
		Status:             string(si.Status),
		Priority:           si.Priority,
		Type:               string(si.IssueType),
		CreatedAt:          si.CreatedAt.Format(time.RFC3339),
		CreatedBy:          si.CreatedBy,
		UpdatedAt:          si.UpdatedAt.Format(time.RFC3339),
		Assignee:           si.Assignee,
		Labels:             si.Labels,
		Ephemeral:          si.Ephemeral,
		AcceptanceCriteria: si.AcceptanceCriteria,
		Metadata:           si.Metadata,
	}

	if si.ClosedAt != nil {
		issue.ClosedAt = si.ClosedAt.Format(time.RFC3339)
	}

	// Populate dependency-derived fields from the SDK issue's Dependencies.
	// The SDK issue may have Dependencies populated (from show) or not (from list).
	if len(si.Dependencies) > 0 {
		var deps []string
		for _, dep := range si.Dependencies {
			if dep.IssueID != si.ID {
				continue
			}

			switch {
			case dep.Type == beadsdk.DepParentChild:
				// If this issue depends on the parent, the parent is the DependsOnID
				issue.Parent = dep.DependsOnID
			case isBlockingDependencyType(string(dep.Type)):
				deps = append(deps, dep.DependsOnID)
			}
		}
		if len(deps) > 0 {
			issue.DependsOn = deps
		}
	}

	return issue
}

func sdkDependencyMetadataToIssueDep(dep *beadsdk.IssueWithDependencyMetadata) IssueDep {
	if dep == nil {
		return IssueDep{}
	}
	return IssueDep{
		ID:             dep.ID,
		Title:          dep.Title,
		Status:         string(dep.Status),
		Priority:       dep.Priority,
		Type:           string(dep.IssueType),
		DependencyType: string(dep.DependencyType),
		CloseReason:    dep.CloseReason,
	}
}

func sdkDependencyMetadataToIssueDeps(deps []*beadsdk.IssueWithDependencyMetadata) []IssueDep {
	if len(deps) == 0 {
		return nil
	}
	issueDeps := make([]IssueDep, 0, len(deps))
	for _, dep := range deps {
		issueDep := sdkDependencyMetadataToIssueDep(dep)
		if issueDep.ID == "" {
			continue
		}
		issueDeps = append(issueDeps, issueDep)
	}
	return issueDeps
}

// sdkIssuesToIssues converts a slice of SDK issues to gastown issues.
func sdkIssuesToIssues(sdkIssues []*beadsdk.Issue) []*Issue {
	if sdkIssues == nil {
		return nil
	}
	issues := make([]*Issue, len(sdkIssues))
	for i, si := range sdkIssues {
		issues[i] = sdkIssueToIssue(si)
	}
	return issues
}

// issueFilterFromListOpts builds a beadsdk IssueFilter from ListOptions.
func issueFilterFromListOpts(opts ListOptions) beadsdk.IssueFilter {
	f := beadsdk.IssueFilter{
		Limit: opts.Limit,
	}

	if opts.Status != "" && opts.Status != "all" {
		status := beadsdk.Status(opts.Status)
		f.Status = &status
	}

	// Prefer Label; fall back to deprecated Type
	if opts.Label != "" {
		f.Labels = []string{opts.Label}
	} else if opts.Type != "" {
		f.Labels = []string{"gt:" + opts.Type}
	}

	if opts.Priority >= 0 {
		f.Priority = &opts.Priority
	}

	if opts.Parent != "" {
		f.ParentID = &opts.Parent
	}

	if opts.Assignee != "" {
		f.Assignee = &opts.Assignee
	}

	if opts.NoAssignee {
		f.NoAssignee = true
	}

	if opts.Ephemeral {
		eph := true
		f.Ephemeral = &eph
	}

	return f
}

// workFilterFromListOpts builds a beadsdk WorkFilter from ListOptions.
func workFilterFromListOpts(opts ListOptions) beadsdk.WorkFilter {
	f := beadsdk.WorkFilter{
		Limit: opts.Limit,
	}

	if opts.Label != "" {
		f.Labels = []string{opts.Label}
	} else if opts.Type != "" {
		f.Labels = []string{"gt:" + opts.Type}
	}

	if opts.Priority >= 0 {
		f.Priority = &opts.Priority
	}

	if opts.Assignee != "" {
		f.Assignee = &opts.Assignee
	}

	if opts.NoAssignee {
		f.Unassigned = true
	}

	return f
}

// storeList implements List using the in-process store.
func (b *Beads) storeList(opts ListOptions) ([]*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	filter := issueFilterFromListOpts(opts)
	sdkIssues, err := b.store.SearchIssues(ctx, "", filter)
	if err != nil {
		return nil, fmt.Errorf("store list: %w", err)
	}

	return sdkIssuesToIssues(sdkIssues), nil
}

// storeShow implements Show using the in-process store.
func (b *Beads) storeShow(id string) (*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	si, err := b.store.GetIssue(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("store show: %w", err)
	}

	issue, err := b.storeHydrateIssueDetails(ctx, sdkIssueToIssue(si))
	if err != nil {
		return nil, err
	}

	// Enrich with labels (SDK GetIssue may not include them)
	if issue.Labels == nil {
		labels, labelsErr := b.store.GetLabels(ctx, id)
		if labelsErr == nil {
			issue.Labels = labels
		}
	}

	return issue, nil
}

// storeShowMultiple implements ShowMultiple using the in-process store.
func (b *Beads) storeShowMultiple(ids []string) (map[string]*Issue, error) {
	if len(ids) == 0 {
		return make(map[string]*Issue), nil
	}

	ctx, cancel := storeCtx()
	defer cancel()

	sdkIssues, err := b.store.GetIssuesByIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("store show multiple: %w", err)
	}

	result := make(map[string]*Issue, len(sdkIssues))
	for _, si := range sdkIssues {
		issue, hydrateErr := b.storeHydrateIssueDetails(ctx, sdkIssueToIssue(si))
		if hydrateErr != nil {
			return nil, hydrateErr
		}
		result[si.ID] = issue
	}
	return result, nil
}

func (b *Beads) storeHydrateIssueDetails(ctx context.Context, issue *Issue) (*Issue, error) {
	if issue == nil {
		return nil, nil
	}
	deps, err := b.store.GetDependenciesWithMetadata(ctx, issue.ID)
	if err != nil {
		return nil, fmt.Errorf("store dependencies for %s: %w", issue.ID, err)
	}
	issue.Dependencies = sdkDependencyMetadataToIssueDeps(deps)
	issue.DependencyCount = len(issue.Dependencies)
	return issue, nil
}

// storeCreate implements Create using the in-process store.
func (b *Beads) storeCreate(opts CreateOptions) (*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	sdkIssue := &beadsdk.Issue{
		Title:       opts.Title,
		Description: opts.Description,
		Priority:    opts.Priority,
		Ephemeral:   opts.Ephemeral,
	}

	// Set issue type from Labels, Label, or Type (same precedence as CLI path)
	if len(opts.Labels) > 0 {
		sdkIssue.Labels = opts.Labels
	} else if opts.Label != "" {
		sdkIssue.Labels = []string{opts.Label}
	} else if opts.Type != "" {
		sdkIssue.Labels = []string{"gt:" + opts.Type}
	}

	// Actor
	actor := opts.Actor
	if actor == "" {
		actor = b.getActor()
	}

	if err := b.store.CreateIssue(ctx, sdkIssue, actor); err != nil {
		return nil, fmt.Errorf("store create: %w", err)
	}

	// Handle parent relationship
	if opts.Parent != "" {
		dep := &beadsdk.Dependency{
			IssueID:     sdkIssue.ID,
			DependsOnID: opts.Parent,
			Type:        beadsdk.DepParentChild,
		}
		if depErr := b.store.AddDependency(ctx, dep, actor); depErr != nil {
			return nil, fmt.Errorf("store create: issue %s created but parent link failed: %w", sdkIssue.ID, depErr)
		}
	}

	return sdkIssueToIssue(sdkIssue), nil
}

// storeUpdate implements Update using the in-process store.
func (b *Beads) storeUpdate(id string, opts UpdateOptions) error {
	ctx, cancel := storeCtx()
	defer cancel()

	updates := make(map[string]interface{})

	if opts.Title != nil {
		updates["title"] = *opts.Title
	}
	if opts.Status != nil {
		updates["status"] = *opts.Status
	}
	if opts.Priority != nil {
		updates["priority"] = *opts.Priority
	}
	if opts.Description != nil {
		updates["description"] = *opts.Description
	}
	if opts.Assignee != nil {
		updates["assignee"] = *opts.Assignee
	}

	actor := b.getActor()

	// Apply updates if there are field changes
	if len(updates) > 0 {
		if err := b.store.UpdateIssue(ctx, id, updates, actor); err != nil {
			return fmt.Errorf("store update: %w", err)
		}
	}

	// Handle label operations
	if len(opts.SetLabels) > 0 {
		// Set-labels: get current, remove all, add new
		currentLabels, err := b.store.GetLabels(ctx, id)
		if err != nil {
			return fmt.Errorf("store update: get labels for %s: %w", id, err)
		}
		for _, l := range currentLabels {
			if err := b.store.RemoveLabel(ctx, id, l, actor); err != nil {
				return fmt.Errorf("store update: remove label %q from %s: %w", l, id, err)
			}
		}
		for _, l := range opts.SetLabels {
			if err := b.store.AddLabel(ctx, id, l, actor); err != nil {
				return fmt.Errorf("store update: add label %q to %s: %w", l, id, err)
			}
		}
	} else {
		for _, l := range opts.AddLabels {
			if err := b.store.AddLabel(ctx, id, l, actor); err != nil {
				return fmt.Errorf("store update: add label %q to %s: %w", l, id, err)
			}
		}
		for _, l := range opts.RemoveLabels {
			if err := b.store.RemoveLabel(ctx, id, l, actor); err != nil {
				return fmt.Errorf("store update: remove label %q from %s: %w", l, id, err)
			}
		}
	}

	return nil
}

// storeClose implements Close using the in-process store.
func (b *Beads) storeClose(reason, session string, ids ...string) error {
	ctx, cancel := storeCtx()
	defer cancel()

	actor := b.getActor()

	for _, id := range ids {
		if err := b.store.CloseIssue(ctx, id, reason, actor, session); err != nil {
			return fmt.Errorf("store close %s: %w", id, err)
		}
	}
	return nil
}

// storeReady implements Ready using the in-process store.
func (b *Beads) storeReady() ([]*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	sdkIssues, err := b.store.GetReadyWork(ctx, beadsdk.WorkFilter{})
	if err != nil {
		return nil, fmt.Errorf("store ready: %w", err)
	}

	return sdkIssuesToIssues(sdkIssues), nil
}

// storeReadyWithFilter implements Ready with a WorkFilter using the in-process store.
func (b *Beads) storeReadyWithFilter(filter beadsdk.WorkFilter) ([]*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	sdkIssues, err := b.store.GetReadyWork(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("store ready: %w", err)
	}

	return sdkIssuesToIssues(sdkIssues), nil
}

// storeBlocked implements Blocked using the in-process store.
func (b *Beads) storeBlocked() ([]*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	blocked, err := b.store.GetBlockedIssues(ctx, beadsdk.WorkFilter{})
	if err != nil {
		return nil, fmt.Errorf("store blocked: %w", err)
	}

	issues := make([]*Issue, len(blocked))
	for i, bi := range blocked {
		issue := bi.Issue // BlockedIssue embeds Issue by value
		issues[i] = sdkIssueToIssue(&issue)
	}
	return issues, nil
}

// storeSearch implements Search using the in-process store.
func (b *Beads) storeSearch(opts SearchOptions) ([]*Issue, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	filter := beadsdk.IssueFilter{
		Limit: opts.Limit,
	}

	if opts.Status != "" && opts.Status != "all" {
		status := beadsdk.Status(opts.Status)
		filter.Status = &status
	}

	if opts.Label != "" {
		filter.Labels = []string{opts.Label}
	}

	if opts.DescContains != "" {
		filter.DescriptionContains = opts.DescContains
	}

	sdkIssues, err := b.store.SearchIssues(ctx, opts.Query, filter)
	if err != nil {
		return nil, fmt.Errorf("store search: %w", err)
	}

	return sdkIssuesToIssues(sdkIssues), nil
}

// storeAddDependency implements AddDependency using the in-process store.
func (b *Beads) storeAddDependency(issue, dependsOn string) error {
	ctx, cancel := storeCtx()
	defer cancel()

	dep := &beadsdk.Dependency{
		IssueID:     issue,
		DependsOnID: dependsOn,
		Type:        beadsdk.DepBlocks,
	}

	return b.store.AddDependency(ctx, dep, b.getActor())
}

// storeRemoveDependency implements RemoveDependency using the in-process store.
func (b *Beads) storeRemoveDependency(issue, dependsOn string) error {
	ctx, cancel := storeCtx()
	defer cancel()

	return b.store.RemoveDependency(ctx, issue, dependsOn, b.getActor())
}

// storeAddLabel implements AddLabel using the in-process store.
func (b *Beads) storeAddLabel(id, label string) error {
	ctx, cancel := storeCtx()
	defer cancel()

	return b.store.AddLabel(ctx, id, label, b.getActor())
}

// storeRemoveLabel implements RemoveLabel using the in-process store.
func (b *Beads) storeRemoveLabel(id, label string) error {
	ctx, cancel := storeCtx()
	defer cancel()

	return b.store.RemoveLabel(ctx, id, label, b.getActor())
}

// storeGetLabels implements GetLabels using the in-process store.
func (b *Beads) storeGetLabels(id string) ([]string, error) {
	ctx, cancel := storeCtx()
	defer cancel()

	return b.store.GetLabels(ctx, id)
}

// storeDelegationSet stores delegation data in the issue's metadata under the
// "delegated_from" key. Merges with any existing metadata to avoid clobbering
// other keys.
func (b *Beads) storeDelegationSet(childID string, d *Delegation) error {
	ctx, cancel := storeCtx()
	defer cancel()

	actor := b.getActor()

	// Fetch current metadata to merge into.
	si, err := b.store.GetIssue(ctx, childID)
	if err != nil {
		return fmt.Errorf("fetching issue for delegation set: %w", err)
	}

	meta, err := mergeMetadataKey(si.Metadata, "delegated_from", d)
	if err != nil {
		return fmt.Errorf("building delegation metadata: %w", err)
	}

	return b.store.UpdateIssue(ctx, childID, map[string]interface{}{"metadata": meta}, actor)
}

// storeDelegationClear removes the "delegated_from" key from the issue's metadata.
func (b *Beads) storeDelegationClear(childID string) error {
	ctx, cancel := storeCtx()
	defer cancel()

	actor := b.getActor()

	si, err := b.store.GetIssue(ctx, childID)
	if err != nil {
		return fmt.Errorf("fetching issue for delegation clear: %w", err)
	}

	meta, err := deleteMetadataKey(si.Metadata, "delegated_from")
	if err != nil {
		return fmt.Errorf("clearing delegation metadata: %w", err)
	}

	return b.store.UpdateIssue(ctx, childID, map[string]interface{}{"metadata": meta}, actor)
}

// mergeMetadataKey sets a key in a JSON metadata blob, preserving other keys.
func mergeMetadataKey(existing json.RawMessage, key string, value interface{}) (json.RawMessage, error) {
	m := make(map[string]json.RawMessage)
	if len(existing) > 0 {
		if err := json.Unmarshal(existing, &m); err != nil {
			// If existing metadata is malformed, start fresh.
			m = make(map[string]json.RawMessage)
		}
	}
	v, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	m[key] = v
	return json.Marshal(m)
}

// deleteMetadataKey removes a key from a JSON metadata blob.
func deleteMetadataKey(existing json.RawMessage, key string) (json.RawMessage, error) {
	if len(existing) == 0 {
		return json.RawMessage("{}"), nil
	}
	m := make(map[string]json.RawMessage)
	if err := json.Unmarshal(existing, &m); err != nil {
		return json.RawMessage("{}"), nil
	}
	delete(m, key)
	return json.Marshal(m)
}
