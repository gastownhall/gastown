package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/wisp"
)

// TestAreScheduledFailClosed verifies that areScheduled fails closed when
// running outside a town root — all requested IDs should be treated as scheduled.
// This prevents false stranded detection and duplicate scheduling on transient errors.
func TestAreScheduledFailClosed(t *testing.T) {
	// Run areScheduled from a temp dir that is NOT a town root.
	// workspace.FindFromCwd will fail, triggering the fail-closed path.
	tmpDir := t.TempDir()
	origDir, _ := os.Getwd()
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("chdir to temp dir: %v", err)
	}
	defer func() { _ = os.Chdir(origDir) }()

	requestedIDs := []string{"bead-1", "bead-2", "bead-3"}
	result := areScheduled(requestedIDs)

	// All IDs should appear as scheduled (fail closed)
	for _, id := range requestedIDs {
		if !result[id] {
			t.Errorf("areScheduled fail-closed: expected %q to be marked as scheduled, but it was not", id)
		}
	}
}

// TestAreScheduledEmptyInput verifies areScheduled returns empty map for no input.
func TestAreScheduledEmptyInput(t *testing.T) {
	result := areScheduled(nil)
	if len(result) != 0 {
		t.Errorf("areScheduled(nil) should return empty map, got %d entries", len(result))
	}
	result = areScheduled([]string{})
	if len(result) != 0 {
		t.Errorf("areScheduled([]) should return empty map, got %d entries", len(result))
	}
}

// TestResolveFormula verifies formula resolution precedence:
// explicit flag > wisp layer > bead layer > system default > settings file > hardcoded fallback.
func TestResolveFormula(t *testing.T) {
	t.Parallel()

	t.Run("explicit flag wins", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("mol-evolve", false, "/tmp/nonexistent", "myrig", "")
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("hookRawBead returns empty", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("mol-evolve", true, "/tmp/nonexistent", "myrig", "")
		if got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})

	t.Run("system default mol-polecat-work", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)
		got := resolveFormula("", false, tmpDir, rigName, "")
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})

	t.Run("wisp layer overrides system default", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)

		wispCfg := wisp.NewConfig(tmpDir, rigName)
		if err := wispCfg.Set("default_formula", "mol-evolve"); err != nil {
			t.Fatalf("wisp set: %v", err)
		}

		got := resolveFormula("", false, tmpDir, rigName, "")
		if got != "mol-evolve" {
			t.Errorf("got %q, want %q", got, "mol-evolve")
		}
	})

	t.Run("explicit flag overrides wisp layer", func(t *testing.T) {
		t.Parallel()
		tmpDir := t.TempDir()
		rigName := "testrig"
		_ = os.MkdirAll(filepath.Join(tmpDir, rigName), 0o755)

		wispCfg := wisp.NewConfig(tmpDir, rigName)
		if err := wispCfg.Set("default_formula", "mol-evolve"); err != nil {
			t.Fatalf("wisp set: %v", err)
		}

		got := resolveFormula("mol-custom", false, tmpDir, rigName, "")
		if got != "mol-custom" {
			t.Errorf("got %q, want %q", got, "mol-custom")
		}
	})

	t.Run("empty rigName falls back to hardcoded default", func(t *testing.T) {
		t.Parallel()
		got := resolveFormula("", false, "/tmp/nonexistent", "", "")
		if got != "mol-polecat-work" {
			t.Errorf("got %q, want %q", got, "mol-polecat-work")
		}
	})
}

// installLabelsBD puts a fake bd on PATH whose show returns one issue with
// labels. Any other id is not found.
func installLabelsBD(t *testing.T, id string, labels string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX shell fake bd")
	}
	binDir := t.TempDir()
	script := `#!/bin/sh
[ "$1" = "--allow-stale" ] && shift
case "$1" in
  version) echo "bd version test"; exit 0 ;;
  show)
    if [ "$2" = "` + id + `" ]; then
      printf '[{"id":"%s","title":"T","status":"open","issue_type":"task","labels":[` + labels + `]}]\n' "$2"
      exit 0
    fi
    echo "Issue not found" >&2; exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	beads.ResetBdAllowStaleCacheForTest()
	t.Cleanup(beads.ResetBdAllowStaleCacheForTest)
}

func gatedRig(t *testing.T, enabled bool) (townRoot, rigName string) {
	t.Helper()
	townRoot, rigName = t.TempDir(), "r1"
	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(filepath.Join(rigPath, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := config.NewRigSettings()
	settings.Workflow = &config.WorkflowConfig{Gated: &config.GatedWorkflow{Enabled: enabled}}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), settings); err != nil {
		t.Fatal(err)
	}
	return townRoot, rigName
}

func TestResolveFormula_GatedItemUsesStepFormula(t *testing.T) {
	townRoot, rigName := gatedRig(t, true)
	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:plan","gtb:rig:r1"`)

	if got := resolveFormula("", false, townRoot, rigName, "gt-abc"); got != "gtb-task-plan" {
		t.Errorf("gated item: got %q, want gtb-task-plan", got)
	}
	if got := resolveFormula("x", false, townRoot, rigName, "gt-abc"); got != "x" {
		t.Errorf("explicit --formula: got %q, want x", got)
	}
	// Not a gated item (no gtb:wf label), unknown bead, or no item ID: old resolution.
	if got := resolveFormula("", false, townRoot, rigName, "gt-missing"); got != "mol-polecat-work" {
		t.Errorf("missing bead: got %q", got)
	}
	if got := resolveFormula("", false, townRoot, rigName, ""); got != "mol-polecat-work" {
		t.Errorf("no item: got %q", got)
	}
}

