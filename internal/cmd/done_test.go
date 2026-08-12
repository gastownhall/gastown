package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/session"
)

// TestDoneUsesResolveBeadsDir verifies that the done command correctly uses
// beads.ResolveBeadsDir to follow redirect files when initializing beads.
// This is critical for polecat/crew worktrees that use .beads/redirect to point
// to the shared mayor/rig/.beads directory.
//
// The done.go file has two code paths that initialize beads:
//   - Line 181: ExitCompleted path - bd := beads.New(beads.ResolveBeadsDir(cwd))
//   - Line 277: ExitPhaseComplete path - bd := beads.New(beads.ResolveBeadsDir(cwd))
//
// Both must use ResolveBeadsDir to properly handle redirects.
func TestDoneUsesResolveBeadsDir(t *testing.T) {
	// Create a temp directory structure simulating polecat worktree with redirect
	tmpDir := t.TempDir()

	// Create structure like:
	//   gastown/
	//     mayor/rig/.beads/          <- shared beads directory
	//     polecats/fixer/.beads/     <- polecat with redirect
	//       redirect -> ../../mayor/rig/.beads

	mayorRigBeadsDir := filepath.Join(tmpDir, "gastown", "mayor", "rig", ".beads")
	polecatDir := filepath.Join(tmpDir, "gastown", "polecats", "fixer")
	polecatBeadsDir := filepath.Join(polecatDir, ".beads")

	// Create directories
	if err := os.MkdirAll(mayorRigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	if err := os.MkdirAll(polecatBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir polecats/fixer/.beads: %v", err)
	}

	// Create redirect file pointing to mayor/rig/.beads
	redirectContent := "../../mayor/rig/.beads"
	redirectPath := filepath.Join(polecatBeadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte(redirectContent), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	t.Run("redirect followed from polecat directory", func(t *testing.T) {
		// This mirrors how done.go initializes beads at line 181 and 277
		resolvedDir := beads.ResolveBeadsDir(polecatDir)

		// Should resolve to mayor/rig/.beads
		if resolvedDir != mayorRigBeadsDir {
			t.Errorf("ResolveBeadsDir(%s) = %s, want %s", polecatDir, resolvedDir, mayorRigBeadsDir)
		}

		// Verify the beads instance is created with the resolved path
		// We use the same pattern as done.go: beads.New(beads.ResolveBeadsDir(cwd))
		bd := beads.New(beads.ResolveBeadsDir(polecatDir))
		if bd == nil {
			t.Error("beads.New returned nil")
		}
	})

	t.Run("redirect not present uses local beads", func(t *testing.T) {
		// Without redirect, should use local .beads
		localDir := filepath.Join(tmpDir, "gastown", "mayor", "rig")
		resolvedDir := beads.ResolveBeadsDir(localDir)

		if resolvedDir != mayorRigBeadsDir {
			t.Errorf("ResolveBeadsDir(%s) = %s, want %s", localDir, resolvedDir, mayorRigBeadsDir)
		}
	})
}

func TestForceCloseIssueWithRetryClosesNoMergeIssue(t *testing.T) {
	var gotReason string
	var gotIDs []string
	calls := 0

	err := forceCloseIssueWithRetry(func(reason string, ids ...string) error {
		calls++
		gotReason = reason
		gotIDs = append([]string(nil), ids...)
		return nil
	}, "gt-abc", "No-merge work completed; merge queue skipped", "Issue %s closed (no-merge)")
	if err != nil {
		t.Fatalf("forceCloseIssueWithRetry returned error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("close calls = %d, want 1", calls)
	}
	if gotReason != "No-merge work completed; merge queue skipped" {
		t.Errorf("reason = %q", gotReason)
	}
	if len(gotIDs) != 1 || gotIDs[0] != "gt-abc" {
		t.Errorf("ids = %v, want [gt-abc]", gotIDs)
	}
}

func TestForceCloseIssueWithRetryReturnsFinalError(t *testing.T) {
	wantErr := errors.New("dolt locked")
	calls := 0

	err := forceCloseIssueWithRetrySleep(func(string, ...string) error {
		calls++
		return wantErr
	}, "gt-abc", "No-merge work completed; merge queue skipped", "Issue %s closed (no-merge)", func(time.Duration) {})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if calls != 3 {
		t.Fatalf("close calls = %d, want 3", calls)
	}
}

func TestReviewOnlyCloseRequiresEvidence(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-review",
		Description: "review_only: true\n",
	}

	reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, "abc123")
	if reason == "" {
		t.Fatal("expected review-only close skip reason")
	}
	if !fatal {
		t.Fatal("review-only close without evidence should fail closed")
	}
	if !strings.Contains(reason, "no fresh assignment timestamp") {
		t.Fatalf("reason = %q, want missing evidence", reason)
	}
}

func TestReviewOnlyCloseRejectsNotesAndDesignEvidence(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-review",
		Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
		Assignee:    "gastown/polecats/toast",
		Notes:       "FINDINGS: reviewed and no code changes needed",
		Design:      "PR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123",
	}

	reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, "abc123")
	if reason == "" || !fatal {
		t.Fatalf("notes/design should not satisfy review evidence: reason=%q fatal=%v", reason, fatal)
	}
}

func TestReviewOnlyCloseAllowsFreshEvidenceComment(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-review",
		Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
		Assignee:    "gastown/polecats/toast",
		Comments: []beads.Comment{
			{
				Author:    "gastown/polecats/toast",
				CreatedAt: "2026-07-01T12:05:00Z",
				Text:      "PR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123",
			},
		},
	}

	reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, "abc123")
	if reason != "" || fatal {
		t.Fatalf("doneReviewOnlyCloseSkipReason = %q, %v; want allowed", reason, fatal)
	}
}

func TestReviewOnlyGeneratedCommentsDoNotCountAsEvidence(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-review",
		Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
		Assignee:    "gastown/polecats/toast",
		Comments: []beads.Comment{
			{Author: "gastown/polecats/toast", CreatedAt: "2026-07-01T12:05:00Z", Text: "verified_push_skipped: --skip-verify on no-MR close\nPR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123"},
			{Author: "gastown/polecats/toast", CreatedAt: "2026-07-01T12:06:00Z", Text: "MR created: gt-wisp-abc\nPR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123"},
		},
	}

	reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, "abc123")
	if reason == "" || !fatal {
		t.Fatalf("generated comments should not satisfy review evidence: reason=%q fatal=%v", reason, fatal)
	}
}

func TestReviewOnlyCloseRejectsStaleComment(t *testing.T) {
	tests := []struct {
		name      string
		createdAt string
	}{
		{name: "before attached_at", createdAt: "2026-07-01T11:59:59Z"},
		{name: "equal to attached_at", createdAt: "2026-07-01T12:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &beads.Issue{
				ID:          "gt-review",
				Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
				Assignee:    "gastown/polecats/toast",
				Comments: []beads.Comment{{
					Author:    "gastown/polecats/toast",
					CreatedAt: tt.createdAt,
					Text:      "PR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123",
				}},
			}

			reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, "abc123")
			if reason == "" || !fatal {
				t.Fatalf("stale comment should not satisfy review evidence: reason=%q fatal=%v", reason, fatal)
			}
		})
	}
}

