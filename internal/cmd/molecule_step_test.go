package cmd

import (
	"fmt"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
)

func TestExtractMoleculeIDFromStep(t *testing.T) {
	tests := []struct {
		name     string
		stepID   string
		expected string
	}{
		{
			name:     "simple step",
			stepID:   "gt-abc.1",
			expected: "gt-abc",
		},
		{
			name:     "multi-digit step number",
			stepID:   "gt-xyz.12",
			expected: "gt-xyz",
		},
		{
			name:     "molecule with dash",
			stepID:   "gt-my-mol.3",
			expected: "gt-my-mol",
		},
		{
			name:     "bd prefix",
			stepID:   "bd-mol-abc.2",
			expected: "bd-mol-abc",
		},
		{
			name:     "complex id",
			stepID:   "gt-some-complex-id.99",
			expected: "gt-some-complex-id",
		},
		{
			name:     "not a step - no suffix",
			stepID:   "gt-5gq8r",
			expected: "",
		},
		{
			name:     "wisp format - no dot",
			stepID:   "go-wisp-75l",
			expected: "", // Wisp IDs don't encode molecule ID - use Parent field
		},
		{
			name:     "not a step - non-numeric suffix",
			stepID:   "gt-abc.xyz",
			expected: "",
		},
		{
			name:     "not a step - mixed suffix",
			stepID:   "gt-abc.1a",
			expected: "",
		},
		{
			name:     "empty string",
			stepID:   "",
			expected: "",
		},
		{
			name:     "just a dot",
			stepID:   ".",
			expected: "",
		},
		{
			name:     "trailing dot",
			stepID:   "gt-abc.",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMoleculeIDFromStep(tt.stepID)
			if result != tt.expected {
				t.Errorf("extractMoleculeIDFromStep(%q) = %q, want %q", tt.stepID, result, tt.expected)
			}
		})
	}
}

// mockBeadsForStep extends mockBeads with parent filtering for step tests.
// It simulates the real bd behavior where:
// - List() returns issues with DependsOn empty (bd list doesn't return deps)
// - Show()/ShowMultiple() returns issues with Dependencies populated (bd show does)
type mockBeadsForStep struct {
	issues map[string]*beads.Issue
}

func newMockBeadsForStep() *mockBeadsForStep {
	return &mockBeadsForStep{
		issues: make(map[string]*beads.Issue),
	}
}

func (m *mockBeadsForStep) addIssue(issue *beads.Issue) {
	m.issues[issue.ID] = issue
}

func (m *mockBeadsForStep) Show(id string) (*beads.Issue, error) {
	if issue, ok := m.issues[id]; ok {
		return issue, nil
	}
	return nil, beads.ErrNotFound
}

// ShowMultiple simulates bd show with multiple IDs - returns full issue data including Dependencies
func (m *mockBeadsForStep) ShowMultiple(ids []string) (map[string]*beads.Issue, error) {
	result := make(map[string]*beads.Issue)
	for _, id := range ids {
		if issue, ok := m.issues[id]; ok {
			result[id] = issue
		}
	}
	return result, nil
}

// List simulates bd list behavior - returns issues but with DependsOn EMPTY.
// This is the key behavior that caused the bug: bd list doesn't return dependency info.
func (m *mockBeadsForStep) List(opts beads.ListOptions) ([]*beads.Issue, error) {
	var result []*beads.Issue
	for _, issue := range m.issues {
		// Filter by parent
		if opts.Parent != "" && issue.Parent != opts.Parent {
			continue
		}
		// Filter by status (unless "all")
		if opts.Status != "" && opts.Status != "all" && issue.Status != opts.Status {
			continue
		}
		// CRITICAL: Simulate bd list behavior - DependsOn is NOT populated
		// Create a copy with empty DependsOn to simulate real bd list output
		issueCopy := *issue
		issueCopy.DependsOn = nil // bd list doesn't return this
		result = append(result, &issueCopy)
	}
	return result, nil
}

