package beads

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
)

// mockStorage implements beadsdk.Storage for unit testing the store integration layer.
// Embeds beadsdk.Storage to satisfy unimplemented methods (they panic if called).
type mockStorage struct {
	beadsdk.Storage // embedded for unimplemented methods
	issues          map[string]*beadsdk.Issue
	labels          map[string][]string // issueID -> labels
	deps            map[string][]string // issueID -> depends-on IDs
	nextID          int
	prefix          string
	closed          map[string]bool
	closeErr        error
	createErr       error
	updateErr       error
	searchErr       error
	getErr          error
	addLabelErr     error
	removeLabelErr  error
	addDepErr       error
	removeDepErr    error
	getLabelsErr    error
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		issues: make(map[string]*beadsdk.Issue),
		labels: make(map[string][]string),
		deps:   make(map[string][]string),
		closed: make(map[string]bool),
		prefix: "test",
	}
}

func (m *mockStorage) CreateIssue(_ context.Context, issue *beadsdk.Issue, _ string) error {
	if m.createErr != nil {
		return m.createErr
	}
	m.nextID++
	issue.ID = fmt.Sprintf("%s-%d", m.prefix, m.nextID)
	issue.Status = beadsdk.StatusOpen
	issue.CreatedAt = time.Now()
	issue.UpdatedAt = time.Now()
	m.issues[issue.ID] = issue
	return nil
}

func (m *mockStorage) GetIssue(_ context.Context, id string) (*beadsdk.Issue, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	issue, ok := m.issues[id]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return issue, nil
}

func (m *mockStorage) GetIssuesByIDs(_ context.Context, ids []string) ([]*beadsdk.Issue, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	var result []*beadsdk.Issue
	for _, id := range ids {
		if issue, ok := m.issues[id]; ok {
			result = append(result, issue)
		}
	}
	return result, nil
}

func (m *mockStorage) GetDependenciesWithMetadata(_ context.Context, issueID string) ([]*beadsdk.IssueWithDependencyMetadata, error) {
	issue, ok := m.issues[issueID]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}

	var result []*beadsdk.IssueWithDependencyMetadata
	for _, dep := range issue.Dependencies {
		if dep.IssueID != issueID {
			continue
		}

		target, ok := m.issues[dep.DependsOnID]
		if !ok {
			target = &beadsdk.Issue{ID: dep.DependsOnID}
		}
		result = append(result, &beadsdk.IssueWithDependencyMetadata{
			Issue:          *target,
			DependencyType: dep.Type,
		})
	}
	return result, nil
}

func (m *mockStorage) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	if m.updateErr != nil {
		return m.updateErr
	}
	issue, ok := m.issues[id]
	if !ok {
		return fmt.Errorf("issue %s not found", id)
	}
	if v, ok := updates["title"]; ok {
		issue.Title = v.(string)
	}
	if v, ok := updates["status"]; ok {
		issue.Status = beadsdk.Status(v.(string))
	}
	if v, ok := updates["assignee"]; ok {
		issue.Assignee = v.(string)
	}
	if v, ok := updates["description"]; ok {
		issue.Description = v.(string)
	}
	if v, ok := updates["priority"]; ok {
		issue.Priority = v.(int)
	}
	issue.UpdatedAt = time.Now()
	return nil
}

func (m *mockStorage) CloseIssue(_ context.Context, id, reason, actor, session string) error {
	if m.closeErr != nil {
		return m.closeErr
	}
	issue, ok := m.issues[id]
	if !ok {
		return fmt.Errorf("issue %s not found", id)
	}
	issue.Status = beadsdk.StatusClosed
	now := time.Now()
	issue.ClosedAt = &now
	m.closed[id] = true
	return nil
}

func (m *mockStorage) DeleteIssue(_ context.Context, id string) error {
	delete(m.issues, id)
	return nil
}

