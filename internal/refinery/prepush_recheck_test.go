package refinery

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	gitpkg "github.com/steveyegge/gastown/internal/git"
)

type prepushStore struct {
	beadsdk.Storage
	issues       map[string]*beadsdk.Issue
	closeReasons map[string]string
	beforeGet    func(id string)
}

type prepushPRProvider struct {
	beforeMerge func()
	mergeCalled bool
}

func (p *prepushPRProvider) FindPRNumber(string) (int, error) {
	return 42, nil
}

func (p *prepushPRProvider) IsPRApproved(int) (bool, error) {
	if p.beforeMerge != nil {
		p.beforeMerge()
	}
	return true, nil
}

func (p *prepushPRProvider) MergePR(int, string) (string, error) {
	p.mergeCalled = true
	return "deadbeef", nil
}

func newPrepushStore(issues ...*beadsdk.Issue) *prepushStore {
	store := &prepushStore{
		issues:       make(map[string]*beadsdk.Issue, len(issues)),
		closeReasons: make(map[string]string),
	}
	for _, issue := range issues {
		store.issues[issue.ID] = issue
	}
	return store
}

func (s *prepushStore) GetIssue(_ context.Context, id string) (*beadsdk.Issue, error) {
	if s.beforeGet != nil {
		s.beforeGet(id)
	}
	issue, ok := s.issues[id]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return issue, nil
}