func TestResolveFormula_GatedNeedsEnabledRigAndLabel(t *testing.T) {
	townRoot, rigName := gatedRig(t, false)
	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:plan"`)
	if got := resolveFormula("", false, townRoot, rigName, "gt-abc"); got != "mol-polecat-work" {
		t.Errorf("disabled rig: got %q", got)
	}

	townRoot, rigName = gatedRig(t, true)
	installLabelsBD(t, "gt-abc", `"gtb:step:plan"`)
	if got := resolveFormula("", false, townRoot, rigName, "gt-abc"); got != "mol-polecat-work" {
		t.Errorf("no gtb:wf label: got %q", got)
	}
}

// installLabelsBDWithUpdate is installLabelsBD that also logs update calls.
func installLabelsBDWithUpdate(t *testing.T, id, labels string) string {
	t.Helper()
	installLabelsBD(t, id, labels)
	logPath := filepath.Join(t.TempDir(), "bd.log")
	binDir := filepath.Dir(lookPathT(t, "bd"))
	orig, err := os.ReadFile(filepath.Join(binDir, "bd"))
	if err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(string(orig), "case \"$1\" in", "printf '%s\\n' \"$*\" >> "+logPath+"\ncase \"$1\" in", 1)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return logPath
}

func lookPathT(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestBindGatedItemBranch (R19): sling records gtb:branch on a gated item,
// replacing the old binding, and leaves non-gated beads alone.
func TestBindGatedItemBranch(t *testing.T) {
	townRoot, rigName := gatedRig(t, true)
	log := installLabelsBDWithUpdate(t, "gt-abc", `"gtb:wf:task","gtb:step:execute","gtb:branch:polecat/old"`)
	bindGatedItemBranch(townRoot, rigName, "gt-abc", "polecat/new")
	raw, _ := os.ReadFile(log)
	if !strings.Contains(string(raw), "--add-label=gtb:branch:polecat/new") || !strings.Contains(string(raw), "--remove-label=gtb:branch:polecat/old") {
		t.Errorf("bd calls:\n%s", raw)
	}

	townRoot, rigName = gatedRig(t, true)
	log = installLabelsBDWithUpdate(t, "gt-abc", `"gt:task"`)
	bindGatedItemBranch(townRoot, rigName, "gt-abc", "polecat/new")
	if raw, _ := os.ReadFile(log); strings.Contains(string(raw), "update") {
		t.Errorf("non-workflow bead updated:\n%s", raw)
	}

	townRoot, rigName = gatedRig(t, false)
	log = installLabelsBDWithUpdate(t, "gt-abc", `"gtb:wf:task","gtb:step:execute"`)
	bindGatedItemBranch(townRoot, rigName, "gt-abc", "polecat/new")
	if raw, _ := os.ReadFile(log); strings.Contains(string(raw), "update") {
		t.Errorf("disabled rig updated:\n%s", raw)
	}
}

// gatedRigWithRepo is gatedRig plus a mayor/rig git repo holding branch.
func gatedRigWithRepo(t *testing.T, branch string) (townRoot, rigName string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	townRoot, rigName = gatedRig(t, true)
	repo := filepath.Join(townRoot, rigName, "mayor", "rig")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "branch", branch},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return townRoot, rigName
}

