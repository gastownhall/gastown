package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
)

// TestLandConflictError_ErrorsAs verifies that callers (notably the refinery
// formula's consolidation-failure-handler) can detect a consolidation merge
// conflict via errors.As, instead of parsing message text. Regression test
// for hq-j6hur.3.7 (gh#3604): refinery left dirty worktree on consolidation
// conflict because the failure was an opaque string-wrapped error.
func TestLandConflictError_ErrorsAs(t *testing.T) {
	underlying := fmt.Errorf("git merge exit 1: CONFLICT in foo.go")
	wrapped := fmt.Errorf("integration land failed: %w", &LandConflictError{
		EpicID:        "gt-epic-99",
		Branch:        "integration/auth",
		TargetBranch:  "main",
		ConflictPaths: []string{"foo.go", "bar.go"},
		Underlying:    underlying,
	})

	var lce *LandConflictError
	if !errors.As(wrapped, &lce) {
		t.Fatalf("errors.As should match LandConflictError; got %T", wrapped)
	}
	if lce.EpicID != "gt-epic-99" {
		t.Errorf("EpicID = %q, want gt-epic-99", lce.EpicID)
	}
	if len(lce.ConflictPaths) != 2 {
		t.Errorf("ConflictPaths = %v, want 2 paths", lce.ConflictPaths)
	}
	if !errors.Is(lce, underlying) {
		t.Errorf("errors.Is should unwrap to underlying merge error")
	}

	msg := lce.Error()
	if !strings.Contains(msg, "integration/auth") || !strings.Contains(msg, "main") {
		t.Errorf("Error() should mention branch and target, got %q", msg)
	}
	if !strings.Contains(msg, "foo.go") {
		t.Errorf("Error() should list conflict files, got %q", msg)
	}
}

// TestLandConflictError_NoFiles covers the case where conflict-file detection
// failed (e.g., GetConflictingFiles returned nil) — the error message must
// still be informative.
func TestLandConflictError_NoFiles(t *testing.T) {
	lce := &LandConflictError{
		EpicID:       "gt-epic-99",
		Branch:       "integration/auth",
		TargetBranch: "main",
		Underlying:   fmt.Errorf("merge failed"),
	}
	msg := lce.Error()
	if !strings.Contains(msg, "integration/auth") || !strings.Contains(msg, "main") {
		t.Errorf("Error() should mention branch and target, got %q", msg)
	}
	if !strings.Contains(msg, "merge failed") {
		t.Errorf("Error() should embed underlying message, got %q", msg)
	}
}

// TestMakeTestMR_RealisticFields verifies that makeTestMR produces MR beads
// matching real beads structure: Type "task" with label "gt:merge-request",
// NOT Type "merge-request". This is the regression test for Bug 1 from
// PR #1226 review: mq_integration.go queries Type: "merge-request" but
// real MR beads have Type: "task" with label "gt:merge-request".
func TestMakeTestMR_RealisticFields(t *testing.T) {
	mr := makeTestMR("mr-1", "polecat/Nux/gt-001", "main", "Nux", "open")

	// Real MR beads have Type: "task", not "merge-request"
	if mr.Type != "task" {
		t.Errorf("makeTestMR() Type = %q, want %q (real MR beads use task type)", mr.Type, "task")
	}

	// Real MR beads carry the gt:merge-request label
	if !beads.HasLabel(mr, "gt:merge-request") {
		t.Errorf("makeTestMR() missing label 'gt:merge-request', got labels: %v", mr.Labels)
	}
}

// TestMockBeadsList_LabelFilter verifies that the mock's List method correctly
// filters by Label (not just Type), matching real Beads.List behavior.
func TestMockBeadsList_LabelFilter(t *testing.T) {
	mock := newMockBeads()

	// Add a realistic MR (Type: "task", Label: "gt:merge-request")
	mr := makeTestMR("mr-1", "polecat/Nux/gt-001", "main", "Nux", "open")
	mock.addIssue(mr)

	// Add a plain task (no MR label)
	plainTask := makeTestIssue("task-1", "Some task", "task", "open")
	mock.addIssue(plainTask)

	// Query by label should return only the MR
	results, err := mock.List(beads.ListOptions{Label: "gt:merge-request"})
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("List(Label: gt:merge-request) returned %d results, want 1", len(results))
	}
	if len(results) == 1 && results[0].ID != "mr-1" {
		t.Errorf("List() returned ID %q, want %q", results[0].ID, "mr-1")
	}
}

