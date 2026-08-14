package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/dog"
)

// TestFindAgentWorkOnceDogPrefersAssignedRigBead reproduces GH-4516: a dog
// can retain an unrelated legacy hook while a newly slung rig bead is hooked
// and assigned to it. The source bead is authoritative and must win even
// though dogs are town-level agents and the source lives in a rig database.
func TestFindAgentWorkOnceDogPrefersAssignedRigBead(t *testing.T) {
	townRoot := t.TempDir()
	dogDir := filepath.Join(townRoot, "deacon", "dogs", "alpha")
	rigDir := filepath.Join(townRoot, "gastown", "mayor", "rig")
	for _, dir := range []string{dogDir, rigDir, filepath.Join(townRoot, "mayor"), filepath.Join(townRoot, ".beads")} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	rigs := &config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{
		"gastown": {
			GitURL:  "file:///tmp/gastown.git",
			AddedAt: time.Unix(1, 0).UTC(),
			BeadsConfig: &config.BeadsConfig{
				Repo:   "local",
				Prefix: "gt-",
			},
		},
	}}
	if err := config.SaveRigsConfig(filepath.Join(townRoot, "mayor", "rigs.json"), rigs); err != nil {
		t.Fatalf("save rigs config: %v", err)
	}
	startedAt := time.Date(2026, 7, 17, 5, 0, 0, 0, time.UTC)
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "gt-new",
		WorkKind:      dog.WorkKindBead,
		WorkStartedAt: startedAt,
		LastActive:    startedAt,
		CreatedAt:     startedAt.Add(-time.Hour),
		UpdatedAt:     startedAt,
	})

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
      echo '[{"id":"gt-old","title":"Unrelated old work","status":"hooked","assignee":"deacon/dogs/alpha","updated_at":"2026-07-17T04:00:00Z"}]'
      exit 0
      ;;
    gt-new)
      case "$PWD" in
        */gastown/mayor/rig) ;;
        *) [ "$DOG_ALLOW_SOURCE_SHOW" = "1" ] || { echo '[]'; exit 0; } ;;
      esac
      printf '[{"id":"gt-new","title":"Requested dog work","description":"attached_formula: code-review\\nattached_at: 2026-07-17T05:00:00Z","status":"hooked","assignee":"%s","updated_at":"2026-07-17T05:00:00Z"}]\n' "${DOG_SOURCE_ASSIGNEE:-deacon/dogs/alpha}"
      exit 0
      ;;
  esac
  if [ "$arg" = "list" ]; then
    case "$PWD" in
      */gastown/mayor/rig)
        printf '%s\n' '[{"id":"gt-new","title":"Requested dog work","description":"attached_formula: code-review\nattached_at: 2026-07-17T05:00:00Z","status":"hooked","assignee":"deacon/dogs/alpha","updated_at":"2026-07-17T05:00:00Z"}]'
        ;;
      */.beads)
        if [ "$DOG_HIDE_OLD" = "1" ]; then
          echo '[]'
        else
          echo '[{"id":"gt-old","title":"Unrelated old work","status":"hooked","assignee":"deacon/dogs/alpha","updated_at":"2026-07-17T04:00:00Z"}]'
        fi
        ;;
      *) echo '[]' ;;
    esac
    exit 0
  fi
done
echo '[]'
`
	bdScriptWindows := `@echo off
echo []
`
	writeBDStub(t, binDir, bdScript, bdScriptWindows)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	direct, err := listAssignedActiveWork(beads.New(rigDir), "deacon/dogs/alpha")
	if err != nil {
		t.Fatalf("direct rig query: %v", err)
	}
	if len(direct) != 1 || direct[0].ID != "gt-new" {
		t.Fatalf("fixture rig query = %+v; want gt-new", direct)
	}
	t.Log("direct rig query resolves authoritative assignment gt-new")

	ctx := RoleContext{
		Role:     RoleDog,
		Polecat:  "alpha",
		TownRoot: townRoot,
		WorkDir:  dogDir,
	}
	got, err := findAgentWorkOnce(ctx, "deacon/dogs/alpha")
	if err != nil {
		t.Fatalf("findAgentWorkOnce: %v", err)
	}
	if got == nil {
		t.Fatal("findAgentWorkOnce returned no work; want newly assigned rig bead gt-new")
	}
	if got.ID != "gt-new" {
		t.Fatalf("findAgentWorkOnce returned %s (%q); want authoritative assigned bead gt-new", got.ID, got.Title)
	}

	t.Setenv("DOG_ALLOW_SOURCE_SHOW", "1")
	dispatch := &DogDispatchInfo{
		DogName:       "alpha",
		AgentID:       "deacon/dogs/alpha",
		townRoot:      townRoot,
		workDesc:      "gt-new",
		workStartedAt: startedAt,
		ownsWork:      true,
		rigsConfig:    rigs,
	}
	if err := dispatch.verifyBareBeadAssignment("gt-new"); err != nil {
		t.Fatalf("verifyBareBeadAssignment: %v", err)
	}

	t.Setenv("DOG_SOURCE_ASSIGNEE", "gastown/crew/other")
	if err := dispatch.verifyBareBeadAssignment("gt-new"); err == nil {
		t.Fatal("verifyBareBeadAssignment accepted a source bead assigned to another agent")
	}

	// Formula dispatch uses exact formula and assignment-time metadata rather
	// than inferring work from an unrelated sole hook.
	t.Setenv("DOG_SOURCE_ASSIGNEE", "deacon/dogs/alpha")
	t.Setenv("DOG_HIDE_OLD", "1")
	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "code-review",
		WorkKind:      dog.WorkKindFormula,
		WorkStartedAt: startedAt,
		LastActive:    startedAt,
		CreatedAt:     startedAt.Add(-time.Hour),
		UpdatedAt:     startedAt,
	})
	formulaWork, err := findAssignedDogWork(ctx, "deacon/dogs/alpha")
	if err != nil {
		t.Fatalf("findAssignedDogWork formula: %v", err)
	}
	if formulaWork == nil || formulaWork.ID != "gt-new" {
		t.Fatalf("formula lookup = %+v, want exact attached formula gt-new", formulaWork)
	}
	dispatch.workDesc = "code-review"
	if err := dispatch.verifyFormulaAssignment("gt-new"); err != nil {
		t.Fatalf("verifyFormulaAssignment: %v", err)
	}

	writeDogStateForDispatchTest(t, townRoot, "alpha", &dog.DogState{
		Name:          "alpha",
		State:         dog.StateWorking,
		Work:          "plugin:rebuild-gt",
		WorkKind:      dog.WorkKindPlugin,
		WorkStartedAt: startedAt,
		CreatedAt:     startedAt.Add(-time.Hour),
		UpdatedAt:     startedAt,
	})
	pluginWork, err := findAssignedDogWork(ctx, "deacon/dogs/alpha")
	if err != nil {
		t.Fatalf("findAssignedDogWork plugin: %v", err)
	}
	if pluginWork != nil {
		t.Fatalf("plugin mail work unexpectedly resolved a source hook: %+v", pluginWork)
	}
}
