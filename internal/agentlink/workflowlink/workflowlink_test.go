package workflowlink

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/agentlink"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/workflow"
)

var t0 = time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)

type fixture struct {
	town  string
	env   workflow.GateEnv
	queue *agentlink.Queue
	app   *WorkflowApplier
	col   *WorkflowCollector

	slung, cancelled []string
	peeked           string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "r"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := &fixture{town: town, queue: agentlink.OpenQueue(town)}
	f.env = workflow.GateEnv{
		TownRoot: town, Rig: "r",
		Store:  workflow.NewMemStore(),
		Items:  workflow.NewMemItems(),
		Config: &config.GatedWorkflow{Enabled: true},
	}
	f.env = Observe(f.env, f.queue, nil)
	rigs := func() ([]agentlink.Rig, error) {
		return []agentlink.Rig{{Name: "r", Remote: "git@x:r.git", Gated: true}, {Name: "plain"}}, nil
	}
	envFor := func(rig string) (workflow.GateEnv, error) {
		if rig != "r" {
			return workflow.GateEnv{}, errors.New("no env for " + rig)
		}
		return f.env, nil
	}
	f.app = &WorkflowApplier{
		TownRoot: town,
		Rigs:     rigs,
		Env:      envFor,
		Sling: func(_ context.Context, id, rig string) error {
			f.slung = append(f.slung, id+"@"+rig)
			return nil
		},
		Cancel: func(_ context.Context, id string) error {
			f.cancelled = append(f.cancelled, id)
			return nil
		},
		Peek: func(_ context.Context, rig, id string) (string, error) { return f.peeked, nil },
		Now:  func() time.Time { return t0 },
	}
	f.col = &WorkflowCollector{
		TownRoot: town,
		Rigs:     rigs,
		Env:      envFor,
		Queue:    f.queue,
		Sessions: func() ([]agentlink.SessionView, error) {
			return []agentlink.SessionView{{Name: "r-toast", Role: "polecat", Rig: "r", ItemID: "mem-1"}}, nil
		},
		DiffStat: func(rig, branch string) ([]agentlink.FileStat, error) {
			return []agentlink.FileStat{{Path: "a.go", Added: 3, Deleted: 1}}, nil
		},
	}
	return f
}

// item creates a task with its spec artifact and returns its ID and the
// artifact's sha.
func (f *fixture) item(t *testing.T, content string) (string, string) {
	t.Helper()
	it, err := f.env.Items.Create("r", workflow.KindTask, "Title", "")
	if err != nil {
		t.Fatal(err)
	}
	p := workflow.ArtifactPath(f.town, "r", it.ID, "spec")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sha, _, err := workflow.ReadArtifact(p)
	if err != nil {
		t.Fatal(err)
	}
	return it.ID, sha
}

func req(typ, item string, params any) agentlink.Request {
	b, _ := json.Marshal(params)
	return agentlink.Request{Key: "k-" + typ, Type: typ, ItemID: item, Params: b, ExpiresAt: t0.Add(time.Hour)}
}

func approveReq(item, sha string) agentlink.Request {
	return req("approve", item, agentlink.ApproveParams{Artifact: "spec", SHA: sha, Actor: "baovo15"})
}

