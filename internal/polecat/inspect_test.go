package polecat

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/git"
)

func TestInspectWorkstateReusableIdle(t *testing.T) {
	env := setupInspectWorkstateEnv(t, inspectWorkstateFixture{
		agentDesc: "agent\n\nrole_type: polecat\nrig: testrig\nagent_state: idle\nhook_bead: null\ncleanup_status: clean\n",
	})
	got := InspectWorkstate("toast", env.bd, env.clone, StateIdle, "")
	if !got.Reusable || !got.SafeToNuke || got.Verdict != WorkstateVerdictSafeToNuke {
		t.Fatalf("InspectWorkstate() = %+v, want reusable idle", got)
	}
}

func TestInspectWorkstateHookBlocksReuse(t *testing.T) {
	env := setupInspectWorkstateEnv(t, inspectWorkstateFixture{
		agentDesc: "agent\n\nrole_type: polecat\nrig: testrig\nagent_state: idle\nhook_bead: gt-work\ncleanup_status: clean\n",
		shows: map[string]string{
			"gt-work": `[{"id":"gt-work","title":"work","status":"hooked","issue_type":"task"}]`,
		},
	})
	got := InspectWorkstate("toast", env.bd, env.clone, StateIdle, "gt-work")
	if got.Reusable || !got.NeedsRecovery || got.Reason != "hook-still-set" {
		t.Fatalf("InspectWorkstate() = %+v, want hook-still-set", got)
	}
}

func TestInspectWorkstateGitDirtyBlocksReuse(t *testing.T) {
	env := setupInspectWorkstateEnv(t, inspectWorkstateFixture{
		agentDesc: "agent\n\nrole_type: polecat\nrig: testrig\nagent_state: idle\nhook_bead: null\ncleanup_status: clean\n",
		dirtyGit:  true,
	})
	got := InspectWorkstate("toast", env.bd, env.clone, StateIdle, "")
	if got.Reusable || !got.NeedsRecovery || got.Reason != "git-dirty" {
		t.Fatalf("InspectWorkstate() = %+v, want git-dirty", got)
	}
}

func TestInspectWorkstateActiveMRBlocksReuse(t *testing.T) {
	env := setupInspectWorkstateEnv(t, inspectWorkstateFixture{
		agentDesc: "agent\n\nrole_type: polecat\nrig: testrig\nagent_state: idle\nhook_bead: null\ncleanup_status: clean\nactive_mr: gt-mr\n",
		shows: map[string]string{
			"gt-mr": `[{"id":"gt-mr","title":"MR","status":"open","issue_type":"task","labels":["gt:merge-request"],"description":"branch: polecat/toast\ntarget: main\n"}]`,
		},
	})
	got := InspectWorkstate("toast", env.bd, env.clone, StateIdle, "")
	if got.Reusable || got.Verdict != WorkstateVerdictPendingMR {
		t.Fatalf("InspectWorkstate() = %+v, want pending MR", got)
	}
}

type inspectWorkstateFixture struct {
	agentDesc string
	shows     map[string]string
	dirtyGit  bool
}

type inspectWorkstateEnv struct {
	bd    *beads.Beads
	clone string
}

func setupInspectWorkstateEnv(t *testing.T, fx inspectWorkstateFixture) inspectWorkstateEnv {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script bd stub not supported on Windows")
	}

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")
	clone := filepath.Join(rigPath, "polecats", "toast", "testrig")
	if err := os.MkdirAll(clone, 0755); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	initInspectGitClone(t, clone, fx.dirtyGit)

	shows := map[string]string{}
	for id, payload := range fx.shows {
		shows[id] = payload
	}
	agentJSON := `[{"id":"gt-testrig-polecat-toast","title":"agent","issue_type":"agent","status":"open","description":` + jsonString(fx.agentDesc) + `}]`
	binDir := t.TempDir()
	script := inspectBdStub(agentJSON, shows)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	return inspectWorkstateEnv{bd: beads.New(rigPath), clone: clone}
}

func initInspectGitClone(t *testing.T, clone string, dirty bool) {
	t.Helper()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = clone
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(clone, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "base")
	run("branch", "-M", "main")
	remote := filepath.Join(filepath.Dir(clone), "remote.git")
	if err := exec.Command("git", "init", "--bare", remote).Run(); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}
	run("remote", "add", "origin", remote)
	run("push", "-u", "origin", "main")
	if dirty {
		if err := os.WriteFile(filepath.Join(clone, "dirty.txt"), []byte("dirty\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	_ = git.NewGit(clone)
}

func inspectBdStub(agentJSON string, shows map[string]string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\ncmd=\"\"\nid=\"\"\nprev=\"\"\nfor arg in \"$@\"; do\n  case \"$arg\" in\n    --*) ;;\n    *)\n      if [ -z \"$cmd\" ]; then cmd=\"$arg\"; fi\n      if [ \"$prev\" = \"show\" ]; then id=\"$arg\"; fi\n      prev=\"$arg\"\n      ;;\n  esac\ndone\ncase \"$cmd\" in\n  list)\n    echo '[]'\n    ;;\n  show)\n    case \"$id\" in\n")
	for id, payload := range shows {
		b.WriteString("      " + id + ")\n        printf '%s\\n' '" + strings.ReplaceAll(payload, "'", "'\"'\"'") + "'\n        ;;\n")
	}
	b.WriteString("      *)\n        printf '%s\\n' '" + strings.ReplaceAll(agentJSON, "'", "'\"'\"'") + "'\n        ;;\n    esac\n    ;;\n  *)\n    echo '[]'\n    ;;\nesac\n")
	return b.String()
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
