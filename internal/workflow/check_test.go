package workflow

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCheck_UnapprovedRefused(t *testing.T) {
	env, dir := tddEnv(t)
	env.Config.CheckCommands = []string{"go test ./...", "rm -rf /"}
	r := &fakeRunner{}
	_, err := Check(env, r, "it-1", dir)
	if err == nil || !strings.Contains(err.Error(), "check commands not approved") {
		t.Fatalf("err = %v", err)
	}
	if len(r.calls) != 0 {
		t.Fatal("unapproved commands must not run")
	}
	if cs, _ := env.Store.Checks("it-1"); len(cs) != 0 {
		t.Fatalf("stored = %+v", cs)
	}
}

func approvedChecks(env GateEnv, cmds ...string) {
	env.Config.CheckCommands = cmds
	env.Config.CheckCommandsApprovedSHA = CheckCommandsSHA(cmds)
}

func TestCheck_AllPass(t *testing.T) {
	env, dir := tddEnv(t)
	approvedChecks(env, "go vet ./...", "go test ./...")
	r := &fakeRunner{results: []fakeResult{{exit: 0, tail: "vet ok"}, {exit: 0, tail: "test ok"}}}
	c, err := Check(env, r, "it-1", dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !c.Passed || c.Tail != "test ok" || c.Source != "gtb" || c.Provenance != "verified" ||
		c.CmdSetSHA != CheckCommandsSHA(env.Config.CheckCommands) || !c.At.Equal(fixedNow) || c.At.Location() != time.UTC {
		t.Fatalf("check = %+v", c)
	}
	want := []fakeCall{{dir, []string{"sh", "-c", "go vet ./..."}}, {dir, []string{"sh", "-c", "go test ./..."}}}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %+v", r.calls)
	}
	if cs, _ := env.Store.Checks("it-1"); len(cs) != 1 || !cs[0].Passed {
		t.Fatalf("stored = %+v", cs)
	}
}

func TestCheck_StopsAtFirstFailure(t *testing.T) {
	env, dir := tddEnv(t)
	approvedChecks(env, "lint", "go test ./...", "never")
	r := &fakeRunner{results: []fakeResult{{exit: 0, tail: "ok"}, {exit: 1, tail: "FAIL x"}}}
	c, err := Check(env, r, "it-1", dir)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if c.Passed || !strings.HasPrefix(c.Tail, "go test ./...") || !strings.HasSuffix(c.Tail, "FAIL x") {
		t.Fatalf("check = %+v", c)
	}
	if len(r.calls) != 2 {
		t.Fatalf("calls = %d, want stop after failure", len(r.calls))
	}
	if cs, _ := env.Store.Checks("it-1"); len(cs) != 1 || cs[0].Passed {
		t.Fatalf("stored = %+v", cs)
	}
}

func TestCheck_FailingTailCapped(t *testing.T) {
	for _, tc := range []struct {
		name, cmd string
		keepsOut  bool
	}{
		// 908-byte command: prefix fits, output end fills the rest.
		{"long command", "go test " + strings.Repeat("é", 450), true},
		// 1409-byte command: the command prefix alone uses the budget.
		{"command over budget", "go test " + strings.Repeat("é", 700), false},
		{"short command", "go test ./...", true},
	} {
		env, dir := tddEnv(t)
		approvedChecks(env, tc.cmd)
		out := strings.Repeat("x", 1) + strings.Repeat("ü", 499) + "!" // 1000 bytes, ends with "!"
		r := &fakeRunner{results: []fakeResult{{exit: 1, tail: out}}}
		c, err := Check(env, r, "it-1", dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Tail) > 1000 || !utf8.ValidString(c.Tail) || c.Passed {
			t.Fatalf("%s: len %d valid %v", tc.name, len(c.Tail), utf8.ValidString(c.Tail))
		}
		if !strings.HasPrefix(c.Tail, tc.cmd[:min(len(tc.cmd), 100)]) || strings.HasSuffix(c.Tail, "!") != tc.keepsOut {
			t.Fatalf("%s: tail must keep command prefix and output end: %q...%q", tc.name, c.Tail[:20], c.Tail[len(c.Tail)-5:])
		}
	}
}

func TestCapTail(t *testing.T) {
	if got := capTail("cmd", "out"); got != "cmd\nout" {
		t.Fatalf("short = %q", got)
	}
	got := capTail(strings.Repeat("c", 400), strings.Repeat("o", 1000))
	if len(got) != 1000 || !strings.HasPrefix(got, strings.Repeat("c", 400)+"\n") {
		t.Fatalf("len %d", len(got))
	}
	got = capTail(strings.Repeat("ç", 800), "OUT")
	if len(got) != 1000 || !utf8.ValidString(got) || !strings.HasPrefix(got, "ççç") || strings.Contains(got, "OUT") {
		t.Fatalf("long cmd: len %d %q", len(got), got[len(got)-6:])
	}
	got = capTail("c", strings.Repeat("ü", 600))
	if len(got) != 1000 || !utf8.ValidString(got) || !strings.HasPrefix(got, "c\n") {
		t.Fatalf("odd budget: len %d", len(got))
	}
}

// R39: a multi-line command is flattened so the failing tail's command
// prefix is exactly one line.
func TestCapTail_MultiLineCommandOneLinePrefix(t *testing.T) {
	for cmd, want := range map[string]string{
		"set -e\nmake test": "set -e ⏎ make test\n",
		"a\r\nb\rc\nd":      "a ⏎ b ⏎ c ⏎ d\n",
		"go test ./...":     "go test ./...\n",
		"trailing\n":        "trailing ⏎ \n",
	} {
		got := capTail(cmd, "out1\nout2")
		if got != want+"out1\nout2" {
			t.Fatalf("capTail(%q) = %q", cmd, got)
		}
	}
	env, dir := tddEnv(t)
	approvedChecks(env, "set -e\nmake test")
	r := &fakeRunner{results: []fakeResult{{exit: 1, tail: "FAIL x"}}}
	c, err := Check(env, r, "it-1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Tail != "set -e ⏎ make test\nFAIL x" || c.CmdSetSHA != CheckCommandsSHA([]string{"set -e\nmake test"}) {
		t.Fatalf("check = %+v", c)
	}
	if want := []string{"sh", "-c", "set -e\nmake test"}; !reflect.DeepEqual(r.calls[0].argv, want) {
		t.Fatalf("argv = %q, the command itself must run unchanged", r.calls[0].argv)
	}
}
