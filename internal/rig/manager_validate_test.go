package rig

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/git"
)

func TestAddRig_ValidateCheckoutRefusalCleansUpAndAllowsRetry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-based bd shim not reliable on Windows CI")
	}
	fakeBDForAddRig(t)

	root, rigsConfig := setupTestTown(t)
	if err := os.MkdirAll(filepath.Join(root, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := createTestGitRepoForRig(t, "tbbrepo")
	if err := os.MkdirAll(filepath.Join(repo, ".tbb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".tbb", "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"git", "add", "."}, {"git", "commit", "-m", "add tbb"}} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}

	refusal := errors.New("refused")
	var seen string
	manager := NewManager(root, rigsConfig, git.NewGit(root))
	_, err := manager.AddRig(AddRigOptions{
		Name:          "tbbrig",
		GitURL:        repo,
		BeadsPrefix:   "tb",
		SkipDoltCheck: true,
		ValidateCheckout: func(dir string) error {
			seen = dir
			if _, statErr := os.Stat(filepath.Join(dir, ".tbb")); statErr != nil {
				t.Errorf("checkout %s has no .tbb/: %v", dir, statErr)
			}
			return refusal
		},
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("AddRig err = %v, want refusal", err)
	}
	rigPath := filepath.Join(root, "tbbrig")
	if want := filepath.Join(rigPath, "mayor", "rig"); seen != want {
		t.Fatalf("ValidateCheckout dir = %q, want %q", seen, want)
	}
	if _, statErr := os.Stat(rigPath); !os.IsNotExist(statErr) {
		t.Fatalf("rig dir still exists after refusal (stat err %v)", statErr)
	}
	if manager.RigExists("tbbrig") {
		t.Fatal("rig registered in memory after refusal")
	}
	if cfg, loadErr := config.LoadRigsConfig(filepath.Join(root, "mayor", "rigs.json")); loadErr == nil {
		if _, ok := cfg.Rigs["tbbrig"]; ok {
			t.Fatal("rig registered in rigs.json after refusal")
		}
	}

	// Retry without the refusal succeeds on the same name.
	if _, err := manager.AddRig(AddRigOptions{
		Name: "tbbrig", GitURL: repo, BeadsPrefix: "tb", SkipDoltCheck: true,
	}); err != nil {
		t.Fatalf("retry AddRig: %v", err)
	}
}
