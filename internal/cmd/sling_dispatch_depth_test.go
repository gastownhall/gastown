package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestExecuteSling_FlagLikeTitle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBDStub(t, binDir, `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"--force","status":"open","assignee":"","description":""}]'
    ;;
esac
exit 0
`, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	spawned := false
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = prevSpawn })
	spawnPolecatForSling = func(string, SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawned = true
		return nil, nil
	}

	_, err := executeSling(SlingParams{
		BeadID:   "gt-flaglike",
		RigName:  "gastown",
		TownRoot: townRoot,
	})
	if err == nil {
		t.Fatal("expected flag-like title to be refused")
	}
	if !strings.Contains(err.Error(), "CLI flag") {
		t.Fatalf("error = %v, want CLI flag refusal", err)
	}
	if spawned {
		t.Fatal("flag-like title must fail before polecat spawn")
	}
}

func TestExecuteSling_NoOpAlreadyHookedInRig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(filepath.Join(binDir), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBDStub(t, binDir, `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Implement feature","status":"hooked","assignee":"gastown/polecats/toast","description":""}]'
    ;;
esac
exit 0
`, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prevDead := isHookedAgentDeadFn
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() {
		isHookedAgentDeadFn = prevDead
		spawnPolecatForSling = prevSpawn
	})
	isHookedAgentDeadFn = func(string) bool { return false }
	spawnPolecatForSling = func(string, SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		t.Fatal("no-op must not spawn a polecat")
		return nil, nil
	}

	result, err := executeSling(SlingParams{
		BeadID:      "gt-abc",
		RigName:     "gastown",
		FormulaName: "mol-polecat-work",
		TownRoot:    townRoot,
		NoBoot:      true,
	})
	if err != nil {
		t.Fatalf("expected no-op success, got %v", err)
	}
	if result == nil || !result.Success || !result.NoOp {
		t.Fatalf("result = %+v, want Success+NoOp", result)
	}
	if result.PolecatName != "toast" {
		t.Fatalf("PolecatName = %q, want toast", result.PolecatName)
	}
}

func TestExecuteSling_NewFormulaIsNotNoOp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows")
	}

	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeBDStub(t, binDir, `#!/bin/sh
case "$1" in
  show)
    echo '[{"title":"Implement feature","status":"hooked","assignee":"gastown/polecats/toast","description":""}]'
    ;;
esac
exit 0
`, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	prevDead := isHookedAgentDeadFn
	t.Cleanup(func() { isHookedAgentDeadFn = prevDead })
	isHookedAgentDeadFn = func(string) bool { return false }

	_, err := executeSling(SlingParams{
		BeadID:      "gt-abc",
		RigName:     "gastown",
		FormulaName: "mol-review",
		TownRoot:    townRoot,
		NoBoot:      true,
	})
	if err == nil {
		t.Fatal("expected already-hooked error when applying a new formula without --force")
	}
	if !strings.Contains(err.Error(), "already hooked") {
		t.Fatalf("error = %v, want already hooked", err)
	}
}

func TestIsDefaultRigSlingNoop(t *testing.T) {
	info := &beadInfo{Assignee: "gastown/polecats/toast"}
	if !isDefaultRigSlingNoop(SlingParams{RigName: "gastown", FormulaName: "mol-polecat-work"}, info, "") {
		t.Fatal("default formula on matching rig should no-op")
	}
	if isDefaultRigSlingNoop(SlingParams{RigName: "gastown", FormulaName: "mol-review"}, info, "") {
		t.Fatal("explicit other formula should not no-op")
	}
	if isDefaultRigSlingNoop(SlingParams{RigName: "other", FormulaName: ""}, info, "") {
		t.Fatal("different rig should not no-op")
	}
}