func (m *mockStorage) SearchIssues(_ context.Context, query string, filter beadsdk.IssueFilter) ([]*beadsdk.Issue, error) {
	if m.searchErr != nil {
		return nil, m.searchErr
	}
	var result []*beadsdk.Issue
	for _, issue := range m.issues {
		if filter.Status != nil && issue.Status != *filter.Status {
			continue
		}
		if filter.Assignee != nil && issue.Assignee != *filter.Assignee {
			continue
		}
		if filter.ParentID != nil {
			// Simple parent check via deps
			found := false
			for _, d := range issue.Dependencies {
				if d.Type == beadsdk.DepParentChild && d.DependsOnID == *filter.ParentID {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if len(filter.Labels) > 0 {
			issueLabels := m.labels[issue.ID]
			hasAll := true
			for _, wantLabel := range filter.Labels {
				found := false
				for _, l := range issueLabels {
					if l == wantLabel {
						found = true
						break
					}
				}
				// Also check issue.Labels
				if !found {
					for _, l := range issue.Labels {
						if l == wantLabel {
							found = true
							break
						}
					}
				}
				if !found {
					hasAll = false
					break
				}
			}
			if !hasAll {
				continue
			}
		}
		result = append(result, issue)
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (m *mockStorage) AddDependency(_ context.Context, dep *beadsdk.Dependency, _ string) error {
	if m.addDepErr != nil {
		return m.addDepErr
	}
	m.deps[dep.IssueID] = append(m.deps[dep.IssueID], dep.DependsOnID)
	// Also add to the issue's Dependencies for sdkIssueToIssue to find
	if issue, ok := m.issues[dep.IssueID]; ok {
		issue.Dependencies = append(issue.Dependencies, dep)
	}
	return nil
}

func (m *mockStorage) RemoveDependency(_ context.Context, issueID, dependsOnID, _ string) error {
	if m.removeDepErr != nil {
		return m.removeDepErr
	}
	deps := m.deps[issueID]
	for i, d := range deps {
		if d == dependsOnID {
			m.deps[issueID] = append(deps[:i], deps[i+1:]...)
			break
		}
	}
	return nil
}

func (m *mockStorage) AddLabel(_ context.Context, issueID, label, _ string) error {
	if m.addLabelErr != nil {
		return m.addLabelErr
	}
	if _, ok := m.issues[issueID]; !ok {
		return fmt.Errorf("issue %s not found", issueID)
	}
	m.labels[issueID] = append(m.labels[issueID], label)
	return nil
}

func (m *mockStorage) RemoveLabel(_ context.Context, issueID, label, _ string) error {
	if m.removeLabelErr != nil {
		return m.removeLabelErr
	}
	if _, ok := m.issues[issueID]; !ok {
		return fmt.Errorf("issue %s not found", issueID)
	}
	labels := m.labels[issueID]
	for i, l := range labels {
		if l == label {
			m.labels[issueID] = append(labels[:i], labels[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("issue %s does not have label %s", issueID, label)
}

func (m *mockStorage) GetLabels(_ context.Context, issueID string) ([]string, error) {
	if m.getLabelsErr != nil {
		return nil, m.getLabelsErr
	}
	if _, ok := m.issues[issueID]; !ok {
		return nil, fmt.Errorf("issue %s not found", issueID)
	}
	return m.labels[issueID], nil
}

func (m *mockStorage) GetReadyWork(_ context.Context, filter beadsdk.WorkFilter) ([]*beadsdk.Issue, error) {
	// Return all open issues (simplified: no real blocking logic)
	var result []*beadsdk.Issue
	for _, issue := range m.issues {
		if issue.Status != beadsdk.StatusOpen {
			continue
		}
		result = append(result, issue)
	}
	if filter.Limit > 0 && len(result) > filter.Limit {
		result = result[:filter.Limit]
	}
	return result, nil
}

func (m *mockStorage) Close() error { return nil }

// --- Tests ---

func newTestBeads(store *mockStorage) *Beads {
	return &Beads{workDir: "/tmp/test", store: store, isolated: true}
}

func TestStoreList(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	// Create some issues
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "alpha"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "beta"}, "test")

	issues, err := b.List(ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2 issues, got %d", len(issues))
	}
}

func TestStoreListWithStatusFilter(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "open-one"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "closed-one"}, "test")
	store.CloseIssue(context.Background(), "test-2", "", "", "")

	issues, err := b.List(ListOptions{Status: "open"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 open issue, got %d", len(issues))
	}
	if issues[0].Title != "open-one" {
		t.Fatalf("expected 'open-one', got %q", issues[0].Title)
	}
}

func TestStoreListError(t *testing.T) {
	store := newMockStorage()
	store.searchErr = errors.New("db down")
	b := newTestBeads(store)

	_, err := b.List(ListOptions{})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, store.searchErr) && err.Error() != "store list: db down" {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStoreShow(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{
		Title:       "test issue",
		Description: "some desc",
		Priority:    2,
	}, "actor")

	issue, err := b.Show("test-1")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if issue.Title != "test issue" {
		t.Fatalf("expected 'test issue', got %q", issue.Title)
	}
	if issue.Priority != 2 {
		t.Fatalf("expected priority 2, got %d", issue.Priority)
	}
}

func TestStoreShowNotFound(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	_, err := b.Show("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestStoreShowMultiple(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "one"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "two"}, "test")

	result, err := b.ShowMultiple([]string{"test-1", "test-2"})
	if err != nil {
		t.Fatalf("ShowMultiple: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result["test-1"].Title != "one" {
		t.Fatalf("expected 'one', got %q", result["test-1"].Title)
	}
}

func TestStoreShowMultipleEmpty(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	result, err := b.ShowMultiple(nil)
	if err != nil {
		t.Fatalf("ShowMultiple nil: %v", err)
	}
	if len(result) != 0 {
		t.Fatalf("expected 0, got %d", len(result))
	}
}

func TestStoreCreate(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	issue, err := b.Create(CreateOptions{
		Title:       "new bug",
		Description: "a bug",
		Priority:    1,
		Type:        "bug",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if issue.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if issue.Title != "new bug" {
		t.Fatalf("expected 'new bug', got %q", issue.Title)
	}
}

func TestStoreCreateWithParent(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	// Create parent first
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "parent"}, "test")

	// Create child with parent
	issue, err := b.Create(CreateOptions{
		Title:  "child",
		Parent: "test-1",
	})
	if err != nil {
		t.Fatalf("Create with parent: %v", err)
	}
	if issue.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	// Check dependency was recorded
	if len(store.deps[issue.ID]) != 1 || store.deps[issue.ID][0] != "test-1" {
		t.Fatalf("expected parent dep on test-1, got %v", store.deps[issue.ID])
	}
}

func TestStoreCreateParentLinkError(t *testing.T) {
	store := newMockStorage()
	store.addDepErr = errors.New("dep failed")
	b := newTestBeads(store)

	_, err := b.Create(CreateOptions{
		Title:  "child",
		Parent: "parent-1",
	})
	if err == nil {
		t.Fatal("expected error when parent link fails")
	}
	if !errors.Is(err, store.addDepErr) && !containsString(err.Error(), "parent link failed") {
		t.Fatalf("expected parent link error, got: %v", err)
	}
}

func TestStoreCreateError(t *testing.T) {
	store := newMockStorage()
	store.createErr = errors.New("create failed")
	b := newTestBeads(store)

	_, err := b.Create(CreateOptions{Title: "fail"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreClose(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "to close"}, "test")

	err := b.Close("test-1")
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !store.closed["test-1"] {
		t.Fatal("expected issue to be closed")
	}
}

func TestStoreCloseMultiple(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "a"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "b"}, "test")

	err := b.Close("test-1", "test-2")
	if err != nil {
		t.Fatalf("Close multiple: %v", err)
	}
	if !store.closed["test-1"] || !store.closed["test-2"] {
		t.Fatal("expected both issues closed")
	}
}

func TestStoreCloseWithReason(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "done"}, "test")

	err := b.CloseWithReason("completed by agent", "test-1")
	if err != nil {
		t.Fatalf("CloseWithReason: %v", err)
	}
	if !store.closed["test-1"] {
		t.Fatal("expected issue to be closed")
	}
}

func TestStoreCloseError(t *testing.T) {
	store := newMockStorage()
	store.closeErr = errors.New("close failed")
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	err := b.Close("test-1")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreReady(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "ready-one"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "closed-one"}, "test")
	store.CloseIssue(context.Background(), "test-2", "", "", "")

	issues, err := b.Ready()
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 ready issue, got %d", len(issues))
	}
	if issues[0].Title != "ready-one" {
		t.Fatalf("expected 'ready-one', got %q", issues[0].Title)
	}
}

func TestStoreUpdate(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "original"}, "test")

	newTitle := "updated"
	err := b.Update("test-1", UpdateOptions{Title: &newTitle})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if store.issues["test-1"].Title != "updated" {
		t.Fatalf("expected 'updated', got %q", store.issues["test-1"].Title)
	}
}

func TestStoreUpdateError(t *testing.T) {
	store := newMockStorage()
	store.updateErr = errors.New("update failed")
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	newTitle := "fail"
	err := b.Update("test-1", UpdateOptions{Title: &newTitle})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestStoreUpdateAddLabels(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "label-test"}, "test")

	err := b.Update("test-1", UpdateOptions{AddLabels: []string{"bug", "urgent"}})
	if err != nil {
		t.Fatalf("Update add labels: %v", err)
	}
	labels := store.labels["test-1"]
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d: %v", len(labels), labels)
	}
}

