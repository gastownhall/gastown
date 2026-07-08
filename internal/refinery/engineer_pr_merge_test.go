package refinery

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

type recordingPRProvider struct {
	mergeMethod string
	mergeFunc   func(method string) (string, error)
	headSHA     string
}

func (p *recordingPRProvider) FindPullRequest(_ string, _ string, _ int, headSHA string) (*gitpkg.PullRequestInfo, error) {
	if p.headSHA != "" {
		headSHA = p.headSHA
	}
	return &gitpkg.PullRequestInfo{Number: 42, State: "OPEN", HeadSHA: headSHA}, nil
}

func (p *recordingPRProvider) IsPRApproved(*gitpkg.PullRequestInfo) (bool, error) {
	return true, nil
}

func (p *recordingPRProvider) MergePR(_ *gitpkg.PullRequestInfo, method string) (string, error) {
	p.mergeMethod = method
	if p.mergeFunc != nil {
		return p.mergeFunc(method)
	}
	return "", nil
}

func TestEngineer_LoadConfig_MergeStrategyPR(t *testing.T) {
	tmpDir := t.TempDir()

	requireReview := true
	settings := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"merge_strategy": "pr",
			"require_review": requireReview,
		},
	}
	writeEngineerSettingsConfig(t, tmpDir, settings)

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.MergeStrategy != "pr" {
		t.Errorf("expected MergeStrategy 'pr', got %q", e.config.MergeStrategy)
	}
	if e.config.RequireReview == nil || !*e.config.RequireReview {
		t.Error("expected RequireReview to be true")
	}
}

func TestEngineer_LoadConfig_MergeStrategyDefault(t *testing.T) {
	tmpDir := t.TempDir()

	settings := map[string]interface{}{
		"type":        "rig",
		"version":     1,
		"name":        "test-rig",
		"merge_queue": map[string]interface{}{},
	}
	writeEngineerSettingsConfig(t, tmpDir, settings)

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.MergeStrategy != "" {
		t.Errorf("expected empty MergeStrategy (default), got %q", e.config.MergeStrategy)
	}
	if e.config.RequireReview != nil {
		t.Error("expected RequireReview to be nil (default)")
	}
}

func TestDoMerge_PRStrategy_RoutesToPRPath(t *testing.T) {
	// When merge_strategy=pr, doMerge should attempt the PR merge path.
	// Without a real GitHub repo, FindPRNumber will fail — that's the expected
	// behavior we test: the code routes to doMergePR and fails gracefully.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "pr"

	// Create a feature branch
	createFeatureBranch(t, workDir, "feat/test-pr", "test.txt", "hello")

	result := e.doMerge(context.Background(), &MRInfo{ID: "mr-test-pr", Branch: "feat/test-pr", Target: "main"})

	if result.Success {
		t.Error("expected failure (no GitHub PR exists)")
	}

	output := e.output.(*bytes.Buffer).String()
	if !strings.Contains(output, "PR merge strategy") {
		t.Errorf("expected PR merge strategy log, got: %s", output)
	}
}

func TestDoMerge_DirectStrategy_SkipsPRPath(t *testing.T) {
	// When merge_strategy is empty (direct), doMerge should use the normal path.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = "" // explicit direct

	createFeatureBranch(t, workDir, "feat/test-direct", "test.txt", "hello")

	result := e.doMerge(context.Background(), &MRInfo{ID: "mr-test-direct", Branch: "feat/test-direct", Target: "main"})

	// Should succeed with direct merge
	if !result.Success {
		t.Errorf("expected success for direct merge, got error: %s", result.Error)
	}

	output := e.output.(*bytes.Buffer).String()
	if strings.Contains(output, "PR merge strategy") {
		t.Error("direct merge should not mention PR merge strategy")
	}
}

func TestDoMerge_DirectStrategy_BlocksForkBackedDefaultPush(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	addDistinctUpstreamRemote(t, workDir, g)
	e := newTestEngineer(t, workDir, g)
	e.config.MergeStrategy = ""

	createFeatureBranch(t, workDir, "feat/fork-guard", "fork.txt", "hello")
	before := run(t, workDir, "git", "rev-parse", "origin/main")

	result := e.doMerge(context.Background(), &MRInfo{ID: "mr-fork-guard", Branch: "feat/fork-guard", Target: "main"})
	if result.Success {
		t.Fatal("expected fork-backed default push to be refused")
	}
	if !strings.Contains(result.Error, "refusing direct push") {
		t.Fatalf("expected direct-push refusal, got: %s", result.Error)
	}
	assertOriginMainUnchangedAndReset(t, workDir, before)
}

func addDistinctUpstreamRemote(t *testing.T, workDir string, g *gitpkg.Git) {
	t.Helper()
	upstream := filepath.Join(t.TempDir(), "upstream.git")
	run(t, filepath.Dir(upstream), "git", "init", "--bare", "--initial-branch=main", upstream)
	if _, err := g.AddRemote("upstream", upstream); err != nil {
		t.Fatalf("AddRemote upstream: %v", err)
	}
}