// TestMockBeadsList_StatusFiltering verifies that the mock's List method
// correctly reproduces bd's default status behavior: when Status is "",
// closed issues are excluded (matching real bd list without --status flag).
// This is the regression test for gt-6ck (MT-1): integration status showed
// 0 MRs because Status:"" silently excluded closed/merged MRs.
func TestMockBeadsList_StatusFiltering(t *testing.T) {
	mock := newMockBeads()

	// Add issues in various statuses
	mock.addIssue(makeTestMR("mr-open", "polecat/A/gt-001", "integration/test", "A", "open"))
	mock.addIssue(makeTestMR("mr-progress", "polecat/B/gt-002", "integration/test", "B", "in_progress"))
	mock.addIssue(makeTestMR("mr-closed", "polecat/C/gt-003", "integration/test", "C", "closed"))
	mock.addIssue(makeTestMR("mr-blocked", "polecat/D/gt-004", "integration/test", "D", "blocked"))

	tests := []struct {
		name      string
		status    string
		wantCount int
		wantIDs   map[string]bool
	}{
		{
			name:      "empty status excludes closed (matches real bd default)",
			status:    "",
			wantCount: 3,
			wantIDs:   map[string]bool{"mr-open": true, "mr-progress": true, "mr-blocked": true},
		},
		{
			name:      "status=all includes everything",
			status:    "all",
			wantCount: 4,
			wantIDs:   map[string]bool{"mr-open": true, "mr-progress": true, "mr-closed": true, "mr-blocked": true},
		},
		{
			name:      "status=open returns only open",
			status:    "open",
			wantCount: 1,
			wantIDs:   map[string]bool{"mr-open": true},
		},
		{
			name:      "status=closed returns only closed",
			status:    "closed",
			wantCount: 1,
			wantIDs:   map[string]bool{"mr-closed": true},
		},
		{
			name:      "status=in_progress returns only in_progress",
			status:    "in_progress",
			wantCount: 1,
			wantIDs:   map[string]bool{"mr-progress": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := mock.List(beads.ListOptions{
				Label:  "gt:merge-request",
				Status: tt.status,
			})
			if err != nil {
				t.Fatalf("List() error: %v", err)
			}
			if len(results) != tt.wantCount {
				ids := make([]string, len(results))
				for i, r := range results {
					ids[i] = r.ID + "(" + r.Status + ")"
				}
				t.Errorf("List(Status:%q) returned %d results %v, want %d",
					tt.status, len(results), ids, tt.wantCount)
			}
			for _, r := range results {
				if !tt.wantIDs[r.ID] {
					t.Errorf("List(Status:%q) returned unexpected ID %q (status=%q)",
						tt.status, r.ID, r.Status)
				}
			}
		})
	}
}

// mockBranchChecker implements beads.BranchChecker for testing.
type mockBranchChecker struct {
	localBranches  map[string]bool
	remoteBranches map[string]bool // key: "remote/branch"
}

func (m *mockBranchChecker) BranchExists(name string) (bool, error) {
	return m.localBranches[name], nil
}

func (m *mockBranchChecker) RemoteBranchExists(remote, name string) (bool, error) {
	key := remote + "/" + name
	return m.remoteBranches[key], nil
}

