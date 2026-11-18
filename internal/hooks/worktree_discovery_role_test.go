package hooks

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverWorktreesForRole_CloneFreeWitnessDoesNotRecurseCustomerRepos(t *testing.T) {
	witnessDir := filepath.Join(t.TempDir(), "witness")
	customerRepo := filepath.Join(witnessDir, "customer-checkout")
	if err := os.MkdirAll(filepath.Join(customerRepo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(witnessDir, "mail"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverWorktreesForRole(witnessDir, "witness")
	if len(got) != 1 || got[0] != witnessDir {
		t.Fatalf("clone-free witness hook target = %v, want only %q", got, witnessDir)
	}
}

func TestDiscoverWorktreesForRole_LegacyWitnessTargetsRigCloneOnly(t *testing.T) {
	witnessDir := filepath.Join(t.TempDir(), "witness")
	legacyDir := filepath.Join(witnessDir, "rig")
	if err := os.MkdirAll(filepath.Join(legacyDir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(witnessDir, "customer-checkout", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverWorktreesForRole(witnessDir, "witness")
	if len(got) != 1 || got[0] != legacyDir {
		t.Fatalf("legacy witness hook target = %v, want only %q", got, legacyDir)
	}
}

func TestDiscoverWorktreesForRole_PolecatKeepsNestedWorktreeDiscovery(t *testing.T) {
	polecatsDir := t.TempDir()
	worktree := filepath.Join(polecatsDir, "fury", "gastown")
	if err := os.MkdirAll(filepath.Join(worktree, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}

	got := DiscoverWorktreesForRole(polecatsDir, "polecat")
	if len(got) != 1 || got[0] != worktree {
		t.Fatalf("polecat hook target = %v, want %q", got, worktree)
	}
}