func TestReviewOnlyCloseRejectsWrongAuthorOrHead(t *testing.T) {
	tests := []struct {
		name    string
		author  string
		head    string
		current string
	}{
		{name: "wrong author", author: "gastown/polecats/other", head: "abc123", current: "abc123"},
		{name: "wrong head", author: "gastown/polecats/toast", head: "def456", current: "abc123"},
		{name: "missing head", author: "gastown/polecats/toast", head: "", current: "abc123"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := "PR-SHERIFF-EVIDENCE: pass"
			if tt.head != "" {
				text += "\nhead_sha: " + tt.head
			}
			issue := &beads.Issue{
				ID:          "gt-review",
				Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
				Assignee:    "gastown/polecats/toast",
				Comments: []beads.Comment{{
					Author:    tt.author,
					CreatedAt: "2026-07-01T12:05:00Z",
					Text:      text,
				}},
			}
			reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, tt.current)
			if reason == "" || !fatal {
				t.Fatalf("invalid evidence should fail closed: reason=%q fatal=%v", reason, fatal)
			}
		})
	}
}

func TestReviewOnlyCloseRejectsMissingAssigneeOrInvalidCommentTime(t *testing.T) {
	tests := []struct {
		name      string
		assignee  string
		createdAt string
	}{
		{name: "missing assignee", assignee: "", createdAt: "2026-07-01T12:05:00Z"},
		{name: "invalid comment time", assignee: "gastown/polecats/toast", createdAt: "not-a-time"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issue := &beads.Issue{
				ID:          "gt-review",
				Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
				Assignee:    tt.assignee,
				Comments: []beads.Comment{{
					Author:    "gastown/polecats/toast",
					CreatedAt: tt.createdAt,
					Text:      "PR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123",
				}},
			}
			reason, fatal := doneReviewOnlyCloseSkipReasonForHead(nil, issue.ID, issue, "abc123")
			if reason == "" || !fatal {
				t.Fatalf("invalid metadata should fail closed: reason=%q fatal=%v", reason, fatal)
			}
		})
	}
}

func TestNonReviewOnlyCloseDoesNotRequireEvidence(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-review",
		Description: "no_merge: true\n",
	}

	reason, fatal := doneReviewOnlyCloseSkipReason(nil, issue.ID, issue)
	if reason != "" || fatal {
		t.Fatalf("non-review-only close gate = %q, %v; want no restriction", reason, fatal)
	}
}

func TestNonReviewOnlyReviewGateDoesNotChangeCriteriaHandling(t *testing.T) {
	issue := &beads.Issue{
		ID:                 "gt-review",
		Description:        "no_merge: true\n",
		AcceptanceCriteria: "- [ ] still open\n",
	}

	reason, fatal := doneSourceCloseSkipReason(nil, issue.ID, issue)
	if reason == "" || fatal {
		t.Fatalf("criteria gate = %q, %v; want non-fatal skip", reason, fatal)
	}
	if !strings.Contains(reason, "unchecked acceptance criteria") {
		t.Fatalf("reason = %q, want criteria reason", reason)
	}
}

func TestSourceCloseRejectsNonConcreteIssue(t *testing.T) {
	issue := &beads.Issue{
		ID:     "gt-mr",
		Labels: []string{"gt:merge-request"},
	}

	reason, fatal := doneSourceCloseSkipReason(nil, issue.ID, issue)
	if reason == "" || !fatal {
		t.Fatalf("source close gate = %q, %v; want fatal non-concrete rejection", reason, fatal)
	}
	if !strings.Contains(reason, "not concrete") {
		t.Fatalf("reason = %q, want non-concrete reason", reason)
	}
}

func TestSourceCloseRejectsLocalMergeStrategy(t *testing.T) {
	issue := &beads.Issue{
		ID:          "gt-work",
		Type:        "task",
		Description: "merge_strategy: local\n",
	}

	reason, fatal := doneSourceCloseSkipReason(nil, issue.ID, issue)
	if reason == "" || fatal {
		t.Fatalf("local source close gate = %q, %v; want non-fatal skip", reason, fatal)
	}
	if !strings.Contains(reason, "merge_strategy=local") {
		t.Fatalf("reason = %q, want local merge strategy reason", reason)
	}
}

func TestDirectMergeRejectsUnsafeSourceBeforePush(t *testing.T) {
	freshEvidenceReviewOnly := &beads.Issue{
		ID:          "gt-review",
		Type:        "task",
		Description: "review_only: true\nattached_at: 2026-07-01T12:00:00Z\n",
		Assignee:    "gastown/polecats/toast",
		Comments: []beads.Comment{{
			Author:    "gastown/polecats/toast",
			CreatedAt: "2026-07-01T12:05:00Z",
			Text:      "PR-SHERIFF-EVIDENCE: pass\nhead_sha: abc123",
		}},
	}
	tests := []struct {
		name        string
		issueID     string
		issue       *beads.Issue
		wantReason  string
		wantAllowed bool
	}{
		{
			name:       "missing source id",
			issue:      &beads.Issue{ID: "gt-work", Type: "task"},
			wantReason: "source issue is required",
		},
		{
			name:       "non concrete source",
			issueID:    "gt-mr",
			issue:      &beads.Issue{ID: "gt-mr", Labels: []string{"gt:merge-request"}},
			wantReason: "not concrete",
		},
		{
			name:       "review only source",
			issueID:    "gt-review",
			issue:      freshEvidenceReviewOnly,
			wantReason: "review-only issue gt-review cannot be direct-merged",
		},
		{
			name:       "no merge source",
			issueID:    "gt-work",
			issue:      &beads.Issue{ID: "gt-work", Type: "task", Description: "no_merge: true\n"},
			wantReason: "no_merge=true",
		},
		{
			name:       "local merge strategy source",
			issueID:    "gt-work",
			issue:      &beads.Issue{ID: "gt-work", Type: "task", Description: "merge_strategy: local\n"},
			wantReason: "merge_strategy=local",
		},
		{
			name:       "unchecked criteria",
			issueID:    "gt-work",
			issue:      &beads.Issue{ID: "gt-work", Type: "task", AcceptanceCriteria: "- [ ] still open\n"},
			wantReason: "unchecked acceptance criteria",
		},
		{
			name:        "eligible source",
			issueID:     "gt-work",
			issue:       &beads.Issue{ID: "gt-work", Type: "task"},
			wantAllowed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason := doneDirectMergeSkipReason(nil, tt.issueID, tt.issue, "main")
			if tt.wantAllowed {
				if reason != "" {
					t.Fatalf("direct merge gate = %q; want allowed", reason)
				}
				return
			}
			if reason == "" || !strings.Contains(reason, tt.wantReason) {
				t.Fatalf("direct merge gate = %q; want reason containing %q", reason, tt.wantReason)
			}
		})
	}
}

