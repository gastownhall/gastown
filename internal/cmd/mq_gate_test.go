package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/refinery"
	"github.com/steveyegge/gastown/internal/workflow"
)

func runMQGateCmd(t *testing.T, d refinery.GateDecision, runErr error, args ...string) (string, int) {
	t.Helper()
	old, oldJSON := mqGateRun, mqGateJSON
	t.Cleanup(func() { mqGateRun, mqGateJSON = old, oldJSON })
	mqGateRun = func(string, string) (refinery.GateDecision, error) { return d, runErr }
	mqGateJSON = false
	cmd := &cobra.Command{Use: "gate", Args: cobra.ExactArgs(2), RunE: runMQGate, SilenceUsage: true, SilenceErrors: true}
	cmd.Flags().BoolVar(&mqGateJSON, "json", false, "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	code := 0
	if err != nil {
		code = 1
		if c, ok := IsSilentExit(err); ok {
			code = c
		}
	}
	return out.String(), code
}

func TestMQGate_ExitCodesAndJSON(t *testing.T) {
	for name, tc := range map[string]struct {
		d      refinery.GateDecision
		err    error
		code   int
		result string
	}{
		"ready":       {d: refinery.GateDecision{Gated: true, Result: "ready", HeadSHA: "abc", Version: "0.25.0"}, code: 0, result: "ready"},
		"passthrough": {d: refinery.GateDecision{Result: "passthrough"}, code: 0, result: "passthrough"},
		"pending":     {d: refinery.GateDecision{Gated: true, Result: "pending", Reason: "CI pending"}, code: 3, result: "pending"},
		"rejected":    {d: refinery.GateDecision{Gated: true, Result: "rejected", Reason: "CI failed"}, code: 5, result: "rejected"},
		"refused":     {d: refinery.GateDecision{Gated: true, Result: "error", Reason: "review not approved"}, code: 1, result: "error"},
		"load error":  {err: errors.New("MR gt-x not found"), code: 1, result: "error"},
	} {
		t.Run(name, func(t *testing.T) {
			out, code := runMQGateCmd(t, tc.d, tc.err, "gastown", "gt-mr", "--json")
			if code != tc.code {
				t.Fatalf("exit = %d, want %d (out %q)", code, tc.code, out)
			}
			var got map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
				t.Fatalf("json %q: %v", out, err)
			}
			if got["result"] != tc.result {
				t.Fatalf("result = %v, want %s", got["result"], tc.result)
			}
			if _, ok := got["gated"]; !ok {
				t.Fatalf("json missing gated: %s", out)
			}
			if tc.err != nil && !strings.Contains(got["reason"].(string), "not found") {
				t.Fatalf("reason = %v", got["reason"])
			}
			if tc.d.HeadSHA != "" && (got["head_sha"] != tc.d.HeadSHA || got["version"] != tc.d.Version) {
				t.Fatalf("json = %s", out)
			}
		})
	}
}

// gatedPostMergeRig sets up a rig dir with the gated workflow enabled and an
// in-memory workflow env holding a task item at step merge.
func gatedPostMergeRig(t *testing.T) (rigPath string, env workflow.GateEnv, mr *refinery.MergeRequest) {
	t.Helper()
	rigPath = filepath.Join(t.TempDir(), "gastown")
	settings := config.NewRigSettings()
	settings.Workflow = &config.WorkflowConfig{Gated: &config.GatedWorkflow{Enabled: true}}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), settings); err != nil {
		t.Fatal(err)
	}
	env = workflow.GateEnv{
		Rig:    "gastown",
		Store:  workflow.NewMemStore(),
		Items:  workflow.NewMemItems(),
		Config: &config.GatedWorkflow{Enabled: true},
		Now:    func() time.Time { return time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC) },
	}
	it, err := env.Items.Create("gastown", workflow.KindTask, "gated", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := env.Store.Transition(it.ID, "review", "merge", "test"); err != nil {
		t.Fatal(err)
	}
	if err := env.Items.SetStep(it.ID, "merge", time.Now()); err != nil {
		t.Fatal(err)
	}
	old := newWorkflowEnv
	newWorkflowEnv = func(_ *cobra.Command, rig string) (workflow.GateEnv, error) {
		if rig != "gastown" {
			t.Errorf("workflow env for rig %q", rig)
		}
		return env, nil
	}
	t.Cleanup(func() { newWorkflowEnv = old })
	mr = testMQPostMergeMR()
	mr.IssueID = it.ID
	return rigPath, env, mr
}

func addCICheck(t *testing.T, env workflow.GateEnv, id string, passed bool, tail string) {
	t.Helper()
	if err := env.Store.AddCheck(workflow.CheckRun{ItemID: id, CmdSetSHA: "ci", Passed: passed, Tail: tail, Source: "gtb", Provenance: "verified", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond) // distinct At for ordering
}

func TestRunVerifiedMQPostMerge_GatedPassRecordsDone(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, prHead: mr.CommitSHA}

	if _, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true); err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge not called for a gated item that passed the gate")
	}
	it, _ := env.Items.Get(mr.IssueID)
	trs, _ := env.Store.Transitions(mr.IssueID)
	if it.Step != workflow.StepDone || trs[len(trs)-1].To != workflow.StepDone || trs[len(trs)-1].Actor != "refinery" {
		t.Fatalf("step %s transitions %+v, want done by refinery", it.Step, trs)
	}
}

