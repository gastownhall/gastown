package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRoleWorktreeIntegrity_CloneFreeWitnessSkipsGitRequirement(t *testing.T) {
	townRoot := t.TempDir()
	witnessDir := filepath.Join(townRoot, "testrig", "witness")
	if err := os.MkdirAll(witnessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Clone-free Witness priming must not inherit or validate unrelated git
	// metadata from an ancestor/customer workspace.
	if err := os.WriteFile(filepath.Join(townRoot, ".git"), []byte("not worktree metadata\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureRoleWorktreeIntegrity(witnessDir, townRoot, RoleWitness); err != nil {
		t.Fatalf("canonical clone-free witness prime should not require .git: %v", err)
	}
}

func TestEnsureRoleWorktreeIntegrity_WorktreeRolesRemainStrict(t *testing.T) {
	townRoot := t.TempDir()
	for _, role := range []Role{RolePolecat, RoleCrew} {
		workDir := filepath.Join(townRoot, string(role))
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := ensureRoleWorktreeIntegrity(workDir, townRoot, role); err == nil {
			t.Fatalf("%s prime accepted a missing .git marker", role)
		}
	}
}

func TestEnsureRoleWorktreeIntegrity_ArbitraryWitnessDirectoryRemainsStrict(t *testing.T) {
	townRoot := t.TempDir()
	unmanagedWitness := filepath.Join(t.TempDir(), "customer", "witness")
	if err := os.MkdirAll(unmanagedWitness, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := ensureRoleWorktreeIntegrity(unmanagedWitness, townRoot, RoleWitness); err == nil {
		t.Fatal("unmanaged directory named witness bypassed worktree integrity")
	}
}

func TestEnsureRoleWorktreeIntegrity_LegacyWitnessCloneStillValidated(t *testing.T) {
	townRoot := t.TempDir()
	legacyDir := filepath.Join(townRoot, "testrig", "witness", "rig")
	gitDir := filepath.Join(legacyDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ensureRoleWorktreeIntegrity(legacyDir, townRoot, RoleWitness); err != nil {
		t.Fatalf("legacy witness clone should remain valid: %v", err)
	}
}

func TestEnsureRoleWorktreeIntegrity_MisplacedRootWitnessGitIsRejected(t *testing.T) {
	for _, markerKind := range []string{"directory", "file"} {
		t.Run(markerKind, func(t *testing.T) {
			townRoot := t.TempDir()
			witnessDir := filepath.Join(townRoot, "testrig", "witness")
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

			err := ensureRoleWorktreeIntegrity(witnessDir, townRoot, RoleWitness)
			if err == nil {
				t.Fatal("misplaced witness/.git product worktree bypassed canonical clone-free integrity")
			}
			if !strings.Contains(err.Error(), "witness/.git") ||
				!strings.Contains(strings.ToLower(err.Error()), "clone-free") {
				t.Fatalf("misplaced root git error was not actionable: %v", err)
			}
		})
	}
}
