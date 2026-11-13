package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/constants"
)

// writeRigRoute wires a bead prefix to a rig directory and writes that rig's
// config.json, so beads.GetRigPathForPrefix + rig.LoadRigConfig can resolve the
// rig's default branch from a bead ID during the test.
func writeRigRoute(t *testing.T, townRoot, prefix, rigDir, configJSON string) {
	t.Helper()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0700); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	route := `{"prefix":"` + prefix + `","path":"` + rigDir + `"}` + "\n"
	if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(route), 0600); err != nil {
		t.Fatalf("write routes.jsonl: %v", err)
	}
	rigPath := filepath.Join(townRoot, rigDir)
	if err := os.MkdirAll(rigPath, 0700); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}
	if configJSON != "" {
		if err := os.WriteFile(filepath.Join(rigPath, "config.json"), []byte(configJSON), 0600); err != nil {
			t.Fatalf("write config.json: %v", err)
		}
	}
}

func TestResolveFormulaBaseBranch(t *testing.T) {
	t.Run("uses rig config default_branch when it is not main", func(t *testing.T) {
		townRoot := t.TempDir()
		writeRigRoute(t, townRoot, "feat-", "featrig", `{"default_branch":"develop"}`)

		if got := resolveFormulaBaseBranch(townRoot, "feat-123"); got != "develop" {
			t.Fatalf("resolveFormulaBaseBranch = %q, want %q", got, "develop")
		}
	})

	t.Run("falls back to main when the bead has no rig route", func(t *testing.T) {
		townRoot := t.TempDir()
		if got := resolveFormulaBaseBranch(townRoot, "unknown-1"); got != constants.BranchMain {
			t.Fatalf("resolveFormulaBaseBranch = %q, want %q", got, constants.BranchMain)
		}
	})

	t.Run("falls back to main when rig config omits default_branch", func(t *testing.T) {
		townRoot := t.TempDir()
		writeRigRoute(t, townRoot, "bare-", "barerig", `{}`)
		if got := resolveFormulaBaseBranch(townRoot, "bare-9"); got != constants.BranchMain {
			t.Fatalf("resolveFormulaBaseBranch = %q, want %q", got, constants.BranchMain)
		}
	})
}

func TestEnsureFormulaRequiredVars_UsesResolvedDefaultBranch(t *testing.T) {
	t.Run("applies the resolved default branch when base_branch is absent", func(t *testing.T) {
		vars := ensureFormulaRequiredVars("mol-polecat-work", nil, "develop")
		if !containsVar(vars, "base_branch=develop") {
			t.Fatalf("vars = %v, want base_branch=develop", vars)
		}
		if containsVar(vars, "base_branch=main") {
			t.Fatalf("vars = %v, must not default base_branch to main on a non-main rig", vars)
		}
	})

	t.Run("does not override an explicit base_branch", func(t *testing.T) {
		vars := ensureFormulaRequiredVars("mol-polecat-work", []string{"base_branch=release-2"}, "develop")
		if !containsVar(vars, "base_branch=release-2") {
			t.Fatalf("vars = %v, want explicit base_branch=release-2 preserved", vars)
		}
		if containsVar(vars, "base_branch=develop") {
			t.Fatalf("vars = %v, resolved default must not override an explicit base_branch", vars)
		}
	})

	t.Run("falls back to main when no default branch is resolved", func(t *testing.T) {
		vars := ensureFormulaRequiredVars("mol-polecat-work", nil, "")
		if !containsVar(vars, "base_branch="+constants.BranchMain) {
			t.Fatalf("vars = %v, want base_branch=%s", vars, constants.BranchMain)
		}
	})
}

func TestFormulaVarsForBead_ResolvesBaseBranchFromRig(t *testing.T) {
	townRoot := t.TempDir()
	writeRigRoute(t, townRoot, "feat-", "featrig", `{"default_branch":"develop"}`)

	vars := formulaVarsForBead("mol-polecat-work", "feat-1", "Title", nil, townRoot)

	if !containsVar(vars, "base_branch=develop") {
		t.Fatalf("vars = %v, want base_branch resolved from rig config (develop), not the literal main", vars)
	}
	if !containsVar(vars, "feature=Title") || !containsVar(vars, "issue=feat-1") {
		t.Fatalf("vars = %v, missing feature/issue vars", vars)
	}
}

func containsVar(vars []string, want string) bool {
	for _, v := range vars {
		if v == want {
			return true
		}
	}
	return false
}
