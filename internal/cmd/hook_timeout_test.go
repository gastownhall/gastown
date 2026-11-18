package cmd

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
)

func TestHookStatusTimeoutDiagnosticNamesStageAndQuery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	stage := &hookStatusStage{}
	stage.begin("rig active-work query: bd list/query --assignee=gastown/dogs/bravo")
	err := stage.wrap(ctx, context.DeadlineExceeded)
	message := err.Error()

	for _, want := range []string{
		"gt hook timed out",
		"rig active-work query",
		"bd list/query",
		"gastown/dogs/bravo",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("timeout diagnostic %q does not contain %q", message, want)
		}
	}
}

func TestHookStatusStagePreservesNonTimeoutQueryError(t *testing.T) {
	stage := &hookStatusStage{}
	stage.begin("town active-work query: bd query ephemeral=true")

	err := stage.wrap(context.Background(), context.Canceled)
	if got := err.Error(); !strings.Contains(got, "town active-work query") ||
		!strings.Contains(got, "context canceled") {
		t.Fatalf("query diagnostic lost stage or cause: %q", got)
	}
}

func TestRunMoleculeStatusBoundsSlowDogHookQuery(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX shell bd stub")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	dogDir := filepath.Join(townRoot, "deacon", "dogs", "bravo")
	if err := os.MkdirAll(dogDir, 0755); err != nil {
		t.Fatalf("mkdir dog dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dogDir, ".git"), 0755); err != nil {
		t.Fatalf("mkdir dog git metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dogDir, ".git", "HEAD"), []byte("ref: refs/heads/test\n"), 0644); err != nil {
		t.Fatalf("write dog git HEAD: %v", err)
	}
	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin dir: %v", err)
	}
	stub := `#!/bin/sh
if [ "${1:-}" = "--allow-stale" ]; then
  shift
fi
if [ "${1:-}" = "version" ]; then
  echo "bd test"
  exit 0
fi
sleep 30
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(stub), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dogDir); err != nil {
		t.Fatalf("chdir dog: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("GT_ROLE", "")
	beads.ResetBdAllowStaleCacheForTest()

	oldTimeout := hookStatusTimeout
	hookStatusTimeout = 150 * time.Millisecond
	t.Cleanup(func() { hookStatusTimeout = oldTimeout })

	start := time.Now()
	err = runMoleculeStatus(&cobra.Command{}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("slow dog hook query unexpectedly succeeded")
	}
	if elapsed > time.Second {
		t.Fatalf("dog hook query exceeded command bound: %s", elapsed)
	}
	for _, want := range []string{
		"gt hook timed out after 150ms",
		"rig active-work query",
		"bd list",
		"--assignee=deacon/dogs/bravo",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("timeout diagnostic %q does not contain %q", err, want)
		}
	}
}
