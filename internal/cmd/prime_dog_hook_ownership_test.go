package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
)

// TestFindAgentWorkOnceDogIgnoresLegacyHook covers the other GH-4516 startup
// outcome: a dog agent bead points at older active work while no source bead
// matches current dog state. That stale pointer is metadata, not authority.
func TestGetAgentIdentityDog(t *testing.T) {
	got := getAgentIdentity(RoleContext{Role: RoleDog, Polecat: "alpha"})
	if got != "deacon/dogs/alpha" {
		t.Fatalf("getAgentIdentity(dog alpha) = %q, want deacon/dogs/alpha", got)
	}
}

func TestFindAgentWorkOnceDogIgnoresLegacyHook(t *testing.T) {
	townRoot := t.TempDir()
	dogDir := filepath.Join(townRoot, "deacon", "dogs", "alpha")
	for _, dir := range []string{dogDir, filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), &config.RigsConfig{
		Version: 1,
		Rigs:    map[string]config.RigEntry{},
	}); err != nil {
		t.Fatalf("save rigs config: %v", err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	bdScript := `#!/bin/sh
for arg in "$@"; do
  case "$arg" in
    hq-dog-alpha)
      echo '[{"id":"hq-dog-alpha","title":"Dog alpha","status":"open","hook_bead":"gt-old"}]'
      exit 0
      ;;
    gt-old)
      echo '[{"id":"gt-old","title":"Older dog work","status":"hooked","assignee":"deacon/dogs/alpha"}]'
      exit 0
      ;;
    list) echo '[]'; exit 0 ;;
  esac
done
echo '[]'
`
	writeBDStub(t, binDir, bdScript, "@echo off\necho []\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := findAgentWorkOnce(RoleContext{
		Role:     RoleDog,
		Polecat:  "alpha",
		TownRoot: townRoot,
		WorkDir:  dogDir,
	}, "deacon/dogs/alpha")
	if err != nil {
		t.Fatalf("findAgentWorkOnce: %v", err)
	}
	if got != nil {
		t.Fatalf("findAgentWorkOnce returned stale legacy hook %s; want no authoritative dog work", got.ID)
	}
}