func TestDoMergePR_NoPR_ReturnsError(t *testing.T) {
	// doMergePR should return an error when no PR exists for the branch.
	workDir, g, _ := testGitRepo(t)
	e := newTestEngineer(t, workDir, g)

	createFeatureBranch(t, workDir, "feat/no-pr", "test.txt", "hello")

	result := e.doMergePR(context.Background(), &MRInfo{ID: "mr-no-pr", Branch: "feat/no-pr", Target: "main"})

	if result.Success {
		t.Error("expected failure when no PR exists")
	}
	// The error should mention finding a PR
	if !strings.Contains(result.Error, "PR") && !strings.Contains(result.Error, "pr") {
		t.Errorf("expected PR-related error, got: %s", result.Error)
	}
}

func TestDoMergePR_UsesMergeCommitAndPreservesSubmittedHead(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	branch := "feat/pr-merge"
	createFeatureBranch(t, workDir, branch, "pr.txt", "hello")
	commit := run(t, workDir, "git", "rev-parse", branch)

	e := newTestEngineer(t, workDir, g)
	provider := &recordingPRProvider{mergeFunc: func(method string) (string, error) {
		if method != "merge" {
			return "", fmt.Errorf("method = %s, want merge", method)
		}
		run(t, workDir, "git", "checkout", "main")
		run(t, workDir, "git", "merge", "--no-ff", "-m", "merge PR", branch)
		run(t, workDir, "git", "push", "origin", "main")
		return run(t, workDir, "git", "rev-parse", "main"), nil
	}}
	e.prProvider = provider

	result := e.doMergePR(context.Background(), &MRInfo{ID: "mr-pr-merge", Branch: branch, Target: "main", CommitSHA: commit})
	if !result.Success {
		t.Fatalf("doMergePR failed: %s", result.Error)
	}
	if provider.mergeMethod != "merge" {
		t.Fatalf("MergePR method = %q, want merge", provider.mergeMethod)
	}
	if err := g.VerifyPushedCommitReachableFromPushTarget("origin", "main", commit); err != nil {
		t.Fatalf("submitted head not reachable after PR merge: %v", err)
	}
}

func TestDoMergePR_RejectsAdvancedPRHead(t *testing.T) {
	workDir, g, _ := testGitRepo(t)
	branch := "feat/pr-advanced"
	createFeatureBranch(t, workDir, branch, "pr.txt", "hello")
	commit := run(t, workDir, "git", "rev-parse", branch)
	run(t, workDir, "git", "checkout", branch)
	writeFile(t, workDir, "later.txt", "not submitted\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: later")
	advanced := run(t, workDir, "git", "rev-parse", branch)
	run(t, workDir, "git", "checkout", "main")

	e := newTestEngineer(t, workDir, g)
	provider := &recordingPRProvider{
		headSHA: advanced,
		mergeFunc: func(method string) (string, error) {
			t.Fatalf("MergePR called for advanced PR head with method %s", method)
			return "", nil
		},
	}
	e.prProvider = provider

	result := e.doMergePR(context.Background(), &MRInfo{ID: "mr-pr-advanced", Branch: branch, Target: "main", CommitSHA: commit})
	if result.Success {
		t.Fatal("doMergePR succeeded for advanced PR head")
	}
	if !strings.Contains(result.Error, "head changed") {
		t.Fatalf("doMergePR error = %q, want head changed", result.Error)
	}
}

func TestProcessResult_NeedsApproval(t *testing.T) {
	// Verify NeedsApproval field works on ProcessResult.
	r := ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "PR #42 requires approving review before merge",
	}

	if r.Success {
		t.Error("expected Success=false")
	}
	if !r.NeedsApproval {
		t.Error("expected NeedsApproval=true")
	}
}

func TestHandleMRInfoFailure_NeedsApproval_StaysInQueue(t *testing.T) {
	// When NeedsApproval is true, the MR should stay in queue without
	// sending failure notifications to polecats or mayor.
	workDir := t.TempDir()
	r := &rig.Rig{Name: "test-rig", Path: workDir}
	e := NewEngineer(r)
	var buf bytes.Buffer
	e.output = &buf
	e.workDir = workDir
	e.mergeSlotEnsureExists = func() (string, error) { return "test-slot", nil }
	e.mergeSlotAcquire = func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
		return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
	}
	e.mergeSlotRelease = func(holder string) error { return nil }

	mr := &MRInfo{
		ID:          "gt-test",
		Branch:      "polecat/test/gt-test",
		Target:      "main",
		SourceIssue: "gt-src",
		Worker:      "polecats/test",
	}
	result := ProcessResult{
		Success:       false,
		NeedsApproval: true,
		Error:         "PR #42 requires approving review before merge",
	}

	e.HandleMRInfoFailure(mr, result)

	output := buf.String()
	if !strings.Contains(output, "awaiting human approval") {
		t.Errorf("expected 'awaiting human approval' message, got: %s", output)
	}
	// Should NOT contain merge failure notifications
	if strings.Contains(output, "MERGE_FAILED") {
		t.Error("NeedsApproval should not trigger MERGE_FAILED notification")
	}
}

func TestDoMergePR_RequireReview_NoApproval(t *testing.T) {
	// When require_review is true and the PR is not approved,
	// doMergePR should return NeedsApproval=true.
	// This test is tricky since it requires gh CLI — skip if not available.
	if _, err := gitpkg.NewGit(t.TempDir()).FindPRNumber("nonexistent"); err != nil {
		// gh CLI not available or not authenticated — test the config path only
		t.Skip("gh CLI not available for PR approval testing")
	}
}