func TestSourceValidationRejectsInternalIssues(t *testing.T) {
	if err := validateConcreteSourceIssue("gt-work", &beads.Issue{ID: "gt-work", Type: "task"}); err != nil {
		t.Fatalf("concrete source rejected: %v", err)
	}
	if err := validateConcreteSourceIssue("gt-mr", &beads.Issue{ID: "gt-mr", Labels: []string{"gt:merge-request"}}); err == nil {
		t.Fatal("internal source accepted; want rejection")
	}
}

func TestValidateMergeRequestSourceRejectsMissingAndMismatchedSource(t *testing.T) {
	missing := &beads.Issue{ID: "gt-mr", Description: "branch: polecat/test/gt-work\n"}
	if err := validateMergeRequestSource(missing, "gt-work", &beads.Issue{ID: "gt-work", Type: "task"}); err == nil || !strings.Contains(err.Error(), "missing source_issue") {
		t.Fatalf("missing source validation error = %v, want missing source_issue", err)
	}

	mismatched := &beads.Issue{ID: "gt-mr", Description: "source_issue: gt-other\n"}
	if err := validateMergeRequestSource(mismatched, "gt-work", &beads.Issue{ID: "gt-work", Type: "task"}); err == nil || !strings.Contains(err.Error(), "does not match expected") {
		t.Fatalf("mismatched source validation error = %v, want mismatch", err)
	}
}

// TestDoneBeadsInitWithoutRedirect verifies that beads initialization works
// normally when no redirect file exists.
func TestDoneBeadsInitWithoutRedirect(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a simple .beads directory without redirect (like mayor/rig)
	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// ResolveBeadsDir should return the same directory when no redirect exists
	resolvedDir := beads.ResolveBeadsDir(tmpDir)
	if resolvedDir != beadsDir {
		t.Errorf("ResolveBeadsDir(%s) = %s, want %s", tmpDir, resolvedDir, beadsDir)
	}

	// Beads initialization should work the same way done.go does it
	bd := beads.New(beads.ResolveBeadsDir(tmpDir))
	if bd == nil {
		t.Error("beads.New returned nil")
	}
}

// TestDoneBeadsInitBothCodePaths documents that both code paths in done.go
// that create beads instances use ResolveBeadsDir:
//   - ExitCompleted (line 181): for MR creation and issue operations
//   - ExitPhaseComplete (line 277): for gate waiter registration
//
// This test verifies the pattern by demonstrating that the resolved directory
// is used consistently for different operations.
func TestDoneBeadsInitBothCodePaths(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup: crew directory with redirect to mayor/rig/.beads
	mayorRigBeadsDir := filepath.Join(tmpDir, "mayor", "rig", ".beads")
	crewDir := filepath.Join(tmpDir, "crew", "max")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	if err := os.MkdirAll(mayorRigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir mayor/rig/.beads: %v", err)
	}
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir crew/max/.beads: %v", err)
	}

	// Create redirect
	redirectPath := filepath.Join(crewBeadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../mayor/rig/.beads"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	t.Run("ExitCompleted path uses ResolveBeadsDir", func(t *testing.T) {
		// This simulates the line 181 path in done.go:
		// bd := beads.New(beads.ResolveBeadsDir(cwd))
		resolvedDir := beads.ResolveBeadsDir(crewDir)
		if resolvedDir != mayorRigBeadsDir {
			t.Errorf("ExitCompleted path: ResolveBeadsDir(%s) = %s, want %s",
				crewDir, resolvedDir, mayorRigBeadsDir)
		}

		bd := beads.New(beads.ResolveBeadsDir(crewDir))
		if bd == nil {
			t.Error("beads.New returned nil for ExitCompleted path")
		}
	})

	t.Run("ExitPhaseComplete path uses ResolveBeadsDir", func(t *testing.T) {
		// This simulates the line 277 path in done.go:
		// bd := beads.New(beads.ResolveBeadsDir(cwd))
		resolvedDir := beads.ResolveBeadsDir(crewDir)
		if resolvedDir != mayorRigBeadsDir {
			t.Errorf("ExitPhaseComplete path: ResolveBeadsDir(%s) = %s, want %s",
				crewDir, resolvedDir, mayorRigBeadsDir)
		}

		bd := beads.New(beads.ResolveBeadsDir(crewDir))
		if bd == nil {
			t.Error("beads.New returned nil for ExitPhaseComplete path")
		}
	})
}

// TestDoneRedirectChain verifies behavior with chained redirects.
// ResolveBeadsDir follows chains up to depth 3 as a safety net for legacy configs.
// SetupRedirect avoids creating chains (bd CLI doesn't support them), but if
// chains exist we follow them to the final destination.
func TestDoneRedirectChain(t *testing.T) {
	tmpDir := t.TempDir()

	// Create chain: worktree -> intermediate -> canonical
	canonicalBeadsDir := filepath.Join(tmpDir, "canonical", ".beads")
	intermediateDir := filepath.Join(tmpDir, "intermediate")
	intermediateBeadsDir := filepath.Join(intermediateDir, ".beads")
	worktreeDir := filepath.Join(tmpDir, "worktree")
	worktreeBeadsDir := filepath.Join(worktreeDir, ".beads")

	// Create all directories
	for _, dir := range []string{canonicalBeadsDir, intermediateBeadsDir, worktreeBeadsDir} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	// Create redirects
	// intermediate -> canonical
	if err := os.WriteFile(filepath.Join(intermediateBeadsDir, "redirect"), []byte("../canonical/.beads"), 0644); err != nil {
		t.Fatalf("write intermediate redirect: %v", err)
	}
	// worktree -> intermediate
	if err := os.WriteFile(filepath.Join(worktreeBeadsDir, "redirect"), []byte("../intermediate/.beads"), 0644); err != nil {
		t.Fatalf("write worktree redirect: %v", err)
	}

	// ResolveBeadsDir follows chains up to depth 3 as a safety net.
	// Note: SetupRedirect avoids creating chains (bd CLI doesn't support them),
	// but if chains exist from legacy configs, we follow them to the final destination.
	resolved := beads.ResolveBeadsDir(worktreeDir)

	// Should resolve to canonical (follows the full chain)
	if resolved != canonicalBeadsDir {
		t.Errorf("ResolveBeadsDir should follow chain to final destination: got %s, want %s",
			resolved, canonicalBeadsDir)
	}
}

// TestDoneEmptyRedirectFallback verifies that an empty or whitespace-only
// redirect file falls back to the local .beads directory.
func TestDoneEmptyRedirectFallback(t *testing.T) {
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create empty redirect file
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("   \n"), 0644); err != nil {
		t.Fatalf("write empty redirect: %v", err)
	}

	// Should fall back to local .beads
	resolved := beads.ResolveBeadsDir(tmpDir)
	if resolved != beadsDir {
		t.Errorf("empty redirect should fallback: got %s, want %s", resolved, beadsDir)
	}
}

