package workflow

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
)

type fakeChanged struct {
	files []string
	err   error
}

func (f *fakeChanged) Changed(string) ([]string, error) { return f.files, f.err }

var testChecks = []string{"go test ./..."}

func newTestEnv(t *testing.T) GateEnv {
	t.Helper()
	return GateEnv{
		TownRoot: t.TempDir(),
		Rig:      "r",
		Store:    NewMemStore(),
		Items:    NewMemItems(),
		Config: &config.GatedWorkflow{
			Enabled:                  true,
			CheckCommands:            testChecks,
			CheckCommandsApprovedSHA: CheckCommandsSHA(testChecks),
			MaxFixFiles:              DefaultMaxFixFiles,
			MaxFixRounds:             DefaultMaxFixRounds,
			MaxPlanLoops:             DefaultMaxPlanLoops,
		},
		Git: &fakeChanged{},
	}
}

// newItemAt creates an item of kind k at step s. For the kind's first step
// the bead is set to s entered at the given time and no transitions exist.
// For any later step (StepDone included) real store history is recorded
// from the first step to s, and the bead mirrors the last transition (so
// entered is ignored).
func newItemAt(t *testing.T, env GateEnv, k Kind, s Step, entered time.Time) *Item {
	t.Helper()
	return newChildAt(t, env, k, "", s, entered)
}

func newChildAt(t *testing.T, env GateEnv, k Kind, parent string, s Step, entered time.Time) *Item {
	t.Helper()
	it, err := env.Items.Create(env.Rig, k, "t", parent)
	must(t, err)
	if s != firstStep(k) {
		walkTo(t, env, it.ID, k, s)
		trs, err := env.Store.Transitions(it.ID)
		must(t, err)
		entered = trs[len(trs)-1].At
	}
	must(t, env.Items.SetStep(it.ID, s, entered))
	it, err = env.Items.Get(it.ID)
	must(t, err)
	return it
}

// walkTo records store transitions for item id from its kind's first step
// to s. StepDone is reached from the kind's last step.
func walkTo(t *testing.T, env GateEnv, id string, k Kind, s Step) {
	t.Helper()
	cur := firstStep(k)
	for cur != s {
		next, ok := Next(k, cur)
		if !ok {
			if s != StepDone {
				t.Fatalf("walkTo: step %s not reachable for %s", s, k)
			}
			next = StepDone
		}
		must(t, env.Store.Transition(id, cur, next, "walk"))
		cur = next
	}
}

