package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// writeBeadsDir creates dir/.beads and optionally a redirect and metadata.json.
func writeBeadsDir(t *testing.T, dir, redirect, doltDatabase string) {
	t.Helper()
	beadsDir := filepath.Join(dir, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", beadsDir, err)
	}
	if redirect != "" {
		if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte(redirect+"\n"), 0644); err != nil {
			t.Fatalf("write redirect: %v", err)
		}
	}
	if doltDatabase != "" {
		body := `{"database":"beads.db","backend":"dolt","dolt_database":"` + doltDatabase + `"}`
		if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(body), 0644); err != nil {
			t.Fatalf("write metadata.json: %v", err)
		}
	}
}

// A rig with its own Dolt database whose .beads is ITSELF a redirect to
// mayor/rig/.beads — the layout of fastlio2_ros2 and navigation_server. bd does
// not follow redirect chains, so the worktree redirect must name the final
// destination, not the intermediate rig hop (hq-szhze).
func TestComputeRedirectTargetCollapsesChainWhenRigHasOwnDB(t *testing.T) {
	townRoot := t.TempDir()
	rigRoot := filepath.Join(townRoot, "fastlio2_ros2")

	writeBeadsDir(t, rigRoot, "mayor/rig/.beads", "fastlio2_ros2")
	writeBeadsDir(t, filepath.Join(rigRoot, "mayor", "rig"), "", "fastlio2_ros2")

	worktree := filepath.Join(rigRoot, "polecats", "obsidian", "fastlio2_ros2")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	got, err := ComputeRedirectTarget(townRoot, worktree)
	if err != nil {
		t.Fatalf("ComputeRedirectTarget() error = %v", err)
	}

	const want = "../../../mayor/rig/.beads"
	if got != want {
		t.Errorf("ComputeRedirectTarget() = %q, want %q", got, want)
	}

	// The target must resolve in one hop to a directory with no further redirect.
	resolved := filepath.Clean(filepath.Join(worktree, got))
	if _, err := os.Stat(filepath.Join(resolved, "redirect")); err == nil {
		t.Errorf("resolved target %s still contains a redirect — chain not collapsed", resolved)
	}
	if wantResolved := filepath.Join(rigRoot, "mayor", "rig", ".beads"); resolved != wantResolved {
		t.Errorf("target resolves to %s, want %s", resolved, wantResolved)
	}
}

// A rig with its own database and NO redirect of its own: the worktree should
// point at the rig's .beads directly.
func TestComputeRedirectTargetRigOwnDBNoChain(t *testing.T) {
	townRoot := t.TempDir()
	rigRoot := filepath.Join(townRoot, "dashboard")

	writeBeadsDir(t, rigRoot, "", "dashboard")

	worktree := filepath.Join(rigRoot, "polecats", "jasper", "dashboard")
	if err := os.MkdirAll(worktree, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}

	got, err := ComputeRedirectTarget(townRoot, worktree)
	if err != nil {
		t.Fatalf("ComputeRedirectTarget() error = %v", err)
	}
	const want = "../../../.beads"
	if got != want {
		t.Errorf("ComputeRedirectTarget() = %q, want %q", got, want)
	}
}

func TestCollapseRigRedirect(t *testing.T) {
	tests := []struct {
		name     string
		redirect string // "" means no redirect file
		want     string
	}{
		{name: "no redirect points at rig beads", redirect: "", want: "../../.beads"},
		{name: "relative redirect is re-anchored", redirect: "mayor/rig/.beads", want: "../../mayor/rig/.beads"},
		{name: "absolute redirect passes through", redirect: "/srv/beads", want: "/srv/beads"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rigRoot := t.TempDir()
			writeBeadsDir(t, rigRoot, tt.redirect, "somerig")
			got := collapseRigRedirect(filepath.Join(rigRoot, ".beads"), "../../")
			if got != tt.want {
				t.Errorf("collapseRigRedirect() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Blank/whitespace-only redirect files must not produce a target ending in the
// bare upPath (which would point at the rig root, not a .beads directory).
func TestCollapseRigRedirectIgnoresBlankRedirect(t *testing.T) {
	rigRoot := t.TempDir()
	writeBeadsDir(t, rigRoot, "   ", "somerig")
	got := collapseRigRedirect(filepath.Join(rigRoot, ".beads"), "../../")
	if got != "../../.beads" {
		t.Errorf("collapseRigRedirect() = %q, want %q", got, "../../.beads")
	}
}