func TestRunVerifiedMQPostMerge_GatedRefusals(t *testing.T) {
	for name, setup := range map[string]func(env workflow.GateEnv, mr *refinery.MergeRequest){
		"no CI evidence": func(workflow.GateEnv, *refinery.MergeRequest) {},
		"CI pass for other head": func(env workflow.GateEnv, mr *refinery.MergeRequest) {
			addCICheck(t, env, mr.IssueID, true, "head deadbeef")
		},
		"latest CI failed": func(env workflow.GateEnv, mr *refinery.MergeRequest) {
			addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
			addCICheck(t, env, mr.IssueID, false, "test")
		},
		"not at merge": func(env workflow.GateEnv, mr *refinery.MergeRequest) {
			addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
			_ = env.Store.Transition(mr.IssueID, "merge", "execute", "refinery")
		},
	} {
		t.Run(name, func(t *testing.T) {
			rigPath, env, mr := gatedPostMergeRig(t)
			setup(env, mr)
			mgr := &fakeMQPostMergeManager{mr: mr}
			rigGit := &fakeMQPostMergeGit{remoteTip: mr.CommitSHA, localHead: mr.CommitSHA}
			_, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, false)
			want := "gated item " + mr.IssueID + " did not pass the merge gate (run gtb mq gate)"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want %q", err, want)
			}
			if mgr.postMergeCalled || len(rigGit.deletedBranches) != 0 {
				t.Fatal("post-merge closed or cleaned up a gated item that did not pass the gate")
			}
			if trs, _ := env.Store.Transitions(mr.IssueID); trs[len(trs)-1].To == workflow.StepDone {
				t.Fatal("done recorded on refusal")
			}
		})
	}
}

func TestRunVerifiedMQPostMerge_NonGatedItemUnchanged(t *testing.T) {
	rigPath, _, mr := gatedPostMergeRig(t)
	mr.IssueID = "gt-not-an-item"
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, prHead: mr.CommitSHA}
	if _, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true); err != nil || !mgr.postMergeCalled {
		t.Fatalf("err = %v called = %v, want plain post-merge", err, mgr.postMergeCalled)
	}
}

// moveBackToMerge moves the item merge->execute->merge so a CI pass
// recorded before the call predates the latest transition into merge.
func moveBackToMerge(t *testing.T, env workflow.GateEnv, id string) {
	t.Helper()
	if err := env.Store.Transition(id, "merge", "execute", "refinery"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if err := env.Store.Transition(id, "execute", "merge", "refinery"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
}

func TestRunVerifiedMQPostMerge_GatedStaleCIPassRefused(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	moveBackToMerge(t, env, mr.IssueID)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{remoteTip: mr.CommitSHA, localHead: mr.CommitSHA}

	_, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, false)
	if err == nil || !strings.Contains(err.Error(), "did not pass the merge gate") {
		t.Fatalf("err = %v, want refusal for a CI pass older than the latest merge transition", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("post-merge ran on a stale CI pass")
	}
	if trs, _ := env.Store.Transitions(mr.IssueID); trs[len(trs)-1].To == workflow.StepDone {
		t.Fatal("done recorded on a stale CI pass")
	}
}

func TestRunVerifiedMQPostMerge_GatedFreshCIPassAfterMergeAccepted(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	moveBackToMerge(t, env, mr.IssueID)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, prHead: mr.CommitSHA}

	if _, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true); err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge not called for a gated item with a fresh CI pass")
	}
}

func TestRunVerifiedMQPostMerge_GatedMergedPRHeadMismatchRefused(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, prHead: "h2h2h2h2"}

	_, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true)
	if err == nil || !strings.Contains(err.Error(), "merged PR head") {
		t.Fatalf("err = %v, want refusal for a merged PR head other than the gated head", err)
	}
	if mgr.postMergeCalled {
		t.Fatal("post-merge ran with a merged PR head other than the gated head")
	}
	if trs, _ := env.Store.Transitions(mr.IssueID); trs[len(trs)-1].To == workflow.StepDone {
		t.Fatal("done recorded with a mismatched PR head")
	}
}

func TestRunVerifiedMQPostMerge_GatedMergedPRHeadUnknownFailsClosed(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, lookupErr: errors.New("gh unavailable")}

	if _, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true); err == nil {
		t.Fatal("runVerifiedMQPostMerge succeeded without a determinable merged PR head")
	}
	if mgr.postMergeCalled {
		t.Fatal("post-merge ran without a determinable merged PR head")
	}
}

func TestRunVerifiedMQPostMerge_GatedMergedPRHeadMatchAccepted(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, prHead: mr.CommitSHA}

	if _, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true); err != nil {
		t.Fatalf("runVerifiedMQPostMerge: %v", err)
	}
	if !mgr.postMergeCalled {
		t.Fatal("PostMerge not called when the merged PR head is the gated head")
	}
}

// A PR head moved after the gate is rejected at the lookup level (the ref
// pins the gated head), before the after-the-fact compare runs.
func TestRunVerifiedMQPostMerge_GatedMergedPRHeadPinnedAtLookup(t *testing.T) {
	rigPath, env, mr := gatedPostMergeRig(t)
	addCICheck(t, env, mr.IssueID, true, "head "+mr.CommitSHA)
	mgr := &fakeMQPostMergeManager{mr: mr}
	rigGit := &fakeMQPostMergeGit{localHead: mr.CommitSHA, prHead: "h2h2h2h2"}

	_, _, err := runVerifiedMQPostMerge(mgr, "gastown", rigPath, rigGit, mr.ID, true)
	if err == nil || !strings.Contains(err.Error(), "merged PR head for MR") || !strings.Contains(err.Error(), "head changed") {
		t.Fatalf("err = %v, want lookup-level head rejection naming the merged PR head", err)
	}
	if rigGit.lookupHeadPinned != mr.CommitSHA {
		t.Fatalf("lookup ref HeadSHA = %q, want gated head %s", rigGit.lookupHeadPinned, mr.CommitSHA)
	}
	if mgr.postMergeCalled {
		t.Fatal("post-merge ran with a moved PR head")
	}
}