// TestDoneCircularRedirectProtection verifies that circular redirects
// are detected and handled safely.
func TestDoneCircularRedirectProtection(t *testing.T) {
	tmpDir := t.TempDir()

	beadsDir := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	// Create circular redirect (points to itself)
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte(".beads"), 0644); err != nil {
		t.Fatalf("write circular redirect: %v", err)
	}

	// Should detect circular redirect and return original
	resolved := beads.ResolveBeadsDir(tmpDir)
	if resolved != beadsDir {
		t.Errorf("circular redirect should return original: got %s, want %s", resolved, beadsDir)
	}
}

func TestSelectAssignedIssue(t *testing.T) {
	tests := []struct {
		name        string
		branchIssue string
		assigned    []string
		wantIssue   string
		wantAmbig   bool
	}{
		{
			name:      "single assignment selected",
			assigned:  []string{"gt-real"},
			wantIssue: "gt-real",
		},
		{
			name:        "stale branch overridden by single assignment",
			branchIssue: "gt-old",
			assigned:    []string{"gt-real"},
			wantIssue:   "gt-real",
		},
		{
			name:        "branch matching assignment needs no override",
			branchIssue: "gt-real",
			assigned:    []string{"gt-real"},
		},
		{
			name:        "subtask branch matching assignment needs no override",
			branchIssue: "gt-real.1",
			assigned:    []string{"gt-real"},
		},
		{
			name:        "branch matching one of multiple assignments needs no override",
			branchIssue: "gt-real",
			assigned:    []string{"gt-real", "gt-other"},
		},
		{
			name:      "duplicate assignment ids collapse",
			assigned:  []string{"gt-real", "gt-real"},
			wantIssue: "gt-real",
		},
		{
			name:      "multiple assignments are ambiguous",
			assigned:  []string{"gt-b", "gt-a"},
			wantAmbig: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotIssue, gotAmbig := selectAssignedIssue(tt.branchIssue, tt.assigned)
			if gotIssue != tt.wantIssue || gotAmbig != tt.wantAmbig {
				t.Fatalf("selectAssignedIssue(%q, %v) = (%q, %v), want (%q, %v)",
					tt.branchIssue, tt.assigned, gotIssue, gotAmbig, tt.wantIssue, tt.wantAmbig)
			}
		})
	}
}

// TestIsStaleBranchIssue verifies the stale-branch guard (hq-l0fj): a
// branch-derived issue id is overridden only when it conflicts with the
// hooked bead and is not a subtask of it.
func TestIsStaleBranchIssue(t *testing.T) {
	tests := []struct {
		name        string
		branchIssue string
		hookedIssue string
		want        bool
	}{
		{"matching ids are not stale", "hq-oibv", "hq-oibv", false},
		{"reused branch from closed bead is stale", "re-ofo", "hq-oibv", true},
		{"subtask of hooked bead is not stale", "gt-abc.1", "gt-abc", false},
		{"different bead with shared prefix is stale", "gt-abc1", "gt-abc", true},
		{"no branch issue is not stale", "", "hq-oibv", false},
		{"no hooked bead is not stale", "re-ofo", "", false},
		{"both empty is not stale", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStaleBranchIssue(tt.branchIssue, tt.hookedIssue); got != tt.want {
				t.Errorf("isStaleBranchIssue(%q, %q) = %v, want %v", tt.branchIssue, tt.hookedIssue, got, tt.want)
			}
		})
	}
}

// TestIsPolecatActor verifies that isPolecatActor correctly identifies
// polecat actors vs other roles based on the BD_ACTOR format.
func TestIsPolecatActor(t *testing.T) {
	tests := []struct {
		actor string
		want  bool
	}{
		// Polecats: rigname/polecats/polecatname
		{"testrig/polecats/furiosa", true},
		{"testrig/polecats/nux", true},
		{"myrig/polecats/witness", true}, // even if named "witness", still a polecat

		// Non-polecats
		{"gastown/crew/george", false},
		{"gastown/crew/max", false},
		{"testrig/witness", false},
		{"testrig/deacon", false},
		{"testrig/mayor", false},
		{"gastown/refinery", false},

		// Edge cases
		{"", false},
		{"single", false},
		{"polecats/name", false}, // needs rig prefix
		{"testrig/polecats", false},
		{"testrig/polecats/", false},
		{"/polecats/furiosa", false},
		{"testrig/polecats/furiosa/extra", false},
	}

	for _, tt := range tests {
		t.Run(tt.actor, func(t *testing.T) {
			got := isPolecatActor(tt.actor)
			if got != tt.want {
				t.Errorf("isPolecatActor(%q) = %v, want %v", tt.actor, got, tt.want)
			}
		})
	}
}

// TestDoneIntentLabelFormat verifies the done-intent label format matches
// the expected pattern: done-intent:<type>:<unix-ts>
func TestDoneIntentLabelFormat(t *testing.T) {
	now := time.Unix(1738972800, 0)
	tests := []struct {
		exitType string
		want     string
	}{
		{"COMPLETED", "done-intent:COMPLETED:1738972800"},
		{"ESCALATED", "done-intent:ESCALATED:1738972800"},
		{"DEFERRED", "done-intent:DEFERRED:1738972800"},
		{"PHASE_COMPLETE", "done-intent:PHASE_COMPLETE:1738972800"},
	}

	for _, tt := range tests {
		t.Run(tt.exitType, func(t *testing.T) {
			label := doneIntentLabel(tt.exitType, now)
			if label != tt.want {
				t.Errorf("label format = %q, want %q", label, tt.want)
			}
		})
	}
}

// TestShouldNudgeRefinery locks in the gh#3885 invariant: only COMPLETED
// exits with a created MR bead may wake the refinery. DEFERRED/ESCALATED
// exits — used by polecats finishing operational tasks with no code changes —
// must never emit MQ_SUBMIT, even if an mrID is somehow populated. The
// "stray MR" cases guard against a regression to a bare `mrID != ""` check.
func TestShouldNudgeRefinery(t *testing.T) {
	tests := []struct {
		name     string
		exitType string
		mrID     string
		want     bool
	}{
		{"completed with MR nudges", ExitCompleted, "gt-abc123", true},
		{"completed without MR does not nudge", ExitCompleted, "", false},
		{"deferred without MR does not nudge", ExitDeferred, "", false},
		{"deferred with stray MR does not nudge", ExitDeferred, "gt-abc123", false},
		{"escalated without MR does not nudge", ExitEscalated, "", false},
		{"escalated with stray MR does not nudge", ExitEscalated, "gt-abc123", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldNudgeRefinery(tt.exitType, tt.mrID); got != tt.want {
				t.Errorf("shouldNudgeRefinery(%q, %q) = %v, want %v",
					tt.exitType, tt.mrID, got, tt.want)
			}
		})
	}
}

func TestShouldUpdateAgentStateOnDone(t *testing.T) {
	tests := []struct {
		name       string
		pushFailed bool
		mrFailed   bool
		want       bool
	}{
		{"clean submission updates state", false, false, true},
		{"push failure preserves hook", true, false, false},
		{"mr failure preserves hook", false, true, false},
		{"both failures preserve hook", true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldUpdateAgentStateOnDone(tt.pushFailed, tt.mrFailed)
			if got != tt.want {
				t.Errorf("shouldUpdateAgentStateOnDone(%v, %v) = %v, want %v", tt.pushFailed, tt.mrFailed, got, tt.want)
			}
		})
	}
}

