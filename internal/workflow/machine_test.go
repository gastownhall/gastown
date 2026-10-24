package workflow

import (
	"reflect"
	"testing"
)

func TestSteps_Task(t *testing.T) {
	want := []Step{"spec", "plan", "execute", "verify", "review", "merge"}
	if got := Steps(KindTask); !reflect.DeepEqual(got, want) {
		t.Fatalf("Steps(KindTask) = %v, want %v", got, want)
	}
}

func TestNext_LastStep(t *testing.T) {
	s, ok := Next(KindTask, "merge")
	if s != "" || ok {
		t.Fatalf("Next(KindTask, merge) = (%q, %v), want (\"\", false)", s, ok)
	}
}

func TestArtifactPath(t *testing.T) {
	got := ArtifactPath("/t", "r", "gt-1", "spec")
	if want := "/t/r/.gtb/items/gt-1/spec.md"; got != want {
		t.Fatalf("ArtifactPath = %q, want %q", got, want)
	}
}

func TestSteps_FixAndEpic(t *testing.T) {
	if got, want := Steps(KindFix), []Step{"report", "reproduce", "rca", "fix", "verify", "merge"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Steps(KindFix) = %v, want %v", got, want)
	}
	if got, want := Steps(KindEpic), []Step{"epic", "breakdown", "tasks", "verification"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Steps(KindEpic) = %v, want %v", got, want)
	}
	if got := Steps("bogus"); got != nil {
		t.Errorf("Steps(bogus) = %v, want nil", got)
	}
}

func TestNext_Advances(t *testing.T) {
	if s, ok := Next(KindTask, "spec"); s != "plan" || !ok {
		t.Errorf("Next(task, spec) = (%q, %v)", s, ok)
	}
	if s, ok := Next(KindEpic, "verification"); s != "" || ok {
		t.Errorf("Next(epic, verification) = (%q, %v)", s, ok)
	}
	if s, ok := Next(KindTask, "nope"); s != "" || ok {
		t.Errorf("Next(task, nope) = (%q, %v)", s, ok)
	}
	if s, ok := Next(KindTask, StepDone); s != "" || ok {
		t.Errorf("Next(task, done) = (%q, %v)", s, ok)
	}
}

func TestArtifactFor(t *testing.T) {
	cases := []struct {
		k    Kind
		s    Step
		want string
	}{
		{KindTask, "spec", "spec"}, {KindTask, "plan", "plan"}, {KindTask, "review", "review"},
		{KindTask, "execute", ""}, {KindTask, "verify", ""}, {KindTask, "merge", ""},
		{KindFix, "report", "report"}, {KindFix, "rca", "rca"}, {KindFix, "reproduce", ""}, {KindFix, "fix", ""},
		{KindEpic, "epic", "epic"}, {KindEpic, "breakdown", "breakdown"}, {KindEpic, "verification", "verification"},
		{KindEpic, "tasks", ""}, {KindFix, "spec", ""},
	}
	for _, c := range cases {
		if got := ArtifactFor(c.k, c.s); got != c.want {
			t.Errorf("ArtifactFor(%s, %s) = %q, want %q", c.k, c.s, got, c.want)
		}
	}
}