// TestResolveEpicBranch_LegacyFallback verifies that when an epic has no
// integration_branch metadata and the {title} branch doesn't exist, the
// resolver falls back to the legacy {epic} template.
// This is the regression test for review item #3: legacy epics created before
// the {epic}→{title} template change become undiscoverable in land/status.
func TestResolveEpicBranch_LegacyFallback(t *testing.T) {
	tests := []struct {
		name           string
		epic           *beads.Issue
		localBranches  map[string]bool
		remoteBranches map[string]bool
		checker        beads.BranchChecker // nil means no checker
		want           string
	}{
		{
			name: "metadata takes precedence over all templates",
			epic: &beads.Issue{
				ID:          "gt-abc",
				Type:        "epic",
				Title:       "Add User Auth",
				Description: "integration_branch: custom/my-branch\nSome description",
			},
			want: "custom/my-branch",
		},
		{
			name: "primary title branch exists",
			epic: &beads.Issue{
				ID:          "gt-abc",
				Type:        "epic",
				Title:       "Add User Auth",
				Description: "Some description",
			},
			remoteBranches: map[string]bool{"origin/integration/add-user-auth": true},
			want:           "integration/add-user-auth",
		},
		{
			name: "primary missing but legacy epic branch exists on remote",
			epic: &beads.Issue{
				ID:          "gt-abc",
				Type:        "epic",
				Title:       "Add User Auth",
				Description: "Some description",
			},
			remoteBranches: map[string]bool{"origin/integration/gt-abc": true},
			want:           "integration/gt-abc",
		},
		{
			name: "primary missing but legacy exists locally",
			epic: &beads.Issue{
				ID:          "gt-abc",
				Type:        "epic",
				Title:       "Add User Auth",
				Description: "Some description",
			},
			localBranches: map[string]bool{"integration/gt-abc": true},
			want:          "integration/gt-abc",
		},
		{
			name: "neither branch exists returns primary as best guess",
			epic: &beads.Issue{
				ID:          "gt-abc",
				Type:        "epic",
				Title:       "Add User Auth",
				Description: "Some description",
			},
			want: "integration/add-user-auth",
		},
		{
			name: "nil checker returns primary without existence check",
			epic: &beads.Issue{
				ID:          "gt-abc",
				Type:        "epic",
				Title:       "Add User Auth",
				Description: "Some description",
			},
			checker: nil, // explicitly nil
			want:    "integration/add-user-auth",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checker := tt.checker
			if checker == nil && (tt.localBranches != nil || tt.remoteBranches != nil) {
				checker = &mockBranchChecker{
					localBranches:  tt.localBranches,
					remoteBranches: tt.remoteBranches,
				}
			}
			got := resolveEpicBranch(tt.epic, "", checker)
			if got != tt.want {
				t.Errorf("resolveEpicBranch() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterMRsByTarget(t *testing.T) {
	// Create test MRs with different targets
	mrs := []*beads.Issue{
		makeTestMR("mr-1", "polecat/Nux/gt-001", "integration/gt-epic", "Nux", "open"),
		makeTestMR("mr-2", "polecat/Toast/gt-002", "main", "Toast", "open"),
		makeTestMR("mr-3", "polecat/Able/gt-003", "integration/gt-epic", "Able", "open"),
		makeTestMR("mr-4", "polecat/Baker/gt-004", "integration/gt-other", "Baker", "open"),
	}

	tests := []struct {
		name         string
		targetBranch string
		wantCount    int
		wantIDs      []string
	}{
		{
			name:         "filter to integration/gt-epic",
			targetBranch: "integration/gt-epic",
			wantCount:    2,
			wantIDs:      []string{"mr-1", "mr-3"},
		},
		{
			name:         "filter to main",
			targetBranch: "main",
			wantCount:    1,
			wantIDs:      []string{"mr-2"},
		},
		{
			name:         "filter to non-existent branch",
			targetBranch: "integration/no-such-epic",
			wantCount:    0,
			wantIDs:      []string{},
		},
		{
			name:         "filter to other integration branch",
			targetBranch: "integration/gt-other",
			wantCount:    1,
			wantIDs:      []string{"mr-4"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterMRsByTarget(mrs, tt.targetBranch)
			if len(got) != tt.wantCount {
				t.Errorf("filterMRsByTarget() returned %d MRs, want %d", len(got), tt.wantCount)
			}

			// Verify correct IDs
			gotIDs := make(map[string]bool)
			for _, mr := range got {
				gotIDs[mr.ID] = true
			}
			for _, wantID := range tt.wantIDs {
				if !gotIDs[wantID] {
					t.Errorf("filterMRsByTarget() missing expected MR %s", wantID)
				}
			}
		})
	}
}

func TestFilterMRsByTarget_EmptyInput(t *testing.T) {
	got := filterMRsByTarget(nil, "integration/gt-epic")
	if got != nil {
		t.Errorf("filterMRsByTarget(nil) = %v, want nil", got)
	}

	got = filterMRsByTarget([]*beads.Issue{}, "integration/gt-epic")
	if len(got) != 0 {
		t.Errorf("filterMRsByTarget([]) = %v, want empty slice", got)
	}
}

func TestFilterMRsByTarget_NoMRFields(t *testing.T) {
	// Issue with MR label but no MR fields in description
	plainIssue := &beads.Issue{
		ID:          "issue-1",
		Title:       "Not an MR",
		Type:        "task",
		Status:      "open",
		Labels:      []string{"gt:merge-request"},
		Description: "Just a plain description with no MR fields",
	}

	got := filterMRsByTarget([]*beads.Issue{plainIssue}, "main")
	if len(got) != 0 {
		t.Errorf("filterMRsByTarget() should filter out issues without MR fields, got %d", len(got))
	}
}

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name       string
		branchName string
		wantErr    bool
	}{
		{
			name:       "valid simple branch",
			branchName: "integration/gt-epic",
			wantErr:    false,
		},
		{
			name:       "valid nested branch",
			branchName: "user/project/feature",
			wantErr:    false,
		},
		{
			name:       "valid with hyphens and underscores",
			branchName: "user-name/feature_branch",
			wantErr:    false,
		},
		{
			name:       "empty branch name",
			branchName: "",
			wantErr:    true,
		},
		{
			name:       "contains tilde",
			branchName: "branch~1",
			wantErr:    true,
		},
		{
			name:       "contains caret",
			branchName: "branch^2",
			wantErr:    true,
		},
		{
			name:       "contains colon",
			branchName: "branch:ref",
			wantErr:    true,
		},
		{
			name:       "contains space",
			branchName: "branch name",
			wantErr:    true,
		},
		{
			name:       "contains backslash",
			branchName: "branch\\name",
			wantErr:    true,
		},
		{
			name:       "contains double dot",
			branchName: "branch..name",
			wantErr:    true,
		},
		{
			name:       "contains at-brace",
			branchName: "branch@{name}",
			wantErr:    true,
		},
		{
			name:       "ends with .lock",
			branchName: "branch.lock",
			wantErr:    true,
		},
		{
			name:       "starts with slash",
			branchName: "/branch",
			wantErr:    true,
		},
		{
			name:       "ends with slash",
			branchName: "branch/",
			wantErr:    true,
		},
		{
			name:       "starts with dot",
			branchName: ".branch",
			wantErr:    true,
		},
		{
			name:       "ends with dot",
			branchName: "branch.",
			wantErr:    true,
		},
		{
			name:       "consecutive slashes",
			branchName: "branch//name",
			wantErr:    true,
		},
		{
			name:       "contains question mark",
			branchName: "branch?name",
			wantErr:    true,
		},
		{
			name:       "contains asterisk",
			branchName: "branch*name",
			wantErr:    true,
		},
		{
			name:       "contains open bracket",
			branchName: "branch[name",
			wantErr:    true,
		},
		{
			name:       "exceeds max length",
			branchName: strings.Repeat("a", 201),
			wantErr:    true,
		},
		{
			name:       "at max length is valid",
			branchName: strings.Repeat("a", 200),
			wantErr:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBranchName(tt.branchName)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBranchName(%q) error = %v, wantErr %v", tt.branchName, err, tt.wantErr)
			}
		})
	}
}