func TestUpdateAgentStateAfterSubmissionSkipsFailedSubmissions(t *testing.T) {
	calls := 0
	old := updateAgentStateOnDoneFn
	updateAgentStateOnDoneFn = func(cwd, townRoot, exitType, issueID string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { updateAgentStateOnDoneFn = old })

	if err := updateAgentStateAfterSubmission("/work", "/town", ExitCompleted, "gt-abc", true, false); err != nil {
		t.Fatalf("updateAgentStateAfterSubmission push failure: %v", err)
	}
	if err := updateAgentStateAfterSubmission("/work", "/town", ExitCompleted, "gt-abc", false, true); err != nil {
		t.Fatalf("updateAgentStateAfterSubmission mr failure: %v", err)
	}
	if calls != 0 {
		t.Fatalf("state update calls after failed submissions = %d, want 0", calls)
	}

	if err := updateAgentStateAfterSubmission("/work", "/town", ExitCompleted, "gt-abc", false, false); err != nil {
		t.Fatalf("updateAgentStateAfterSubmission clean submission: %v", err)
	}
	if calls != 1 {
		t.Fatalf("state update calls after clean submission = %d, want 1", calls)
	}
}

func TestShouldRetirePolecatSessionAfterDone(t *testing.T) {
	tests := []struct {
		name          string
		exitType      string
		mergeStrategy string
		pushFailed    bool
		mrFailed      bool
		want          bool
	}{
		{"completed default strategy retires", ExitCompleted, "", false, false, true},
		{"completed direct strategy retires", ExitCompleted, "direct", false, false, true},
		{"completed mr strategy retires", ExitCompleted, "mr", false, false, true},
		{"local strategy preserves session", ExitCompleted, "local", false, false, false},
		{"deferred preserves session", ExitDeferred, "", false, false, false},
		{"escalated preserves session", ExitEscalated, "", false, false, false},
		{"push failure preserves session", ExitCompleted, "", true, false, false},
		{"mr failure preserves session", ExitCompleted, "", false, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRetirePolecatSessionAfterDone(tt.exitType, tt.mergeStrategy, tt.pushFailed, tt.mrFailed)
			if got != tt.want {
				t.Errorf("shouldRetirePolecatSessionAfterDone(%q, %q, %v, %v) = %v, want %v",
					tt.exitType, tt.mergeStrategy, tt.pushFailed, tt.mrFailed, got, tt.want)
			}
		})
	}
}

type fakeDoneSessionKiller struct {
	name        string
	excludePIDs []string
	calls       int
}

func (f *fakeDoneSessionKiller) KillSessionWithProcessesExcluding(name string, excludePIDs []string) error {
	f.calls++
	f.name = name
	f.excludePIDs = append([]string(nil), excludePIDs...)
	return nil
}

func TestRetirePolecatSessionAfterDoneUsesPIDExclusion(t *testing.T) {
	fake := &fakeDoneSessionKiller{}
	old := newDoneSessionKiller
	newDoneSessionKiller = func() doneSessionKiller { return fake }
	t.Cleanup(func() { newDoneSessionKiller = old })

	if err := retirePolecatSessionAfterDone("gastown", "nitro", 12345); err != nil {
		t.Fatalf("retirePolecatSessionAfterDone: %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("killer calls = %d, want 1", fake.calls)
	}
	wantSession := session.PolecatSessionName(session.PrefixFor("gastown"), "nitro")
	if fake.name != wantSession {
		t.Fatalf("session name = %q, want %q", fake.name, wantSession)
	}
	if len(fake.excludePIDs) != 1 || fake.excludePIDs[0] != "12345" {
		t.Fatalf("excludePIDs = %#v, want [12345]", fake.excludePIDs)
	}
}

func TestRetirePolecatSessionAfterDoneNoopsWithoutIdentity(t *testing.T) {
	fake := &fakeDoneSessionKiller{}
	old := newDoneSessionKiller
	newDoneSessionKiller = func() doneSessionKiller { return fake }
	t.Cleanup(func() { newDoneSessionKiller = old })

	for _, tt := range []struct {
		name        string
		rigName     string
		polecatName string
		pid         int
	}{
		{"missing rig", "", "nitro", 12345},
		{"missing polecat", "gastown", "", 12345},
		{"missing pid", "gastown", "nitro", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := retirePolecatSessionAfterDone(tt.rigName, tt.polecatName, tt.pid); err != nil {
				t.Fatalf("retirePolecatSessionAfterDone: %v", err)
			}
		})
	}
	if fake.calls != 0 {
		t.Fatalf("killer calls = %d, want 0", fake.calls)
	}
}