func (s *prepushStore) GetLabels(_ context.Context, id string) ([]string, error) {
	issue, ok := s.issues[id]
	if !ok {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return append([]string(nil), issue.Labels...), nil
}

func (s *prepushStore) GetDependenciesWithMetadata(_ context.Context, id string) ([]*beadsdk.IssueWithDependencyMetadata, error) {
	if _, ok := s.issues[id]; !ok {
		return nil, fmt.Errorf("issue %s not found", id)
	}
	return nil, nil
}

func (s *prepushStore) UpdateIssue(_ context.Context, id string, updates map[string]interface{}, _ string) error {
	issue, ok := s.issues[id]
	if !ok {
		return fmt.Errorf("issue %s not found", id)
	}
	for key, value := range updates {
		switch key {
		case "description":
			issue.Description, _ = value.(string)
		case "status":
			if status, ok := value.(string); ok {
				issue.Status = beadsdk.Status(status)
			}
		}
	}
	issue.UpdatedAt = time.Now()
	return nil
}

func (s *prepushStore) CloseIssue(_ context.Context, id, reason, _, _ string) error {
	issue, ok := s.issues[id]
	if !ok {
		return fmt.Errorf("issue %s not found", id)
	}
	now := time.Now()
	issue.Status = beadsdk.StatusClosed
	issue.ClosedAt = &now
	issue.UpdatedAt = now
	s.closeReasons[id] = reason
	return nil
}

func prepushIssue(id, description string, labels ...string) *beadsdk.Issue {
	now := time.Now()
	return &beadsdk.Issue{
		ID:          id,
		Title:       id,
		Description: description,
		Status:      beadsdk.StatusOpen,
		IssueType:   beadsdk.IssueType("task"),
		Priority:    2,
		CreatedAt:   now,
		UpdatedAt:   now,
		Labels:      labels,
	}
}

func prepushMRIssue(id, branch, target, sourceIssue string) *beadsdk.Issue {
	desc := beads.FormatMRFields(&beads.MRFields{
		Branch:      branch,
		Target:      target,
		SourceIssue: sourceIssue,
		Worker:      "polecats/test",
		Rig:         "test-rig",
	})
	return prepushIssue(id, desc, "gt:merge-request")
}

func newPrepushEngineer(t *testing.T, workDir string, store *prepushStore) *Engineer {
	t.Helper()
	g := newTestGit(t, workDir)
	e := newTestEngineer(t, workDir, g)
	e.beads = beads.NewWithStore(workDir, store)
	return e
}

func newTestGit(t *testing.T, workDir string) *gitpkg.Git {
	t.Helper()
	return gitpkg.NewGit(workDir)
}

func assertOriginMainUnchangedAndReset(t *testing.T, workDir, before string) {
	t.Helper()
	afterOrigin := run(t, workDir, "git", "rev-parse", "origin/main")
	if afterOrigin != before {
		t.Fatalf("origin/main changed: before %s after %s", before, afterOrigin)
	}
	remoteLine := run(t, workDir, "git", "ls-remote", "origin", "refs/heads/main")
	remoteFields := strings.Fields(remoteLine)
	if len(remoteFields) == 0 || remoteFields[0] != before {
		t.Fatalf("remote main changed: before %s ls-remote %q", before, remoteLine)
	}
	localMain := run(t, workDir, "git", "rev-parse", "main")
	if localMain != before {
		t.Fatalf("local main was not reset to origin/main: local %s origin %s", localMain, before)
	}
}

func TestRecheckMRStillMergeable_RejectsMissingSourceField(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	store := newPrepushStore(prepushMRIssue("gt-mr", "feature", "main", ""))
	e := newPrepushEngineer(t, workDir, store)

	result := e.recheckMRStillMergeable(&MRInfo{ID: "gt-mr", Branch: "feature", Target: "main"}, "main")
	if result.Success || !result.NoMerge {
		t.Fatalf("missing source_issue should be rejected, got: %+v", result)
	}
	if got := store.closeReasons["gt-mr"]; got != "rejected: MR has missing source_issue" {
		t.Fatalf("MR close reason = %q, want missing source rejection", got)
	}
}

func TestRecheckMRStillMergeable_RejectsMissingSourceIssue(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	store := newPrepushStore(prepushMRIssue("gt-mr", "feature", "main", "gt-missing"))
	e := newPrepushEngineer(t, workDir, store)

	mr := &MRInfo{ID: "gt-mr", Branch: "feature", Target: "main", SourceIssue: "gt-missing"}
	result := e.recheckMRStillMergeable(mr, "main")
	if result.Success || !result.NoMerge {
		t.Fatalf("missing source issue should be rejected, got: %+v", result)
	}
	if got := store.closeReasons["gt-mr"]; got != "rejected: source_issue gt-missing is missing" {
		t.Fatalf("MR close reason = %q, want missing source issue", got)
	}
}

func TestRecheckMRStillMergeable_RejectsNonConcreteSource(t *testing.T) {
	tests := []struct {
		name  string
		label string
	}{
		{name: "merge_request", label: "gt:merge-request"},
		{name: "handoff", label: "gt:handoff"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir, _, cleanup := testGitRepo(t)
			defer cleanup()
			store := newPrepushStore(
				prepushIssue("gt-src", "", tc.label),
				prepushMRIssue("gt-mr", "feature", "main", "gt-src"),
			)
			e := newPrepushEngineer(t, workDir, store)

			mr := &MRInfo{ID: "gt-mr", Branch: "feature", Target: "main", SourceIssue: "gt-src"}
			result := e.recheckMRStillMergeable(mr, "main")
			if result.Success || !result.NoMerge {
				t.Fatalf("non-concrete source should be rejected, got: %+v", result)
			}
			if !strings.Contains(store.closeReasons["gt-mr"], "not concrete") {
				t.Fatalf("MR close reason = %q, want non-concrete rejection", store.closeReasons["gt-mr"])
			}
		})
	}
}

func TestRecheckMRStillMergeable_RejectsClosedSource(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	source := prepushIssue("gt-src", "")
	now := time.Now()
	source.Status = beadsdk.StatusClosed
	source.ClosedAt = &now
	store := newPrepushStore(source, prepushMRIssue("gt-mr", "feature", "main", "gt-src"))
	e := newPrepushEngineer(t, workDir, store)

	mr := &MRInfo{ID: "gt-mr", Branch: "feature", Target: "main", SourceIssue: "gt-src"}
	result := e.recheckMRStillMergeable(mr, "main")
	if result.Success || !result.NoMerge {
		t.Fatalf("closed source should be rejected, got: %+v", result)
	}
	if got := store.closeReasons["gt-mr"]; got != "rejected: source_issue gt-src status is closed" {
		t.Fatalf("MR close reason = %q, want closed source rejection", got)
	}
}