func TestApplyApprove_StaleSHA(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "---\nstatus: draft\n---\nv1\n")
	// The artifact changes after the dashboard read it.
	p := workflow.ArtifactPath(f.town, "r", id, "spec")
	if err := os.WriteFile(p, []byte("---\nstatus: draft\n---\nv2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := f.app.Apply(context.Background(), approveReq(id, sha))
	if res.OK || res.Error != "stale_sha" || res.Key != "k-approve" {
		t.Fatalf("result = %+v, want stale_sha", res)
	}
	if as, _ := f.env.Store.Approvals(id); len(as) != 0 {
		t.Fatalf("approvals recorded on stale sha: %+v", as)
	}
}

func TestApplyApprove_RecordsDashboardChannel(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "spec body\n")
	for _, typ := range []string{"approve", "request_changes"} {
		r := approveReq(id, sha)
		r.Type, r.Key = typ, "k-"+typ
		if res := f.app.Apply(context.Background(), r); !res.OK {
			t.Fatalf("%s: %+v", typ, res)
		}
	}
	as, _ := f.env.Store.Approvals(id)
	if len(as) != 2 {
		t.Fatalf("approvals = %+v", as)
	}
	for i, verdict := range []string{"approve", "request_changes"} {
		a := as[i]
		want := workflow.Approval{ItemID: id, Artifact: "spec", SHA: sha, Verdict: verdict, Role: "human", Source: "human", Channel: "dashboard", Actor: "baovo15", At: t0}
		if a != want {
			t.Fatalf("approval[%d] = %+v, want %+v", i, a, want)
		}
	}
	if !workflow.HumanApprovedCurrent("spec", sha, as[:1]) {
		t.Fatal("dashboard approval not accepted by the gate")
	}
	// Each approval is queued as an evidence event.
	pend, _ := f.queue.Pending(100)
	n := 0
	for _, e := range pend {
		if e.Type == agentlink.EventEvidence && e.ItemID == id && strings.Contains(string(e.Data), `"channel":"dashboard"`) {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("evidence events = %d in %+v", n, pend)
	}
}

func TestApplyApprove_Validation(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "x\n")
	cases := map[string]agentlink.Request{
		"bad id":       approveReq("../etc", sha),
		"bad artifact": req("approve", id, agentlink.ApproveParams{Artifact: "../spec", SHA: sha, Actor: "a"}),
		"no actor":     req("approve", id, agentlink.ApproveParams{Artifact: "spec", SHA: sha}),
		"no sha":       req("approve", id, agentlink.ApproveParams{Artifact: "spec", Actor: "a"}),
		"bad json":     {Key: "k", Type: "approve", ItemID: id, Params: json.RawMessage(`[`), ExpiresAt: t0.Add(time.Hour)},
	}
	for name, r := range cases {
		if res := f.app.Apply(context.Background(), r); res.OK || res.Error != agentlink.ErrCodeInvalid {
			t.Errorf("%s: %+v", name, res)
		}
	}
	if res := f.app.Apply(context.Background(), approveReq("gt-missing", sha)); res.OK || res.Error != agentlink.ErrCodeNotFound {
		t.Errorf("unknown item: %+v", res)
	}
	if as, _ := f.env.Store.Approvals(id); len(as) != 0 {
		t.Fatalf("recorded: %+v", as)
	}
}

func TestApply_UnknownTypeUnsupported(t *testing.T) {
	f := newFixture(t)
	res := f.app.Apply(context.Background(), agentlink.Request{Key: "k1", Type: "shell", ExpiresAt: t0.Add(time.Hour)})
	if res.OK || res.Error != "unsupported" || res.Key != "k1" {
		t.Fatalf("result = %+v", res)
	}
}

func TestApply_ExpiredNoSideEffects(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "x\n")
	r := approveReq(id, sha)
	r.ExpiresAt = t0.Add(-time.Second)
	if res := f.app.Apply(context.Background(), r); res.OK || res.Error != "expired" {
		t.Fatalf("result = %+v", res)
	}
	s := req("item.sling", id, nil)
	s.ExpiresAt = t0
	f.app.Apply(context.Background(), s)
	if as, _ := f.env.Store.Approvals(id); len(as) != 0 || len(f.slung) != 0 {
		t.Fatalf("side effects: %+v %v", as, f.slung)
	}
}

