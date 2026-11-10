package refinery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	gitpkg "github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/workflow"
)

// gatedPRProvider is a fake PRProvider for the gated merge tests.
type gatedPRProvider struct {
	passed, pending bool
	checksErr       error
	checksCalls     int
	checkedHead     string
	noPR            bool
	mergeFunc       func() (string, error)
	mergeCalled     bool
}

func (p *gatedPRProvider) FindPullRequest(_ string, _ string, _ int, headSHA string) (*gitpkg.PullRequestInfo, error) {
	if p.noPR {
		return nil, nil
	}
	return &gitpkg.PullRequestInfo{Number: 7, State: "OPEN", HeadSHA: headSHA}, nil
}

func (p *gatedPRProvider) IsPRApproved(*gitpkg.PullRequestInfo) (bool, error) { return true, nil }

func (p *gatedPRProvider) MergePR(*gitpkg.PullRequestInfo, string) (string, error) {
	p.mergeCalled = true
	if p.mergeFunc != nil {
		return p.mergeFunc()
	}
	return "", errors.New("unexpected merge")
}

func (p *gatedPRProvider) ChecksPassed(pr *gitpkg.PullRequestInfo) (bool, bool, error) {
	p.checksCalls++
	p.checkedHead = pr.HeadSHA
	return p.passed, p.pending, p.checksErr
}

// failingItems wraps Items and fails Get with a non-not-found error.
type failingItems struct{ workflow.Items }

func (failingItems) Get(string) (*workflow.Item, error) {
	return nil, errors.New("dolt: connection refused")
}

type gatedFixture struct {
	t        *testing.T
	workDir  string
	branch   string
	env      workflow.GateEnv
	item     *workflow.Item
	store    *prepushStore
	e        *Engineer
	provider *gatedPRProvider
	mr       *MRInfo
}

const gatedReview = "---\nstatus: approved\nbump: \"minor\"\n---\nLooks good.\n"