func TestDoMerge_RechecksSourceFlagsBeforeDirectPush(t *testing.T) {
	tests := []struct {
		name        string
		description string
	}{
		{name: "no_merge", description: "no_merge: true"},
		{name: "review_only", description: "review_only: true"},
		{name: "local_merge", description: "merge_strategy: local"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			workDir, _, cleanup := testGitRepo(t)
			defer cleanup()
			createFeatureBranch(t, workDir, "feature-"+tc.name, tc.name+".txt", tc.name+"\n")
			store := newPrepushStore(
				prepushIssue("gt-src", ""),
				prepushMRIssue("gt-mr", "feature-"+tc.name, "main", "gt-src"),
			)
			e := newPrepushEngineer(t, workDir, store)
			before := run(t, workDir, "git", "rev-parse", "origin/main")

			mutated := false
			e.mergeSlotAcquire = func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
				if !mutated {
					store.issues["gt-src"].Description = tc.description
					store.issues["gt-src"].UpdatedAt = time.Now()
					mutated = true
				}
				return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
			}

			mr := &MRInfo{ID: "gt-mr", Branch: "feature-" + tc.name, Target: "main", SourceIssue: "gt-src"}
			result := e.doMerge(context.Background(), mr)
			if result.Success || !result.NoMerge {
				t.Fatalf("expected clean policy rejection before direct push, got: %+v", result)
			}
			if !mutated {
				t.Fatal("expected merge slot hook to mutate source before push")
			}
			assertOriginMainUnchangedAndReset(t, workDir, before)
		})
	}
}

func TestDoMerge_RechecksBeforeSubmodulePush(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()

	subRoot := t.TempDir()
	subBare := filepath.Join(subRoot, "sub.git")
	subWork := filepath.Join(subRoot, "sub-work")
	run(t, subRoot, "git", "init", "--bare", "--initial-branch=main", subBare)
	run(t, subRoot, "git", "clone", subBare, subWork)
	run(t, subWork, "git", "config", "user.email", "test@test.com")
	run(t, subWork, "git", "config", "user.name", "Test")
	writeFile(t, subWork, "README.md", "submodule v1\n")
	run(t, subWork, "git", "add", ".")
	run(t, subWork, "git", "commit", "-m", "submodule initial")
	run(t, subWork, "git", "push", "-u", "origin", "main")

	run(t, workDir, "git", "-c", "protocol.file.allow=always", "submodule", "add", subBare, "libs/sub")
	run(t, workDir, "git", "commit", "-m", "add submodule")
	run(t, workDir, "git", "push", "origin", "main")

	submodulePath := filepath.Join(workDir, "libs", "sub")
	run(t, submodulePath, "git", "config", "user.email", "test@test.com")
	run(t, submodulePath, "git", "config", "user.name", "Test")
	writeFile(t, submodulePath, "v2.txt", "submodule v2\n")
	run(t, submodulePath, "git", "add", ".")
	run(t, submodulePath, "git", "commit", "-m", "submodule update")
	newSubmoduleSHA := run(t, submodulePath, "git", "rev-parse", "HEAD")

	run(t, workDir, "git", "checkout", "-b", "feature-submodule", "main")
	run(t, workDir, "git", "add", "libs/sub")
	run(t, workDir, "git", "commit", "-m", "feat: update submodule")
	run(t, workDir, "git", "checkout", "main")

	store := newPrepushStore(prepushIssue("gt-src", ""), prepushMRIssue("gt-mr", "feature-submodule", "main", "gt-src"))
	sourceReads := 0
	store.beforeGet = func(id string) {
		if id != "gt-src" {
			return
		}
		sourceReads++
		if sourceReads == 2 {
			store.issues["gt-src"].Description = "no_merge: true"
			store.issues["gt-src"].UpdatedAt = time.Now()
		}
	}
	e := newPrepushEngineer(t, workDir, store)

	mr := &MRInfo{ID: "gt-mr", Branch: "feature-submodule", Target: "main", SourceIssue: "gt-src"}
	result := e.doMerge(context.Background(), mr)
	if result.Success || !result.NoMerge {
		t.Fatalf("expected clean policy rejection before submodule push, got: %+v", result)
	}
	if sourceReads < 2 {
		t.Fatalf("expected source re-read before submodule push, got %d reads", sourceReads)
	}
	remoteLine := run(t, workDir, "git", "ls-remote", subBare, "refs/heads/main")
	if strings.Fields(remoteLine)[0] == newSubmoduleSHA {
		t.Fatal("submodule commit was pushed despite pre-push rejection")
	}
}