func writeArtifact(t *testing.T, env GateEnv, id, name, content string) {
	t.Helper()
	p := ArtifactPath(env.TownRoot, env.Rig, id, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func addApproval(t *testing.T, env GateEnv, id, name, verdict, role string, at time.Time) {
	t.Helper()
	_, sha, _, err := ReadArtifact(ArtifactPath(env.TownRoot, env.Rig, id, name))
	if err != nil {
		t.Fatal(err)
	}
	source := "agent"
	if role == "human" {
		source = "human"
	}
	addApprovalFrom(t, env, Approval{ItemID: id, Artifact: name, SHA: sha, Verdict: verdict, Role: role, Source: source, Actor: "a", At: at})
}

// addApprovalFrom records a exactly as given (for forged-source tests).
func addApprovalFrom(t *testing.T, env GateEnv, a Approval) {
	t.Helper()
	if err := env.Store.AddApproval(a); err != nil {
		t.Fatal(err)
	}
}

func approve(t *testing.T, env GateEnv, id, name string) {
	t.Helper()
	addApproval(t, env, id, name, "approve", "human", time.Now())
}

func addTDD(t *testing.T, env GateEnv, id, step, phase, prov string, at time.Time) {
	t.Helper()
	if err := env.Store.AddTDD(TDDRun{ItemID: id, PlanStep: step, Phase: phase, Source: "gtb", Provenance: prov, At: at}); err != nil {
		t.Fatal(err)
	}
}

func assertGate(t *testing.T, got GateResult, passed bool, waiting string, reasonSub string) {
	t.Helper()
	if got.Passed != passed {
		t.Fatalf("Passed = %v, want %v (reasons %q)", got.Passed, passed, got.Reasons)
	}
	if got.WaitingForHuman != waiting {
		t.Fatalf("WaitingForHuman = %q, want %q (reasons %q)", got.WaitingForHuman, waiting, got.Reasons)
	}
	if reasonSub != "" && !hasReason(got, reasonSub) {
		t.Fatalf("reasons %q do not contain %q", got.Reasons, reasonSub)
	}
	if passed && len(got.Reasons) != 0 {
		t.Fatalf("passed gate has reasons %q", got.Reasons)
	}
}

func hasReason(r GateResult, sub string) bool {
	for _, s := range r.Reasons {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

type gateCase struct {
	name      string
	kind      Kind
	step      Step
	setup     func(t *testing.T, env GateEnv, it *Item)
	passed    bool
	waiting   string
	reasonSub string
}

func runGateCases(t *testing.T, cases []gateCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			it := newItemAt(t, env, tc.kind, tc.step, time.Now().Add(-time.Hour))
			if tc.setup != nil {
				tc.setup(t, env, it)
			}
			it, err := env.Items.Get(it.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertGate(t, Gate(env, it), tc.passed, tc.waiting, tc.reasonSub)
		})
	}
}

// Row: any step with an artifact.
func TestGate_ArtifactApproval(t *testing.T) {
	runGateCases(t, []gateCase{
		{name: "approved", kind: KindTask, step: "spec", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "---\nstatus: draft\n---\nbody\n")
			approve(t, env, it.ID, "spec")
		}},
		{name: "not approved", kind: KindTask, step: "spec", waiting: "approve spec", reasonSub: "spec.md not approved", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "body\n")
		}},
		{name: "missing file", kind: KindTask, step: "spec", reasonSub: "spec.md missing"},
		{name: "status edit keeps approval", kind: KindFix, step: "rca", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "rca", "---\nstatus: draft\n---\nbody\n")
			approve(t, env, it.ID, "rca")
			writeArtifact(t, env, it.ID, "rca", "---\nstatus: approved\n---\nbody\n")
		}},
		{name: "fix report", kind: KindFix, step: "report", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "report", "body\n")
			approve(t, env, it.ID, "report")
		}},
		{name: "epic epic", kind: KindEpic, step: "epic", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "epic", "AC1 works\n")
			approve(t, env, it.ID, "epic")
		}},
		{name: "request_changes after approve blocks", kind: KindTask, step: "spec", reasonSub: "changes requested on spec", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "body\n")
			now := time.Now()
			addApproval(t, env, it.ID, "spec", "approve", "human", now)
			addApproval(t, env, it.ID, "spec", "request_changes", "human", now.Add(time.Second))
		}},
		{name: "reject at same time appended later blocks", kind: KindTask, step: "spec", reasonSub: "changes requested on spec", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "body\n")
			now := time.Now()
			addApproval(t, env, it.ID, "spec", "approve", "human", now)
			addApproval(t, env, it.ID, "spec", "reject", "human", now)
		}},
		{name: "request_changes on older sha then revised", kind: KindTask, step: "spec", waiting: "approve spec", reasonSub: "changes requested on spec", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "v1\n")
			now := time.Now()
			addApproval(t, env, it.ID, "spec", "approve", "human", now)
			addApproval(t, env, it.ID, "spec", "request_changes", "human", now.Add(time.Second))
			writeArtifact(t, env, it.ID, "spec", "v2\n")
		}},
		{name: "approve after request_changes passes", kind: KindTask, step: "spec", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "body\n")
			now := time.Now()
			addApproval(t, env, it.ID, "spec", "request_changes", "human", now)
			addApproval(t, env, it.ID, "spec", "approve", "human", now.Add(time.Second))
		}},
		{name: "agent request_changes does not block", kind: KindTask, step: "spec", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "body\n")
			now := time.Now()
			addApproval(t, env, it.ID, "spec", "approve", "human", now)
			addApproval(t, env, it.ID, "spec", "request_changes", "checker", now.Add(time.Second))
		}},
		{name: "latest by At not append order", kind: KindTask, step: "spec", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "spec", "body\n")
			now := time.Now()
			addApproval(t, env, it.ID, "spec", "approve", "human", now.Add(time.Second))
			addApproval(t, env, it.ID, "spec", "request_changes", "human", now)
		}},
	})
}

