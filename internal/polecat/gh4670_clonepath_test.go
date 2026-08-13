package polecat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/rig"
)

// GH#4670 old-vs-fresh sandbox discriminator: clonePath prefers
// polecats/<name>/<rigname>/ whenever that directory exists, even if the
// actual git worktree is the older polecats/<name>/ layout. Codex then
// starts outside a git repo and exits.

func TestGH4670_ClonePath_OldWorktreeShadowedByNestedDir(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rigName := "gastown"
	name := "Toast"

	oldWorktree := filepath.Join(root, "polecats", name)
	if err := os.MkdirAll(oldWorktree, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldWorktree, ".git"), []byte("gitdir: /does/not/exist\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Nested dir named after the rig — either a repo subdirectory or a
	// half-migrated new-layout folder. Not a git worktree.
	nested := filepath.Join(oldWorktree, rigName)
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatal(err)
	}

	m := NewSessionManager(nil, &rig.Rig{Name: rigName, Path: root})
	got := m.clonePath(name)
	if got != oldWorktree {
		t.Fatalf("clonePath = %q, want old git worktree %q (nested non-git dir %q shadowed it)", got, oldWorktree, nested)
	}
}

func TestGH4670_ClonePath_FreshLayout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	rigName := "gastown"
	name := "Toast"
	fresh := filepath.Join(root, "polecats", name, rigName)
	if err := os.MkdirAll(fresh, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh, ".git"), []byte("gitdir: /fresh/worktree\n"), 0644); err != nil {
		t.Fatal(err)
	}

	m := NewSessionManager(nil, &rig.Rig{Name: rigName, Path: root})
	got := m.clonePath(name)
	if got != fresh {
		t.Fatalf("clonePath = %q, want fresh layout %q", got, fresh)
	}
}
