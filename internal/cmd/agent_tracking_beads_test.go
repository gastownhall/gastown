package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAgentTrackingBeadsDirPrefersCwdRigRedirectOverBeadsDir(t *testing.T) {
	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	t.Setenv("BEADS_DIR", townBeads)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	gotWorkDir, err := findCwdBeadsWorkDir()
	if err != nil {
		t.Fatalf("findCwdBeadsWorkDir() error = %v", err)
	}
	if gotWorkDir != rigWorkDir {
		t.Fatalf("findCwdBeadsWorkDir() = %q, want %q", gotWorkDir, rigWorkDir)
	}

	gotBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		t.Fatalf("resolveAgentTrackingBeadsDir() error = %v", err)
	}
	if gotBeadsDir != rigBeads {
		t.Fatalf("resolveAgentTrackingBeadsDir() = %q, want %q", gotBeadsDir, rigBeads)
	}

	gotLocalWorkDir, err := findLocalBeadsDir()
	if err != nil {
		t.Fatalf("findLocalBeadsDir() error = %v", err)
	}
	if gotLocalWorkDir != townRoot {
		t.Fatalf("findLocalBeadsDir() = %q, want env parent %q", gotLocalWorkDir, townRoot)
	}
}