func TestGate_ApprovalVoidedByEdit(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindTask, "spec", time.Now())
	writeArtifact(t, env, it.ID, "spec", "---\nstatus: draft\n---\nbody\n")
	approve(t, env, it.ID, "spec")
	assertGate(t, Gate(env, it), true, "", "")
	writeArtifact(t, env, it.ID, "spec", "---\nstatus: draft\n---\nbody edited\n")
	assertGate(t, Gate(env, it), false, "approve spec", "spec.md not approved")
}

func TestGate_ApprovalByAgentRoleIgnored(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindTask, "spec", time.Now())
	writeArtifact(t, env, it.ID, "spec", "body\n")
	addApproval(t, env, it.ID, "spec", "approve", "checker", time.Now())
	assertGate(t, Gate(env, it), false, "approve spec", "spec.md not approved")
}

// Row: task/plan.
func TestGate_TaskPlan(t *testing.T) {
	planOK := func(t *testing.T, env GateEnv, it *Item) {
		writeArtifact(t, env, it.ID, "plan", "---\nsteps: a\n---\nplan\n")
		approve(t, env, it.ID, "plan")
	}
	loops := func(n int) func(t *testing.T, env GateEnv, it *Item) {
		return func(t *testing.T, env GateEnv, it *Item) {
			planOK(t, env, it) // newItemAt already recorded spec -> plan
			for i := 0; i < n; i++ {
				must(t, env.Store.Transition(it.ID, "plan", "execute", "x"))
				must(t, env.Store.Transition(it.ID, "execute", "plan", "x"))
			}
		}
	}
	runGateCases(t, []gateCase{
		{name: "approved", kind: KindTask, step: "plan", passed: true, setup: planOK},
		{name: "reject on current sha", kind: KindTask, step: "plan", reasonSub: "plan.md rejected by plan-checker", setup: func(t *testing.T, env GateEnv, it *Item) {
			planOK(t, env, it)
			addApproval(t, env, it.ID, "plan", "reject", "plan-checker", time.Now().Add(-time.Minute))
		}},
		{name: "reject on old sha ignored", kind: KindTask, step: "plan", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "plan", "old\n")
			addApproval(t, env, it.ID, "plan", "reject", "plan-checker", time.Now().Add(-time.Minute))
			planOK(t, env, it)
		}},
		{name: "re-entries below budget", kind: KindTask, step: "plan", passed: true, setup: loops(DefaultMaxPlanLoops - 1)},
		{name: "re-entries exhaust budget", kind: KindTask, step: "plan", reasonSub: "plan loop budget 3 exhausted", setup: loops(DefaultMaxPlanLoops)},
	})
}