func (m *mockBeadsForStep) Close(ids ...string) error {
	for _, id := range ids {
		if issue, ok := m.issues[id]; ok {
			issue.Status = "closed"
		} else {
			return beads.ErrNotFound
		}
	}
	return nil
}

// makeStepIssue creates a test step issue with both DependsOn and Dependencies set.
// In real usage:
// - bd list returns issues with DependsOn empty
// - bd show returns issues with Dependencies populated (with DependencyType)
// The mock simulates this: List() clears DependsOn, Show() returns the full issue.
func makeStepIssue(id, title, parent, status string, dependsOn []string) *beads.Issue {
	issue := &beads.Issue{
		ID:        id,
		Title:     title,
		Type:      "task",
		Status:    status,
		Priority:  2,
		Parent:    parent,
		DependsOn: dependsOn, // This gets cleared by mock List() to simulate bd list
		CreatedAt: "2025-01-01T12:00:00Z",
		UpdatedAt: "2025-01-01T12:00:00Z",
	}
	// Also set Dependencies (what bd show returns) for proper testing.
	// Use "blocks" dependency type since that's what formula instantiation creates
	// for inter-step dependencies (vs "parent-child" for parent relationships).
	for _, depID := range dependsOn {
		issue.Dependencies = append(issue.Dependencies, beads.IssueDep{
			ID:             depID,
			Title:          "Dependency " + depID,
			DependencyType: "blocks", // Only "blocks" deps should block progress
		})
	}
	return issue
}

// TestStepDoneScenarios tests complete step-done scenarios
func TestStepDoneScenarios(t *testing.T) {
	tests := []struct {
		name         string
		stepID       string
		setupFunc    func(*mockBeadsForStep)
		wantAction   string // "continue", "done", "no_more_ready"
		wantNextStep string
	}{
		{
			name:   "complete step, continue to next",
			stepID: "gt-mol.1",
			setupFunc: func(m *mockBeadsForStep) {
				m.addIssue(makeStepIssue("gt-mol.1", "Step 1", "gt-mol", "open", nil))
				m.addIssue(makeStepIssue("gt-mol.2", "Step 2", "gt-mol", "open", []string{"gt-mol.1"}))
			},
			wantAction:   "continue",
			wantNextStep: "gt-mol.2",
		},
		{
			name:   "complete final step, molecule done",
			stepID: "gt-mol.2",
			setupFunc: func(m *mockBeadsForStep) {
				m.addIssue(makeStepIssue("gt-mol.1", "Step 1", "gt-mol", "closed", nil))
				m.addIssue(makeStepIssue("gt-mol.2", "Step 2", "gt-mol", "open", []string{"gt-mol.1"}))
			},
			wantAction: "done",
		},
		{
			name:   "complete step, remaining blocked",
			stepID: "gt-mol.1",
			setupFunc: func(m *mockBeadsForStep) {
				m.addIssue(makeStepIssue("gt-mol.1", "Step 1", "gt-mol", "open", nil))
				m.addIssue(makeStepIssue("gt-mol.2", "Step 2", "gt-mol", "in_progress", nil)) // another parallel task
				m.addIssue(makeStepIssue("gt-mol.3", "Synthesis", "gt-mol", "open", []string{"gt-mol.1", "gt-mol.2"}))
			},
			wantAction: "no_more_ready", // .2 is in_progress, .3 blocked
		},
		{
			name:   "parallel workflow - complete one, next ready",
			stepID: "gt-mol.1",
			setupFunc: func(m *mockBeadsForStep) {
				m.addIssue(makeStepIssue("gt-mol.1", "Parallel A", "gt-mol", "open", nil))
				m.addIssue(makeStepIssue("gt-mol.2", "Parallel B", "gt-mol", "open", nil))
				m.addIssue(makeStepIssue("gt-mol.3", "Synthesis", "gt-mol", "open", []string{"gt-mol.1", "gt-mol.2"}))
			},
			wantAction:   "continue",
			wantNextStep: "gt-mol.2", // B is still ready
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockBeadsForStep()
			tt.setupFunc(m)

			// Extract molecule ID
			moleculeID := extractMoleculeIDFromStep(tt.stepID)
			if moleculeID == "" {
				t.Fatalf("could not extract molecule ID from %s", tt.stepID)
			}

			// Simulate closing the step
			if err := m.Close(tt.stepID); err != nil {
				t.Fatalf("failed to close step: %v", err)
			}

			// Now find next ready step using the FIXED algorithm
			children, _ := m.List(beads.ListOptions{Parent: moleculeID, Status: "all"})

			closedIDs := make(map[string]bool)
			var openStepIDs []string
			hasNonClosedSteps := false
			for _, child := range children {
				switch child.Status {
				case "closed":
					closedIDs[child.ID] = true
				case "open":
					openStepIDs = append(openStepIDs, child.ID)
					hasNonClosedSteps = true
				default:
					// in_progress or other - not closed, not available
					hasNonClosedSteps = true
				}
			}

			allComplete := !hasNonClosedSteps

			var action string
			var nextStepID string

			if allComplete {
				action = "done"
			} else {
				// Fetch full details for open steps (Dependencies will be populated)
				openStepsMap, _ := m.ShowMultiple(openStepIDs)

				// Find ready step using Dependencies (not DependsOn!)
				// Only "blocks" type dependencies block progress - ignore "parent-child".
				var readyStep *beads.Issue
				for _, stepID := range openStepIDs {
					step := openStepsMap[stepID]
					if step == nil {
						continue
					}

					// Use Dependencies (from bd show), NOT DependsOn (empty from bd list)
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
						readyStep = step
						break
					}
				}

				if readyStep != nil {
					action = "continue"
					nextStepID = readyStep.ID
				} else {
					action = "no_more_ready"
				}
			}

			if action != tt.wantAction {
				t.Errorf("action = %s, want %s", action, tt.wantAction)
			}

			if tt.wantNextStep != "" && nextStepID != tt.wantNextStep {
				t.Errorf("nextStep = %s, want %s", nextStepID, tt.wantNextStep)
			}
		})
	}
}