func TestDoMergePR_RechecksSourceBeforeMergeAPI(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	store := newPrepushStore(prepushIssue("gt-src", ""), prepushMRIssue("gt-mr-pr", "feature-pr", "main", "gt-src"))
	e := newPrepushEngineer(t, workDir, store)
	requireReview := true
	e.config.RequireReview = &requireReview
	provider := &prepushPRProvider{beforeMerge: func() {
		store.issues["gt-src"].Description = "no_merge: true"
		store.issues["gt-src"].UpdatedAt = time.Now()
	}}
	e.prProvider = provider

	mr := &MRInfo{ID: "gt-mr-pr", Branch: "feature-pr", Target: "main", SourceIssue: "gt-src"}
	result := e.doMergePR(context.Background(), mr)
	if result.Success || !result.NoMerge {
		t.Fatalf("expected clean policy rejection before PR merge API, got: %+v", result)
	}
	if provider.mergeCalled {
		t.Fatal("MergePR was called after source became no_merge")
	}
}

func TestProcessBatch_RechecksBatchBeforePush(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	createFeatureBranch(t, workDir, "feature-a", "a.txt", "a\n")
	createFeatureBranch(t, workDir, "feature-b", "b.txt", "b\n")
	store := newPrepushStore(
		prepushIssue("gt-src-a", ""),
		prepushIssue("gt-src-b", ""),
		prepushMRIssue("gt-mr-a", "feature-a", "main", "gt-src-a"),
		prepushMRIssue("gt-mr-b", "feature-b", "main", "gt-src-b"),
	)
	e := newPrepushEngineer(t, workDir, store)
	before := run(t, workDir, "git", "rev-parse", "origin/main")

	mutated := false
	e.mergeSlotAcquire = func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
		if !mutated {
			store.issues["gt-src-b"].Description = "no_merge: true"
			store.issues["gt-src-b"].UpdatedAt = time.Now()
			mutated = true
		}
		return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
	}

	batch := []*MRInfo{
		{ID: "gt-mr-a", Branch: "feature-a", Target: "main", SourceIssue: "gt-src-a"},
		{ID: "gt-mr-b", Branch: "feature-b", Target: "main", SourceIssue: "gt-src-b"},
	}
	result := e.ProcessBatch(context.Background(), batch, "main", DefaultBatchConfig())
	if len(result.Merged) != 0 {
		t.Fatalf("expected no merged MRs, got %d", len(result.Merged))
	}
	if result.Error != nil {
		t.Fatalf("expected clean policy dequeue, got error: %v", result.Error)
	}
	if !mutated {
		t.Fatal("expected merge slot hook to mutate batch source before push")
	}
	assertOriginMainUnchangedAndReset(t, workDir, before)
	if got := store.issues["gt-mr-b"].Status; got != beadsdk.StatusClosed {
		t.Fatalf("invalidated MR status = %s, want closed", got)
	}
	if got := store.issues["gt-mr-a"].Status; got != beadsdk.StatusOpen {
		t.Fatalf("unaffected MR status = %s, want open", got)
	}
}