// newGatedFixture builds a repo whose MR branch carries package.json and
// .claude-plugin/plugin.json at 0.24.7, a gated task item at step merge with
// a human-approved review.md (bump: minor), and an engineer whose workflow
// env is the in-memory one.
func newGatedFixture(t *testing.T) *gatedFixture {
	t.Helper()
	workDir, _, _ := testGitRepo(t)
	branch := "polecat/test/gated"
	if err := os.MkdirAll(filepath.Join(workDir, ".claude-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, workDir, "package.json", "{\n  \"name\": \"tbb\",\n  \"version\": \"0.24.7\"\n}\n")
	writeFile(t, workDir, ".claude-plugin/plugin.json", "{\"name\":\"tbb\",\"version\":\"0.24.7\"}\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "chore: version files")
	run(t, workDir, "git", "push", "origin", "main")
	run(t, workDir, "git", "checkout", "-b", branch, "main")
	writeFile(t, workDir, "feature.txt", "work\n")
	run(t, workDir, "git", "add", ".")
	run(t, workDir, "git", "commit", "-m", "feat: work")
	run(t, workDir, "git", "push", "origin", branch)
	run(t, workDir, "git", "checkout", "main")
	head := run(t, workDir, "git", "rev-parse", branch)

	town := t.TempDir()
	env := workflow.GateEnv{
		TownRoot: town,
		Rig:      "test-rig",
		Store:    workflow.NewMemStore(),
		Items:    workflow.NewMemItems(),
		Config: &config.GatedWorkflow{
			Enabled:      true,
			VersionFiles: []string{"package.json", ".claude-plugin/plugin.json"},
		},
	}
	it, err := env.Items.Create("test-rig", workflow.KindTask, "gated", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Items.SetBranch(it.ID, branch); err != nil {
		t.Fatal(err)
	}
	if err := env.Store.Transition(it.ID, "review", "merge", "test"); err != nil {
		t.Fatal(err)
	}
	if err := env.Items.SetStep(it.ID, "merge", time.Now()); err != nil {
		t.Fatal(err)
	}
	f := &gatedFixture{t: t, workDir: workDir, branch: branch, env: env, item: it}
	f.writeReview(gatedReview)
	f.approveReview()

	f.store = newPrepushStore(prepushIssue(it.ID, ""), prepushMRIssue("gt-mr", branch, "main", it.ID, head))
	f.e = newTestEngineer(t, workDir, gitpkg.NewGit(workDir))
	f.e.beads = beads.NewWithStore(workDir, gatedStore{f.store})
	f.e.config.MergeStrategy = "pr"
	f.provider = &gatedPRProvider{pending: true}
	f.e.prProvider = f.provider
	prev := newRefineryWorkflowEnv
	newRefineryWorkflowEnv = func(*Engineer) (workflow.GateEnv, error) { return f.env, nil }
	t.Cleanup(func() { newRefineryWorkflowEnv = prev })
	f.mr = &MRInfo{ID: "gt-mr", Branch: branch, Target: "main", SourceIssue: it.ID, CommitSHA: head, Worker: "polecats/test"}
	return f
}

func (f *gatedFixture) writeReview(body string) {
	f.t.Helper()
	p := workflow.ArtifactPath(f.env.TownRoot, f.env.Rig, f.item.ID, "review")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gatedFixture) approveReview() {
	f.t.Helper()
	_, sha, _, err := workflow.ReadArtifact(workflow.ArtifactPath(f.env.TownRoot, f.env.Rig, f.item.ID, "review"))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := f.env.Store.AddApproval(workflow.Approval{ItemID: f.item.ID, Artifact: "review", SHA: sha, Verdict: "approve", Role: "human", Source: "human", Channel: "terminal", Actor: "me"}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *gatedFixture) step() workflow.Step {
	f.t.Helper()
	it, err := f.env.Items.Get(f.item.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return it.Step
}

func (f *gatedFixture) transitions() []workflow.Transition {
	f.t.Helper()
	trs, err := f.env.Store.Transitions(f.item.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return trs
}

func (f *gatedFixture) remoteSubject() string {
	return run(f.t, f.workDir, "git", "log", "-1", "--format=%s", "origin/"+f.branch)
}

func (f *gatedFixture) remoteCount() string {
	run(f.t, f.workDir, "git", "fetch", "origin")
	return run(f.t, f.workDir, "git", "rev-list", "--count", "origin/"+f.branch)
}

func TestWorkflowPreMerge_CIPendingRequeues(t *testing.T) {
	f := newGatedFixture(t)
	before := f.mr.CommitSHA

	res := f.e.workflowGate(context.Background(), f.mr)
	if res.OK || !res.Gated || !res.Pending {
		t.Fatalf("result = %+v, want gated pending", res)
	}
	if pr := res.Result; pr.Success || !pr.NeedsApproval || pr.NoMerge {
		t.Fatalf("process result = %+v, want requeue (NeedsApproval)", pr)
	}
	// No workflow state change.
	if f.step() != "merge" || len(f.transitions()) != 1 {
		t.Fatalf("step %s transitions %+v, want merge and unchanged", f.step(), f.transitions())
	}
	if checks, _ := f.env.Store.Checks(f.item.ID); len(checks) != 0 {
		t.Fatalf("checks = %+v, want none", checks)
	}
	if got := f.store.issues["gt-mr"].Status; got != beadsdk.StatusOpen {
		t.Fatalf("MR status = %s, want open", got)
	}
	// The bump was committed on the MR branch and pushed; CI is read for it.
	run(t, f.workDir, "git", "fetch", "origin")
	if s := f.remoteSubject(); s != "chore: bump version to 0.25.0" {
		t.Fatalf("origin branch subject = %q", s)
	}
	newHead := run(t, f.workDir, "git", "rev-parse", "origin/"+f.branch)
	if newHead == before || f.mr.CommitSHA != newHead || f.provider.checkedHead != newHead {
		t.Fatalf("head before %s after %s mr %s checked %s", before, newHead, f.mr.CommitSHA, f.provider.checkedHead)
	}
	if local := run(t, f.workDir, "git", "rev-parse", "refs/heads/"+f.branch); local != newHead {
		t.Fatalf("local branch %s, want %s", local, newHead)
	}
	if fields := f.store.issues["gt-mr"].Description; !strings.Contains(fields, newHead) {
		t.Fatalf("MR bead commit_sha not updated: %s", fields)
	}
	for _, file := range []string{"package.json", ".claude-plugin/plugin.json"} {
		if body := run(t, f.workDir, "git", "show", newHead+":"+file); !strings.Contains(body, `"0.25.0"`) {
			t.Fatalf("%s at bump commit = %s", file, body)
		}
	}
	author := run(t, f.workDir, "git", "log", "-1", "--format=%an <%ae>", newHead)
	if author != "Test <test@test.com>" {
		t.Fatalf("bump author = %q, want refinery identity", author)
	}
}

func TestWorkflowPreMerge_IdempotentRerunNoSecondBump(t *testing.T) {
	f := newGatedFixture(t)
	if ok, _ := f.e.workflowPreMerge(context.Background(), f.mr); ok {
		t.Fatal("pending run returned ok")
	}
	count := f.remoteCount()
	head := f.mr.CommitSHA

	// Requeued: the next poll reloads the MR with the recorded commit_sha.
	mr2 := *f.mr
	if ok, _ := f.e.workflowPreMerge(context.Background(), &mr2); ok {
		t.Fatal("second pending run returned ok")
	}
	if got := f.remoteCount(); got != count {
		t.Fatalf("commit count %s -> %s, want no second bump", count, got)
	}
	if mr2.CommitSHA != head {
		t.Fatalf("head moved %s -> %s", head, mr2.CommitSHA)
	}
	if f.provider.checksCalls != 2 {
		t.Fatalf("checks calls = %d, want 2", f.provider.checksCalls)
	}
}

func TestWorkflowPreMerge_CIFailedBackToExecute(t *testing.T) {
	f := newGatedFixture(t)
	f.provider.pending = false
	f.provider.checksErr = &ChecksFailedError{Names: []string{"test", "lint"}}

	res := f.e.workflowGate(context.Background(), f.mr)
	if res.OK || !res.Result.NoMerge {
		t.Fatalf("result = %+v, want rejected", res)
	}
	if f.step() != "execute" {
		t.Fatalf("step = %s, want execute", f.step())
	}
	trs := f.transitions()
	if last := trs[len(trs)-1]; last.From != "merge" || last.To != "execute" || last.Actor != "refinery" {
		t.Fatalf("last transition = %+v", last)
	}
	checks, _ := f.env.Store.Checks(f.item.ID)
	if len(checks) != 1 {
		t.Fatalf("checks = %+v, want one", checks)
	}
	c := checks[0]
	if c.Passed || c.Source != "gtb" || c.Provenance != "verified" || c.CmdSetSHA != "ci" || !strings.Contains(c.Tail, "test") || !strings.Contains(c.Tail, "lint") {
		t.Fatalf("check = %+v", c)
	}
	if got := f.store.issues["gt-mr"].Status; got != beadsdk.StatusClosed {
		t.Fatalf("MR status = %s, want closed", got)
	}
	if reason := f.store.closeReasons["gt-mr"]; !strings.HasPrefix(reason, "rejected: ") {
		t.Fatalf("MR close reason = %q, want rejected", reason)
	}
	if got := f.store.issues[f.item.ID].Status; got != beadsdk.StatusOpen {
		t.Fatalf("item bead status = %s, want open", got)
	}
}

func TestWorkflowPreMerge_CIFailedTailCapped(t *testing.T) {
	f := newGatedFixture(t)
	f.provider.pending = false
	names := make([]string, 300)
	for i := range names {
		names[i] = "check-with-a-long-name"
	}
	f.provider.checksErr = &ChecksFailedError{Names: names}
	f.e.workflowGate(context.Background(), f.mr)
	checks, _ := f.env.Store.Checks(f.item.ID)
	if len(checks) != 1 || len(checks[0].Tail) > 1000 {
		t.Fatalf("tail len = %d, want <= 1000", len(checks[0].Tail))
	}
}

func TestWorkflowPreMerge_NonGatedPassThrough(t *testing.T) {
	for name, env := range map[string]workflow.GateEnv{
		"disabled rig": {Config: &config.GatedWorkflow{Enabled: false}},
		"no gtb item":  {Config: &config.GatedWorkflow{Enabled: true}, Store: workflow.NewMemStore(), Items: workflow.NewMemItems()},
	} {
		t.Run(name, func(t *testing.T) {
			workDir, _, _ := testGitRepo(t)
			createFeatureBranch(t, workDir, "feature", "a.txt", "a\n")
			head := run(t, workDir, "git", "rev-parse", "feature")
			store := newPrepushStore(prepushIssue("gt-src", ""), prepushMRIssue("gt-mr", "feature", "main", "gt-src", head))
			e := newPrepushEngineer(t, workDir, store)
			provider := &gatedPRProvider{}
			e.prProvider = provider
			prev := newRefineryWorkflowEnv
			newRefineryWorkflowEnv = func(*Engineer) (workflow.GateEnv, error) { return env, nil }
			defer func() { newRefineryWorkflowEnv = prev }()

			mr := &MRInfo{ID: "gt-mr", Branch: "feature", Target: "main", SourceIssue: "gt-src", CommitSHA: head}
			ok, reason := e.workflowPreMerge(context.Background(), mr)
			if !ok || reason != "" {
				t.Fatalf("ok=%v reason=%q, want pass-through", ok, reason)
			}
			if mr.CommitSHA != head || run(t, workDir, "git", "rev-parse", "feature") != head {
				t.Fatal("branch touched")
			}
			if provider.checksCalls != 0 || store.issues["gt-mr"].Status != beadsdk.StatusOpen {
				t.Fatal("non-gated MR was touched")
			}
		})
	}
}

func TestWorkflowPreMerge_LookupErrorFailsClosed(t *testing.T) {
	f := newGatedFixture(t)
	f.env.Items = failingItems{f.env.Items}
	before := f.remoteCount()

	res := f.e.workflowGate(context.Background(), f.mr)
	if res.OK || res.Result.Success || res.Result.NoMerge || res.Reason == "" {
		t.Fatalf("result = %+v, want fail closed requeue", res)
	}
	if f.remoteCount() != before || f.provider.checksCalls != 0 {
		t.Fatal("lookup error still bumped or read CI")
	}

	// An env error also fails closed.
	newRefineryWorkflowEnv = func(*Engineer) (workflow.GateEnv, error) { return workflow.GateEnv{}, errors.New("no dolt") }
	f.e.wfEnv = nil // env errors are not cached; drop the env cached above
	if ok, reason := f.e.workflowPreMerge(context.Background(), f.mr); ok || !strings.Contains(reason, "no dolt") {
		t.Fatalf("ok=%v reason=%q, want fail closed", ok, reason)
	}
}

func TestWorkflowPreMerge_ReviewChangedSinceApprovalRefused(t *testing.T) {
	f := newGatedFixture(t)
	f.writeReview(gatedReview + "Edited after approval.\n")
	before := f.remoteCount()

	ok, reason := f.e.workflowPreMerge(context.Background(), f.mr)
	if ok || !strings.Contains(reason, "review.md") {
		t.Fatalf("ok=%v reason=%q, want refused on review", ok, reason)
	}
	if f.remoteCount() != before || f.provider.checksCalls != 0 {
		t.Fatal("refused gate still bumped or read CI")
	}
	if f.step() != "merge" {
		t.Fatalf("step = %s, want merge", f.step())
	}
}

func TestWorkflowPreMerge_RequiresMergeStep(t *testing.T) {
	f := newGatedFixture(t)
	if err := f.env.Store.Transition(f.item.ID, "merge", "execute", "test"); err != nil {
		t.Fatal(err)
	}
	before := f.remoteCount()
	ok, reason := f.e.workflowPreMerge(context.Background(), f.mr)
	if ok || !strings.Contains(reason, "not merge") {
		t.Fatalf("ok=%v reason=%q, want refused on step", ok, reason)
	}
	if f.remoteCount() != before {
		t.Fatal("bumped an item not at merge")
	}
}

func TestWorkflowPreMerge_NoVersionFilesRefused(t *testing.T) {
	f := newGatedFixture(t)
	f.env.Config.VersionFiles = nil
	before := f.remoteCount()
	ok, reason := f.e.workflowPreMerge(context.Background(), f.mr)
	if ok || !strings.Contains(reason, "no version_files configured for gated rig") {
		t.Fatalf("ok=%v reason=%q", ok, reason)
	}
	if f.remoteCount() != before {
		t.Fatal("bumped without version files")
	}
}

func TestWorkflowPreMerge_MergeSuccessMarksDone(t *testing.T) {
	f := newGatedFixture(t)
	f.provider.pending, f.provider.passed = false, true
	f.provider.mergeFunc = func() (string, error) {
		run(t, f.workDir, "git", "checkout", "main")
		run(t, f.workDir, "git", "merge", "--no-ff", "-m", "merge PR", f.branch)
		run(t, f.workDir, "git", "push", "origin", "main")
		return run(t, f.workDir, "git", "rev-parse", "main"), nil
	}

	result := f.e.doMerge(context.Background(), f.mr)
	if !result.Success {
		t.Fatalf("doMerge failed: %s", result.Error)
	}
	if !f.provider.mergeCalled {
		t.Fatal("PR was not merged")
	}
	if f.step() != workflow.StepDone {
		t.Fatalf("step = %s, want done", f.step())
	}
	trs := f.transitions()
	if last := trs[len(trs)-1]; last.From != "merge" || last.To != workflow.StepDone || last.Actor != "refinery" {
		t.Fatalf("last transition = %+v", last)
	}
	if subject := run(t, f.workDir, "git", "log", "-1", "--format=%s", f.mr.CommitSHA); subject != "chore: bump version to 0.25.0" {
		t.Fatalf("merged head subject = %q", subject)
	}
	// Post-merge cleanup closes the item bead the way non-gated merges do.
	if !f.e.HandleMRInfoSuccess(f.mr, result) {
		t.Fatal("HandleMRInfoSuccess failed")
	}
	if got := f.store.issues[f.item.ID].Status; got != beadsdk.StatusClosed {
		t.Fatalf("item bead status = %s, want closed", got)
	}
}

func TestWorkflowPreMerge_GatedRefusesDirectStrategy(t *testing.T) {
	f := newGatedFixture(t)
	f.e.config.MergeStrategy = ""
	f.provider.pending, f.provider.passed = false, true
	before := run(t, f.workDir, "git", "rev-parse", "origin/main")
	result := f.e.doMerge(context.Background(), f.mr)
	if result.Success {
		t.Fatal("gated item merged with direct strategy")
	}
	if after := run(t, f.workDir, "git", "rev-parse", "origin/main"); after != before {
		t.Fatal("origin/main moved")
	}
}

func TestEngineer_LoadConfig_GatedForcesPR(t *testing.T) {
	for name, mq := range map[string]string{
		"no merge_queue": `{"type":"rig","version":1,"name":"test-rig"}`,
		"direct":         `{"type":"rig","version":1,"name":"test-rig","merge_queue":{"merge_strategy":"direct"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, dir, "config.json", mq)
			if err := os.MkdirAll(filepath.Join(dir, "settings"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, dir, "settings/config.json", `{"type":"rig-settings","version":1,"workflow":{"gated":{"enabled":true}}}`)
			e := newTestEngineer(t, dir, gitpkg.NewGit(dir))
			e.rig.Path = dir
			if err := e.LoadConfig(); err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if e.config.MergeStrategy != "pr" || e.prProvider == nil {
				t.Fatalf("strategy %q provider %v, want forced pr", e.config.MergeStrategy, e.prProvider)
			}
			if out := e.output.(interface{ String() string }).String(); strings.Count(out, "forced merge_strategy=pr") != 1 {
				t.Fatalf("output = %q, want one forced line", out)
			}
		})
	}
}

func TestClassifyGHChecks(t *testing.T) {
	for name, tc := range map[string]struct {
		stdout, stderr string
		runErr         error
		passed, pend   bool
		failNames      []string
		wantErr        bool
	}{
		"all success":     {stdout: `[{"name":"a","state":"SUCCESS"},{"name":"b","state":"SKIPPED"},{"name":"c","state":"NEUTRAL"}]`, passed: true},
		"one pending":     {stdout: `[{"name":"a","state":"SUCCESS"},{"name":"b","state":"IN_PROGRESS"}]`, runErr: errors.New("exit 8"), pend: true},
		"queued":          {stdout: `[{"name":"a","state":"QUEUED"}]`, pend: true},
		"failure":         {stdout: `[{"name":"a","state":"FAILURE"},{"name":"b","state":"PENDING"},{"name":"c","state":"CANCELLED"}]`, runErr: errors.New("exit 1"), failNames: []string{"a", "c"}},
		"zero checks":     {stdout: `[]`, pend: true},
		"no checks error": {stderr: "no checks reported on the 'x' branch", runErr: errors.New("exit 1"), pend: true},
		"gh broken":       {stderr: "gh: not authenticated", runErr: errors.New("exit 4"), wantErr: true},
		"garbage":         {stdout: `nope`, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			passed, pending, err := classifyGHChecks([]byte(tc.stdout), []byte(tc.stderr), tc.runErr)
			var failed *ChecksFailedError
			switch {
			case tc.failNames != nil:
				if !errors.As(err, &failed) || strings.Join(failed.Names, ",") != strings.Join(tc.failNames, ",") {
					t.Fatalf("err = %v, want failed %v", err, tc.failNames)
				}
				if passed {
					t.Fatal("failed checks reported passed")
				}
				return
			case tc.wantErr:
				if err == nil || errors.As(err, &failed) {
					t.Fatalf("err = %v, want unknown-state error", err)
				}
				return
			}
			if err != nil || passed != tc.passed || pending != tc.pend {
				t.Fatalf("got passed=%v pending=%v err=%v, want %v %v", passed, pending, err, tc.passed, tc.pend)
			}
		})
	}
}

func TestBitbucketChecksPassedNotSupported(t *testing.T) {
	p := &bitbucketPRProvider{}
	if _, _, err := p.ChecksPassed(&gitpkg.PullRequestInfo{Number: 1}); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("err = %v, want not supported", err)
	}
}

// gatedStore extends prepushStore with an empty SearchIssues, which the
// post-merge cleanup (ListAllOpenMRs) needs.
type gatedStore struct{ *prepushStore }

func (s gatedStore) SearchIssues(context.Context, string, beadsdk.IssueFilter) ([]*beadsdk.Issue, error) {
	return nil, nil
}

func TestProcessBatch_GatedMRUsesGate(t *testing.T) {
	f := newGatedFixture(t)
	createFeatureBranch(t, f.workDir, "feature-b", "b.txt", "b\n")
	commitB := run(t, f.workDir, "git", "rev-parse", "feature-b")
	f.store.issues["gt-src-b"] = prepushIssue("gt-src-b", "")
	f.store.issues["gt-mr-b"] = prepushMRIssue("gt-mr-b", "feature-b", "main", "gt-src-b", commitB)
	// feature-b is not gated and merges via the (fake) PR; the gated MR stays pending.
	f.provider.mergeFunc = func() (string, error) {
		run(t, f.workDir, "git", "checkout", "main")
		run(t, f.workDir, "git", "merge", "--no-ff", "-m", "merge PR", "feature-b")
		run(t, f.workDir, "git", "push", "origin", "main")
		return run(t, f.workDir, "git", "rev-parse", "main"), nil
	}
	mrB := &MRInfo{ID: "gt-mr-b", Branch: "feature-b", Target: "main", SourceIssue: "gt-src-b", CommitSHA: commitB}
	f.e.ProcessBatch(context.Background(), []*MRInfo{f.mr, mrB}, "main", DefaultBatchConfig())
	if f.provider.checksCalls == 0 {
		t.Fatal("gated MR in a batch skipped the merge gate")
	}
	if f.step() != "merge" {
		t.Fatalf("step = %s, want merge (CI pending)", f.step())
	}
	if err := gitpkg.NewGit(f.workDir).VerifyPushedCommitReachableFromPushTarget("origin", "main", f.mr.CommitSHA); err == nil {
		t.Fatal("gated MR merged while CI pending")
	}
}

// failCheckStore makes AddCheck fail.
type failCheckStore struct{ workflow.Store }

func (failCheckStore) AddCheck(workflow.CheckRun) error { return errors.New("dolt: write failed") }

func (f *gatedFixture) mergeViaFakePR() {
	f.provider.pending, f.provider.passed = false, true
	f.provider.mergeFunc = func() (string, error) {
		run(f.t, f.workDir, "git", "checkout", "main")
		run(f.t, f.workDir, "git", "merge", "--no-ff", "-m", "merge PR", f.branch)
		run(f.t, f.workDir, "git", "push", "origin", "main")
		return run(f.t, f.workDir, "git", "rev-parse", "main"), nil
	}
}

func TestProcessBatch_GatedSuccessClosesBeads(t *testing.T) {
	f := newGatedFixture(t)
	f.mergeViaFakePR()
	submitted := f.mr.CommitSHA

	res := f.e.ProcessBatch(context.Background(), []*MRInfo{f.mr}, "main", DefaultBatchConfig())
	if res.Error != nil || len(res.Merged) != 1 {
		t.Fatalf("batch result = %+v (err %v), want one merged", res, res.Error)
	}
	if f.mr.CommitSHA == submitted {
		t.Fatal("caller's MR did not adopt the bump head")
	}
	if f.step() != workflow.StepDone {
		t.Fatalf("step = %s, want done", f.step())
	}
	if got := f.store.issues["gt-mr"].Status; got != beadsdk.StatusClosed {
		t.Fatalf("MR bead status = %s, want closed", got)
	}
	if reason := f.store.closeReasons["gt-mr"]; reason != string(CloseReasonMerged) {
		t.Fatalf("MR close reason = %q, want merged", reason)
	}
	if got := f.store.issues[f.item.ID].Status; got != beadsdk.StatusClosed {
		t.Fatalf("item bead status = %s, want closed", got)
	}
}

func TestWorkflowPreMerge_MalformedSettingsPausesMerges(t *testing.T) {
	workDir, _, _ := testGitRepo(t)
	createFeatureBranch(t, workDir, "feature", "a.txt", "a\n")
	head := run(t, workDir, "git", "rev-parse", "feature")
	store := newPrepushStore(prepushIssue("gt-src", ""), prepushMRIssue("gt-mr", "feature", "main", "gt-src", head))
	e := newPrepushEngineer(t, workDir, store)
	rigDir := t.TempDir()
	e.rig.Path = rigDir
	if err := os.MkdirAll(filepath.Join(rigDir, "settings"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, rigDir, "settings/config.json", `{"workflow": {`)

	mr := &MRInfo{ID: "gt-mr", Branch: "feature", Target: "main", SourceIssue: "gt-src", CommitSHA: head}
	ok, reason := e.workflowPreMerge(context.Background(), mr)
	if ok {
		t.Fatal("malformed settings passed through")
	}
	if !strings.Contains(reason, "merges paused until settings parse") || !strings.Contains(reason, filepath.Join(rigDir, "settings", "config.json")) {
		t.Fatalf("reason = %q, want settings path and pause message", reason)
	}
	if err := e.LoadConfig(); err == nil || !strings.Contains(err.Error(), "merges paused until settings parse") {
		t.Fatalf("LoadConfig err = %v, want pause message", err)
	}
	if store.issues["gt-mr"].Status != beadsdk.StatusOpen {
		t.Fatal("MR closed on settings error")
	}
}

func TestWorkflowPreMerge_CIFailedEvidenceWriteFailsRefuses(t *testing.T) {
	f := newGatedFixture(t)
	f.provider.pending = false
	f.provider.checksErr = &ChecksFailedError{Names: []string{"test"}}
	f.env.Store = failCheckStore{f.env.Store}

	res := f.e.workflowGate(context.Background(), f.mr)
	if res.OK || res.Rejected || res.Result.NoMerge || !strings.Contains(res.Reason, "recording CI failure") {
		t.Fatalf("result = %+v, want refusal (retry)", res)
	}
	if f.step() != "merge" || len(f.transitions()) != 1 {
		t.Fatalf("step %s transitions %+v, want unchanged", f.step(), f.transitions())
	}
	if f.store.issues["gt-mr"].Status != beadsdk.StatusOpen {
		t.Fatal("MR closed without CI evidence")
	}
}

func TestWorkflowPreMerge_ReadyRecordsCIPassForHead(t *testing.T) {
	f := newGatedFixture(t)
	f.provider.pending, f.provider.passed = false, true
	d := f.e.gateDecision(context.Background(), f.mr)
	if d.Result != GateReady || !d.Gated || d.HeadSHA != f.mr.CommitSHA || d.Version != "0.25.0" {
		t.Fatalf("decision = %+v", d)
	}
	checks, _ := f.env.Store.Checks(f.item.ID)
	if len(checks) != 1 || !checks[0].Passed || checks[0].CmdSetSHA != "ci" || checks[0].Tail != "head "+d.HeadSHA || checks[0].Source != "gtb" || checks[0].Provenance != "verified" {
		t.Fatalf("checks = %+v", checks)
	}
	if f.provider.mergeCalled || f.step() != "merge" {
		t.Fatal("gate decision merged or moved the item")
	}
}

func TestWorkflowPreMerge_VersionMovedRefusesSecondBump(t *testing.T) {
	f := newGatedFixture(t)
	if ok, _ := f.e.workflowPreMerge(context.Background(), f.mr); ok {
		t.Fatal("pending run returned ok")
	}
	count := f.remoteCount()
	// Someone else lands a minor bump on main: target is now 0.25.0, which
	// the branch's bump commit already claims.
	run(t, f.workDir, "git", "checkout", "main")
	writeFile(t, f.workDir, "package.json", "{\n  \"name\": \"tbb\",\n  \"version\": \"0.25.0\"\n}\n")
	writeFile(t, f.workDir, ".claude-plugin/plugin.json", "{\"name\":\"tbb\",\"version\":\"0.25.0\"}\n")
	run(t, f.workDir, "git", "commit", "-am", "chore: bump version to 0.25.0")
	run(t, f.workDir, "git", "push", "origin", "main")

	mr2 := *f.mr
	ok, reason := f.e.workflowPreMerge(context.Background(), &mr2)
	if ok || !strings.Contains(reason, "rebase onto main (version moved)") {
		t.Fatalf("ok=%v reason=%q, want version moved", ok, reason)
	}
	if got := f.remoteCount(); got != count {
		t.Fatalf("commit count %s -> %s, want no second bump", count, got)
	}
}

func TestWorkflowPreMerge_CITailRuneSafe(t *testing.T) {
	f := newGatedFixture(t)
	f.provider.pending = false
	f.provider.checksErr = &ChecksFailedError{Names: []string{strings.Repeat("a", 999) + "é"}}
	f.e.workflowGate(context.Background(), f.mr)
	checks, _ := f.env.Store.Checks(f.item.ID)
	if len(checks) != 1 || !utf8.ValidString(checks[0].Tail) || len(checks[0].Tail) > 1000 {
		t.Fatalf("tail = %q (len %d)", checks[0].Tail, len(checks[0].Tail))
	}
}
