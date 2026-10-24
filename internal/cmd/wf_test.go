package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/workflow"
)

// wfTestEnv is the in-memory workflow environment injected through
// newWorkflowEnv for command tests. Nothing here touches Dolt, bd or git.
type wfTestEnv struct {
	env          workflow.GateEnv
	checkout     string
	gotRig       string
	realCheckout func(string, string) (string, error)
}

func useMemWorkflowEnv(t *testing.T) *wfTestEnv {
	t.Helper()
	town := t.TempDir()
	w := &wfTestEnv{
		env: workflow.GateEnv{
			TownRoot: town,
			Rig:      "r1",
			Store:    workflow.NewMemStore(),
			Items:    workflow.NewMemItems(),
			Config:   &config.GatedWorkflow{Enabled: true},
			Now:      func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
		},
		checkout: t.TempDir(),
	}
	oldEnv, oldDir, oldActor, oldBranch := newWorkflowEnv, workflowCheckoutDir, workflowActor, workflowBranch
	w.realCheckout = oldDir
	newWorkflowEnv = func(_ *cobra.Command, rig string) (workflow.GateEnv, error) {
		w.gotRig = rig
		return w.env, nil
	}
	workflowCheckoutDir = func(string, string) (string, error) { return w.checkout, nil }
	workflowActor = func() string { return "tester" }
	workflowBranch = func(string) string { return "polecat/x" }
	t.Cleanup(func() {
		newWorkflowEnv, workflowCheckoutDir, workflowActor, workflowBranch = oldEnv, oldDir, oldActor, oldBranch
	})
	return w
}

// runWF runs args against a fresh root holding the workflow commands and
// returns stdout, stderr and the exit code main would use.
func runWF(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	root := &cobra.Command{Use: "gtb", SilenceUsage: true}
	root.AddGroup(&cobra.Group{ID: GroupWork, Title: "Work Management:"})
	root.AddCommand(newWorkflowCommands()...)
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetArgs(args)
	err := root.Execute()
	code := 0
	if err != nil {
		code = 1
		if c, ok := IsSilentExit(err); ok {
			code = c
		}
	}
	return out.String(), errb.String(), code
}

func clearAgentMarkers(t *testing.T) {
	t.Helper()
	for _, k := range workflow.AgentMarkerEnv {
		t.Setenv(k, "")
	}
}

func newItemID(t *testing.T, kind, title string) string {
	t.Helper()
	out, errOut, code := runWF(t, "item", "new", kind, title, "--json")
	if code != 0 {
		t.Fatalf("item new exit %d: %s %s", code, out, errOut)
	}
	var v struct{ ID string }
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.ID == "" {
		t.Fatalf("item new json %q: %v", out, err)
	}
	return v.ID
}

// bindBranch binds item id to the fake checkout's branch ("polecat/x"), as
// sling does when it spawns the item's polecat (R19).
func bindBranch(t *testing.T, w *wfTestEnv, id string) {
	t.Helper()
	if err := w.env.Items.SetBranch(id, "polecat/x"); err != nil {
		t.Fatal(err)
	}
}

type conflictStore struct{ workflow.Store }

func (conflictStore) Transition(string, workflow.Step, workflow.Step, string) error {
	return workflow.ErrStepConflict
}