// Row: task/execute.
func TestGate_TaskExecute(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	plan := func(steps string) func(t *testing.T, env GateEnv, it *Item) {
		return func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "plan", "---\nsteps: "+steps+"\n---\nplan\n")
			approve(t, env, it.ID, "plan")
		}
	}
	both := func(t *testing.T, env GateEnv, it *Item, step string) {
		addTDD(t, env, it.ID, step, "red", "verified", base)
		addTDD(t, env, it.ID, step, "green", "verified", base.Add(time.Second))
	}
	runGateCases(t, []gateCase{
		{name: "all steps red then green", kind: KindTask, step: "execute", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			plan(`"a, b ,c"`)(t, env, it)
			both(t, env, it, "a")
			both(t, env, it, "b")
			both(t, env, it, "c")
		}},
		{name: "missing step", kind: KindTask, step: "execute", reasonSub: "plan step b has no verified red then green", setup: func(t *testing.T, env GateEnv, it *Item) {
			plan("a,b")(t, env, it)
			both(t, env, it, "a")
		}},
		{name: "green before red", kind: KindTask, step: "execute", reasonSub: "plan step a has no verified red then green", setup: func(t *testing.T, env GateEnv, it *Item) {
			plan("a")(t, env, it)
			addTDD(t, env, it.ID, "a", "green", "verified", base)
			addTDD(t, env, it.ID, "a", "red", "verified", base.Add(time.Second))
		}},
		{name: "unverified green", kind: KindTask, step: "execute", reasonSub: "plan step a has no verified red then green", setup: func(t *testing.T, env GateEnv, it *Item) {
			plan("a")(t, env, it)
			addTDD(t, env, it.ID, "a", "red", "verified", base)
			addTDD(t, env, it.ID, "a", "green", "claimed", base.Add(time.Second))
		}},
		{name: "no steps", kind: KindTask, step: "execute", reasonSub: "plan.md must list steps", setup: plan("")},
		{name: "plan missing", kind: KindTask, step: "execute", reasonSub: "plan.md missing"},
		{name: "plan never approved", kind: KindTask, step: "execute", waiting: "approve plan", reasonSub: "approve plan", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "plan", "---\nsteps: a\n---\n")
			both(t, env, it, "a")
		}},
	})
}

func checkPass(at time.Time) func(t *testing.T, env GateEnv, it *Item) {
	return func(t *testing.T, env GateEnv, it *Item) {
		must(t, env.Store.AddCheck(CheckRun{ItemID: it.ID, Source: "gtb", CmdSetSHA: CheckCommandsSHA(testChecks), Passed: true, At: at}))
	}
}

// Row: task/verify, fix/verify.
func TestGate_Verify(t *testing.T) {
	now := time.Now()
	runGateCases(t, []gateCase{
		// A zero At is stamped "now" by the store, i.e. after entering verify.
		{name: "task fresh pass", kind: KindTask, step: "verify", passed: true, setup: checkPass(time.Time{})},
		{name: "fix fresh pass", kind: KindFix, step: "verify", passed: true, setup: checkPass(time.Time{})},
		{name: "no check", kind: KindTask, step: "verify", reasonSub: "no passing check since entering verify"},
		{name: "failed check", kind: KindFix, step: "verify", reasonSub: "no passing check since entering verify", setup: func(t *testing.T, env GateEnv, it *Item) {
			must(t, env.Store.AddCheck(CheckRun{ItemID: it.ID, Source: "gtb", CmdSetSHA: CheckCommandsSHA(testChecks), Passed: false, At: now}))
		}},
		{name: "stale command set", kind: KindTask, step: "verify", reasonSub: "no passing check since entering verify", setup: func(t *testing.T, env GateEnv, it *Item) {
			must(t, env.Store.AddCheck(CheckRun{ItemID: it.ID, Source: "gtb", CmdSetSHA: "other", Passed: true, At: now}))
		}},
		{name: "checks not approved", kind: KindTask, step: "verify", waiting: "approve check commands", reasonSub: "check commands not approved", setup: func(t *testing.T, env GateEnv, it *Item) {
			checkPass(now)(t, env, it)
			env.Config.CheckCommandsApprovedSHA = "stale"
		}},
	})
}

func TestGate_VerifyRequiresFreshCheck(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindTask, "verify", time.Time{})
	entered := it.StepEnteredAt // the store's transition into verify
	checkPass(entered.Add(-time.Second))(t, env, it)
	assertGate(t, Gate(env, it), false, "", "no passing check since entering verify")
	checkPass(entered)(t, env, it)
	assertGate(t, Gate(env, it), true, "", "")
}