func TestStoreUpdateAddLabelsError(t *testing.T) {
	store := newMockStorage()
	store.addLabelErr = errors.New("label add failed")
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	err := b.Update("test-1", UpdateOptions{AddLabels: []string{"bug"}})
	if err == nil {
		t.Fatal("expected error from label add")
	}
}

func TestStoreUpdateRemoveLabels(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	store.labels["test-1"] = []string{"bug", "urgent"}

	err := b.Update("test-1", UpdateOptions{RemoveLabels: []string{"bug"}})
	if err != nil {
		t.Fatalf("Update remove labels: %v", err)
	}
	labels := store.labels["test-1"]
	if len(labels) != 1 || labels[0] != "urgent" {
		t.Fatalf("expected [urgent], got %v", labels)
	}
}

func TestStoreUpdateRemoveLabelsError(t *testing.T) {
	store := newMockStorage()
	store.removeLabelErr = errors.New("label remove failed")
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	err := b.Update("test-1", UpdateOptions{RemoveLabels: []string{"bug"}})
	if err == nil {
		t.Fatal("expected error from label remove")
	}
}

func TestStoreUpdateSetLabels(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	store.labels["test-1"] = []string{"old-label"}

	err := b.Update("test-1", UpdateOptions{SetLabels: []string{"new-label-a", "new-label-b"}})
	if err != nil {
		t.Fatalf("Update set labels: %v", err)
	}
	labels := store.labels["test-1"]
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %v", labels)
	}
}

