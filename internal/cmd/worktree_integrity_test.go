package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	worktreeintegrity "github.com/jonbaldie/gastown/internal/worktree"
)

func TestEnsureRoleWorktreeIntegrityRequiresPolecatMetadata(t *testing.T) {
	townRoot := t.TempDir()
	cwd := filepath.Join(townRoot, "gastown", "polecats", "deathclaw", "gastown")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}

	err := ensureRoleWorktreeIntegrity(cwd, townRoot, RolePolecat)
	if !errors.Is(err, worktreeintegrity.ErrIntegrityViolation) {
		t.Fatalf("ensureRoleWorktreeIntegrity() error = %v, want ErrIntegrityViolation", err)
	}
	if !strings.Contains(err.Error(), "gt doctor --fix") {
		t.Fatalf("ensureRoleWorktreeIntegrity() error = %v, want remediation", err)
	}
}

func TestEnsureRoleWorktreeIntegrityAllowsNeutralDirectoryWithoutMetadata(t *testing.T) {
	townRoot := t.TempDir()

	if err := ensureRoleWorktreeIntegrity(townRoot, townRoot, RoleUnknown); err != nil {
		t.Fatalf("ensureRoleWorktreeIntegrity() error = %v, want nil", err)
	}
}

func TestEnsureRoleWorktreeIntegrityRejectsMalformedOptionalMetadata(t *testing.T) {
	townRoot := t.TempDir()
	cwd := filepath.Join(townRoot, "scratch")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".git"), []byte("corrupted\n"), 0644); err != nil {
		t.Fatal(err)
	}

	err := ensureRoleWorktreeIntegrity(cwd, townRoot, RoleUnknown)
	if !errors.Is(err, worktreeintegrity.ErrIntegrityViolation) {
		t.Fatalf("ensureRoleWorktreeIntegrity() error = %v, want ErrIntegrityViolation", err)
	}
}

func TestRunMoleculeStatusExplicitTargetValidatesCallerWorktree(t *testing.T) {
	t.Setenv(EnvGTRole, "")
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(townRoot, "gastown", "polecats", "deathclaw")
	if err := os.MkdirAll(cwd, 0755); err != nil {
		t.Fatal(err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWD)
	})

	err = runMoleculeStatus(nil, []string{"gastown/polecats/toast"})
	if !errors.Is(err, worktreeintegrity.ErrIntegrityViolation) {
		t.Fatalf("runMoleculeStatus() error = %v, want ErrIntegrityViolation", err)
	}
}