// makeStepIssueWithDepType creates a test step issue where the dependency type
// can be explicitly set (not just "blocks"). This simulates scenarios where
// bd mol wisp or other code paths create dependencies with non-"blocks" types.
func makeStepIssueWithDepType(id, title, parent, status string, deps []beads.IssueDep) *beads.Issue {
	return &beads.Issue{
		ID:           id,
		Title:        title,
		Type:         "task",
		Status:       status,
		Priority:     2,
		Parent:       parent,
		CreatedAt:    "2025-01-01T12:00:00Z",
		UpdatedAt:    "2025-01-01T12:00:00Z",
		Dependencies: deps,
	}
}

// TestDepTypeBlockingSemantics verifies that isBlockingDepType matches beads'
// canonical AffectsReadyWork semantics: only "blocks", "conditional-blocks",
// and "waits-for" are blocking. Unknown/custom types (including "needs", empty
// string) are non-blocking — matching beads' default behavior. Parent-child is
// non-blocking for step gating (it represents molecule→step hierarchy).
func TestDepTypeBlockingSemantics(t *testing.T) {
	tests := []struct {
		name      string
		depType   string
		wantReady int // How many steps should be ready
	}{
		// Blocking types (beads AffectsReadyWork minus parent-child)
		{
			name:      "blocks type is blocking",
			depType:   "blocks",
			wantReady: 1,
		},
		{
			name:      "conditional-blocks type is blocking",
			depType:   "conditional-blocks",
			wantReady: 1,
		},
		{
			name:      "waits-for type is blocking",
			depType:   "waits-for",
			wantReady: 1,
		},
		// Non-blocking types (matching beads: unknown/custom types don't affect ready work)
		{
			name:      "empty dependency type is non-blocking",
			depType:   "",
			wantReady: 3,
		},
		{
			name:      "needs type is non-blocking (not a beads type)",
			depType:   "needs",
			wantReady: 3,
		},
		{
			name:      "parent-child is non-blocking for step gating",
			depType:   "parent-child",
			wantReady: 3,
		},
		{
			name:      "tracks is non-blocking",
			depType:   "tracks",
			wantReady: 3,
		},
		{
			name:      "relates-to is non-blocking",
			depType:   "relates-to",
			wantReady: 3,
		},
		{
			name:      "unknown custom type is non-blocking",
			depType:   "depends-on",
			wantReady: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMockBeadsForStep()

			// Step 1: no deps (always ready)
			m.addIssue(makeStepIssueWithDepType("gt-mol.1", "Ensure labels", "gt-mol", "open", nil))

			// Step 2: depends on step 1 via the test dep type
			m.addIssue(makeStepIssueWithDepType("gt-mol.2", "Split and file", "gt-mol", "open", []beads.IssueDep{
				{ID: "gt-mol.1", Title: "Ensure labels", DependencyType: tt.depType},
			}))

			// Step 3: depends on step 2 via the test dep type
			m.addIssue(makeStepIssueWithDepType("gt-mol.3", "Finalize", "gt-mol", "open", []beads.IssueDep{
				{ID: "gt-mol.2", Title: "Split and file", DependencyType: tt.depType},
			}))

			children, _ := m.List(beads.ListOptions{Parent: "gt-mol", Status: "all"})
			closedIDs := make(map[string]bool)
			var openStepIDs []string
			for _, child := range children {
				if child.Status == "closed" {
					closedIDs[child.ID] = true
				} else if child.Status == "open" {
					openStepIDs = append(openStepIDs, child.ID)
				}
			}
			openStepsMap, _ := m.ShowMultiple(openStepIDs)

			var readySteps []string
			for _, stepID := range openStepIDs {
				step := openStepsMap[stepID]
				if step == nil {
					continue
				}
				allDepsClosed := true
				hasBlockingDeps := false
				for _, dep := range step.Dependencies {
					if !isBlockingDepType(dep.DependencyType) {
						continue
					}
					hasBlockingDeps = true
					if !closedIDs[dep.ID] {
						allDepsClosed = false
						break
					}
				}
				if !hasBlockingDeps || allDepsClosed {
					readySteps = append(readySteps, step.ID)
				}
			}

			if len(readySteps) != tt.wantReady {
				t.Errorf("got %d ready steps, want %d (readySteps=%v)", len(readySteps), tt.wantReady, readySteps)
			}
		})
	}
}

