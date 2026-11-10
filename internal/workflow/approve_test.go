package workflow

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeTerminal struct {
	tty     bool
	answer  bool
	err     error
	prompts []string
}

func (f *fakeTerminal) IsTTY() bool { return f.tty }
func (f *fakeTerminal) Confirm(p string) (bool, error) {
	f.prompts = append(f.prompts, p)
	return f.answer, f.err
}

func noEnv(string) string { return "" }

func approveEnv(t *testing.T) (GateEnv, string) {
	t.Helper()
	env := newTestEnv(t)
	env.Now = func() time.Time { return fixedNow }
	path := ArtifactPath(env.TownRoot, env.Rig, "it-1", "spec")
	writeFile(t, path, "---\nstatus: draft\n---\nbody\n")
	_, sha, _, err := ReadArtifact(path)
	if err != nil {
		t.Fatal(err)
	}
	return env, sha
}

func TestHumanApprove_NoTTYRefused(t *testing.T) {
	env, _ := approveEnv(t)
	term := &fakeTerminal{tty: false, answer: true}
	if _, err := HumanApprove(env, term, noEnv, "it-1", "spec", "approve", "me"); !errors.Is(err, ErrNotHuman) {
		t.Fatalf("err = %v, want ErrNotHuman", err)
	}
	if len(term.prompts) != 0 {
		t.Fatal("must not prompt")
	}
	if as, _ := env.Store.Approvals("it-1"); len(as) != 0 {
		t.Fatalf("stored = %+v", as)
	}
}

func TestApproveGuard_TTYButAgentEnv(t *testing.T) {
	env, _ := approveEnv(t)
	term := &fakeTerminal{tty: true, answer: true}
	getenv := func(k string) string {
		if k == "GT_ROLE" {
			return "rig/polecats/nux"
		}
		return ""
	}
	if _, err := HumanApprove(env, term, getenv, "it-1", "spec", "approve", "me"); !errors.Is(err, ErrNotHuman) {
		t.Fatalf("err = %v, want ErrNotHuman", err)
	}
	if len(term.prompts) != 0 {
		t.Fatal("must not prompt")
	}
	if as, _ := env.Store.Approvals("it-1"); len(as) != 0 {
		t.Fatalf("stored = %+v", as)
	}
}

func TestApproveGuard_EveryMarker(t *testing.T) {
	for _, marker := range AgentMarkerEnv {
		env, _ := approveEnv(t)
		getenv := func(k string) string {
			if k == marker {
				return "1"
			}
			return ""
		}
		if _, err := HumanApprove(env, &fakeTerminal{tty: true, answer: true}, getenv, "it-1", "spec", "approve", "me"); !errors.Is(err, ErrNotHuman) {
			t.Errorf("%s: err = %v, want ErrNotHuman", marker, err)
		}
	}
}

func TestApproveGuard_CheckedBeforeReadingFile(t *testing.T) {
	env := newTestEnv(t)
	if _, err := HumanApprove(env, &fakeTerminal{}, noEnv, "it-1", "missing", "approve", "me"); !errors.Is(err, ErrNotHuman) {
		t.Fatalf("err = %v, want ErrNotHuman before file read", err)
	}
}

func TestHumanApprove_Records(t *testing.T) {
	for verdict, prompt := range map[string]string{
		"approve":         "Approve spec sha %s for it-1? [y/N]",
		"request_changes": "Request changes on spec sha %s for it-1? [y/N]",
		"reject":          "Reject spec sha %s for it-1? [y/N]",
	} {
		env, sha := approveEnv(t)
		term := &fakeTerminal{tty: true, answer: true}
		a, err := HumanApprove(env, term, noEnv, "it-1", "spec", verdict, "me")
		if err != nil {
			t.Fatalf("%s: %v", verdict, err)
		}
		want := Approval{ItemID: "it-1", Artifact: "spec", SHA: sha, Verdict: verdict, Role: "human", Source: "human", Channel: "terminal", Actor: "me", At: fixedNow.UTC()}
		if a != want {
			t.Fatalf("%s: approval = %+v, want %+v", verdict, a, want)
		}
		if len(term.prompts) != 1 || term.prompts[0] != fmt.Sprintf(prompt, sha[:12]) {
			t.Fatalf("%s: prompts = %q", verdict, term.prompts)
		}
		if as, _ := env.Store.Approvals("it-1"); len(as) != 1 || as[0] != want {
			t.Fatalf("%s: stored = %+v", verdict, as)
		}
	}
}