func TestApply_ItemCreate(t *testing.T) {
	f := newFixture(t)
	res := f.app.Apply(context.Background(), req("item.create", "", map[string]string{"rig": "r", "kind": "fix", "title": "  Broken   thing ", "parent": ""}))
	if !res.OK {
		t.Fatalf("result = %+v", res)
	}
	var out struct{ ID, Rig, Kind, Step string }
	json.Unmarshal(res.Data, &out)
	if out.Rig != "r" || out.Kind != "fix" || out.Step != "report" {
		t.Fatalf("data = %s", res.Data)
	}
	it, err := f.env.Items.Get(out.ID)
	if err != nil || it.Title != "Broken thing" {
		t.Fatalf("item = %+v, %v", it, err)
	}
	b, err := os.ReadFile(workflow.ArtifactPath(f.town, "r", out.ID, "report"))
	if err != nil || !strings.Contains(string(b), "status: draft") || !strings.Contains(string(b), "# Broken thing") {
		t.Fatalf("stub = %q, %v", b, err)
	}
	pend, _ := f.queue.Pending(100)
	if len(pend) == 0 || pend[len(pend)-1].Type != agentlink.EventItemUpsert || pend[len(pend)-1].ItemID != out.ID {
		t.Fatalf("no upsert queued: %+v", pend)
	}
	for name, p := range map[string]map[string]string{
		"ungated rig": {"rig": "plain", "kind": "task", "title": "t"},
		"bad rig":     {"rig": "../r", "kind": "task", "title": "t"},
		"bad kind":    {"rig": "r", "kind": "chore", "title": "t"},
		"no title":    {"rig": "r", "kind": "task", "title": "  "},
		"bad parent":  {"rig": "r", "kind": "task", "title": "t", "parent": "a b"},
	} {
		if res := f.app.Apply(context.Background(), req("item.create", "", p)); res.OK || res.Error != agentlink.ErrCodeInvalid {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

func TestApply_SlingCancelSnapshot(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item(t, "x\n")
	if res := f.app.Apply(context.Background(), req("item.sling", id, nil)); !res.OK {
		t.Fatalf("sling: %+v", res)
	}
	if res := f.app.Apply(context.Background(), req("item.cancel", id, nil)); !res.OK {
		t.Fatalf("cancel: %+v", res)
	}
	if len(f.slung) != 1 || f.slung[0] != id+"@r" || len(f.cancelled) != 1 || f.cancelled[0] != id {
		t.Fatalf("slung=%v cancelled=%v", f.slung, f.cancelled)
	}
	f.app.Sling = func(context.Context, string, string) error { return errors.New("exit 1: no capacity") }
	res := f.app.Apply(context.Background(), req("item.sling", id, nil))
	if res.OK || res.Error != agentlink.ErrCodeFailed || !strings.Contains(string(res.Data), "no capacity") {
		t.Fatalf("failed sling: %+v", res)
	}
	if res := f.app.Apply(context.Background(), req("item.sling", "gt-nope", nil)); res.OK || res.Error != agentlink.ErrCodeNotFound {
		t.Fatalf("unknown item: %+v", res)
	}
	if res := f.app.Apply(context.Background(), req("snapshot", "", nil)); !res.OK {
		t.Fatalf("snapshot: %+v", res)
	}
}

func TestApply_InspectAndPeek(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "spec\n")
	if err := f.env.Items.SetBranch(id, "polecat/toast/"+id); err != nil {
		t.Fatal(err)
	}
	res := f.app.Apply(context.Background(), req("inspect", id, nil))
	if !res.OK {
		t.Fatalf("inspect: %+v", res)
	}
	var iv agentlink.ItemView
	json.Unmarshal(res.Data, &iv)
	if iv.ID != id || iv.Step != "spec" || len(iv.Artifacts) != 1 || iv.Artifacts[0].SHA != sha || iv.Artifacts[0].Content != "spec\n" || len(iv.Diffstat) != 0 {
		t.Fatalf("inspect view = %+v", iv)
	}
	f.peeked = strings.Repeat(strings.Repeat("é", 63)+"\n", 650) // ~80 KiB of lines
	res = f.app.Apply(context.Background(), req("terminal.peek", id, nil))
	var pk struct {
		Lines string `json:"lines"`
	}
	json.Unmarshal(res.Data, &pk)
	if !res.OK || len(pk.Lines) > 64<<10 || len(pk.Lines) < 60<<10 || !strings.HasSuffix(f.peeked, pk.Lines) {
		t.Fatalf("peek: ok=%v len=%d", res.OK, len(pk.Lines))
	}
}

func TestObserver_TransitionQueuesUpsert(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "spec\n")
	f.env.Store.AddApproval(workflow.Approval{ItemID: id, Artifact: "spec", SHA: sha, Verdict: "approve", Role: "human", Source: "human", Channel: "terminal", At: time.Now()})
	before, _ := f.queue.Pending(100)
	if _, res, err := workflow.Advance(f.env, id, "me"); err != nil || !res.Passed {
		t.Fatalf("advance: %+v %v", res, err)
	}
	pend, _ := f.queue.Pending(100)
	var views []agentlink.ItemView
	for _, e := range pend[len(before):] {
		if e.Type == agentlink.EventItemUpsert && e.ItemID == id {
			var iv agentlink.ItemView
			json.Unmarshal(e.Data, &iv)
			views = append(views, iv)
		}
	}
	if len(views) == 0 || views[0].Step != "plan" || views[0].Rig != "r" || views[len(views)-1].Step != "plan" {
		t.Fatalf("upserts = %+v", views)
	}
	if err := f.env.Items.SetWaiting(id, "approve plan"); err != nil {
		t.Fatal(err)
	}
	pend, _ = f.queue.Pending(100)
	var last agentlink.ItemView
	json.Unmarshal(pend[len(pend)-1].Data, &last)
	if last.WaitingForHuman != "approve plan" {
		t.Fatalf("waiting upsert = %+v", last)
	}
}

func TestObserve_NilQueueIsNoop(t *testing.T) {
	env := workflow.GateEnv{Items: workflow.NewMemItems()}
	got := Observe(env, nil, nil)
	if got.Observer != nil || got.Items != env.Items {
		t.Fatal("Observe with nil queue changed the env")
	}
}

func TestCollector_SnapshotAndScan(t *testing.T) {
	f := newFixture(t)
	id, sha := f.item(t, "spec\n")
	f.env.Items.SetBranch(id, "polecat/toast/"+id)
	// Source files in the item dir are never sent; only <name>.md artifacts.
	dir := filepath.Dir(workflow.ArtifactPath(f.town, "r", id, "spec"))
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package x"), 0o600)
	os.MkdirAll(filepath.Join(dir, "outputs"), 0o700)
	os.WriteFile(filepath.Join(dir, "outputs", "run.md"), []byte("log"), 0o600)
	f.queue.Append(agentlink.Event{Type: agentlink.EventHeartbeat})
	pend, _ := f.queue.Pending(10)
	f.queue.Ack(pend[len(pend)-1].ID) // as if the loop had sent everything so far
	acked := pend[len(pend)-1].ID

	snap, err := f.col.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Rigs) != 2 || !snap.Rigs[0].Gated || snap.Cursor != acked {
		t.Fatalf("rigs/cursor = %+v %s", snap.Rigs, snap.Cursor)
	}
	if len(snap.Items) != 1 {
		t.Fatalf("items = %+v", snap.Items)
	}
	iv := snap.Items[0]
	if iv.ID != id || iv.Branch == "" || len(iv.Diffstat) != 1 || len(iv.Artifacts) != 1 || iv.Artifacts[0].Name != "spec" || iv.Artifacts[0].SHA != sha {
		t.Fatalf("item view = %+v", iv)
	}
	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions = %+v", snap.Sessions)
	}

	// Nothing changed since the snapshot: the scan queues nothing.
	if err := f.col.Scan(); err != nil {
		t.Fatal(err)
	}
	if p, _ := f.queue.Pending(10); len(p) != 0 {
		t.Fatalf("scan after snapshot queued %+v", p)
	}
	// An edit is queued once, as an artifact event with the new sha.
	p := workflow.ArtifactPath(f.town, "r", id, "spec")
	os.WriteFile(p, []byte("spec v2\n"), 0o600)
	f.col.Scan()
	f.col.Scan()
	got, _ := f.queue.Pending(10)
	if len(got) != 1 || got[0].Type != agentlink.EventArtifact || got[0].ItemID != id {
		t.Fatalf("scan events = %+v", got)
	}
	var av agentlink.ArtifactView
	json.Unmarshal(got[0].Data, &av)
	_, sha2, _, _ := workflow.ReadArtifact(p)
	if av.Name != "spec" || av.SHA != sha2 || av.Content != "spec v2\n" {
		t.Fatalf("artifact = %+v", av)
	}
	// A fresh collector (daemon restart) remembers what was sent.
	col2 := &WorkflowCollector{TownRoot: f.col.TownRoot, Rigs: f.col.Rigs, Env: f.col.Env, Queue: f.col.Queue, DiffStat: f.col.DiffStat}
	f.queue.Ack(got[0].ID)
	col2.Scan()
	if p, _ := f.queue.Pending(10); len(p) != 0 {
		t.Fatalf("restart rescan queued %+v", p)
	}
}