// TestReadyStepOrderReversed verifies that readySteps are sorted by sequence
// number even when bd list returns children in reverse creation order.
func TestReadyStepOrderReversed(t *testing.T) {
	m := newMockBeadsForStep()

	// Add issues in REVERSE order to simulate bd list's reverse-creation ordering.
	m.addIssue(makeStepIssueWithDepType("gt-mol.5", "Finalize", "gt-mol", "open", nil))
	m.addIssue(makeStepIssueWithDepType("gt-mol.4", "Queue validity", "gt-mol", "open", nil))
	m.addIssue(makeStepIssueWithDepType("gt-mol.3", "Dedup", "gt-mol", "open", nil))
	m.addIssue(makeStepIssueWithDepType("gt-mol.2", "Split and file", "gt-mol", "open", nil))
	m.addIssue(makeStepIssueWithDepType("gt-mol.1", "Ensure labels", "gt-mol", "open", nil))

	// Run the algorithm - all steps have no deps so all are "ready"
	children, _ := m.List(beads.ListOptions{Parent: "gt-mol", Status: "all"})
	closedIDs := make(map[string]bool)
	var openStepIDs []string
	for _, child := range children {
		if child.Status == "open" {
			openStepIDs = append(openStepIDs, child.ID)
		}
	}
	openStepsMap, _ := m.ShowMultiple(openStepIDs)

	var readySteps []*beads.Issue
	for _, stepID := range openStepIDs {
		step := openStepsMap[stepID]
		if step == nil {
			continue
		}
		allDepsClosed := true
		hasBlockingDeps := false
		for _, dep := range step.Dependencies {
			if !isBlockingDepType(dep.DependencyType) {
				continue
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

	// Sort by sequence number
	sortStepsBySequence(readySteps)

	if len(readySteps) != 5 {
		t.Fatalf("expected 5 ready steps (no deps), got %d", len(readySteps))
	}

	// Verify first ready step is step 1, not step 5
	if readySteps[0].ID != "gt-mol.1" {
		t.Errorf("first ready step is %s, want gt-mol.1", readySteps[0].ID)
	}

	// Verify full sequential ordering
	for i, step := range readySteps {
		expectedID := fmt.Sprintf("gt-mol.%d", i+1)
		if step.ID != expectedID {
			t.Errorf("readySteps[%d] = %s, want %s", i, step.ID, expectedID)
		}
	}
}

// TestMoleculeDepTypeFilterMixed verifies that the dependency filter correctly
// distinguishes blocking types ("blocks") from non-blocking types ("parent-child",
// empty string) when both appear on the same step. Matches beads' AffectsReadyWork
// semantics: only "blocks", "conditional-blocks", "waits-for" are blocking.
func TestMoleculeDepTypeFilterMixed(t *testing.T) {
	m := newMockBeadsForStep()

	// Root molecule
	m.addIssue(&beads.Issue{
		ID:     "gt-mol",
		Title:  "intake",
		Type:   "epic",
		Status: "open",
	})

	// Step 1: no deps
	m.addIssue(makeStepIssueWithDepType("gt-mol.1", "Ensure labels", "gt-mol", "open", nil))

	// Step 2: blocked by step 1 via "blocks" type, parent-child ignored
	m.addIssue(makeStepIssueWithDepType("gt-mol.2", "Split and file", "gt-mol", "open", []beads.IssueDep{
		{ID: "gt-mol.1", Title: "Ensure labels", DependencyType: "blocks"},
		{ID: "gt-mol", Title: "intake", DependencyType: "parent-child"},
	}))

	// Step 3: depends on step 2 via "blocks", also has non-blocking parent-child
	m.addIssue(makeStepIssueWithDepType("gt-mol.3", "Finalize", "gt-mol", "open", []beads.IssueDep{
		{ID: "gt-mol.2", Title: "Split and file", DependencyType: "blocks"},
		{ID: "gt-mol", Title: "intake", DependencyType: "parent-child"},
	}))

	// Run algorithm using isBlockingDepType
	children, _ := m.List(beads.ListOptions{Parent: "gt-mol", Status: "all"})
	closedIDs := make(map[string]bool)
	var openStepIDs []string
	for _, child := range children {
		if child.Status == "closed" {
			closedIDs[child.ID] = true
		} else if child.Status == "open" {
			openStepIDs = append(openStepIDs, child.ID)
		}
	}
	openStepsMap, _ := m.ShowMultiple(openStepIDs)

	var readySteps []string
	var blockedSteps []string
	for _, stepID := range openStepIDs {
		step := openStepsMap[stepID]
		if step == nil {
			continue
		}
		allDepsClosed := true
		hasBlockingDeps := false
		for _, dep := range step.Dependencies {
			if !isBlockingDepType(dep.DependencyType) {
				continue
			}
			hasBlockingDeps = true
			if !closedIDs[dep.ID] {
				allDepsClosed = false
				break
			}
		}
		if !hasBlockingDeps || allDepsClosed {
			readySteps = append(readySteps, step.ID)
		} else {
			blockedSteps = append(blockedSteps, step.ID)
		}
	}

	// Only step 1 should be ready; steps 2 and 3 are blocked by "blocks" deps
	if len(readySteps) != 1 || readySteps[0] != "gt-mol.1" {
		t.Errorf("readySteps=%v, want [gt-mol.1]", readySteps)
	}
	if len(blockedSteps) != 2 {
		t.Errorf("blockedSteps=%v, want 2 blocked steps", blockedSteps)
	}
}