// Row: fix/reproduce.
func TestGate_FixReproduce(t *testing.T) {
	runGateCases(t, []gateCase{
		{name: "verified red", kind: KindFix, step: "reproduce", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			addTDD(t, env, it.ID, "repro", "red", "verified", time.Now())
		}},
		{name: "manual repro", kind: KindFix, step: "reproduce", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "report", "---\nrepro: 'manual'\n---\nsteps\n")
			approve(t, env, it.ID, "report")
		}},
		{name: "manual repro never approved", kind: KindFix, step: "reproduce", waiting: "approve report", reasonSub: "approve report", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "report", "---\nrepro: manual\n---\nsteps\n")
		}},
		{name: "unverified red", kind: KindFix, step: "reproduce", reasonSub: "no verified red for repro", setup: func(t *testing.T, env GateEnv, it *Item) {
			addTDD(t, env, it.ID, "repro", "red", "claimed", time.Now())
			writeArtifact(t, env, it.ID, "report", "---\nrepro: auto\n---\n")
		}},
		{name: "nothing", kind: KindFix, step: "reproduce", reasonSub: "no verified red for repro"},
	})
}

// Row: fix/fix.
func TestGate_FixFix(t *testing.T) {
	base := time.Now().Add(-time.Minute)
	redGreen := func(t *testing.T, env GateEnv, it *Item) {
		addTDD(t, env, it.ID, "repro", "red", "verified", base)
		addTDD(t, env, it.ID, "repro", "green", "verified", base.Add(time.Second))
	}
	runGateCases(t, []gateCase{
		{name: "green within budget", kind: KindFix, step: "fix", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			redGreen(t, env, it)
			env.Git.(*fakeChanged).files = []string{"a", "b", "c", "d", "e"}
		}},
		{name: "no green", kind: KindFix, step: "fix", reasonSub: "repro has no verified red then green", setup: func(t *testing.T, env GateEnv, it *Item) {
			addTDD(t, env, it.ID, "repro", "red", "verified", base)
		}},
		{name: "changed files error", kind: KindFix, step: "fix", reasonSub: "changed files: boom", setup: func(t *testing.T, env GateEnv, it *Item) {
			redGreen(t, env, it)
			env.Git.(*fakeChanged).err = errors.New("boom")
		}},
	})
}

func TestGate_FixTooManyFiles(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindFix, "fix", time.Now())
	addTDD(t, env, it.ID, "repro", "red", "verified", time.Now())
	addTDD(t, env, it.ID, "repro", "green", "verified", time.Now().Add(time.Second))
	env.Git = &fakeChanged{files: []string{"1", "2", "3", "4", "5", "6"}}
	got := Gate(env, it)
	assertGate(t, got, false, "", "")
	if !reflect.DeepEqual(got.Reasons, []string{"6 files changed, budget 5"}) {
		t.Fatalf("reasons = %q", got.Reasons)
	}
}

// Row: task/review.
func TestGate_TaskReview(t *testing.T) {
	review := func(fm string) func(t *testing.T, env GateEnv, it *Item) {
		return func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "review", "---\n"+fm+"\n---\nlgtm\n")
			approve(t, env, it.ID, "review")
		}
	}
	runGateCases(t, []gateCase{
		{name: "patch", kind: KindTask, step: "review", passed: true, setup: review("bump: patch")},
		{name: "quoted minor", kind: KindTask, step: "review", passed: true, setup: review(`bump: "minor"`)},
		{name: "major", kind: KindTask, step: "review", passed: true, setup: review("bump:  major ")},
		{name: "bad bump", kind: KindTask, step: "review", reasonSub: "review.md must set bump: patch|minor|major", setup: review("bump: huge")},
		{name: "no bump", kind: KindTask, step: "review", reasonSub: "review.md must set bump: patch|minor|major", setup: review("x: y")},
		{name: "not approved", kind: KindTask, step: "review", waiting: "approve review", reasonSub: "review.md not approved", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "review", "---\nbump: patch\n---\n")
		}},
	})
}

