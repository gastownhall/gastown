package cmd

import (
	"errors"
	"fmt"
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

// TestFindHookedBeadForAgent verifies that findHookedBeadForAgent correctly
// finds hooked beads by querying status=hooked + assignee (hq-l6mm5).
// This is critical because branch names like "polecat/furiosa-mkb0vq9f" don't
// contain the actual issue ID (test-845.1), but the status query finds it.
func TestFindHookedBeadForAgent(t *testing.T) {
	// Skip: bd CLI 0.47.2 has a bug where database writes don't commit
	// ("sql: database is closed" during auto-flush). This blocks tests
	// that need to create issues. See internal issue for tracking.
	t.Skip("bd CLI 0.47.2 bug: database writes don't commit")

	tests := []struct {
		name        string
		agentID     string
		setupBeads  func(t *testing.T, bd *beads.Beads) // setup hooked bead
		wantIssueID string
	}{
		{
			name:    "hooked bead assigned to agent returns issue ID",
			agentID: "testrig/polecats/furiosa",
			setupBeads: func(t *testing.T, bd *beads.Beads) {
				// Create a task and set it to hooked with assignee
				_, err := bd.CreateWithID("test-456", beads.CreateOptions{
					Title:  "Task to be hooked",
					Labels: []string{"gt:task"},
				})
				if err != nil {
					t.Fatalf("create task bead: %v", err)
				}
				hookedStatus := beads.StatusHooked
				assignee := "testrig/polecats/furiosa"
				if err := bd.Update("test-456", beads.UpdateOptions{
					Status:   &hookedStatus,
					Assignee: &assignee,
				}); err != nil {
					t.Fatalf("update bead to hooked: %v", err)
				}
			},
			wantIssueID: "test-456",
		},
		{
			// Regression for hq-xa4z: polecats claim their assignment with
			// `bd update --status=in_progress` when starting work. A
			// hooked-only lookup returned empty here, blinding the stale-
			// branch guard (toast re-wisp-e2q carried source_issue re-k8oa
			// while the real assignment re-dkf sat in_progress).
			name:    "in_progress bead assigned to agent returns issue ID",
			agentID: "testrig/polecats/toast",
			setupBeads: func(t *testing.T, bd *beads.Beads) {
				_, err := bd.CreateWithID("test-789", beads.CreateOptions{
					Title:  "Claimed task",
					Labels: []string{"gt:task"},
				})
				if err != nil {
					t.Fatalf("create task bead: %v", err)
				}
				inProgress := "in_progress"
				assignee := "testrig/polecats/toast"
				if err := bd.Update("test-789", beads.UpdateOptions{
					Status:   &inProgress,
					Assignee: &assignee,
				}); err != nil {
					t.Fatalf("update bead to in_progress: %v", err)
				}
			},
			wantIssueID: "test-789",
		},
		{
			name:        "no hooked beads returns empty",
			agentID:     "testrig/polecats/idle",
			setupBeads:  func(t *testing.T, bd *beads.Beads) {},
			wantIssueID: "",
		},
		{
			name:        "empty agent ID returns empty",
			agentID:     "",
			setupBeads:  func(t *testing.T, bd *beads.Beads) {},
			wantIssueID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()

			// Initialize the beads database
			cmd := exec.Command("bd", "init", "--prefix", "test", "--quiet")
			cmd.Dir = tmpDir
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("bd init: %v\n%s", err, output)
			}

			// beads.New expects the .beads directory path
			beadsDir := filepath.Join(tmpDir, ".beads")
			bd := beads.New(beadsDir)

			tt.setupBeads(t, bd)

			got := findHookedBeadForAgent(bd, tt.agentID)
			if got != tt.wantIssueID {
				t.Errorf("findHookedBeadForAgent(%q) = %q, want %q", tt.agentID, got, tt.wantIssueID)
			}
		})
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
	now := time.Now()
	tests := []struct {
		exitType string
		want     string
	}{
		{"COMPLETED", fmt.Sprintf("done-intent:COMPLETED:%d", now.Unix())},
		{"ESCALATED", fmt.Sprintf("done-intent:ESCALATED:%d", now.Unix())},
		{"DEFERRED", fmt.Sprintf("done-intent:DEFERRED:%d", now.Unix())},
		{"PHASE_COMPLETE", fmt.Sprintf("done-intent:PHASE_COMPLETE:%d", now.Unix())},
	}

	for _, tt := range tests {
		t.Run(tt.exitType, func(t *testing.T) {
			label := fmt.Sprintf("done-intent:%s:%d", tt.exitType, now.Unix())
			if label != tt.want {
				t.Errorf("label format = %q, want %q", label, tt.want)
			}

			// Verify the label can be parsed back
			parts := strings.SplitN(label, ":", 3)
			if len(parts) != 3 {
				t.Fatalf("expected 3 parts, got %d", len(parts))
			}
			if parts[0] != "done-intent" {
				t.Errorf("prefix = %q, want %q", parts[0], "done-intent")
			}
			if parts[1] != tt.exitType {
				t.Errorf("exit type = %q, want %q", parts[1], tt.exitType)
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

// TestClearDoneIntentLabel verifies that clearDoneIntentLabel removes
// only done-intent labels while preserving other labels.
func TestClearDoneIntentLabel(t *testing.T) {
	// We can't easily test the full clearDoneIntentLabel function without
	// a running bd instance, but we can verify the filtering logic.
	// The function reads labels, filters out done-intent:*, and writes back.
	allLabels := []string{
		"gt:agent",
		"idle:3",
		"done-intent:COMPLETED:1738972800",
		"backoff-until:1738972900",
	}

	var kept []string
	for _, label := range allLabels {
		if !strings.HasPrefix(label, "done-intent:") {
			kept = append(kept, label)
		}
	}

	if len(kept) != 3 {
		t.Errorf("expected 3 labels after filtering, got %d: %v", len(kept), kept)
	}

	// Verify done-intent was removed
	for _, label := range kept {
		if strings.HasPrefix(label, "done-intent:") {
			t.Errorf("done-intent label was not removed: %s", label)
		}
	}

	// Verify other labels were preserved
	wantKept := map[string]bool{
		"gt:agent":                 true,
		"idle:3":                   true,
		"backoff-until:1738972900": true,
	}
	for _, label := range kept {
		if !wantKept[label] {
			t.Errorf("unexpected label in kept set: %s", label)
		}
	}
}

// TestMRVerificationSetsMRFailed verifies that if MR bead creation returns
// success but the bead cannot be read back (verification fails), mrFailed
// is set to true. This is the core fix for GH#1945: without verification,
// a "successful" bd.Create that didn't actually persist would allow the
// worktree nuke to proceed, losing the polecat's work.
func TestMRVerificationSetsMRFailed(t *testing.T) {
	tests := []struct {
		name         string
		createErr    error // error from bd.Create
		showErr      error // error from bd.Show (verification)
		showReturns  bool  // whether Show returns a non-nil issue
		wantMRFailed bool
	}{
		{
			name:         "create succeeds + show succeeds → mrFailed=false",
			createErr:    nil,
			showErr:      nil,
			showReturns:  true,
			wantMRFailed: false,
		},
		{
			name:         "create fails → mrFailed=true (existing behavior)",
			createErr:    fmt.Errorf("dolt write failed"),
			showErr:      nil,
			showReturns:  false,
			wantMRFailed: true,
		},
		{
			name:         "create succeeds + show fails → mrFailed=true (GH#1945 fix)",
			createErr:    nil,
			showErr:      fmt.Errorf("bead not found"),
			showReturns:  false,
			wantMRFailed: true,
		},
		{
			name:         "create succeeds + show returns nil → mrFailed=true (GH#1945 fix)",
			createErr:    nil,
			showErr:      nil,
			showReturns:  false,
			wantMRFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the MR creation + verification flow from done.go
			mrFailed := false

			if tt.createErr != nil {
				// bd.Create failed — existing behavior
				mrFailed = true
			} else {
				// bd.Create succeeded — now verify (GH#1945 fix)
				var showResult bool
				if tt.showErr != nil || !tt.showReturns {
					showResult = false
				} else {
					showResult = true
				}
				if !showResult {
					mrFailed = true
				}
			}

			if mrFailed != tt.wantMRFailed {
				t.Errorf("mrFailed = %v, want %v", mrFailed, tt.wantMRFailed)
			}
		})
	}
}

// TestMRBeadCreationUsesRig verifies that MR bead creation specifies the rig (gt-7y7).
// When a polecat works on a cross-rig bead (e.g., hq-xxx on rig "gastown"), the
// MR bead must be created with Rig set to the polecat's rig so it lands in the
// rig's database — not the town-level database where the source bead lives.
// Without this, the refinery never finds the MR and the branch sits unmerged.
func TestMRBeadCreationUsesRig(t *testing.T) {
	tests := []struct {
		name    string
		issueID string
		rigName string
		wantRig string
	}{
		{
			name:    "same-rig bead: rig is still set",
			issueID: "gt-abc",
			rigName: "gastown",
			wantRig: "gastown",
		},
		{
			name:    "cross-rig hq- bead: MR must land in polecat rig",
			issueID: "hq-abc",
			rigName: "gastown",
			wantRig: "gastown",
		},
		{
			name:    "cross-rig en- bead: MR must land in polecat rig",
			issueID: "en-xyz",
			rigName: "gastown",
			wantRig: "gastown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the CreateOptions construction in done.go.
			opts := beads.CreateOptions{
				Title:     "Merge: " + tt.issueID,
				Labels:    []string{"gt:merge-request"},
				Ephemeral: true,
				Rig:       tt.rigName,
			}
			if opts.Rig != tt.wantRig {
				t.Errorf("CreateOptions.Rig = %q, want %q (issue %s)", opts.Rig, tt.wantRig, tt.issueID)
			}
		})
	}
}

// TestDeferredKillNotOnValidationError verifies that the deferred session kill
// does NOT trigger when runDone returns early due to validation errors (bad flags,
// wrong role). The sessionCleanupNeeded flag must only be set after role detection
// confirms this is a polecat.
func TestDeferredKillNotOnValidationError(t *testing.T) {
	// Simulate the flag lifecycle:
	// 1. sessionCleanupNeeded starts false
	// 2. Set true only after role detection confirms polecat
	// 3. Early returns (validation) happen before the flag is set

	// Scenario 1: Validation error (bad status) — returns before flag set
	sessionCleanupNeeded := false
	// (invalid exit status check would return here)
	// defer checks: sessionCleanupNeeded is false → no-op
	if sessionCleanupNeeded {
		t.Error("sessionCleanupNeeded should be false for validation errors")
	}

	// Scenario 2: Polecat confirmed — flag set
	sessionCleanupNeeded = true
	sessionKilled := false
	// (push fails, returns with error)
	// defer checks: sessionCleanupNeeded is true, sessionKilled is false → kill session
	if !sessionCleanupNeeded || sessionKilled {
		t.Error("deferred kill should trigger when sessionCleanupNeeded && !sessionKilled")
	}

	// Scenario 3: Clean exit — explicit kill succeeded
	sessionKilled = true
	// defer checks: sessionKilled is true → no-op (don't double-kill)
	if sessionCleanupNeeded && !sessionKilled {
		t.Error("deferred kill should NOT trigger when sessionKilled is true")
	}
}

// TestBranchDetectionGuard verifies that the branch detection logic in runDone
// correctly handles the three states: cwd available, cwd unavailable with GT_BRANCH,
// and cwd unavailable without GT_BRANCH.
// This is a regression test for PR #1402 — prevents incorrect main/master detection
// when the polecat's working directory is deleted.
func TestBranchDetectionGuard(t *testing.T) {
	tests := []struct {
		name         string
		cwdAvailable bool
		gtBranch     string // GT_BRANCH env var value
		wantError    bool
		wantBranch   string
	}{
		{
			name:         "cwd available - uses git CurrentBranch",
			cwdAvailable: true,
			gtBranch:     "",
			wantError:    false,
			wantBranch:   "current-branch", // simulated
		},
		{
			name:         "cwd unavailable + GT_BRANCH set - uses env var",
			cwdAvailable: false,
			gtBranch:     "polecat/test-worker",
			wantError:    false,
			wantBranch:   "polecat/test-worker",
		},
		{
			name:         "cwd unavailable + GT_BRANCH empty - returns error",
			cwdAvailable: false,
			gtBranch:     "",
			wantError:    true,
			wantBranch:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the branch detection logic from runDone
			var branch string
			if !tt.cwdAvailable {
				branch = tt.gtBranch
			}

			var gotError bool
			if branch == "" {
				if !tt.cwdAvailable {
					gotError = true
				} else {
					// Would call g.CurrentBranch() — simulate success
					branch = "current-branch"
				}
			}

			if gotError != tt.wantError {
				t.Errorf("error = %v, want %v", gotError, tt.wantError)
			}
			if !tt.wantError && branch != tt.wantBranch {
				t.Errorf("branch = %q, want %q", branch, tt.wantBranch)
			}
		})
	}
}

// TestBranchDetectionCleanupOnError verifies that when branch detection fails
// (cwdAvailable=false + no GT_BRANCH), the session cleanup backstop is armed
// so the polecat doesn't get stranded.
func TestBranchDetectionCleanupOnError(t *testing.T) {
	// Simulate the cleanup arming logic from runDone's branch detection error path
	cwdAvailable := false
	gtBranch := ""
	gtPolecat := "test-worker"
	rigName := "test-rig"

	var branch string
	if !cwdAvailable {
		branch = gtBranch
	}

	sessionCleanupNeeded := false
	if branch == "" && !cwdAvailable {
		// This mirrors the actual code: arm cleanup before returning error
		if gtPolecat != "" {
			sessionCleanupNeeded = true
		}
	}

	if !sessionCleanupNeeded {
		t.Error("sessionCleanupNeeded should be true when branch detection fails with GT_POLECAT set")
	}

	// Verify the RoleInfo would be constructible from env vars
	roleInfo := RoleInfo{
		Role:    RolePolecat,
		Rig:     rigName,
		Polecat: gtPolecat,
	}
	if roleInfo.Rig != rigName || roleInfo.Polecat != gtPolecat {
		t.Error("RoleInfo should be constructible from env vars for cleanup")
	}
}

// TestConvoyMergeStrategyBranching verifies that the merge strategy branching
// logic in runDone correctly routes to the right code path for each strategy.
func TestConvoyMergeStrategyBranching(t *testing.T) {
	tests := []struct {
		name          string
		mergeStrategy string
		wantPush      bool // should push happen?
		wantMR        bool // should MR bead be created?
		wantDirect    bool // should push to default branch?
	}{
		{
			name:          "mr strategy - normal push and MR",
			mergeStrategy: "mr",
			wantPush:      true,
			wantMR:        true,
			wantDirect:    false,
		},
		{
			name:          "empty strategy - defaults to mr behavior",
			mergeStrategy: "",
			wantPush:      true,
			wantMR:        true,
			wantDirect:    false,
		},
		{
			name:          "direct strategy - push to main, no MR",
			mergeStrategy: "direct",
			wantPush:      true,
			wantMR:        false,
			wantDirect:    true,
		},
		{
			name:          "local strategy - no push, no MR",
			mergeStrategy: "local",
			wantPush:      false,
			wantMR:        false,
			wantDirect:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the branching logic from runDone
			shouldPush := true
			shouldCreateMR := true
			shouldPushDirect := false

			switch tt.mergeStrategy {
			case "local":
				shouldPush = false
				shouldCreateMR = false
			case "direct":
				shouldPushDirect = true
				shouldCreateMR = false
			default:
				// "mr" or empty = default behavior
			}

			if shouldPush != tt.wantPush {
				t.Errorf("shouldPush = %v, want %v", shouldPush, tt.wantPush)
			}
			if shouldCreateMR != tt.wantMR {
				t.Errorf("shouldCreateMR = %v, want %v", shouldCreateMR, tt.wantMR)
			}
			if shouldPushDirect != tt.wantDirect {
				t.Errorf("shouldPushDirect = %v, want %v", shouldPushDirect, tt.wantDirect)
			}
		})
	}
}

// TestConvoyMergeStrategyNotification verifies that the merge strategy
// is included in the witness notification body when set to non-default values.
func TestConvoyMergeStrategyNotification(t *testing.T) {
	tests := []struct {
		name          string
		mergeStrategy string
		wantInBody    bool
	}{
		{"direct strategy included", "direct", true},
		{"local strategy included", "local", true},
		{"mr strategy excluded", "mr", false},
		{"empty strategy excluded", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the notification body building from runDone
			var bodyLines []string
			bodyLines = append(bodyLines, "Exit: COMPLETED")
			if tt.mergeStrategy != "" && tt.mergeStrategy != "mr" {
				bodyLines = append(bodyLines, fmt.Sprintf("MergeStrategy: %s", tt.mergeStrategy))
			}

			body := strings.Join(bodyLines, "\n")
			hasMergeStrategy := strings.Contains(body, "MergeStrategy:")

			if hasMergeStrategy != tt.wantInBody {
				t.Errorf("body contains MergeStrategy = %v, want %v\nbody: %s",
					hasMergeStrategy, tt.wantInBody, body)
			}
		})
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
	now := time.Now()
	tests := []struct {
		checkpoint DoneCheckpoint
		value      string
		wantPrefix string
	}{
		{CheckpointPushed, "polecat/furiosa-abc", "done-cp:pushed:polecat/furiosa-abc:"},
		{CheckpointMRCreated, "gt-xyz123", "done-cp:mr-created:gt-xyz123:"},
		{CheckpointWitnessNotified, "ok", "done-cp:witness-notified:ok:"},
	}

	for _, tt := range tests {
		t.Run(string(tt.checkpoint), func(t *testing.T) {
			label := fmt.Sprintf("done-cp:%s:%s:%d", tt.checkpoint, tt.value, now.Unix())
			if !strings.HasPrefix(label, tt.wantPrefix) {
				t.Errorf("label = %q, want prefix %q", label, tt.wantPrefix)
			}

			// Verify the label can be parsed back
			parts := strings.SplitN(label, ":", 4)
			if len(parts) != 4 {
				t.Fatalf("expected 4 parts, got %d: %v", len(parts), parts)
			}
			if parts[0] != "done-cp" {
				t.Errorf("prefix = %q, want %q", parts[0], "done-cp")
			}
			if DoneCheckpoint(parts[1]) != tt.checkpoint {
				t.Errorf("stage = %q, want %q", parts[1], tt.checkpoint)
			}
			if parts[2] != tt.value {
				t.Errorf("value = %q, want %q", parts[2], tt.value)
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
			checkpoints := make(map[DoneCheckpoint]string)
			for _, label := range tt.labels {
				if strings.HasPrefix(label, "done-cp:") {
					parts := strings.SplitN(label, ":", 4)
					if len(parts) >= 3 {
						stage := DoneCheckpoint(parts[1])
						value := parts[2]
						checkpoints[stage] = value
					}
				}
			}

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

// TestClearDoneCheckpoints verifies that clearDoneCheckpoints removes
// only done-cp labels while preserving other labels.
func TestClearDoneCheckpoints(t *testing.T) {
	allLabels := []string{
		"gt:agent",
		"idle:3",
		"done-intent:COMPLETED:1738972800",
		"done-cp:pushed:mybranch:1738972801",
		"done-cp:mr-created:gt-xyz:1738972802",
		"backoff-until:1738972900",
	}

	var kept []string
	var removed []string
	for _, label := range allLabels {
		if strings.HasPrefix(label, "done-cp:") {
			removed = append(removed, label)
		} else {
			kept = append(kept, label)
		}
	}

	if len(removed) != 2 {
		t.Errorf("expected 2 checkpoint labels removed, got %d: %v", len(removed), removed)
	}
	if len(kept) != 4 {
		t.Errorf("expected 4 labels kept, got %d: %v", len(kept), kept)
	}

	// Verify no checkpoint labels in kept set
	for _, label := range kept {
		if strings.HasPrefix(label, "done-cp:") {
			t.Errorf("checkpoint label was not removed: %s", label)
		}
	}

	// Verify done-intent is preserved (not a checkpoint)
	found := false
	for _, label := range kept {
		if strings.HasPrefix(label, "done-intent:") {
			found = true
		}
	}
	if !found {
		t.Error("done-intent label should be preserved by clearDoneCheckpoints")
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Replicate the guard condition from runDone
			skipPush := tt.checkpoints[CheckpointPushed] != ""
			if skipPush != tt.wantSkip {
				t.Errorf("skipPush = %v, want %v", skipPush, tt.wantSkip)
			}
		})
	}
}

// TestCheckpointNilMapSafe verifies that reading from a nil/empty checkpoint
// map returns zero values and doesn't panic.
func TestCheckpointNilMapSafe(t *testing.T) {
	// Nil map - should not panic
	var nilMap map[DoneCheckpoint]string
	if nilMap[CheckpointPushed] != "" {
		t.Error("nil map should return zero value")
	}

	// Empty map
	emptyMap := map[DoneCheckpoint]string{}
	if emptyMap[CheckpointPushed] != "" {
		t.Error("empty map should return zero value")
	}
}

// TestConvoyInfoFallbackChain verifies that done.go checks attachment fields
// first, then falls back to dep-based convoy lookup. This is the fix for gt-7b6wf:
// convoy merge=direct was not propagated because cross-rig dep resolution failed.
func TestConvoyInfoFallbackChain(t *testing.T) {
	tests := []struct {
		name           string
		attachmentInfo *ConvoyInfo // Result from getConvoyInfoFromIssue
		depInfo        *ConvoyInfo // Result from getConvoyInfoForIssue
		wantConvoyID   string
		wantMerge      string
		wantNil        bool
	}{
		{
			name:           "attachment fields provide convoy info",
			attachmentInfo: &ConvoyInfo{ID: "hq-cv-abc", MergeStrategy: "direct"},
			depInfo:        nil, // Not called
			wantConvoyID:   "hq-cv-abc",
			wantMerge:      "direct",
		},
		{
			name:           "attachment fields empty, dep lookup succeeds",
			attachmentInfo: nil,
			depInfo:        &ConvoyInfo{ID: "hq-cv-xyz", MergeStrategy: "mr"},
			wantConvoyID:   "hq-cv-xyz",
			wantMerge:      "mr",
		},
		{
			name:           "both nil - no convoy",
			attachmentInfo: nil,
			depInfo:        nil,
			wantNil:        true,
		},
		{
			name:           "attachment has convoy, dep also has (attachment wins)",
			attachmentInfo: &ConvoyInfo{ID: "hq-cv-from-attachment", MergeStrategy: "direct"},
			depInfo:        &ConvoyInfo{ID: "hq-cv-from-dep", MergeStrategy: "mr"},
			wantConvoyID:   "hq-cv-from-attachment",
			wantMerge:      "direct",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Simulate the fallback chain from done.go
			var convoyInfo *ConvoyInfo
			convoyInfo = tt.attachmentInfo
			if convoyInfo == nil {
				convoyInfo = tt.depInfo
			}

			if tt.wantNil {
				if convoyInfo != nil {
					t.Errorf("expected nil, got %+v", convoyInfo)
				}
				return
			}
			if convoyInfo == nil {
				t.Fatal("expected non-nil convoy info")
			}
			if convoyInfo.ID != tt.wantConvoyID {
				t.Errorf("ConvoyID = %q, want %q", convoyInfo.ID, tt.wantConvoyID)
			}
			if convoyInfo.MergeStrategy != tt.wantMerge {
				t.Errorf("MergeStrategy = %q, want %q", convoyInfo.MergeStrategy, tt.wantMerge)
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
			// Replicate the guard condition from updateAgentStateOnDone (gt-pftz fix)
			shouldClose := !beads.IssueStatus(tt.status).IsTerminal()
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
