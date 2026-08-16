package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

func setupCmdTestRegistry(t *testing.T) {
	t.Helper()
	registry := session.NewPrefixRegistry()
	registry.Register("gt", "gastown")
	registry.Register("do", "coder_dotfiles")
	registry.Register("mr", "myrig")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(registry)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestStatusLineAvoidsBeadsHotPath(t *testing.T) {
	if !beadsExemptCommands["status-line"] {
		t.Fatal("status-line must be exempt from bd version checks")
	}
	if !branchCheckExemptCommands["status-line"] {
		t.Fatal("status-line must be exempt from git branch/stale checks")
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "statusline.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	for _, forbidden := range []string{
		`internal/beads`,
		`internal/mail`,
		`beads.New`,
		`mail.New`,
		`getHookedWork`,
		`getMailPreview`,
		`ListUnread`,
		`getRefineryManager`,
		`.Queue()`,
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("status-line hot path must not contain %q", forbidden)
		}
	}
}

func TestSchedulerRunAvoidsRootBeadsChecks(t *testing.T) {
	if !beadsExemptCommands["scheduler"] {
		t.Fatal("scheduler must be exempt from root bd version checks")
	}
	if !branchCheckExemptCommands["scheduler"] {
		t.Fatal("scheduler must be exempt from root git branch checks")
	}
	if !isCommandOrAncestorExempt(schedulerRunCmd, beadsExemptCommands) {
		t.Fatal("scheduler run must inherit bd exemption from scheduler parent")
	}
	if !isCommandOrAncestorExempt(schedulerRunCmd, branchCheckExemptCommands) {
		t.Fatal("scheduler run must inherit branch-check exemption from scheduler parent")
	}
}
