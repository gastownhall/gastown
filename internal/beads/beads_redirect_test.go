package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSetupRedirectStripsWorktreeMetadata verifies that SetupRedirect makes a
// worktree .beads strictly redirect-only: any pre-existing metadata.json or
// config.yaml (which would carry a stale/wrong dolt_database and break bd when
// issue_prefix != db name) is removed, leaving only the redirect file.
//
// Regression test for #2682 / #4033: a git-tracked worktree
// refinery/rig/.beads/metadata.json with dolt_database=<prefix> broke bd from
// the worktree because bd read the local metadata instead of following the
// redirect. Per docs/design/architecture.md:193, a worktree .beads is
// redirect-only.
func TestSetupRedirectStripsWorktreeMetadata(t *testing.T) {
	t.Run("bogus worktree metadata.json and config.yaml are removed", func(t *testing.T) {
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		rigBeads := filepath.Join(rigRoot, ".beads")
		// refinery/rig is the worktree whose .beads must become redirect-only.
		worktreePath := filepath.Join(rigRoot, "refinery", "rig")
		worktreeBeads := filepath.Join(worktreePath, ".beads")

		// Rig has its own database with prefix "lc" (dolt_database "laneassist").
		if err := os.MkdirAll(rigBeads, 0755); err != nil {
			t.Fatalf("mkdir rig beads: %v", err)
		}
		rigMeta := []byte(`{"dolt_database":"laneassist","issue_prefix":"lc","backend":"dolt"}`)
		if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), rigMeta, 0644); err != nil {
			t.Fatalf("write rig metadata: %v", err)
		}

		// The worktree .beads carries a STALE/WRONG metadata.json (dolt_database
		// does not match the rig prefix) plus a config.yaml — exactly the state
		// that breaks bd from the worktree.
		if err := os.MkdirAll(worktreeBeads, 0755); err != nil {
			t.Fatalf("mkdir worktree beads: %v", err)
		}
		bogusMeta := []byte(`{"dolt_database":"lc","issue_prefix":"lc","backend":"dolt"}`)
		if err := os.WriteFile(filepath.Join(worktreeBeads, "metadata.json"), bogusMeta, 0644); err != nil {
			t.Fatalf("write worktree metadata: %v", err)
		}
		if err := os.WriteFile(filepath.Join(worktreeBeads, "config.yaml"), []byte("backend: dolt\n"), 0644); err != nil {
			t.Fatalf("write worktree config: %v", err)
		}

		if err := SetupRedirect(townRoot, worktreePath); err != nil {
			t.Fatalf("SetupRedirect failed: %v", err)
		}

		// metadata.json and config.yaml must be gone from the worktree .beads.
		if _, err := os.Stat(filepath.Join(worktreeBeads, "metadata.json")); !os.IsNotExist(err) {
			t.Errorf("worktree metadata.json should be removed, but stat err = %v", err)
		}
		if _, err := os.Stat(filepath.Join(worktreeBeads, "config.yaml")); !os.IsNotExist(err) {
			t.Errorf("worktree config.yaml should be removed, but stat err = %v", err)
		}

		// A redirect file must exist and be the only thing pointing the worktree
		// at the rig-level beads.
		redirectPath := filepath.Join(worktreeBeads, "redirect")
		content, err := os.ReadFile(redirectPath)
		if err != nil {
			t.Fatalf("read redirect: %v", err)
		}
		// refinery/rig -> ../../.beads (rig-level), since rig has its own DB.
		if want := "../../.beads\n"; string(content) != want {
			t.Errorf("redirect content = %q, want %q", string(content), want)
		}

		// The worktree .beads should now contain only the redirect file.
		entries, err := os.ReadDir(worktreeBeads)
		if err != nil {
			t.Fatalf("read worktree beads dir: %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "redirect" {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("worktree .beads should contain only 'redirect', got %v", names)
		}

		// And the worktree should resolve to the rig-level (correct prefix)
		// database, not its own stale local one.
		if resolved := ResolveBeadsDir(worktreePath); resolved != rigBeads {
			t.Errorf("resolved = %q, want %q (rig-level)", resolved, rigBeads)
		}
	})

	t.Run("canonical mayor/rig/.beads is NOT stripped", func(t *testing.T) {
		// SetupRedirect must refuse the canonical beads location so its
		// source-of-truth metadata.json/config.yaml is never removed.
		townRoot := t.TempDir()
		rigRoot := filepath.Join(townRoot, "testrig")
		mayorRigBeads := filepath.Join(rigRoot, "mayor", "rig", ".beads")
		mayorRigPath := filepath.Join(rigRoot, "mayor", "rig")

		if err := os.MkdirAll(mayorRigBeads, 0755); err != nil {
			t.Fatalf("mkdir mayor/rig beads: %v", err)
		}
		canonicalMeta := []byte(`{"dolt_database":"laneassist","issue_prefix":"lc","backend":"dolt"}`)
		metaPath := filepath.Join(mayorRigBeads, "metadata.json")
		if err := os.WriteFile(metaPath, canonicalMeta, 0644); err != nil {
			t.Fatalf("write canonical metadata: %v", err)
		}
		configPath := filepath.Join(mayorRigBeads, "config.yaml")
		if err := os.WriteFile(configPath, []byte("backend: dolt\n"), 0644); err != nil {
			t.Fatalf("write canonical config: %v", err)
		}

		// SetupRedirect must refuse — the canonical guard lives in
		// ComputeRedirectTarget (parts[1] == "mayor").
		err := SetupRedirect(townRoot, mayorRigPath)
		if err == nil {
			t.Fatalf("SetupRedirect should refuse the canonical mayor/rig/.beads")
		}

		// The canonical metadata.json and config.yaml must be untouched.
		if _, statErr := os.Stat(metaPath); statErr != nil {
			t.Errorf("canonical metadata.json must NOT be stripped, stat err = %v", statErr)
		}
		if _, statErr := os.Stat(configPath); statErr != nil {
			t.Errorf("canonical config.yaml must NOT be stripped, stat err = %v", statErr)
		}
	})
}