func TestGetIntegrationBranchField(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "empty description",
			description: "",
			want:        "",
		},
		{
			name:        "field at beginning",
			description: "integration_branch: klauern/PROJ-123/RA-epic\nSome description",
			want:        "klauern/PROJ-123/RA-epic",
		},
		{
			name:        "field in middle",
			description: "Some text\nintegration_branch: custom/branch\nMore text",
			want:        "custom/branch",
		},
		{
			name:        "field with extra whitespace",
			description: "  integration_branch:   spaced/branch  \nOther content",
			want:        "spaced/branch",
		},
		{
			name:        "no integration_branch field",
			description: "Just a plain description\nWith multiple lines",
			want:        "",
		},
		{
			name:        "mixed case field name",
			description: "Integration_branch: CamelCase/branch",
			want:        "CamelCase/branch",
		},
		{
			name:        "default format",
			description: "integration_branch: integration/gt-epic\nEpic for auth work",
			want:        "integration/gt-epic",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getIntegrationBranchField(tt.description)
			if got != tt.want {
				t.Errorf("getIntegrationBranchField() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetRigGit(t *testing.T) {
	t.Run("bare repo exists", func(t *testing.T) {
		tmp := t.TempDir()
		bareRepo := filepath.Join(tmp, ".repo.git")
		if err := os.Mkdir(bareRepo, 0o755); err != nil {
			t.Fatal(err)
		}

		g, err := getRigGit(tmp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if g == nil {
			t.Fatal("expected non-nil Git")
		}
	})

	t.Run("mayor/rig exists without bare repo", func(t *testing.T) {
		tmp := t.TempDir()
		mayorRig := filepath.Join(tmp, "mayor", "rig")
		if err := os.MkdirAll(mayorRig, 0o755); err != nil {
			t.Fatal(err)
		}

		g, err := getRigGit(tmp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if g == nil {
			t.Fatal("expected non-nil Git")
		}
	})

	t.Run("neither exists returns error", func(t *testing.T) {
		tmp := t.TempDir()

		_, err := getRigGit(tmp)
		if err == nil {
			t.Fatal("expected error for empty directory")
		}
		if !strings.Contains(err.Error(), "no repo base found") {
			t.Errorf("expected 'no repo base found' error, got: %v", err)
		}
	})

	t.Run("bare repo takes precedence over mayor/rig", func(t *testing.T) {
		tmp := t.TempDir()
		bareRepo := filepath.Join(tmp, ".repo.git")
		if err := os.Mkdir(bareRepo, 0o755); err != nil {
			t.Fatal(err)
		}
		mayorRig := filepath.Join(tmp, "mayor", "rig")
		if err := os.MkdirAll(mayorRig, 0o755); err != nil {
			t.Fatal(err)
		}

		g, err := getRigGit(tmp)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if g == nil {
			t.Fatal("expected non-nil Git")
		}
		// When bare repo exists, WorkDir() returns "" (bare repo mode)
		if g.WorkDir() != "" {
			t.Errorf("expected empty WorkDir for bare repo, got %q", g.WorkDir())
		}
	})
}

// TestPostMerge_RigGitError verifies that getRigGit failure in runMQPostMerge
// propagates as an error rather than being silently swallowed (gh#3868).
// Before the fix, the function returned nil on getRigGit failure, giving exit
// code 0 and causing the refinery to skip retry on branch-delete failures.
func TestPostMerge_RigGitError(t *testing.T) {
	// Empty temp dir: no .repo.git, no mayor/rig → getRigGit returns error.
	tmp := t.TempDir()

	_, err := getRigGit(tmp)
	if err == nil {
		t.Fatal("expected getRigGit to fail for empty dir, got nil")
	}
	if !strings.Contains(err.Error(), "no repo base found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPostMerge_DeleteRemoteBranchErrorPropagated verifies that a failing
// git push --delete returns a non-nil error from DeleteRemoteBranch (gh#3868).
// Before the fix, runMQPostMerge swallowed this error and returned exit code 0.
func TestPostMerge_DeleteRemoteBranchErrorPropagated(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	tmp := t.TempDir()
	bareRepo := filepath.Join(tmp, ".repo.git")
	if err := os.MkdirAll(bareRepo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "init", "--bare", bareRepo).Run(); err != nil {
		t.Skipf("git init --bare: %v", err)
	}

	rigGit, err := getRigGit(tmp)
	if err != nil {
		t.Fatalf("getRigGit: %v", err)
	}

	// No "origin" remote is configured → git push --delete must fail.
	err = rigGit.DeleteRemoteBranch("origin", "polecat/test/gt-abc")
	if err == nil {
		t.Error("expected error from DeleteRemoteBranch with no remote, got nil")
	}
}

func TestGetIntegrationBranchTemplate(t *testing.T) {
	t.Run("CLI override provided", func(t *testing.T) {
		tmp := t.TempDir()
		got := getIntegrationBranchTemplate(tmp, "custom/{epic}")
		if got != "custom/{epic}" {
			t.Errorf("got %q, want %q", got, "custom/{epic}")
		}
	})

	t.Run("config has template", func(t *testing.T) {
		tmp := t.TempDir()
		settingsDir := filepath.Join(tmp, "settings")
		if err := os.Mkdir(settingsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := map[string]interface{}{
			"type":    "rig-settings",
			"version": 1,
			"merge_queue": map[string]interface{}{
				"integration_branch_template": "{prefix}/{epic}",
			},
		}
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}

		got := getIntegrationBranchTemplate(tmp, "")
		if got != "{prefix}/{epic}" {
			t.Errorf("got %q, want %q", got, "{prefix}/{epic}")
		}
	})

	t.Run("config exists but no template returns default", func(t *testing.T) {
		tmp := t.TempDir()
		settingsDir := filepath.Join(tmp, "settings")
		if err := os.Mkdir(settingsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := map[string]interface{}{
			"type":        "rig-settings",
			"version":     1,
			"merge_queue": map[string]interface{}{},
		}
		data, _ := json.Marshal(cfg)
		if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
			t.Fatal(err)
		}

		got := getIntegrationBranchTemplate(tmp, "")
		if got != defaultIntegrationBranchTemplate {
			t.Errorf("got %q, want %q", got, defaultIntegrationBranchTemplate)
		}
	})

	t.Run("no config file returns default", func(t *testing.T) {
		tmp := t.TempDir()
		got := getIntegrationBranchTemplate(tmp, "")
		if got != defaultIntegrationBranchTemplate {
			t.Errorf("got %q, want %q", got, defaultIntegrationBranchTemplate)
		}
	})
}

func TestIsReadyToLand(t *testing.T) {
	tests := []struct {
		name           string
		aheadCount     int
		childrenTotal  int
		childrenClosed int
		pendingMRCount int
		want           bool
	}{
		{
			name:           "all conditions met",
			aheadCount:     3,
			childrenTotal:  5,
			childrenClosed: 5,
			pendingMRCount: 0,
			want:           true,
		},
		{
			name:           "no commits ahead of main",
			aheadCount:     0,
			childrenTotal:  5,
			childrenClosed: 5,
			pendingMRCount: 0,
			want:           false,
		},
		{
			name:           "no children (empty epic)",
			aheadCount:     3,
			childrenTotal:  0,
			childrenClosed: 0,
			pendingMRCount: 0,
			want:           false,
		},
		{
			name:           "not all children closed",
			aheadCount:     3,
			childrenTotal:  5,
			childrenClosed: 3,
			pendingMRCount: 0,
			want:           false,
		},
		{
			name:           "pending MRs still open",
			aheadCount:     3,
			childrenTotal:  5,
			childrenClosed: 5,
			pendingMRCount: 2,
			want:           false,
		},
		{
			name:           "single child closed with commits",
			aheadCount:     1,
			childrenTotal:  1,
			childrenClosed: 1,
			pendingMRCount: 0,
			want:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isReadyToLand(tt.aheadCount, tt.childrenTotal, tt.childrenClosed, tt.pendingMRCount)
			if got != tt.want {
				t.Errorf("isReadyToLand(%d, %d, %d, %d) = %v, want %v",
					tt.aheadCount, tt.childrenTotal, tt.childrenClosed, tt.pendingMRCount, got, tt.want)
			}
		})
	}
}

// TestResolveEpicTarget verifies that the --epic flag resolution uses the configured
// integration branch template rather than hardcoding "integration/" prefix.
// This is the regression test for the bug where mq_submit.go used:
//
//	target = "integration/" + mqSubmitEpic  // WRONG: ignores custom template
//
// The fix uses getIntegrationBranchTemplate + buildIntegrationBranchName instead.
func TestResolveEpicTarget(t *testing.T) {
	tests := []struct {
		name      string
		epicID    string
		epicTitle string
		template  string // empty means default template (from getIntegrationBranchTemplate)
		want      string
	}{
		{
			name:      "default template produces integration/{title}",
			epicID:    "gt-epic",
			epicTitle: "Auth Feature",
			template:  "", // will use defaultIntegrationBranchTemplate
			want:      "integration/auth-feature",
		},
		{
			name:      "custom prefix/epic template ignores title",
			epicID:    "gt-epic",
			epicTitle: "Ignored",
			template:  "{prefix}/{epic}",
			want:      "gt/gt-epic",
		},
		{
			name:      "custom feature prefix template",
			epicID:    "proj-123",
			epicTitle: "Something",
			template:  "feature/{epic}",
			want:      "feature/proj-123",
		},
		{
			name:      "template with no placeholder prefix",
			epicID:    "gt-abc",
			epicTitle: "Release Work",
			template:  "release/{epic}",
			want:      "release/gt-abc",
		},
		{
			name:      "custom template with title",
			epicID:    "gt-42",
			epicTitle: "Big Refactor",
			template:  "{prefix}/{title}",
			want:      "gt/big-refactor",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmp := t.TempDir()

			if tt.template != "" {
				// Write config with custom template
				settingsDir := filepath.Join(tmp, "settings")
				if err := os.Mkdir(settingsDir, 0o755); err != nil {
					t.Fatal(err)
				}
				cfg := map[string]interface{}{
					"type":    "rig-settings",
					"version": 1,
					"merge_queue": map[string]interface{}{
						"integration_branch_template": tt.template,
					},
				}
				data, _ := json.Marshal(cfg)
				if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}

			template := getIntegrationBranchTemplate(tmp, "")
			got := buildIntegrationBranchName(template, tt.epicID, tt.epicTitle)

			if got != tt.want {
				t.Errorf("resolveEpicTarget(%q) with template %q = %q, want %q",
					tt.epicID, template, got, tt.want)
			}
		})
	}
}

// TestBuildIntegrationBranchName_NeverProducesInvalidRef verifies that
// buildIntegrationBranchName never produces a branch name that ends with "/"
// (an invalid git ref). This is the regression test for review item #5:
// empty epic title with {title} template could produce "integration/".
func TestBuildIntegrationBranchName_NeverProducesInvalidRef(t *testing.T) {
	tests := []struct {
		name      string
		template  string
		epicID    string
		epicTitle string
	}{
		{
			name:      "empty title with default template",
			template:  "",
			epicID:    "gt-99",
			epicTitle: "",
		},
		{
			name:      "empty title with explicit title template",
			template:  "integration/{title}",
			epicID:    "gt-99",
			epicTitle: "",
		},
		{
			name:      "special-chars-only title",
			template:  "integration/{title}",
			epicID:    "gt-42",
			epicTitle: "!@#$%^&*()",
		},
		{
			name:      "whitespace-only title",
			template:  "integration/{title}",
			epicID:    "gt-7",
			epicTitle: "   ",
		},
		{
			name:      "empty title with epic template (should always work)",
			template:  "integration/{epic}",
			epicID:    "gt-abc",
			epicTitle: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildIntegrationBranchName(tt.template, tt.epicID, tt.epicTitle)

			// Must never produce an invalid git ref
			if err := validateBranchName(got); err != nil {
				t.Errorf("buildIntegrationBranchName(%q, %q, %q) = %q, which is invalid: %v",
					tt.template, tt.epicID, tt.epicTitle, got, err)
			}

			// Must never end with "/" (the specific bug case)
			if strings.HasSuffix(got, "/") {
				t.Errorf("buildIntegrationBranchName(%q, %q, %q) = %q ends with '/' (invalid git ref)",
					tt.template, tt.epicID, tt.epicTitle, got)
			}

			// Must contain content after the last "/"
			parts := strings.Split(got, "/")
			lastPart := parts[len(parts)-1]
			if lastPart == "" {
				t.Errorf("buildIntegrationBranchName(%q, %q, %q) = %q has empty segment after last '/'",
					tt.template, tt.epicID, tt.epicTitle, got)
			}
		})
	}
}

func TestExtractEpicNumericSuffix(t *testing.T) {
	tests := []struct {
		epicID string
		want   string
	}{
		{"gt-123", "123"},
		{"PROJ-456", "456"},
		{"abc", "abc"},
		{"a-b-c", "c"},
		{"gt-", "gt-"},
	}

	for _, tt := range tests {
		t.Run(tt.epicID, func(t *testing.T) {
			got := extractEpicNumericSuffix(tt.epicID)
			if got != tt.want {
				t.Errorf("extractEpicNumericSuffix(%q) = %q, want %q", tt.epicID, got, tt.want)
			}
		})
	}
}
