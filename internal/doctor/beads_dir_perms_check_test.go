package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBeadsDirPermsCheck_NoBeadsDir(t *testing.T) {
	check := NewBeadsDirPermsCheck()
	result := check.Run(&CheckContext{TownRoot: t.TempDir()})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want %v", result.Status, StatusOK)
	}
}

func TestBeadsDirPermsCheck_Detects0755AndFixSets0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	townRoot := t.TempDir()
	townBeads := filepath.Join(townRoot, ".beads")
	rigBeads := filepath.Join(townRoot, "demo", ".beads")
	if err := os.MkdirAll(townBeads, 0o755); err != nil {
		t.Fatalf("mkdir town .beads: %v", err)
	}
	if err := os.MkdirAll(rigBeads, 0o755); err != nil {
		t.Fatalf("mkdir rig .beads: %v", err)
	}

	check := NewBeadsDirPermsCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Status = %v, want %v (%s)", result.Status, StatusWarning, result.Message)
	}
	if len(check.loose) != 2 {
		t.Fatalf("loose dirs = %d, want 2: %v", len(check.loose), check.loose)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	assertBeadsMode(t, townBeads, 0o700)
	assertBeadsMode(t, rigBeads, 0o700)

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("Status after fix = %v, want %v (%s)", result.Status, StatusOK, result.Message)
	}
}

func TestBeadsDirPermsCheck_OKWhenAlready0700(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o700); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	check := NewBeadsDirPermsCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want %v (%s)", result.Status, StatusOK, result.Message)
	}
}

func assertBeadsMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}