func TestCleanupStatusAfterSuccessfulPush(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"unpushed", "clean"},
		{"has_unpushed", "clean"},
		{"clean", "clean"},
		{"uncommitted", "uncommitted"},
		{"stash", "stash"},
		{"unknown", "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			if got := cleanupStatusAfterSuccessfulPush(tt.status); got != tt.want {
				t.Errorf("cleanupStatusAfterSuccessfulPush(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestCleanupStatusFromWorkState(t *testing.T) {
	pushErr := errors.New("remote unavailable")
	tests := []struct {
		name          string
		status        *gitpkg.UncommittedWorkStatus
		branchPushed  bool
		unpushedCount int
		pushErr       error
		want          string
	}{
		{name: "nil", status: nil, branchPushed: true, want: "unknown"},
		{
			name:         "runtime only pushed",
			status:       &gitpkg.UncommittedWorkStatus{HasUncommittedChanges: true, ModifiedFiles: []string{".opencode/plugins/gastown.js"}},
			branchPushed: true,
			want:         "clean",
		},
		{
			name:         "runtime plus source",
			status:       &gitpkg.UncommittedWorkStatus{HasUncommittedChanges: true, ModifiedFiles: []string{".opencode/plugins/gastown.js", "internal/cmd/done.go"}},
			branchPushed: true,
			want:         "uncommitted",
		},
		{
			name:         "runtime plus stash",
			status:       &gitpkg.UncommittedWorkStatus{HasUncommittedChanges: true, ModifiedFiles: []string{".opencode/plugins/gastown.js"}, StashCount: 1},
			branchPushed: true,
			want:         "stash",
		},
		{
			name:          "runtime plus unpushed",
			status:        &gitpkg.UncommittedWorkStatus{HasUncommittedChanges: true, ModifiedFiles: []string{".opencode/plugins/gastown.js"}},
			branchPushed:  true,
			unpushedCount: 1,
			want:          "unpushed",
		},
		{
			name:         "runtime plus push error",
			status:       &gitpkg.UncommittedWorkStatus{HasUncommittedChanges: true, ModifiedFiles: []string{".opencode/plugins/gastown.js"}},
			branchPushed: true,
			pushErr:      pushErr,
			want:         "unpushed",
		},
		{
			name:         "runtime conflict",
			status:       &gitpkg.UncommittedWorkStatus{HasUncommittedChanges: true, UnmergedFiles: []string{".opencode/plugins/gastown.js"}},
			branchPushed: true,
			want:         "uncommitted",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := cleanupStatusFromWorkState(tt.status, tt.branchPushed, tt.unpushedCount, tt.pushErr); got != tt.want {
				t.Fatalf("cleanupStatusFromWorkState() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDoneIntentLabelSelection selects the labels that clearDoneIntentLabel removes.
func TestDoneIntentLabelSelection(t *testing.T) {
	allLabels := []string{
		"gt:agent",
		"idle:3",
		"done-intent:COMPLETED:1738972800",
		"backoff-until:1738972900",
	}

	removed := labelsWithPrefix(allLabels, "done-intent:")
	if len(removed) != 1 || removed[0] != "done-intent:COMPLETED:1738972800" {
		t.Errorf("done-intent labels = %v, want completed intent only", removed)
	}
}

// TestConvoyMergeFromFields verifies that convoyMergeFromFields correctly
// extracts the merge strategy from convoy descriptions using typed ConvoyFields.
func TestConvoyMergeFromFields(t *testing.T) {
	tests := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "direct strategy",
			description: "Auto-created convoy tracking gt-abc\nMerge: direct",
			want:        "direct",
		},
		{
			name:        "mr strategy",
			description: "Convoy tracking 3 issues\nOwner: mayor/\nMerge: mr",
			want:        "mr",
		},
		{
			name:        "local strategy",
			description: "Merge: local\nOwner: mayor/",
			want:        "local",
		},
		{
			name:        "no merge field",
			description: "Auto-created convoy tracking gt-abc",
			want:        "",
		},
		{
			name:        "empty description",
			description: "",
			want:        "",
		},
		{
			name:        "merge in middle of description",
			description: "Convoy tracking 1 issues\nMerge: direct\nNotify: mayor/",
			want:        "direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convoyMergeFromFields(tt.description)
			if got != tt.want {
				t.Errorf("convoyMergeFromFields() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDoneCheckpointLabelFormat verifies the done-cp label format matches
// the expected pattern: done-cp:<stage>:<value>:<unix-ts>
func TestDoneCheckpointLabelFormat(t *testing.T) {
	now := time.Unix(1738972800, 0)
	tests := []struct {
		checkpoint DoneCheckpoint
		value      string
		want       string
	}{
		{CheckpointPushed, "polecat/furiosa-abc", "done-cp:pushed:polecat/furiosa-abc:1738972800"},
		{CheckpointMRCreated, "gt-xyz123", "done-cp:mr-created:gt-xyz123:1738972800"},
		{CheckpointWitnessNotified, "ok", "done-cp:witness-notified:ok:1738972800"},
	}

	for _, tt := range tests {
		t.Run(string(tt.checkpoint), func(t *testing.T) {
			label := doneCheckpointLabel(tt.checkpoint, tt.value, now)
			if label != tt.want {
				t.Errorf("label = %q, want %q", label, tt.want)
			}
		})
	}
}

// TestReadDoneCheckpoints verifies that readDoneCheckpoints correctly
// parses checkpoint labels from an issue's label list.
func TestReadDoneCheckpoints(t *testing.T) {
	// Test the parsing logic directly by simulating what readDoneCheckpoints does
	tests := []struct {
		name   string
		labels []string
		want   map[DoneCheckpoint]string
	}{
		{
			name:   "no checkpoints",
			labels: []string{"gt:agent", "idle:3"},
			want:   map[DoneCheckpoint]string{},
		},
		{
			name:   "push checkpoint only",
			labels: []string{"gt:agent", "done-cp:pushed:polecat/furiosa-abc:1738972800"},
			want:   map[DoneCheckpoint]string{CheckpointPushed: "polecat/furiosa-abc"},
		},
		{
			name: "multiple checkpoints",
			labels: []string{
				"gt:agent",
				"done-cp:pushed:polecat/furiosa-abc:1738972800",
				"done-cp:mr-created:gt-xyz123:1738972801",
			},
			want: map[DoneCheckpoint]string{
				CheckpointPushed:    "polecat/furiosa-abc",
				CheckpointMRCreated: "gt-xyz123",
			},
		},
		{
			name: "all checkpoints",
			labels: []string{
				"done-cp:pushed:branch-name:1738972800",
				"done-cp:mr-created:gt-mr1:1738972801",
				"done-cp:witness-notified:ok:1738972803",
			},
			want: map[DoneCheckpoint]string{
				CheckpointPushed:          "branch-name",
				CheckpointMRCreated:       "gt-mr1",
				CheckpointWitnessNotified: "ok",
			},
		},
		{
			name: "mixed with done-intent and other labels",
			labels: []string{
				"gt:agent",
				"done-intent:COMPLETED:1738972800",
				"done-cp:pushed:mybranch:1738972801",
				"idle:2",
			},
			want: map[DoneCheckpoint]string{CheckpointPushed: "mybranch"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the parsing logic from readDoneCheckpoints
			checkpoints := parseDoneCheckpointLabels(tt.labels)

			if len(checkpoints) != len(tt.want) {
				t.Errorf("got %d checkpoints, want %d", len(checkpoints), len(tt.want))
			}
			for k, v := range tt.want {
				if checkpoints[k] != v {
					t.Errorf("checkpoint[%s] = %q, want %q", k, checkpoints[k], v)
				}
			}
		})
	}
}

// TestDoneCheckpointLabelSelection selects the labels that clearDoneCheckpoints removes.
func TestDoneCheckpointLabelSelection(t *testing.T) {
	allLabels := []string{
		"gt:agent",
		"idle:3",
		"done-intent:COMPLETED:1738972800",
		"done-cp:pushed:mybranch:1738972801",
		"done-cp:mr-created:gt-xyz:1738972802",
		"backoff-until:1738972900",
	}

	removed := labelsWithPrefix(allLabels, "done-cp:")
	want := []string{
		"done-cp:pushed:mybranch:1738972801",
		"done-cp:mr-created:gt-xyz:1738972802",
	}
	if len(removed) != len(want) || removed[0] != want[0] || removed[1] != want[1] {
		t.Errorf("checkpoint labels = %v, want %v", removed, want)
	}
}

// TestCheckpointResumeSkipsPush verifies that when a push checkpoint exists,
// the push section is skipped on resume.
func TestCheckpointResumeSkipsPush(t *testing.T) {
	tests := []struct {
		name        string
		checkpoints map[DoneCheckpoint]string
		wantSkip    bool
	}{
		{
			name:        "no checkpoints - push runs normally",
			checkpoints: map[DoneCheckpoint]string{},
			wantSkip:    false,
		},
		{
			name:        "push checkpoint exists - skip push",
			checkpoints: map[DoneCheckpoint]string{CheckpointPushed: "mybranch"},
			wantSkip:    true,
		},
		{
			name: "push and MR checkpoints - skip push",
			checkpoints: map[DoneCheckpoint]string{
				CheckpointPushed:    "mybranch",
				CheckpointMRCreated: "gt-xyz",
			},
			wantSkip: true,
		},
		{
			name:        "stale push checkpoint - push runs normally",
			checkpoints: map[DoneCheckpoint]string{CheckpointPushed: "old-branch"},
			wantSkip:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The real resume guard only skips the push for the current branch.
			skipPush := pushCheckpointMatchesBranch(tt.checkpoints, "mybranch")
			if skipPush != tt.wantSkip {
				t.Errorf("skipPush = %v, want %v", skipPush, tt.wantSkip)
			}
		})
	}
}

// TestHookedBeadCloseNotRestrictedToHookedStatus verifies the gt-pftz fix:
// gt done must close the hooked bead regardless of its current status (hooked,
// in_progress, open), not only when status == "hooked". Polecats update their
// work bead to in_progress during work, so the old exact-match check skipped
// closing and caused infinite dispatch loops.
func TestHookedBeadCloseNotRestrictedToHookedStatus(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		wantClose bool
	}{
		{"status hooked → close", "hooked", true},
		{"status in_progress → close", "in_progress", true},
		{"status open → close", "open", true},
		{"status blocked → close", "blocked", true},
		{"status closed → skip (terminal)", "closed", false},
		{"status tombstone → skip (terminal)", "tombstone", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shouldClose := isClosableHookedBead(tt.status)
			if shouldClose != tt.wantClose {
				t.Errorf("shouldClose for status %q = %v, want %v", tt.status, shouldClose, tt.wantClose)
			}
		})
	}
}

// TestPushSubmoduleChanges_Integration verifies that pushSubmoduleChanges detects
// modified submodules and pushes their commits before the parent repo push (gt-dzs).
func TestPushSubmoduleChanges_Integration(t *testing.T) {
	tmp := t.TempDir()

	// Allow file:// transport for submodule operations
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "protocol.file.allow")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	// Create a "remote" bare repo for the submodule
	subRemote := filepath.Join(tmp, "sub-remote.git")
	testRunGit(t, tmp, "init", "--bare", "--initial-branch", "main", subRemote)

	// Create a working clone of the submodule to add initial content
	subWork := filepath.Join(tmp, "sub-work")
	testRunGit(t, tmp, "clone", subRemote, subWork)
	testRunGit(t, subWork, "config", "user.email", "test@test.com")
	testRunGit(t, subWork, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(subWork, "lib.go"), []byte("package lib\n"), 0644); err != nil {
		t.Fatalf("write sub file: %v", err)
	}
	testRunGit(t, subWork, "add", ".")
	testRunGit(t, subWork, "commit", "-m", "initial sub commit")
	testRunGit(t, subWork, "push", "origin", "main")

	// Create a "remote" bare repo for the parent
	parentRemote := filepath.Join(tmp, "parent-remote.git")
	testRunGit(t, tmp, "init", "--bare", "--initial-branch", "main", parentRemote)

	// Create the parent repo
	parent := filepath.Join(tmp, "parent")
	testRunGit(t, tmp, "init", "--initial-branch", "main", parent)
	testRunGit(t, parent, "config", "user.email", "test@test.com")
	testRunGit(t, parent, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("# Parent\n"), 0644); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	testRunGit(t, parent, "add", ".")
	testRunGit(t, parent, "commit", "-m", "initial parent commit")

	// Add the submodule
	testRunGit(t, parent, "submodule", "add", subRemote, "libs/sub")
	testRunGit(t, parent, "commit", "-m", "add submodule")

	// Add remote and push to parent remote
	testRunGit(t, parent, "remote", "add", "origin", parentRemote)
	testRunGit(t, parent, "push", "origin", "main")

	// Make a new commit in the submodule (but don't push it to submodule remote)
	subPath := filepath.Join(parent, "libs", "sub")
	if err := os.WriteFile(filepath.Join(subPath, "new.go"), []byte("package lib\n// new\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	testRunGit(t, subPath, "add", ".")
	testRunGit(t, subPath, "commit", "-m", "unpushed submodule commit")

	// Get the new submodule SHA
	cmd := exec.Command("git", "-C", subPath, "rev-parse", "HEAD")
	shaBytes, err := cmd.Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	newSHA := strings.TrimSpace(string(shaBytes))

	// Update parent to point to new submodule commit
	testRunGit(t, parent, "add", "libs/sub")
	testRunGit(t, parent, "commit", "-m", "update submodule pointer")

	// Verify the new submodule commit is NOT on the submodule remote yet
	lsCmd := exec.Command("git", "ls-remote", subRemote, "refs/heads/main")
	lsOut, _ := lsCmd.Output()
	remoteSHA := strings.Fields(string(lsOut))[0]
	if remoteSHA == newSHA {
		t.Fatal("new submodule commit should not be on remote yet")
	}

	// Call pushSubmoduleChanges — this should push the submodule commit
	g := gitpkg.NewGit(parent)
	pushSubmoduleChanges(g, "origin/main")

	// Verify the submodule commit IS now on the remote
	lsCmd = exec.Command("git", "ls-remote", subRemote, "refs/heads/main")
	lsOut, _ = lsCmd.Output()
	remoteSHA = strings.Fields(string(lsOut))[0]
	if remoteSHA != newSHA {
		t.Errorf("expected submodule remote main to be %s, got %s", newSHA, remoteSHA)
	}
}

// TestPushSubmoduleChanges_NoSubmodules verifies pushSubmoduleChanges is a no-op
// for repos without submodules (gt-dzs).
func TestPushSubmoduleChanges_NoSubmodules(t *testing.T) {
	tmp := t.TempDir()

	// Create a simple repo with a remote
	parent := filepath.Join(tmp, "repo")
	remote := filepath.Join(tmp, "remote.git")
	testRunGit(t, tmp, "init", "--bare", "--initial-branch", "main", remote)
	testRunGit(t, tmp, "init", "--initial-branch", "main", parent)
	testRunGit(t, parent, "config", "user.email", "test@test.com")
	testRunGit(t, parent, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(parent, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	testRunGit(t, parent, "add", ".")
	testRunGit(t, parent, "commit", "-m", "initial commit")
	testRunGit(t, parent, "remote", "add", "origin", remote)
	testRunGit(t, parent, "push", "origin", "main")

	// Add another commit
	if err := os.WriteFile(filepath.Join(parent, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	testRunGit(t, parent, "add", ".")
	testRunGit(t, parent, "commit", "-m", "add main.go")

	// Should not panic or error — just a no-op
	g := gitpkg.NewGit(parent)
	pushSubmoduleChanges(g, "origin/main")
}

// TestAutoCommitSafetyNet verifies that the gt done auto-commit safety net
// (gt-pvx) correctly detects uncommitted implementation work and auto-commits it.
// This tests the git-level operations that underpin the safety net in done.go.
func TestAutoCommitSafetyNet(t *testing.T) {
	// Set up a git repo with uncommitted changes
	dir := t.TempDir()
	testRunGit(t, dir, "init")
	testRunGit(t, dir, "config", "user.email", "test@test.com")
	testRunGit(t, dir, "config", "user.name", "Test")

	// Create initial commit
	initialFile := filepath.Join(dir, "README.md")
	if err := os.WriteFile(initialFile, []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", "README.md")
	testRunGit(t, dir, "commit", "-m", "initial commit")

	g := gitpkg.NewGit(dir)

	t.Run("detects uncommitted new files", func(t *testing.T) {
		// Create uncommitted implementation files (simulates polecat forgetting to commit)
		implFile := filepath.Join(dir, "main.go")
		if err := os.WriteFile(implFile, []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(implFile)

		ws, err := g.CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if !ws.HasUncommittedChanges {
			t.Error("expected HasUncommittedChanges=true for new file")
		}
		if ws.CleanExcludingRuntime() {
			t.Error("expected CleanExcludingRuntime=false for non-runtime file")
		}
	})

	t.Run("auto-commit preserves work", func(t *testing.T) {
		// Create implementation files
		implFile := filepath.Join(dir, "handler.go")
		if err := os.WriteFile(implFile, []byte("package main\n\nfunc handler() {}\n"), 0644); err != nil {
			t.Fatal(err)
		}

		// Verify uncommitted
		ws, err := g.CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if !ws.HasUncommittedChanges || ws.CleanExcludingRuntime() {
			t.Fatal("expected non-runtime uncommitted changes")
		}

		// Simulate the auto-commit safety net
		if err := g.Add("-A"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		if err := g.Commit("fix: auto-save uncommitted implementation work (gt-pvx safety net)"); err != nil {
			t.Fatalf("git commit: %v", err)
		}

		// Verify clean after auto-commit
		ws2, err := g.CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork after commit: %v", err)
		}
		if ws2.HasUncommittedChanges {
			t.Error("expected clean working tree after auto-commit")
		}
	})

	t.Run("runtime-only changes skip auto-commit", func(t *testing.T) {
		// Runtime artifacts should NOT trigger auto-commit
		runtimeDir := filepath.Join(dir, ".claude")
		if err := os.MkdirAll(runtimeDir, 0755); err != nil {
			t.Fatal(err)
		}
		runtimeFile := filepath.Join(runtimeDir, "settings.json")
		if err := os.WriteFile(runtimeFile, []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(runtimeDir)

		ws, err := g.CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		// HasUncommittedChanges is true (git sees the files), but CleanExcludingRuntime
		// should be true (only runtime artifacts)
		if ws.HasUncommittedChanges && !ws.CleanExcludingRuntime() {
			t.Error("runtime-only changes should be considered clean excluding runtime")
		}
	})

	t.Run("auto-commit excludes runtime artifacts recursively", func(t *testing.T) {
		repo := t.TempDir()
		testRunGit(t, repo, "init")
		testRunGit(t, repo, "config", "user.email", "test@test.com")
		testRunGit(t, repo, "config", "user.name", "Test")
		if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Test\n"), 0644); err != nil {
			t.Fatal(err)
		}
		testRunGit(t, repo, "add", "README.md")
		testRunGit(t, repo, "commit", "-m", "initial commit")

		writeFile := func(path, content string) {
			t.Helper()
			fullPath := filepath.Join(repo, path)
			if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
		}

		writeFile("src/handler.go", "package main\n\nfunc handler() {}\n")
		writeFile(".opencode/plugins/gastown.js", "// generated\n")
		writeFile("services/cyrus/workflow-cyrus-edge/node_modules/pkg/index.js", "module.exports = {}\n")
		writeFile("dashboard/public/meridian-dashboard/.vite/vitest/hash/results.json", "{}\n")
		writeFile("services/workflows/collateral-internal/execution_log.db", "sqlite\n")
		writeFile("api/.pytest_cache/v/cache/nodeids", "[]\n")
		writeFile("src/__pycache__/handler.cpython-312.pyc", "pyc\n")
		writeFile(".beads/.runtime/state.json", "{}\n")

		g := gitpkg.NewGit(repo)
		ws, err := g.CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork: %v", err)
		}
		if !ws.HasUncommittedChanges || ws.CleanExcludingRuntime() {
			t.Fatal("expected mixed source and runtime changes")
		}

		if err := g.Add("-A"); err != nil {
			t.Fatalf("git add: %v", err)
		}
		if runtimePaths := ws.RuntimeArtifactPaths(); len(runtimePaths) > 0 {
			if err := g.ResetFiles(runtimePaths...); err != nil {
				t.Fatalf("reset runtime artifacts: %v", err)
			}
		}
		if err := g.Commit("fix: auto-save uncommitted implementation work (gt-pvx safety net)"); err != nil {
			t.Fatalf("git commit: %v", err)
		}

		changed, err := g.DiffNameOnly("HEAD~1", "HEAD")
		if err != nil {
			t.Fatalf("DiffNameOnly: %v", err)
		}
		if len(changed) != 1 || changed[0] != "src/handler.go" {
			t.Fatalf("auto-save committed %v, want only src/handler.go", changed)
		}

		wsAfter, err := g.CheckUncommittedWork()
		if err != nil {
			t.Fatalf("CheckUncommittedWork after commit: %v", err)
		}
		if !wsAfter.HasUncommittedChanges || !wsAfter.CleanExcludingRuntime() {
			t.Fatalf("runtime artifacts should remain uncommitted and clean-excluded, got %#v", wsAfter)
		}
	})
}

// TestSyncGuardWithUncommittedChanges verifies that the worktree sync guard
// (gt-pvx) prevents switching branches when uncommitted changes remain.
func TestSyncGuardWithUncommittedChanges(t *testing.T) {
	// This tests the logic: if auto-commit fails, we should NOT sync to main
	dir := t.TempDir()
	testRunGit(t, dir, "init")
	testRunGit(t, dir, "config", "user.email", "test@test.com")
	testRunGit(t, dir, "config", "user.name", "Test")

	// Create initial commit on main
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	testRunGit(t, dir, "add", ".")
	testRunGit(t, dir, "commit", "-m", "initial")

	// Create feature branch with uncommitted changes
	testRunGit(t, dir, "checkout", "-b", "polecat/test")
	if err := os.WriteFile(filepath.Join(dir, "impl.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	g := gitpkg.NewGit(dir)
	ws, err := g.CheckUncommittedWork()
	if err != nil {
		t.Fatalf("CheckUncommittedWork: %v", err)
	}

	// The sync guard condition: if uncommitted non-runtime changes exist, syncSafe = false
	syncSafe := true
	if ws.HasUncommittedChanges && !ws.CleanExcludingRuntime() {
		syncSafe = false
	}

	if syncSafe {
		t.Error("syncSafe should be false when uncommitted implementation files exist")
	}
}

func testRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	fullArgs := append([]string{"-c", "protocol.file.allow=always"}, args...)
	cmd := exec.Command("git", fullArgs...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
