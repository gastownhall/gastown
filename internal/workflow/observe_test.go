package workflow

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func TestValidateArtifactName(t *testing.T) {
	for _, ok := range []string{"spec", "plan", "rca", "a_b-c1"} {
		if err := ValidateArtifactName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Spec", "../x", "a/b", "1x", "spec.md"} {
		if err := ValidateArtifactName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestWriteStub(t *testing.T) {
	env := newTestEnv(t)
	p := ArtifactPath(env.TownRoot, env.Rig, "gt-1", "spec")
	if err := WriteStub(p, "gt-1", "gt-0", KindTask, "Do it"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	want := "---\nid: gt-1\nstatus: draft\nparent: gt-0\nkind: task\n---\n# Do it\n"
	if string(b) != want {
		t.Fatalf("stub = %q", b)
	}
	if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
	if err := WriteStub(p, "gt-1", "", KindTask, "again"); err == nil {
		t.Fatal("stub overwritten")
	}
}

type fixedRunner struct {
	exit int
}

func (f fixedRunner) Run(context.Context, string, []string) (int, string, error) {
	return f.exit, "tail", nil
}

func TestEvidence_ObserverEmits(t *testing.T) {
	env := newTestEnv(t)
	obs := &recObserver{}
	env.Observer = obs
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/a_test.go", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Red(env, fixedRunner{exit: 1}, "gt-1", "s1", dir, []string{"go", "test"}, []string{"a_test.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(env, fixedRunner{exit: 0}, "gt-1", dir); err != nil {
		t.Fatal(err)
	}
	writeArtifact(t, env, "gt-1", "review", "r\n")
	if _, err := RecordVerdict(env, "gt-1", "review", "reviewer", "approve", "bot"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(obs.kinds, ",") != "evidence,evidence,evidence" {
		t.Fatalf("kinds = %v", obs.kinds)
	}
	if _, ok := obs.data[0].(TDDRun); !ok {
		t.Fatalf("data[0] = %T", obs.data[0])
	}
	if _, ok := obs.data[1].(CheckRun); !ok {
		t.Fatalf("data[1] = %T", obs.data[1])
	}
	if a, ok := obs.data[2].(Approval); !ok || a.Artifact != "review" {
		t.Fatalf("data[2] = %#v", obs.data[2])
	}
}

type failAddStore struct{ Store }

func (failAddStore) AddApproval(Approval) error { return errors.New("down") }

func TestEvidence_NoEmitOnStoreError(t *testing.T) {
	env := newTestEnv(t)
	env.Store = failAddStore{env.Store}
	obs := &recObserver{}
	env.Observer = obs
	writeArtifact(t, env, "gt-1", "review", "r\n")
	if _, err := RecordVerdict(env, "gt-1", "review", "reviewer", "approve", "bot"); err == nil {
		t.Fatal("want error")
	}
	if len(obs.kinds) != 0 {
		t.Fatalf("emitted %v on failure", obs.kinds)
	}
	_ = time.Now
}