// Row: epic/breakdown.
func TestGate_EpicBreakdown(t *testing.T) {
	setup := func(epic, breakdown string) func(t *testing.T, env GateEnv, it *Item) {
		return func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "epic", epic)
			approve(t, env, it.ID, "epic")
			writeArtifact(t, env, it.ID, "breakdown", breakdown)
			approve(t, env, it.ID, "breakdown")
		}
	}
	epic := "# Epic\nAC1 login works\nAC2: logout\nnot AC3 since not at line start\nac4 lower\n"
	runGateCases(t, []gateCase{
		{name: "covered acyclic", kind: KindEpic, step: "breakdown", passed: true,
			setup: setup(epic, "task a covers AC1\ntask b covers (AC2)\ndepends: a -> b\ndepends: b -> c\n")},
		{name: "missing AC", kind: KindEpic, step: "breakdown", reasonSub: "breakdown.md does not cover AC2",
			setup: setup(epic, "task a covers AC1 and AC20\n")},
		{name: "cycle", kind: KindEpic, step: "breakdown", reasonSub: "dependency cycle: a -> b -> c -> a",
			setup: setup(epic, "AC1 AC2\ndepends: a -> b\ndepends: b -> c\ndepends: c -> a\n")},
		{name: "self cycle", kind: KindEpic, step: "breakdown", reasonSub: "dependency cycle: x -> x",
			setup: setup(epic, "AC1 AC2\ndepends: x -> x\n")},
		{name: "epic missing", kind: KindEpic, step: "breakdown", reasonSub: "epic.md missing", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "breakdown", "x\n")
			approve(t, env, it.ID, "breakdown")
		}},
		{name: "not approved", kind: KindEpic, step: "breakdown", waiting: "approve breakdown", reasonSub: "breakdown.md not approved", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "epic", epic)
			writeArtifact(t, env, it.ID, "breakdown", "AC1 AC2\n")
		}},
	})
}

// Row: epic/tasks.
func TestGate_EpicTasks(t *testing.T) {
	child := func(t *testing.T, env GateEnv, parent string, s Step) *Item {
		return newChildAt(t, env, KindTask, parent, s, time.Now())
	}
	runGateCases(t, []gateCase{
		{name: "all done", kind: KindEpic, step: "tasks", passed: true, setup: func(t *testing.T, env GateEnv, it *Item) {
			child(t, env, it.ID, StepDone)
			child(t, env, it.ID, StepDone)
			child(t, env, "other", "spec")
		}},
		{name: "child at merge not done", kind: KindEpic, step: "tasks", reasonSub: "is at merge, not done", setup: func(t *testing.T, env GateEnv, it *Item) {
			child(t, env, it.ID, StepDone)
			child(t, env, it.ID, "merge")
		}},
		{name: "store step wins for child", kind: KindEpic, step: "tasks", reasonSub: "is at merge, not done", setup: func(t *testing.T, env GateEnv, it *Item) {
			c := child(t, env, it.ID, "merge")
			must(t, env.Items.SetStep(c.ID, StepDone, time.Now())) // bead claims done
		}},
		{name: "no children", kind: KindEpic, step: "tasks", reasonSub: "epic has no child items"},
	})
}

// Row: */merge.
func TestGate_Merge(t *testing.T) {
	runGateCases(t, []gateCase{
		{name: "task", kind: KindTask, step: "merge", reasonSub: "merge is performed by the refinery"},
		{name: "fix", kind: KindFix, step: "merge", reasonSub: "merge is performed by the refinery"},
	})
}

func TestGate_Done(t *testing.T) {
	runGateCases(t, []gateCase{
		{name: "done", kind: KindTask, step: StepDone, reasonSub: "item is done"},
	})
}

func TestGate_StoreStepWins(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindTask, "spec", time.Now())
	// Bead says spec, store says plan.
	must(t, env.Store.Transition(it.ID, "spec", "plan", "x"))
	writeArtifact(t, env, it.ID, "spec", "body\n")
	approve(t, env, it.ID, "spec")
	assertGate(t, Gate(env, it), false, "", "plan.md missing")
}