func TestCollector_OversizedArtifactNotSent(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item(t, strings.Repeat("x", maxArtifactBytes+1))
	snap, err := f.col.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Items) != 1 || len(snap.Items[0].Artifacts) != 0 {
		t.Fatalf("oversized artifact sent: %+v", snap.Items)
	}
	_ = id
}

func TestParseNumStat(t *testing.T) {
	got := parseNumStat("3\t1\ta.go\n-\t-\tbin.png\n10\t0\tdir/b.go\n")
	want := []agentlink.FileStat{{Path: "a.go", Added: 3, Deleted: 1}, {Path: "bin.png"}, {Path: "dir/b.go", Added: 10}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// R34: git URLs from rigs.json never carry credentials to the server.
func TestRedactRemote(t *testing.T) {
	for in, want := range map[string]string{
		"https://u:tok@host/r.git":          "https://host/r.git",
		"https://tok@host/r.git":            "https://host/r.git",
		"https://host/r.git?token=x":        "https://host/r.git",
		"https://u:p@host/r.git?t=x#frag":   "https://host/r.git",
		"git@host:o/r.git":                  "git@host:o/r.git",
		"ssh://git@host/o/r.git":            "ssh://host/o/r.git",
		"/srv/repos/r.git":                  "/srv/repos/r.git",
		"":                                  "",
		"https://u:tok@host/%zz":            "",
		"not a url but has user:pw@host x?": "",
	} {
		if got := RedactRemote(in); got != want {
			t.Errorf("RedactRemote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTownRigs_RemoteRedacted(t *testing.T) {
	town := t.TempDir()
	os.MkdirAll(filepath.Join(town, "mayor"), 0o755)
	os.WriteFile(filepath.Join(town, "mayor", "rigs.json"), []byte(`{"version":1,"rigs":{
		"a":{"git_url":"https://u:tok@host/a.git"},
		"b":{"git_url":"https://host/b.git?token=x"},
		"c":{"git_url":"git@host:o/c.git"}}}`), 0o644)
	rigs, err := NewTown(town, nil).Rigs()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rigs {
		got[r.Name] = r.Remote
	}
	want := map[string]string{"a": "https://host/a.git", "b": "https://host/b.git", "c": "git@host:o/c.git"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("rigs = %v, want %v", got, want)
		}
	}
	b, _ := json.Marshal(rigs)
	if strings.Contains(string(b), "tok") {
		t.Fatalf("credential leaked: %s", b)
	}
}

// R34: free-form error text sent in Result.Data (sling/cancel output, git
// errors) has credentials stripped from any URL it contains.
func TestRedactText(t *testing.T) {
	in := "push failed: fatal: unable to access 'https://x-access-token:ghp_SECRET@github.com/o/r.git/': 403\n" +
		"remote: see https://host/p?token=SECRET2&x=1 and git@host:o/r.git"
	got := RedactText(in)
	if strings.Contains(got, "SECRET") {
		t.Fatalf("secret kept: %q", got)
	}
	for _, keep := range []string{"https://github.com/o/r.git/", "https://host/p", "git@host:o/r.git", "403"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("%q missing from %q", keep, got)
		}
	}
}

func TestApply_FailedSlingOutputRedacted(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item(t, "x\n")
	f.app.Sling = func(context.Context, string, string) error {
		return errors.New("gtb sling: exit status 1: fatal: could not read from 'https://bot:ghp_SECRET@github.com/o/r.git'")
	}
	res := f.app.Apply(context.Background(), req("item.sling", id, nil))
	if res.OK || strings.Contains(string(res.Data), "SECRET") || !strings.Contains(string(res.Data), "github.com/o/r.git") {
		t.Fatalf("result = %s", res.Data)
	}
}

// R37: bare secrets in free text are masked; ordinary text is unchanged.
func TestRedactText_Secrets(t *testing.T) {
	gh := "ghp_" + strings.Repeat("A1b2", 9)
	pat := "github_pat_" + strings.Repeat("Ab1_", 8)
	cases := []struct{ in, want string }{
		{"Authorization: Bearer abc.def.ghi", "Authorization: Bearer <redacted>"},
		{"authorization: basic dXNlcjpwYXNz rest", "authorization: basic <redacted> rest"},
		{"curl -H 'Authorization: Bearer xyz' url", "curl -H 'Authorization: Bearer <redacted>' url"},
		{"token=abc123", "token=<redacted>"},
		{"?a=1&access_token=abc&b=2", "?a=1&access_token=<redacted>&b=2"},
		{"PASSWORD=hunter2 next", "PASSWORD=<redacted> next"},
		{"passwd=x", "passwd=<redacted>"},
		{"secret=s3cr3t", "secret=<redacted>"},
		{"GH_TOKEN=abc go test", "GH_TOKEN=<redacted> go test"},
		{"export NPM_TOKEN=\"quoted value\"", "export NPM_TOKEN=<redacted>"},
		{"AWS_SECRET=xyz DB_PASSWORD='p w'", "AWS_SECRET=<redacted> DB_PASSWORD=<redacted>"},
		{"using " + gh + " now", "using <redacted> now"},
		{"pat " + pat, "pat <redacted>"},
		{"https://x-access-token:SECRET@github.com/o/r.git", "https://github.com/o/r.git"},
		{"x-access-token:SECRET@github.com", "x-access-token:<redacted>@github.com"},
		{"oauth2:glpat-abc@gitlab.com/o/r", "oauth2:<redacted>@gitlab.com/o/r"},
	}
	for _, c := range cases {
		if got := RedactText(c.in); got != c.want {
			t.Errorf("RedactText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, keep := range []string{
		"exit status 1",
		"--- FAIL: TestToken (0.01s)",
		"token expired; run gtb join again",
		"max_tokens=4096 input_tokens=12",
		"TOKEN_COUNT=3",
		"password policy: at least 12 characters",
		"secrets.go:12: undefined: Foo",
		"Authorization required",
		"git@github.com:o/r.git",
		"ok  github.com/x/y 0.12s",
		"ghp_short",
		"a=b c=d",
	} {
		if got := RedactText(keep); got != keep {
			t.Errorf("RedactText(%q) = %q, want unchanged", keep, got)
		}
	}
}

// R37: evidence cmd/tail, peek lines, titles and diffstat paths are redacted
// before they leave the machine.
func TestOutgoingFreeTextRedacted(t *testing.T) {
	secret := "ghp_" + strings.Repeat("Z9", 15)
	tdd, _ := evidenceData(workflow.TDDRun{ItemID: "i", Cmd: `["go","test","-token=` + secret + `"]`, Tail: "fetch https://u:" + secret + "@h/r.git failed"})
	chk, _ := evidenceData(workflow.CheckRun{ItemID: "i", Tail: "GH_TOKEN=" + secret})
	for _, b := range []json.RawMessage{tdd, chk} {
		if strings.Contains(string(b), secret) || !strings.Contains(string(b), "redacted") && !strings.Contains(string(b), "https://h/r.git") {
			t.Fatalf("evidence not redacted: %s", b)
		}
	}
	f := newFixture(t)
	id, _ := f.item(t, "x\n")
	f.peeked = "$ git push\nremote: https://bot:" + secret + "@github.com/o/r.git\n" + strings.Repeat("y", 70<<10)
	res := f.app.Apply(context.Background(), req("terminal.peek", id, nil))
	if strings.Contains(string(res.Data), secret) {
		t.Fatal("peek lines not redacted")
	}
	f.peeked = "token=" + secret
	res = f.app.Apply(context.Background(), req("terminal.peek", id, nil))
	if strings.Contains(string(res.Data), secret) || !strings.Contains(string(res.Data), `token=\u003credacted\u003e`) {
		t.Fatalf("peek = %s", res.Data)
	}
	it, _ := f.env.Items.Create("r", workflow.KindTask, "rotate "+secret, "")
	it.Branch = "b"
	iv := itemView(f.env, it, func(string, string) ([]agentlink.FileStat, error) {
		return []agentlink.FileStat{{Path: "conf/token=" + secret}}, nil
	})
	if b, _ := json.Marshal(iv); strings.Contains(string(b), secret) {
		t.Fatalf("item view leaks: %s", b)
	}
}

// R38: text truncated at its start loses its (possibly cut) first line
// before redaction.
func TestSafeTail(t *testing.T) {
	cases := []struct {
		in        string
		truncated bool
		want      string
	}{
		{"SSWORD=hunter2 x\nok line\n", true, "ok line\n"},
		{"KEN=abc\n", true, ""},
		{"rization: Bearer eyJhbGci\nnext", true, "next"},
		{"no newline at all hunter2", true, ""},
		{"first line\nsecond", false, "first line\nsecond"},
		{"token=abc\nrest", false, "token=<redacted>\nrest"},
	}
	for _, c := range cases {
		if got := SafeTail(c.in, c.truncated); got != c.want {
			t.Errorf("SafeTail(%q, %v) = %q, want %q", c.in, c.truncated, got, c.want)
		}
	}
}

func padTo(s string, n int) string {
	if len(s) >= n {
		return s[:n]
	}
	return s + strings.Repeat("a", n-len(s))
}

func TestEvidenceTail_TruncatedFirstLineDropped(t *testing.T) {
	cut := padTo("SSWORD=hunter2 trailing\n"+strings.Repeat("line\n", 50), workflow.TailBytes)
	for name, rec := range map[string]any{
		"tdd":          workflow.TDDRun{Tail: cut},
		"check passed": workflow.CheckRun{Passed: true, Tail: cut},
		// A failed check tail is capTail: "<cmd>\n" + end of output, so the
		// cut line is the second one; the command line is kept.
		"check failed": workflow.CheckRun{Passed: false, Tail: padTo("make check\nSSWORD=hunter2 x\n"+strings.Repeat("line\n", 50), workflow.TailBytes)},
	} {
		b, _ := evidenceData(rec)
		if strings.Contains(string(b), "hunter2") {
			t.Fatalf("%s: leaked: %.120s", name, b)
		}
		if name == "check failed" && !strings.Contains(string(b), `"tail":"make check\nline`) {
			t.Fatalf("%s: command line not kept: %.120s", name, b)
		}
	}
	short, _ := evidenceData(workflow.TDDRun{Tail: "first line kept\n--- FAIL"})
	if !strings.Contains(string(short), "first line kept") {
		t.Fatalf("untruncated tail lost its first line: %s", short)
	}
	shortCheck, _ := evidenceData(workflow.CheckRun{Tail: "make check\nfirst output line\nFAIL"})
	if !strings.Contains(string(shortCheck), "make check\\nfirst output line") {
		t.Fatalf("untruncated check tail changed: %s", shortCheck)
	}
}

func TestExecGtb_SecretNearCutNotLeaked(t *testing.T) {
	tw := NewTown(t.TempDir(), nil)
	// Output is 805 bytes; the last 800 start inside "GH_TOKEN".
	script := `printf 'GH_TOKEN=SECRETVAL\n'; head -c 785 /dev/zero | tr '\0' a; printf '\n'; exit 1`
	err := tw.ExecGtb(context.Background(), "/bin/sh", "-c", script)
	if err == nil {
		t.Fatal("want error")
	}
	if strings.Contains(err.Error(), "SECRETVAL") {
		t.Fatalf("secret leaked: %.200s", err)
	}
	if !strings.Contains(err.Error(), "aaaa") {
		t.Fatalf("output tail missing: %.200s", err)
	}
}

// R38: a peek capture over the cap loses the line the cut lands in.
func TestPeek_CutLineDropped(t *testing.T) {
	f := newFixture(t)
	id, _ := f.item(t, "x\n")
	// After redaction the secret line reads "export DB_PASSWORD=<redacted>";
	// make the 64 KiB cut land inside it, so the kept part starts "SSWORD=".
	line := "export DB_PASSWORD=<redacted>\n"
	body := strings.Repeat("b", maxPeekBytes-len("SSWORD=<redacted>\n")-1) + "\n"
	f.peeked = "x\nexport DB_PASSWORD=hunter2\n" + body
	if red := RedactText(f.peeked); !strings.HasSuffix(red, "SSWORD=<redacted>\n"+body) || len(red)-len(body) <= len(line) {
		t.Fatalf("fixture does not cut inside the key")
	}
	res := f.app.Apply(context.Background(), req("terminal.peek", id, nil))
	if !res.OK || strings.Contains(string(res.Data), "hunter2") || strings.Contains(string(res.Data), "SSWORD") {
		t.Fatalf("peek leaked: ok=%v %.80s", res.OK, res.Data)
	}
	// Without the R38 rule the cut line would be sent; the kept text is the
	// complete lines after it.
	var pk struct {
		Lines string `json:"lines"`
	}
	json.Unmarshal(res.Data, &pk)
	if pk.Lines != body {
		t.Fatalf("lines = %d bytes, want the %d-byte body", len(pk.Lines), len(body))
	}
}

// checkRunner is a workflow.Runner returning one fixed result.
type checkRunner struct {
	exit int
	tail string
}

func (r checkRunner) Run(context.Context, string, []string) (int, string, error) {
	return r.exit, r.tail, nil
}

// R39: a multi-line check command must not shift checkTail's split into the
// command, or the output's cut first line is sent.
func TestEvidenceTail_MultiLineCheckCommand(t *testing.T) {
	cmd := "set -e\nmake test"
	// Sized so the pre-R39 prefix ("set -e\nmake test\n", 17 bytes) leaves a
	// 983-byte budget whose cut lands inside DB_PASSWORD.
	kept := "SSWORD=hunter2 x\n" + strings.Repeat("line\n", 50)
	kept = padTo(kept, workflow.TailBytes-len(cmd+"\n"))
	out := "export DB_PA" + kept
	cmds := []string{cmd}
	env := workflow.GateEnv{
		TownRoot: t.TempDir(), Rig: "r",
		Store: workflow.NewMemStore(), Items: workflow.NewMemItems(),
		Config: &config.GatedWorkflow{Enabled: true, CheckCommands: cmds, CheckCommandsApprovedSHA: workflow.CheckCommandsSHA(cmds)},
		Now:    func() time.Time { return t0 },
	}
	run, err := workflow.Check(env, checkRunner{exit: 1, tail: out}, "it-1", t.TempDir())
	if err != nil || run.Passed {
		t.Fatalf("Check: %+v %v", run, err)
	}
	b, _ := evidenceData(run)
	if strings.Contains(string(b), "hunter2") {
		t.Fatalf("leaked: %.160s", b)
	}
	if !strings.Contains(string(b), `"tail":"set -e ⏎ make test\nline`) {
		t.Fatalf("command line not kept as one line: %.160s", b)
	}
}