func TestProcessBatch_RechecksBatchBeforeGates(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	createFeatureBranch(t, workDir, "feature-a", "a.txt", "a\n")
	createFeatureBranch(t, workDir, "feature-b", "b.txt", "b\n")
	store := newPrepushStore(
		prepushIssue("gt-src-a", ""),
		prepushIssue("gt-src-b", "no_merge: true"),
		prepushMRIssue("gt-mr-a", "feature-a", "main", "gt-src-a"),
		prepushMRIssue("gt-mr-b", "feature-b", "main", "gt-src-b"),
	)
	e := newPrepushEngineer(t, workDir, store)
	e.config.Gates = map[string]*GateConfig{"fail": {Cmd: "false"}}
	before := run(t, workDir, "git", "rev-parse", "origin/main")

	batch := []*MRInfo{
		{ID: "gt-mr-a", Branch: "feature-a", Target: "main", SourceIssue: "gt-src-a"},
		{ID: "gt-mr-b", Branch: "feature-b", Target: "main", SourceIssue: "gt-src-b"},
	}
	result := e.ProcessBatch(context.Background(), batch, "main", DefaultBatchConfig())
	if result.Error != nil {
		t.Fatalf("expected clean policy dequeue, got error: %v", result.Error)
	}
	if len(result.Culprits) != 0 {
		t.Fatalf("expected no gate culprits for policy-ineligible MR, got %d", len(result.Culprits))
	}
	assertOriginMainUnchangedAndReset(t, workDir, before)
	if got := store.issues["gt-mr-b"].Status; got != beadsdk.StatusClosed {
		t.Fatalf("invalidated MR status = %s, want closed", got)
	}
	if got := store.issues["gt-mr-a"].Status; got != beadsdk.StatusOpen {
		t.Fatalf("unaffected MR status = %s, want open", got)
	}
}

func TestProcessBatch_RechecksMRCloseReasonBeforePush(t *testing.T) {
	workDir, _, cleanup := testGitRepo(t)
	defer cleanup()
	createFeatureBranch(t, workDir, "feature-a", "a.txt", "a\n")
	createFeatureBranch(t, workDir, "feature-b", "b.txt", "b\n")
	store := newPrepushStore(
		prepushIssue("gt-src-a", ""),
		prepushIssue("gt-src-b", ""),
		prepushMRIssue("gt-mr-a", "feature-a", "main", "gt-src-a"),
		prepushMRIssue("gt-mr-b", "feature-b", "main", "gt-src-b"),
	)
	e := newPrepushEngineer(t, workDir, store)
	before := run(t, workDir, "git", "rev-parse", "origin/main")

	mutated := false
	e.mergeSlotAcquire = func(holder string, addWaiter bool) (*beads.MergeSlotStatus, error) {
		if !mutated {
			store.issues["gt-mr-b"].Description += "\nclose_reason: rejected"
			store.issues["gt-mr-b"].UpdatedAt = time.Now()
			mutated = true
		}
		return &beads.MergeSlotStatus{Available: true, Holder: holder}, nil
	}

	batch := []*MRInfo{
		{ID: "gt-mr-a", Branch: "feature-a", Target: "main", SourceIssue: "gt-src-a"},
		{ID: "gt-mr-b", Branch: "feature-b", Target: "main", SourceIssue: "gt-src-b"},
	}
	result := e.ProcessBatch(context.Background(), batch, "main", DefaultBatchConfig())
	if len(result.Merged) != 0 {
		t.Fatalf("expected no merged MRs, got %d", len(result.Merged))
	}
	if result.Error != nil {
		t.Fatalf("expected clean policy dequeue, got error: %v", result.Error)
	}
	if !mutated {
		t.Fatal("expected merge slot hook to mutate batch MR before push")
	}
	assertOriginMainUnchangedAndReset(t, workDir, before)
	if got := store.issues["gt-mr-b"].Status; got != beadsdk.StatusClosed {
		t.Fatalf("invalidated MR status = %s, want closed", got)
	}
	if got := store.closeReasons["gt-mr-b"]; got != "rejected: MR close_reason is rejected" {
		t.Fatalf("invalidated MR close reason = %q, want rejected close_reason", got)
	}
	if got := store.issues["gt-mr-a"].Status; got != beadsdk.StatusOpen {
		t.Fatalf("unaffected MR status = %s, want open", got)
	}
}