func TestEffectiveStep(t *testing.T) {
	env := newTestEnv(t)
	entered := time.Now().Add(-time.Hour).UTC()
	it := newItemAt(t, env, KindTask, "spec", entered)
	s, at, hist, err := effectiveStep(env, it)
	must(t, err)
	if s != "spec" || !at.Equal(entered) || hist {
		t.Fatalf("no transitions: got %s %v %v, want spec %v false", s, at, hist, entered)
	}
	must(t, env.Store.Transition(it.ID, "spec", "plan", "x"))
	trs, _ := env.Store.Transitions(it.ID)
	s, at, hist, err = effectiveStep(env, it)
	must(t, err)
	if s != "plan" || !at.Equal(trs[0].At) || !hist {
		t.Fatalf("with transition: got %s %v, want plan %v", s, at, trs[0].At)
	}
}

func TestUnquote(t *testing.T) {
	for in, want := range map[string]string{
		` patch `: "patch", `"patch"`: "patch", `'patch'`: "patch", `"patch'`: `"patch'`, `"`: `"`, `""`: "",
	} {
		if got := unquote(in); got != want {
			t.Errorf("unquote(%q) = %q, want %q", in, got, want)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestGitChangedFiles_NilBranch(t *testing.T) {
	var _ ChangedFiles = GitChangedFiles{}
	if _, err := (GitChangedFiles{RepoDir: t.TempDir(), Base: "main"}).Changed("x"); err == nil {
		t.Fatal("want error for nil Branch")
	}
}

// R12: content-reading gates require the content's current version approved.
func TestGate_ContentEditAfterApproval(t *testing.T) {
	cases := []struct {
		name     string
		kind     Kind
		step     Step
		artifact string
		setup    func(t *testing.T, env GateEnv, it *Item)
		prePass  bool // the gate passes before the edit
		edit     string
	}{
		{name: "plan steps", kind: KindTask, step: "execute", artifact: "plan",
			setup: func(t *testing.T, env GateEnv, it *Item) {
				writeArtifact(t, env, it.ID, "plan", "---\nsteps: a,b\n---\n")
				approve(t, env, it.ID, "plan")
				addTDD(t, env, it.ID, "a", "red", "verified", time.Now())
				addTDD(t, env, it.ID, "a", "green", "verified", time.Now().Add(time.Second))
			},
			// Step b has no evidence; dropping it would otherwise pass.
			edit: "---\nsteps: a\n---\n"},
		{name: "report manual repro", kind: KindFix, step: "reproduce", artifact: "report",
			setup: func(t *testing.T, env GateEnv, it *Item) {
				writeArtifact(t, env, it.ID, "report", "---\nrepro: manual\n---\nsteps v1\n")
				approve(t, env, it.ID, "report")
			},
			prePass: true,
			edit:    "---\nrepro: manual\n---\nsteps v2\n"},
		{name: "epic ACs", kind: KindEpic, step: "breakdown", artifact: "epic",
			setup: func(t *testing.T, env GateEnv, it *Item) {
				writeArtifact(t, env, it.ID, "epic", "AC1 a\nAC2 b\n")
				approve(t, env, it.ID, "epic")
				writeArtifact(t, env, it.ID, "breakdown", "AC1 AC2\n")
				approve(t, env, it.ID, "breakdown")
			},
			// Dropping AC2 must void the epic approval.
			prePass: true,
			edit:    "AC1 a\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			it := newItemAt(t, env, tc.kind, tc.step, time.Now())
			tc.setup(t, env, it)
			if before := Gate(env, it); before.Passed != tc.prePass {
				t.Fatalf("before edit: %+v, want passed=%v", before, tc.prePass)
			}
			writeArtifact(t, env, it.ID, tc.artifact, tc.edit)
			assertGate(t, Gate(env, it), false, "approve "+tc.artifact, tc.artifact+".md changed since approval")
		})
	}
}

// R10: a bead relabelled past the first step without store history is refused.
func TestGate_BeadRelabelWithoutHistoryRefused(t *testing.T) {
	env := newTestEnv(t)
	it, err := env.Items.Create(env.Rig, KindTask, "t", "")
	must(t, err)
	must(t, env.Items.SetStep(it.ID, "verify", time.Now().Add(-time.Hour)))
	checkPass(time.Now())(t, env, it)
	it, err = env.Items.Get(it.ID)
	must(t, err)
	got := Gate(env, it)
	assertGate(t, got, false, "", "")
	if !reflect.DeepEqual(got.Reasons, []string{"no transition history for step verify"}) {
		t.Fatalf("reasons = %q", got.Reasons)
	}
	if _, res, err := Advance(env, it.ID, "me"); err != nil || res.Passed {
		t.Fatalf("Advance = %+v, %v", res, err)
	}
	if trs, _ := env.Store.Transitions(it.ID); len(trs) != 0 {
		t.Fatalf("Advance transitioned a relabelled item: %+v", trs)
	}
}

// R10: a child counts as done only from its store history.
func TestGate_EpicTasks_BeadOnlyDoneNotCounted(t *testing.T) {
	env := newTestEnv(t)
	epic := newItemAt(t, env, KindEpic, "tasks", time.Now())
	newChildAt(t, env, KindTask, epic.ID, StepDone, time.Now())
	c, err := env.Items.Create(env.Rig, KindTask, "c", epic.ID)
	must(t, err)
	must(t, env.Items.SetStep(c.ID, StepDone, time.Now()))
	assertGate(t, Gate(env, epic), false, "", "child "+c.ID+" has no transition history, not done")
}

func TestGate_HumanRoleWithAgentSourceIgnored(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindTask, "spec", time.Now())
	writeArtifact(t, env, it.ID, "spec", "body\n")
	_, sha, _, err := ReadArtifact(ArtifactPath(env.TownRoot, env.Rig, it.ID, "spec"))
	must(t, err)
	addApprovalFrom(t, env, Approval{ItemID: it.ID, Artifact: "spec", SHA: sha, Verdict: "approve", Role: "human", Source: "agent", Actor: "nux", At: time.Now()})
	assertGate(t, Gate(env, it), false, "approve spec", "spec.md not approved")
}

func TestGate_AgentSourcedTDDIgnored(t *testing.T) {
	forged := func(t *testing.T, env GateEnv, id, step, phase string, at time.Time) {
		must(t, env.Store.AddTDD(TDDRun{ItemID: id, PlanStep: step, Phase: phase, Source: "agent", Provenance: "verified", At: at}))
	}
	base := time.Now().Add(-time.Minute)
	runGateCases(t, []gateCase{
		{name: "execute", kind: KindTask, step: "execute", reasonSub: "plan step a has no verified red then green", setup: func(t *testing.T, env GateEnv, it *Item) {
			writeArtifact(t, env, it.ID, "plan", "---\nsteps: a\n---\nplan\n")
			approve(t, env, it.ID, "plan")
			forged(t, env, it.ID, "a", "red", base)
			forged(t, env, it.ID, "a", "green", base.Add(time.Second))
		}},
		{name: "reproduce", kind: KindFix, step: "reproduce", reasonSub: "no verified red for repro", setup: func(t *testing.T, env GateEnv, it *Item) {
			forged(t, env, it.ID, "repro", "red", base)
		}},
		{name: "fix", kind: KindFix, step: "fix", reasonSub: "repro has no verified red then green", setup: func(t *testing.T, env GateEnv, it *Item) {
			forged(t, env, it.ID, "repro", "red", base)
			forged(t, env, it.ID, "repro", "green", base.Add(time.Second))
		}},
	})
}

func TestGate_AgentSourcedCheckIgnored(t *testing.T) {
	env := newTestEnv(t)
	it := newItemAt(t, env, KindTask, "verify", time.Time{})
	must(t, env.Store.AddCheck(CheckRun{ItemID: it.ID, Source: "agent", Provenance: "verified", CmdSetSHA: CheckCommandsSHA(testChecks), Passed: true}))
	assertGate(t, Gate(env, it), false, "", "no passing check since entering verify")
}