func TestStoreUpdateSetLabelsGetError(t *testing.T) {
	store := newMockStorage()
	store.getLabelsErr = errors.New("get labels failed")
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "x"}, "test")
	err := b.Update("test-1", UpdateOptions{SetLabels: []string{"new"}})
	if err == nil {
		t.Fatal("expected error from get labels")
	}
}

func TestStoreSearch(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "find me"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "other"}, "test")

	issues, err := b.Search(SearchOptions{Query: ""})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(issues) != 2 {
		t.Fatalf("expected 2, got %d", len(issues))
	}
}

func TestStoreAddDependency(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "a"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "b"}, "test")

	err := b.AddDependency("test-1", "test-2")
	if err != nil {
		t.Fatalf("AddDependency: %v", err)
	}
	if len(store.deps["test-1"]) != 1 || store.deps["test-1"][0] != "test-2" {
		t.Fatalf("expected dep test-1 -> test-2, got %v", store.deps["test-1"])
	}
}

func TestStoreRemoveDependency(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "a"}, "test")
	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "b"}, "test")
	store.deps["test-1"] = []string{"test-2"}

	err := b.RemoveDependency("test-1", "test-2")
	if err != nil {
		t.Fatalf("RemoveDependency: %v", err)
	}
	if len(store.deps["test-1"]) != 0 {
		t.Fatalf("expected no deps, got %v", store.deps["test-1"])
	}
}

func TestSdkIssueToIssueDependsOnInit(t *testing.T) {
	// Verify DependsOn is properly initialized (fix #4)
	si := &beadsdk.Issue{
		ID:     "test-1",
		Title:  "with deps",
		Status: beadsdk.StatusOpen,
		Dependencies: []*beadsdk.Dependency{
			{IssueID: "test-1", DependsOnID: "dep-1", Type: beadsdk.DepBlocks},
			{IssueID: "test-1", DependsOnID: "dep-2", Type: beadsdk.DepBlocks},
		},
	}

	issue := sdkIssueToIssue(si)
	if issue.DependsOn == nil {
		t.Fatal("DependsOn should not be nil when dependencies exist")
	}
	if len(issue.DependsOn) != 2 {
		t.Fatalf("expected 2 deps, got %d", len(issue.DependsOn))
	}
}

