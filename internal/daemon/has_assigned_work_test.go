package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHasAssignedOpenWork_UsesPinnedBeadsDirInsteadOfRigOrRepoFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mocks")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(townRoot, ".beads", "routes.jsonl"),
		[]byte("{\"prefix\":\"gt-\",\"path\":\"gastown/mayor/rig\"}\n"),
		0o644,
	); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	expectedBeadsDir := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	script := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    --repo=*)
      echo "unexpected --repo flag with pinned BEADS_DIR" >&2
      exit 1
      ;;
    --rig=*)
      echo "Error: unknown flag: --rig" >&2
      exit 1
      ;;
    esac
done
if [ "$BEADS_DIR" != "` + expectedBeadsDir + `" ]; then
  echo "unexpected BEADS_DIR: $BEADS_DIR" >&2
  exit 1
fi
printf 'BEADS_DIR=%s args=%s\n' "$BEADS_DIR" "$*" >> "` + logPath + `"
printf '[{"id":"gt-123"}]\n'
`
	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		bdPath: bdPath,
	}

	hasWork, workErr := d.hasAssignedOpenWork("gastown", "polecats/rust")
	if workErr != nil {
		t.Fatalf("expected assigned work lookup to succeed, got error: %v", workErr)
	}
	if !hasWork {
		t.Fatal("expected assigned work lookup to succeed")
	}

	logOutput, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read bd log: %v", err)
	}
	loggedArgs := string(logOutput)
	if strings.Contains(loggedArgs, "--rig=") {
		t.Fatalf("expected bd call to avoid --rig, got %q", loggedArgs)
	}
	if strings.Contains(loggedArgs, "--repo=") {
		t.Fatalf("expected bd call to avoid --repo with pinned BEADS_DIR, got %q", loggedArgs)
	}
	if !strings.Contains(loggedArgs, "BEADS_DIR="+expectedBeadsDir) {
		t.Fatalf("expected bd call to pin BEADS_DIR to %q, got %q", expectedBeadsDir, loggedArgs)
	}
}
