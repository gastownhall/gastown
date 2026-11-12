package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWitnessExistsCheck_AcceptsCanonicalCloneFreeLayout(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	witnessDir := filepath.Join(townRoot, rigName, "witness")
	if err := os.MkdirAll(witnessDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Settings and hooks are owned by their dedicated doctor checks. Their
	// absence must not make the structural witness check demand a repo clone.
	check := NewWitnessExistsCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot, RigName: rigName})
	if result.Status != StatusOK {
		t.Fatalf("clone-free witness should be healthy, got %s: %s (%v)",
			result.Status, result.Message, result.Details)
	}
}

func TestWitnessExistsCheck_AcceptsLegacyRigClone(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	legacyGitDir := filepath.Join(townRoot, rigName, "witness", "rig", ".git")
	if err := os.MkdirAll(legacyGitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	check := NewWitnessExistsCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot, RigName: rigName})
	if result.Status != StatusOK {
		t.Fatalf("legacy witness/rig clone should remain supported, got %s: %s (%v)",
			result.Status, result.Message, result.Details)
	}
}

func TestWitnessExistsCheck_RejectsMisplacedRootGitWorktree(t *testing.T) {
	for _, markerKind := range []string{"directory", "file"} {
		t.Run(markerKind, func(t *testing.T) {
			townRoot := t.TempDir()
			rigName := "testrig"
			witnessDir := filepath.Join(townRoot, rigName, "witness")
			rootGit := filepath.Join(witnessDir, ".git")
			if markerKind == "directory" {
				if err := os.MkdirAll(rootGit, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(
					filepath.Join(rootGit, "HEAD"),
					[]byte("ref: refs/heads/main\n"),
					0o644,
				); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(witnessDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(rootGit, []byte("gitdir: /product/worktree\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			check := NewWitnessExistsCheck()
			result := check.Run(&CheckContext{TownRoot: townRoot, RigName: rigName})
			if result.Status == StatusOK {
				t.Fatal("misplaced witness/.git product worktree passed canonical layout check")
			}
			if check.CanFix() {
				t.Fatal("misplaced witness/.git was advertised as automatically fixable")
			}
			details := strings.Join(result.Details, "\n")
			if !strings.Contains(details, "witness/.git") ||
				!strings.Contains(strings.ToLower(details), "clone-free") {
				t.Fatalf("misplaced root git was not clearly classified: %#v", result)
			}
		})
	}
}

func TestWitnessExistsCheck_FixCreatesCanonicalLayoutOnly(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	ctx := &CheckContext{TownRoot: townRoot, RigName: rigName}
	check := NewWitnessExistsCheck()

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("missing witness should warn, got %s", result.Status)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("clone-free witness fix failed: %v", err)
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("fixed canonical witness should pass, got %s: %v", result.Status, result.Details)
	}
	if _, err := os.Stat(filepath.Join(townRoot, rigName, "witness", "rig")); !os.IsNotExist(err) {
		t.Fatalf("fix should not synthesize legacy witness/rig clone, stat err=%v", err)
	}
	for _, detail := range result.Details {
		if strings.Contains(detail, "settings") || strings.Contains(detail, "hook") {
			t.Fatalf("structural check leaked settings/hooks concern: %q", detail)
		}
	}
}

func TestWitnessExistsCheck_InvalidFileIsNotFixable(t *testing.T) {
	townRoot := t.TempDir()
	rigName := "testrig"
	witnessPath := filepath.Join(townRoot, rigName, "witness")
	if err := os.MkdirAll(filepath.Dir(witnessPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(witnessPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	check := NewWitnessExistsCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot, RigName: rigName})
	if result.Status == StatusOK {
		t.Fatalf("invalid witness file passed: %#v", result)
	}
	if check.CanFix() {
		t.Fatal("invalid witness file falsely reported auto-fixable")
	}
	if strings.Contains(result.FixHint, "doctor --fix") {
		t.Fatalf("invalid witness file advertised unsafe automatic fix: %#v", result)
	}
}