func TestStoreShowMultipleHydratesDependencyMetadata(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)
	store.issues["gt-mr"] = &beadsdk.Issue{
		ID:     "gt-mr",
		Title:  "merge request",
		Status: beadsdk.StatusOpen,
		Dependencies: []*beadsdk.Dependency{
			{IssueID: "gt-mr", DependsOnID: "gt-blocker", Type: beadsdk.DepBlocks},
			{IssueID: "gt-mr", DependsOnID: "gt-wait", Type: beadsdk.DependencyType("waits-for")},
			{IssueID: "gt-mr", DependsOnID: "gt-parent", Type: beadsdk.DepParentChild},
			{IssueID: "gt-dependent", DependsOnID: "gt-mr", Type: beadsdk.DepBlocks},
		},
	}
	store.issues["gt-blocker"] = &beadsdk.Issue{ID: "gt-blocker", Title: "blocker", Status: beadsdk.StatusOpen}
	store.issues["gt-wait"] = &beadsdk.Issue{ID: "gt-wait", Title: "closed wait", Status: beadsdk.StatusClosed}
	store.issues["gt-parent"] = &beadsdk.Issue{ID: "gt-parent", Title: "parent", Status: beadsdk.StatusOpen}
	store.issues["gt-merge-mr"] = &beadsdk.Issue{
		ID:     "gt-merge-mr",
		Title:  "merge-blocked request",
		Status: beadsdk.StatusOpen,
		Dependencies: []*beadsdk.Dependency{
			{IssueID: "gt-merge-mr", DependsOnID: "gt-merged", Type: beadsdk.DependencyType("merge-blocks")},
		},
	}
	store.issues["gt-merged"] = &beadsdk.Issue{ID: "gt-merged", Title: "merged dependency", Status: beadsdk.StatusClosed, CloseReason: "Merged in gt-wisp"}

	issues, err := b.storeShowMultiple([]string{"gt-mr", "gt-merge-mr"})
	if err != nil {
		t.Fatalf("storeShowMultiple: %v", err)
	}
	issue := issues["gt-mr"]
	if issue == nil {
		t.Fatal("missing hydrated issue")
	}
	if issue.Parent != "gt-parent" {
		t.Fatalf("Parent = %q, want gt-parent", issue.Parent)
	}
	if len(issue.DependsOn) != 2 {
		t.Fatalf("DependsOn len = %d, want 2: %#v", len(issue.DependsOn), issue.DependsOn)
	}
	if len(issue.Dependencies) != 3 {
		t.Fatalf("Dependencies len = %d, want 3: %#v", len(issue.Dependencies), issue.Dependencies)
	}
	if got := FirstUnresolvedBlockerID(issue); got != "gt-blocker" {
		t.Fatalf("FirstUnresolvedBlockerID() = %q, want gt-blocker", got)
	}
	if !HasUnresolvedBlockers(issue) {
		t.Fatal("SDK dependencies should feed shared blocker semantics")
	}
	if HasUnresolvedBlockers(issues["gt-merge-mr"]) {
		t.Fatal("store-backed merged merge-block should be resolved")
	}
}

func TestSdkIssueToIssueNoDeps(t *testing.T) {
	si := &beadsdk.Issue{
		ID:     "test-1",
		Title:  "no deps",
		Status: beadsdk.StatusOpen,
	}

	issue := sdkIssueToIssue(si)
	if issue.DependsOn != nil {
		t.Fatalf("DependsOn should be nil when no dependencies, got %v", issue.DependsOn)
	}
}

func TestSdkIssueToIssueParent(t *testing.T) {
	si := &beadsdk.Issue{
		ID:     "child-1",
		Title:  "child",
		Status: beadsdk.StatusOpen,
		Dependencies: []*beadsdk.Dependency{
			{IssueID: "child-1", DependsOnID: "parent-1", Type: beadsdk.DepParentChild},
		},
	}

	issue := sdkIssueToIssue(si)
	if issue.Parent != "parent-1" {
		t.Fatalf("expected parent 'parent-1', got %q", issue.Parent)
	}
}

func TestSdkIssueToIssueNil(t *testing.T) {
	if sdkIssueToIssue(nil) != nil {
		t.Fatal("expected nil for nil input")
	}
}

func TestStoreForceCloseWithReason(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{Title: "force-close-me"}, "test")

	err := b.ForceCloseWithReason("nuking polecat", "test-1")
	if err != nil {
		t.Fatalf("ForceCloseWithReason: %v", err)
	}
	if !store.closed["test-1"] {
		t.Fatal("expected issue to be force-closed")
	}
}

func TestStoreReleaseWithReason(t *testing.T) {
	store := newMockStorage()
	b := newTestBeads(store)

	store.CreateIssue(context.Background(), &beadsdk.Issue{
		Title:    "in-progress",
		Assignee: "agent-1",
	}, "test")
	store.issues["test-1"].Status = "in_progress"

	err := b.ReleaseWithReason("test-1", "agent stuck")
	if err != nil {
		t.Fatalf("ReleaseWithReason: %v", err)
	}
	if string(store.issues["test-1"].Status) != "open" {
		t.Fatalf("expected status 'open', got %q", store.issues["test-1"].Status)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