func approveArtifact(t *testing.T, w *wfTestEnv, id, name string) {
	t.Helper()
	_, sha, _, err := workflow.ReadArtifact(workflow.ArtifactPath(w.env.TownRoot, w.env.Rig, id, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := w.env.Store.AddApproval(workflow.Approval{
		ItemID: id, Artifact: name, SHA: sha, Verdict: "approve",
		Role: "human", Source: "human", Channel: "terminal", Actor: "h", At: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestAdvanceCmd_ExitCodes(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")

	out, _, code := runWF(t, "advance", id)
	if code != 3 {
		t.Fatalf("refused gate: exit %d, want 3; out=%q", code, out)
	}
	if !strings.Contains(out, "- spec.md not approved") {
		t.Errorf("reasons not printed one per line: %q", out)
	}
	if !strings.Contains(out, "waiting for: approve spec") {
		t.Errorf("waiting line missing: %q", out)
	}

	approveArtifact(t, w, id, "spec")
	if out, errOut, code := runWF(t, "advance", id); code != 0 {
		t.Fatalf("approved gate: exit %d, want 0; %q %q", code, out, errOut)
	}
	it, _ := w.env.Items.Get(id)
	if it.Step != "plan" {
		t.Fatalf("step = %s, want plan", it.Step)
	}

	id2 := newItemID(t, "task", "y")
	approveArtifact(t, w, id2, "spec")
	w.env.Store = conflictStore{w.env.Store}
	if out, errOut, code := runWF(t, "advance", id2); code != 4 {
		t.Fatalf("conflict: exit %d, want 4; %q %q", code, out, errOut)
	}
}

func TestAdvanceCmd_JSON(t *testing.T) {
	useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	out, _, code := runWF(t, "advance", id, "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	var v struct {
		Moved bool
		Gate  struct {
			Passed          bool
			Reasons         []string
			WaitingForHuman string `json:"waiting_for_human"`
		}
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not one JSON object: %q: %v", out, err)
	}
	if v.Moved || v.Gate.Passed || v.Gate.WaitingForHuman != "approve spec" || len(v.Gate.Reasons) == 0 {
		t.Errorf("unexpected advance json: %+v", v)
	}
}

func TestApproveCmd_RefusedUnderAgentEnv(t *testing.T) {
	useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	t.Setenv("GT_ROLE", "polecat")
	for _, verb := range []string{"approve", "request-changes"} {
		_, errOut, code := runWF(t, verb, id, "spec")
		if code != 1 {
			t.Errorf("%s: exit %d, want 1", verb, code)
		}
		if strings.TrimSpace(errOut) != "approvals must come from a human" {
			t.Errorf("%s: stderr = %q", verb, errOut)
		}
	}
}

func TestApproveChecks_RefusedUnderAgentEnv(t *testing.T) {
	w := useMemWorkflowEnv(t)
	w.env.Config.CheckCommands = []string{"go test ./..."}
	t.Setenv("CLAUDECODE", "1")
	_, errOut, code := runWF(t, "approve-checks")
	if code != 1 || strings.TrimSpace(errOut) != "approvals must come from a human" {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
}

type yesTerminal struct{ prompts []string }

func (y *yesTerminal) IsTTY() bool { return true }
func (y *yesTerminal) Confirm(p string) (bool, error) {
	y.prompts = append(y.prompts, p)
	return true, nil
}

func TestApproveChecks_WritesSHA(t *testing.T) {
	w := useMemWorkflowEnv(t)
	clearAgentMarkers(t)
	cmds := []string{"go test ./...", "go vet ./..."}
	rigPath := filepath.Join(w.env.TownRoot, w.env.Rig)
	settings := config.NewRigSettings()
	settings.Workflow = &config.WorkflowConfig{Gated: &config.GatedWorkflow{Enabled: true, CheckCommands: cmds}}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), settings); err != nil {
		t.Fatal(err)
	}
	w.env.Config.CheckCommands = cmds
	term := &yesTerminal{}
	oldTerm := workflowTerminal
	workflowTerminal = func(io.Writer) workflow.Terminal { return term }
	t.Cleanup(func() { workflowTerminal = oldTerm })

	out, errOut, code := runWF(t, "approve-checks")
	if code != 0 {
		t.Fatalf("exit %d: %q %q", code, out, errOut)
	}
	sha := workflow.CheckCommandsSHA(cmds)
	want := "Approve check commands sha " + sha[:12] + " for rig r1? [y/N]"
	if len(term.prompts) != 1 || term.prompts[0] != want {
		t.Errorf("prompts = %q, want %q", term.prompts, want)
	}
	got, err := config.LoadRigSettings(config.RigSettingsPath(rigPath))
	if err != nil {
		t.Fatal(err)
	}
	if got.Workflow.Gated.CheckCommandsApprovedSHA != sha {
		t.Errorf("approved sha = %q, want %q", got.Workflow.Gated.CheckCommandsApprovedSHA, sha)
	}
}

func TestItemNew_WritesStub(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "Add a thing")
	path := workflow.ArtifactPath(w.env.TownRoot, w.env.Rig, id, "spec")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("stub missing: %v", err)
	}
	s := string(data)
	for _, want := range []string{"id: " + id + "\n", "status: draft\n", "kind: task\n", "# Add a thing\n"} {
		if !strings.Contains(s, want) {
			t.Errorf("stub missing %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "parent:") {
		t.Errorf("parent written though unset:\n%s", s)
	}
	fi, err := os.Stat(filepath.Dir(path))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("item dir mode = %v, want 0700 (%v)", fi.Mode().Perm(), err)
	}
}

func TestItemNew_ParentAndKinds(t *testing.T) {
	w := useMemWorkflowEnv(t)
	epic := newItemID(t, "epic", "Big")
	if _, err := os.Stat(workflow.ArtifactPath(w.env.TownRoot, w.env.Rig, epic, "epic")); err != nil {
		t.Errorf("epic stub: %v", err)
	}
	out, errOut, code := runWF(t, "item", "new", "fix", "Broken", "--parent", epic, "--json")
	if code != 0 {
		t.Fatalf("exit %d %q %q", code, out, errOut)
	}
	var v struct{ ID string }
	_ = json.Unmarshal([]byte(out), &v)
	data, err := os.ReadFile(workflow.ArtifactPath(w.env.TownRoot, w.env.Rig, v.ID, "report"))
	if err != nil || !strings.Contains(string(data), "parent: "+epic+"\n") {
		t.Errorf("fix stub %q: %v", data, err)
	}
}

func TestItemNew_RejectsBadKindAndRig(t *testing.T) {
	w := useMemWorkflowEnv(t)
	called := false
	newWorkflowEnv = func(*cobra.Command, string) (workflow.GateEnv, error) { called = true; return w.env, nil }

	_, errOut, code := runWF(t, "item", "new", "story", "x")
	if code != 1 || !strings.Contains(errOut, `unknown kind "story"`) {
		t.Errorf("bad kind: exit %d %q", code, errOut)
	}
	_, errOut, code = runWF(t, "item", "new", "task", "x", "--rig", "../evil")
	if code != 1 || !strings.Contains(errOut, `invalid rig name "../evil"`) {
		t.Errorf("bad rig: exit %d %q", code, errOut)
	}
	if called {
		t.Error("environment built before validation")
	}
}

func TestWorkflowCmds_DisabledRig(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	w.env.Config.Enabled = false
	want := "gated workflow is not enabled for rig r1 (set workflow.gated.enabled in settings/config.json)"
	for _, args := range [][]string{{"advance", id}, {"item", "new", "task", "y"}, {"verdict", id, "spec", "eng", "approve"}, {"check", id}} {
		_, errOut, code := runWF(t, args...)
		if code != 1 || !strings.Contains(errOut, want) {
			t.Errorf("%v: exit %d %q", args, code, errOut)
		}
	}
	if _, errOut, code := runWF(t, "item", "show", id); code != 0 {
		t.Errorf("item show on disabled rig: exit %d %q", code, errOut)
	}
	if _, errOut, code := runWF(t, "item", "list"); code != 0 {
		t.Errorf("item list on disabled rig: exit %d %q", code, errOut)
	}
}

func TestItemShow_JSON(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	out, errOut, code := runWF(t, "item", "show", id, "--json")
	if code != 0 {
		t.Fatalf("exit %d %q", code, errOut)
	}
	var v struct {
		ID              string `json:"id"`
		Rig             string `json:"rig"`
		Kind            string `json:"kind"`
		Step            string `json:"step"`
		Title           string `json:"title"`
		Parent          string `json:"parent"`
		WaitingForHuman string `json:"waiting_for_human"`
		Gate            struct {
			Passed          bool     `json:"passed"`
			Reasons         []string `json:"reasons"`
			WaitingForHuman string   `json:"waiting_for_human"`
		} `json:"gate"`
		ArtifactPath string `json:"artifact_path"`
		Artifacts    []struct {
			Name   string `json:"name"`
			Path   string `json:"path"`
			SHA    string `json:"sha"`
			Exists bool   `json:"exists"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("not one JSON object: %q: %v", out, err)
	}
	specPath := workflow.ArtifactPath(w.env.TownRoot, "r1", id, "spec")
	if v.ID != id || v.Rig != "r1" || v.Kind != "task" || v.Step != "spec" || v.Title != "x" {
		t.Errorf("header fields: %+v", v)
	}
	if v.ArtifactPath != specPath {
		t.Errorf("artifact_path = %q, want %q", v.ArtifactPath, specPath)
	}
	if v.Gate.Passed || v.Gate.WaitingForHuman != "approve spec" {
		t.Errorf("gate = %+v", v.Gate)
	}
	names := []string{}
	for _, a := range v.Artifacts {
		names = append(names, a.Name)
		if a.Name == "spec" && (!a.Exists || len(a.SHA) != 64 || a.Path != specPath) {
			t.Errorf("spec artifact: %+v", a)
		}
		if a.Name == "plan" && (a.Exists || a.SHA != "") {
			t.Errorf("plan artifact: %+v", a)
		}
	}
	if strings.Join(names, ",") != "spec,plan,review" {
		t.Errorf("artifacts = %v", names)
	}

	// At a step without an artifact, artifact_path is empty.
	approveArtifact(t, w, id, "spec")
	if _, _, code := runWF(t, "advance", id); code != 0 {
		t.Fatal("advance to plan")
	}
	if err := os.WriteFile(workflow.ArtifactPath(w.env.TownRoot, "r1", id, "plan"), []byte("---\nstatus: draft\n---\n- [ ] step-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	approveArtifact(t, w, id, "plan")
	if out, _, code := runWF(t, "advance", id); code != 0 {
		t.Fatalf("advance to execute: %q", out)
	}
	out, _, _ = runWF(t, "item", "show", id, "--json")
	v.ArtifactPath = "unset"
	_ = json.Unmarshal([]byte(out), &v)
	if v.Step != "execute" || v.ArtifactPath != "" {
		t.Errorf("execute step: step=%s artifact_path=%q", v.Step, v.ArtifactPath)
	}
}

func TestItemList_JSON(t *testing.T) {
	useMemWorkflowEnv(t)
	a := newItemID(t, "task", "a")
	b := newItemID(t, "fix", "b")
	out, _, code := runWF(t, "item", "list", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var v struct {
		Rig   string `json:"rig"`
		Items []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatal(err)
	}
	if v.Rig != "r1" || len(v.Items) != 2 || v.Items[0].ID != a || v.Items[1].ID != b {
		t.Errorf("list = %+v", v)
	}
}

func TestVerdictCmd_Records(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	if out, errOut, code := runWF(t, "verdict", id, "spec", "eng", "concerns"); code != 0 {
		t.Fatalf("exit %d %q %q", code, out, errOut)
	}
	as, _ := w.env.Store.Approvals(id)
	if len(as) != 1 || as[0].Role != "eng" || as[0].Verdict != "concerns" || as[0].Actor != "tester" {
		t.Errorf("approvals = %+v", as)
	}
	if _, _, code := runWF(t, "verdict", id, "spec", "human", "approve"); code != 1 {
		t.Errorf("human role verdict: exit %d, want 1", code)
	}
}

func TestRedCmd_NonMatchingGlob(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	bindBranch(t, w, id)
	_, errOut, code := runWF(t, "red", id, "step-1", "--tests", "nope/*_test.go", "--", "go", "test")
	if code != 1 || !strings.Contains(errOut, `--tests glob "nope/*_test.go" matched no files`) {
		t.Fatalf("exit %d %q", code, errOut)
	}
}

type scriptRunner struct {
	exits []int
	calls [][]string
}

func (s *scriptRunner) Run(_ context.Context, _ string, argv []string) (int, string, error) {
	s.calls = append(s.calls, argv)
	e := s.exits[0]
	s.exits = s.exits[1:]
	return e, "tail-output", nil
}

func TestRedGreenCmd_Flow(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	bindBranch(t, w, id)
	if err := os.WriteFile(filepath.Join(w.checkout, "a_test.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &scriptRunner{exits: []int{0, 1, 0}}
	oldRunner := workflowRunner
	workflowRunner = func(workflow.GateEnv, string) workflow.Runner { return r }
	t.Cleanup(func() { workflowRunner = oldRunner })

	// A passing red is refused: exit 3 with the tail.
	out, _, code := runWF(t, "red", id, "step-1", "--tests", "*_test.go", "--", "go", "test", "./...")
	if code != 3 || !strings.Contains(out, "test passed before implementation") {
		t.Fatalf("refused red: exit %d %q", code, out)
	}
	if out, errOut, code := runWF(t, "red", id, "step-1", "--tests", "*_test.go", "--", "go", "test", "./..."); code != 0 {
		t.Fatalf("red: exit %d %q %q", code, out, errOut)
	}
	if out, errOut, code := runWF(t, "green", id, "step-1"); code != 0 {
		t.Fatalf("green: exit %d %q %q", code, out, errOut)
	}
	if got := strings.Join(r.calls[1], " "); got != "go test ./..." {
		t.Errorf("argv = %q", got)
	}
	runs, _ := w.env.Store.TDD(id)
	if len(runs) != 3 || runs[1].Phase != "red" || runs[2].Phase != "green" || strings.Join(runs[1].TestFiles, ",") != "a_test.go" {
		t.Errorf("runs = %+v", runs)
	}
}

func TestCheckCmd_NotApproved(t *testing.T) {
	w := useMemWorkflowEnv(t)
	id := newItemID(t, "task", "x")
	bindBranch(t, w, id)
	w.env.Config.CheckCommands = []string{"true"}
	out, _, code := runWF(t, "check", id)
	if code != 3 || !strings.Contains(out, "waiting for: approve check commands") {
		t.Fatalf("exit %d %q", code, out)
	}
}

func TestWorkflowCommandsRegistered(t *testing.T) {
	for _, name := range []string{"item", "advance", "approve", "request-changes", "approve-checks", "verdict", "red", "green", "check"} {
		n := 0
		for _, c := range rootCmd.Commands() {
			if c.Name() == name {
				n++
				if c.GroupID != GroupWork {
					t.Errorf("%s group = %q, want %q", name, c.GroupID, GroupWork)
				}
			}
		}
		if n != 1 {
			t.Errorf("top-level %q registered %d times, want 1", name, n)
		}
	}
}

// ---- fix round 1 ----

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init %s: %v %s", dir, err, out)
	}
}

func TestResolveCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	town := t.TempDir()
	rigPath := filepath.Join(town, "r1")
	gitInit(t, rigPath) // adopted rig whose root is a repo
	gitInit(t, filepath.Join(rigPath, "mayor", "rig"))
	gitInit(t, filepath.Join(rigPath, "refinery", "rig"))
	polecat := filepath.Join(rigPath, "polecats", "p1", "r1")
	gitInit(t, polecat)
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	for name, cwd := range map[string]string{
		"rig root":     rigPath,
		"mayor/rig":    filepath.Join(rigPath, "mayor", "rig"),
		"refinery/rig": filepath.Join(rigPath, "refinery", "rig"),
		"outside rig":  plain,
	} {
		if dir, err := resolveCheckout(town, "r1", cwd); err == nil || !strings.Contains(err.Error(), "run from the item's worktree (polecat or crew checkout); got ") {
			t.Errorf("%s: dir=%q err=%v, want refusal", name, dir, err)
		}
	}
	sub := filepath.Join(polecat, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := resolveCheckout(town, "r1", sub)
	if err != nil || !samePath(dir, polecat) {
		t.Errorf("polecat worktree: dir=%q err=%v, want %s", dir, err, polecat)
	}
}

func TestPathWithin(t *testing.T) {
	d := t.TempDir()
	for _, c := range []struct {
		dir, p string
		want   bool
	}{
		{d, d, true},
		{d, filepath.Join(d, "a", "b"), true},
		{filepath.Join(d, "a"), filepath.Join(d, "ab"), false},
		{filepath.Join(d, "a"), d, false},
	} {
		if got := pathWithin(c.dir, c.p); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v", c.dir, c.p, got)
		}
	}
}

// TestGitCommands_RequireWorktree runs red/green/check and advance-at-fix
// through the real checkout resolution from the rig root and mayor/rig
// (refused) and from a polecat worktree (accepted).
func TestGitCommands_RequireWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w := useMemWorkflowEnv(t)
	workflowCheckoutDir = w.realCheckout
	w.env.Config.CheckCommands = []string{"true"}
	w.env.Config.CheckCommandsApprovedSHA = workflow.CheckCommandsSHA(w.env.Config.CheckCommands)
	// Create items before the rig root becomes a repo (item new refuses
	// artifact dirs inside a worktree).
	id := newItemID(t, "task", "x")
	fix := newItemID(t, "fix", "f")
	bindBranch(t, w, id)
	bindBranch(t, w, fix)
	rigPath := filepath.Join(w.env.TownRoot, "r1")
	gitInit(t, rigPath)
	mayor := filepath.Join(rigPath, "mayor", "rig")
	gitInit(t, mayor)
	polecat := filepath.Join(rigPath, "polecats", "p1", "r1")
	gitInit(t, polecat)
	if err := os.WriteFile(filepath.Join(polecat, "a_test.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &scriptRunner{exits: []int{1, 0, 0, 0}} // red, green, check, check
	oldRunner := workflowRunner
	workflowRunner = func(workflow.GateEnv, string) workflow.Runner { return r }
	t.Cleanup(func() { workflowRunner = oldRunner })

	for _, tr := range [][2]workflow.Step{{"report", "reproduce"}, {"reproduce", "rca"}, {"rca", "fix"}} {
		if err := w.env.Store.Transition(fix, tr[0], tr[1], "t"); err != nil {
			t.Fatal(err)
		}
	}
	cmds := [][]string{
		{"red", id, "step-1", "--tests", "*_test.go", "--", "go", "test"},
		{"green", id, "step-1"},
		{"check", id},
		{"advance", fix},
	}
	for _, cwd := range []string{rigPath, mayor} {
		t.Chdir(cwd)
		for _, args := range cmds {
			_, errOut, code := runWF(t, args...)
			if code != 1 || !strings.Contains(errOut, "run from the item's worktree (polecat or crew checkout); got ") {
				t.Errorf("cwd %s %v: exit %d %q", cwd, args[0], code, errOut)
			}
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("runner called from a refused cwd: %v", r.calls)
	}

	t.Chdir(polecat)
	for _, args := range cmds[:3] {
		// --json goes first: anything after "--" is red's argv.
		out, errOut, code := runWF(t, append([]string{args[0], "--json"}, args[1:]...)...)
		if code != 0 {
			t.Fatalf("%v from polecat: exit %d %q %q", args[0], code, out, errOut)
		}
		var v struct {
			Checkout struct{ Dir, Branch string }
		}
		if err := json.Unmarshal([]byte(out), &v); err != nil || !samePath(v.Checkout.Dir, polecat) || v.Checkout.Branch != "polecat/x" {
			t.Errorf("%v checkout = %+v (%v)", args[0], v.Checkout, err)
		}
	}
	out, _, code := runWF(t, "check", id)
	if code != 0 || !strings.Contains(out, "checkout: ") || !strings.Contains(out, "(polecat/x)") {
		t.Errorf("human checkout line: exit %d %q", code, out)
	}
	// advance at fix is accepted from the worktree (the gate itself then
	// refuses on missing repro evidence, which is exit 3 not 1).
	out, errOut, code := runWF(t, "advance", fix, "--json")
	if code != 3 || !strings.Contains(out, `"checkout"`) {
		t.Errorf("advance at fix from polecat: exit %d %q %q", code, out, errOut)
	}
}

// TestGitCommands_RequireItemBranch (R19): git-dependent commands refuse an
// item with no bound worktree branch, and a checkout on a different branch.
func TestGitCommands_RequireItemBranch(t *testing.T) {
	w := useMemWorkflowEnv(t)
	w.env.Config.CheckCommands = []string{"true"}
	w.env.Config.CheckCommandsApprovedSHA = workflow.CheckCommandsSHA(w.env.Config.CheckCommands)
	if err := os.WriteFile(filepath.Join(w.checkout, "a_test.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &scriptRunner{}
	oldRunner := workflowRunner
	workflowRunner = func(workflow.GateEnv, string) workflow.Runner { return r }
	t.Cleanup(func() { workflowRunner = oldRunner })
	id := newItemID(t, "task", "x")
	fix := newItemID(t, "fix", "f")
	for _, tr := range [][2]workflow.Step{{"report", "reproduce"}, {"reproduce", "rca"}, {"rca", "fix"}} {
		if err := w.env.Store.Transition(fix, tr[0], tr[1], "t"); err != nil {
			t.Fatal(err)
		}
	}
	cmds := func() [][]string {
		return [][]string{
			{"red", id, "step-1", "--tests", "*_test.go", "--", "go", "test"},
			{"green", id, "step-1"},
			{"check", id},
			{"advance", fix},
		}
	}

	for _, args := range cmds() {
		item := args[1]
		_, errOut, code := runWF(t, args...)
		want := "item " + item + " has no worktree; sling it to a polecat first"
		if code != 1 || !strings.Contains(errOut, want) {
			t.Errorf("no branch %v: exit %d %q, want %q", args[0], code, errOut, want)
		}
	}

	for _, it := range []string{id, fix} {
		if err := w.env.Items.SetBranch(it, "polecat/other"); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range cmds() {
		item := args[1]
		_, errOut, code := runWF(t, args...)
		want := "checkout branch polecat/x is not item " + item + "'s branch polecat/other"
		if code != 1 || !strings.Contains(errOut, want) {
			t.Errorf("wrong branch %v: exit %d %q, want %q", args[0], code, errOut, want)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("runner called for a refused checkout: %v", r.calls)
	}
}

// TestAdvance_NonFixStepNeedsNoCheckout: advance at other steps never
// resolves a checkout.
func TestAdvance_NonFixStepNeedsNoCheckout(t *testing.T) {
	w := useMemWorkflowEnv(t)
	workflowCheckoutDir = func(string, string) (string, error) {
		t.Error("checkout resolved for a non-fix step")
		return "", errNotWorktree("x")
	}
	id := newItemID(t, "task", "x")
	approveArtifact(t, w, id, "spec")
	if out, errOut, code := runWF(t, "advance", id); code != 0 || strings.Contains(out, "checkout:") {
		t.Errorf("exit %d %q %q", code, out, errOut)
	}
}

// TestFixGate_NilGitFails: with no Git source the fix gate refuses rather
// than passing the changed-files budget.
func TestFixGate_NilGitFails(t *testing.T) {
	w := useMemWorkflowEnv(t)
	w.env.Git = nil
	fix := newItemID(t, "fix", "f")
	for _, tr := range [][2]workflow.Step{{"report", "reproduce"}, {"reproduce", "rca"}, {"rca", "fix"}} {
		if err := w.env.Store.Transition(fix, tr[0], tr[1], "t"); err != nil {
			t.Fatal(err)
		}
	}
	it, _ := w.env.Items.Get(fix)
	res := workflow.Gate(w.env, it)
	if res.Passed || !strings.Contains(strings.Join(res.Reasons, "|"), "changed files: no git source configured") {
		t.Errorf("gate = %+v", res)
	}
}

type recordingTerminal struct{ out io.Writer }

func (r recordingTerminal) IsTTY() bool { return true }
func (r recordingTerminal) Confirm(p string) (bool, error) {
	_, err := io.WriteString(r.out, p+" ")
	return true, err
}

// TestHumanCmds_JSONStdoutOnlyJSON: under --json the prompt goes to stderr
// and stdout holds exactly one JSON object.
func TestHumanCmds_JSONStdoutOnlyJSON(t *testing.T) {
	w := useMemWorkflowEnv(t)
	clearAgentMarkers(t)
	cmds := []string{"true"}
	rigPath := filepath.Join(w.env.TownRoot, w.env.Rig)
	settings := config.NewRigSettings()
	settings.Workflow = &config.WorkflowConfig{Gated: &config.GatedWorkflow{Enabled: true, CheckCommands: cmds}}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), settings); err != nil {
		t.Fatal(err)
	}
	oldTerm := workflowTerminal
	workflowTerminal = func(out io.Writer) workflow.Terminal { return recordingTerminal{out: out} }
	t.Cleanup(func() { workflowTerminal = oldTerm })
	id := newItemID(t, "task", "x")

	for _, args := range [][]string{{"approve", id, "spec"}, {"request-changes", id, "spec"}, {"approve-checks"}} {
		out, errOut, code := runWF(t, append(args, "--json")...)
		if code != 0 {
			t.Fatalf("%v: exit %d %q %q", args, code, out, errOut)
		}
		dec := json.NewDecoder(strings.NewReader(out))
		var v map[string]any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%v: stdout not JSON: %q", args, out)
		}
		if dec.More() {
			t.Errorf("%v: stdout has more than one JSON value: %q", args, out)
		}
		if strings.Contains(out, "[y/N]") || !strings.Contains(errOut, "[y/N]") {
			t.Errorf("%v: prompt on wrong stream: stdout=%q stderr=%q", args, out, errOut)
		}
	}
}

// TestItemNew_RefusesArtifactDirInsideRepo: adopted rigs whose root is a
// repo must not get artifacts written into the repo.
func TestItemNew_RefusesArtifactDirInsideRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w := useMemWorkflowEnv(t)
	gitInit(t, filepath.Join(w.env.TownRoot, "r1"))
	_, errOut, code := runWF(t, "item", "new", "task", "x")
	if code != 1 || !strings.Contains(errOut, "is inside git worktree ") || !strings.Contains(errOut, "; gtb keeps artifacts outside repos") {
		t.Fatalf("exit %d %q", code, errOut)
	}
	if items, _ := w.env.Items.List("r1"); len(items) != 0 {
		t.Errorf("bead created before refusal: %+v", items)
	}
	if _, err := os.Stat(filepath.Join(w.env.TownRoot, "r1", ".gtb")); !os.IsNotExist(err) {
		t.Errorf(".gtb created inside the repo: %v", err)
	}
}

// TestItemNew_AllowsTownRootRepo: an HQ git repo at the town root (gt
// install --git / gt git-init) does not block item new (R18).
func TestItemNew_AllowsTownRootRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w := useMemWorkflowEnv(t)
	gitInit(t, w.env.TownRoot)
	id := newItemID(t, "task", "x")
	if _, err := os.Stat(workflow.ArtifactPath(w.env.TownRoot, w.env.Rig, id, "spec")); err != nil {
		t.Errorf("stub missing: %v", err)
	}
}

func TestHQGitignore_IgnoresGtbArtifacts(t *testing.T) {
	if !strings.Contains(HQGitignore, "\n**/.gtb/\n") {
		t.Error("HQGitignore must ignore **/.gtb/")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	town := t.TempDir()
	gitInit(t, town)
	if err := os.WriteFile(filepath.Join(town, ".gitignore"), []byte(HQGitignore), 0o600); err != nil {
		t.Fatal(err)
	}
	art := filepath.Join("r1", ".gtb", "items", "x-1", "spec.md")
	cmd := exec.Command("git", "-C", town, "check-ignore", "-q", art)
	if err := cmd.Run(); err != nil {
		t.Errorf("%s not ignored by HQGitignore: %v", art, err)
	}
}
