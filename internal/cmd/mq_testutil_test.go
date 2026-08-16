package cmd

import (
	"github.com/jonbaldie/gastown/internal/beads"
)

// mockBeads is a test double for beads.Beads
type mockBeads struct {
	issues    map[string]*beads.Issue
	listFunc  func(opts beads.ListOptions) ([]*beads.Issue, error)
	showFunc  func(id string) (*beads.Issue, error)
	closeFunc func(id string) error
}

func newMockBeads() *mockBeads {
	return &mockBeads{
		issues: make(map[string]*beads.Issue),
	}
}

func (m *mockBeads) addIssue(issue *beads.Issue) {
	m.issues[issue.ID] = issue
}

func (m *mockBeads) Show(id string) (*beads.Issue, error) {
	if m.showFunc != nil {
		return m.showFunc(id)
	}
	if issue, ok := m.issues[id]; ok {
		return issue, nil
	}
	return nil, beads.ErrNotFound
}

func (m *mockBeads) List(opts beads.ListOptions) ([]*beads.Issue, error) {
	if m.listFunc != nil {
		return m.listFunc(opts)
	}
	var result []*beads.Issue
	for _, issue := range m.issues {
		if opts.Label != "" {
			if !beads.HasLabel(issue, opts.Label) {
				continue
			}
		}
		// Match real bd behavior: no --status flag defaults to non-closed issues
		if opts.Status == "" {
			if issue.Status == "closed" {
				continue
			}
		} else if opts.Status != "all" && issue.Status != opts.Status {
			continue
		}
		result = append(result, issue)
	}
	return result, nil
}

func (m *mockBeads) Close(id string) error {
	if m.closeFunc != nil {
		return m.closeFunc(id)
	}
	if issue, ok := m.issues[id]; ok {
		issue.Status = "closed"
		return nil
	}
	return beads.ErrNotFound
}

// makeTestIssue creates a test issue with common defaults
func makeTestIssue(id, title, issueType, status string) *beads.Issue {
	return &beads.Issue{
		ID:        id,
		Title:     title,
		Type:      issueType,
		Status:    status,
		Priority:  2,
		CreatedAt: "2025-01-01T12:00:00Z",
		UpdatedAt: "2025-01-01T12:00:00Z",
	}
}

// makeTestMR creates a test merge request issue
func makeTestMR(id, branch, target, worker string, status string) *beads.Issue {
	desc := beads.FormatMRFields(&beads.MRFields{
		Branch:      branch,
		Target:      target,
		Worker:      worker,
		SourceIssue: "gt-src-123",
		Rig:         "testrig",
	})
	return &beads.Issue{
		ID:          id,
		Title:       "Merge: " + branch,
		Type:        "task",
		Status:      status,
		Priority:    2,
		Labels:      []string{"gt:merge-request"},
		Description: desc,
		CreatedAt:   "2025-01-01T12:00:00Z",
		UpdatedAt:   "2025-01-01T12:00:00Z",
	}
}