// TestGatedResumeBranch (R20): a gated item bound to an existing branch
// resumes it; a missing bound branch is refused, never recreated.
func TestGatedResumeBranch(t *testing.T) {
	townRoot, rigName := gatedRigWithRepo(t, "polecat/p1/gt-abc")
	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:execute","gtb:branch:polecat/p1/gt-abc"`)
	got, err := gatedResumeBranch(townRoot, rigName, "gt-abc", "")
	if err != nil || got != "polecat/p1/gt-abc" {
		t.Fatalf("bound branch: got %q, %v", got, err)
	}

	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:execute","gtb:branch:polecat/gone"`)
	if _, err := gatedResumeBranch(townRoot, rigName, "gt-abc", ""); err == nil || err.Error() != "item gt-abc branch polecat/gone is missing" {
		t.Fatalf("missing branch: err = %v", err)
	}

	// First sling (no binding yet) and non-workflow beads keep the caller's value.
	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:spec"`)
	if got, err := gatedResumeBranch(townRoot, rigName, "gt-abc", ""); err != nil || got != "" {
		t.Fatalf("unbound: got %q, %v", got, err)
	}
	installLabelsBD(t, "gt-abc", `"gt:task"`)
	if got, err := gatedResumeBranch(townRoot, rigName, "gt-abc", "feature/x"); err != nil || got != "feature/x" {
		t.Fatalf("non-workflow: got %q, %v", got, err)
	}
}

// TestResolveTargetGatedItemResumesBranch (R20): the rig-target spawn path
// passes the item's bound branch as ResumeBranch, and refuses (without
// spawning) when that branch is missing.
func TestResolveTargetGatedItemResumesBranch(t *testing.T) {
	townRoot := setupPolecatCapacityRig(t, 1)
	settings := config.NewRigSettings()
	settings.Workflow = &config.WorkflowConfig{Gated: &config.GatedWorkflow{Enabled: true}}
	if err := config.SaveRigSettings(config.RigSettingsPath(filepath.Join(townRoot, "gastown")), settings); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(townRoot, "gastown", "mayor", "rig")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", repo},
		{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", repo, "branch", "polecat/toast/gt-abc"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	oldSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = oldSpawn })
	var gotResume []string
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		gotResume = append(gotResume, opts.ResumeBranch)
		return &SpawnedPolecatInfo{RigName: "gastown", PolecatName: "toast", Branch: opts.ResumeBranch,
			ClonePath: filepath.Join(townRoot, "gastown", "polecats", "toast", "gastown")}, nil
	}
	opts := ResolveTargetOptions{TownRoot: townRoot, HookBead: "gt-abc", SkipPolecatAdmission: true, NoBoot: true}

	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:verify","gtb:branch:polecat/toast/gt-abc"`)
	if _, err := resolveTarget("gastown", opts); err != nil {
		t.Fatalf("resolveTarget: %v", err)
	}
	if len(gotResume) != 1 || gotResume[0] != "polecat/toast/gt-abc" {
		t.Fatalf("ResumeBranch = %v, want [polecat/toast/gt-abc]", gotResume)
	}

	installLabelsBD(t, "gt-abc", `"gtb:wf:task","gtb:step:verify","gtb:branch:polecat/gone"`)
	if _, err := resolveTarget("gastown", opts); err == nil || !strings.Contains(err.Error(), "item gt-abc branch polecat/gone is missing") {
		t.Fatalf("missing branch: err = %v", err)
	}
	if len(gotResume) != 1 {
		t.Fatalf("spawned despite a missing branch: %v", gotResume)
	}
}

// TestBatchFormulas_PerBead (R23): batch sling resolves each bead's formula
// with its own ID, so a gated item gets its step formula and a plain bead
// keeps the default; each distinct formula is listed once for cooking.
func TestBatchFormulas_PerBead(t *testing.T) {
	townRoot, rigName := gatedRig(t, true)
	installLabelsBD(t, "gt-abc", `"gtb:wf:fix","gtb:step:rca"`)
	oldF, oldRaw := slingFormula, slingHookRawBead
	slingFormula, slingHookRawBead = "", false
	t.Cleanup(func() { slingFormula, slingHookRawBead = oldF, oldRaw })

	formulaFor, order := batchFormulas([]string{"gt-abc", "gt-plain", "gt-plain2"}, townRoot, rigName)
	if formulaFor["gt-abc"] != "gtb-fix-rca" || formulaFor["gt-plain"] != "mol-polecat-work" || formulaFor["gt-plain2"] != "mol-polecat-work" {
		t.Errorf("formulaFor = %v", formulaFor)
	}
	if strings.Join(order, ",") != "gtb-fix-rca,mol-polecat-work" {
		t.Errorf("cook order = %v", order)
	}

	slingFormula = "mol-x" // explicit --formula still wins for every bead
	if formulaFor, _ := batchFormulas([]string{"gt-abc"}, townRoot, rigName); formulaFor["gt-abc"] != "mol-x" {
		t.Errorf("explicit formula: %v", formulaFor)
	}
}