func TestHumanApprove_NotConfirmed(t *testing.T) {
	env, _ := approveEnv(t)
	term := &fakeTerminal{tty: true, answer: false}
	if _, err := HumanApprove(env, term, noEnv, "it-1", "spec", "approve", "me"); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("err = %v, want ErrNotConfirmed", err)
	}
	if as, _ := env.Store.Approvals("it-1"); len(as) != 0 {
		t.Fatalf("stored = %+v", as)
	}
}

func TestHumanApprove_BadVerdictAndMissingFile(t *testing.T) {
	env, _ := approveEnv(t)
	term := &fakeTerminal{tty: true, answer: true}
	if _, err := HumanApprove(env, term, noEnv, "it-1", "spec", "concerns", "me"); err == nil {
		t.Fatal("want error for verdict concerns")
	}
	if _, err := HumanApprove(env, term, noEnv, "it-1", "plan", "approve", "me"); err == nil {
		t.Fatal("want error for missing artifact")
	}
	if _, err := HumanApprove(env, term, noEnv, "it-1", "../spec", "approve", "me"); err == nil {
		t.Fatal("want error for path-like artifact name")
	}
	if as, _ := env.Store.Approvals("it-1"); len(as) != 0 {
		t.Fatalf("stored = %+v", as)
	}
}

func TestConfirmAnswer(t *testing.T) {
	for in, want := range map[string]bool{"y": true, "Y\n": true, " yes \n": true, "YES": true, "": false, "n": false, "yep": false, "no": false} {
		if got := confirmAnswer(in); got != want {
			t.Errorf("confirmAnswer(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestStdTerminal_NotTTY(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "in"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if (StdTerminal{In: f, Out: f}).IsTTY() {
		t.Fatal("regular file is not a TTY")
	}
	if (StdTerminal{In: f, Out: &discard{}}).IsTTY() {
		t.Fatal("non-file Out is not a TTY")
	}
}

type discard struct{}

func (*discard) Write(p []byte) (int, error) { return len(p), nil }

func TestRecordVerdict_HumanRoleRefused(t *testing.T) {
	env, _ := approveEnv(t)
	for _, role := range []string{"human", " Human ", "HUMAN"} {
		if _, err := RecordVerdict(env, "it-1", "spec", role, "approve", "agent"); err == nil {
			t.Errorf("role %q: want error", role)
		}
	}
	if as, _ := env.Store.Approvals("it-1"); len(as) != 0 {
		t.Fatalf("stored = %+v", as)
	}
}

func TestRecordVerdict_Records(t *testing.T) {
	env, sha := approveEnv(t)
	a, err := RecordVerdict(env, "it-1", "spec", "reviewer", "concerns", "nux")
	if err != nil {
		t.Fatal(err)
	}
	if a.Source != "agent" || a.Role != "reviewer" || a.SHA != sha || a.Verdict != "concerns" || a.Actor != "nux" || !a.At.Equal(fixedNow) {
		t.Fatalf("approval = %+v", a)
	}
	for _, bad := range []string{"request_changes", "lgtm", ""} {
		if _, err := RecordVerdict(env, "it-1", "spec", "reviewer", bad, "nux"); err == nil {
			t.Errorf("verdict %q: want error", bad)
		}
	}
	if _, err := RecordVerdict(env, "it-1", "plan", "reviewer", "approve", "nux"); err == nil {
		t.Error("missing artifact: want error")
	}
	if _, err := RecordVerdict(env, "it-1", "spec", "  ", "approve", "nux"); err == nil {
		t.Error("blank role: want error")
	}
}
