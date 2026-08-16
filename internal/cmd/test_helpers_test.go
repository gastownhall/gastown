package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildGT builds the gt binary and returns its path.
// It caches the build across tests in the same run.
var cachedGTBinary string

func buildGT(t *testing.T) string {
	t.Helper()

	if cachedGTBinary != "" {
		// Verify cached binary still exists
		if _, err := os.Stat(cachedGTBinary); err == nil {
			return cachedGTBinary
		}
		// Binary was cleaned up, rebuild
		cachedGTBinary = ""
	}

	// Find project root (where go.mod is)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working directory: %v", err)
	}

	// Walk up to find go.mod
	projectRoot := wd
	for {
		if _, err := os.Stat(filepath.Join(projectRoot, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(projectRoot)
		if parent == projectRoot {
			t.Fatal("could not find project root (go.mod)")
		}
		projectRoot = parent
	}

	// Build gt binary to a persistent temp location (not per-test)
	tmpDir := os.TempDir()
	binaryName := "gt-integration-test"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	tmpBinary := filepath.Join(tmpDir, binaryName)
	cmd := exec.Command("go", "build", "-o", tmpBinary, "./cmd/gt")
	cmd.Dir = projectRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build gt: %v\nOutput: %s", err, output)
	}

	cachedGTBinary = tmpBinary
	return tmpBinary
}
