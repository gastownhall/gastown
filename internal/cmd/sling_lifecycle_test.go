package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/scheduler/capacity"
	"github.com/jonbaldie/gastown/internal/sling"
)

func stubRawSlingCollaborators(t *testing.T, townRoot string) (spawnOpts *SlingSpawnOptions, createdConvoys *[]string) {
	t.Helper()
	var captured SlingSpawnOptions
	var convoys []string
	prevSpawn := spawnPolecatForSling
	prevHook := hookBeadWithRetryWithTownRootFn
	prevAddTracking := addTrackingRelationFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		hookBeadWithRetryWithTownRootFn = prevHook
		addTrackingRelationFn = prevAddTracking
	})
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		captured = opts
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
			Pane:        "%1",
		}, nil
	}
	hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
		return nil
	}
	addTrackingRelationFn = func(gotTownRoot, convoyID, issueID string) error {
		convoys = append(convoys, convoyID)
		return nil
	}
	return &captured, &convoys
}

func completeRawIntentFields() *capacity.SlingContextFields {
	return &capacity.SlingContextFields{
		Version:      1,
		WorkBeadID:   "gt-rawrollback",
		TargetRig:    "gastown",
		Args:         "implement the thing",
		Vars:         "feature=widget\nissue=gt-rawrollback",
		Merge:        "local",
		Convoy:       "hq-cv-scheduled",
		ResumeBranch: "feature/resume-me",
		Account:      "work",
		Agent:        "codex",
		HookRawBead:  true,
		Owned:        true,
		Mode:         "ralph",
		ReviewOnly:   true,
		NoMerge:      true,
		EnqueuedAt:   "2026-08-14T00:00:00Z",
	}
}

func assertCompleteRawAttachment(t *testing.T, desc, wantConvoy string) {
	t.Helper()
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil {
		t.Fatal("issue has no attachment fields")
	}
	if fields.ConvoyID != wantConvoy {
		t.Errorf("ConvoyID = %q, want %q", fields.ConvoyID, wantConvoy)
	}
	if !fields.ConvoyOwned {
		t.Error("ConvoyOwned = false, want true")
	}
	if fields.MergeStrategy != "local" {
		t.Errorf("MergeStrategy = %q, want local", fields.MergeStrategy)
	}
	if fields.AttachedArgs != "implement the thing" {
		t.Errorf("AttachedArgs = %q", fields.AttachedArgs)
	}
	if fields.Mode != "ralph" {
		t.Errorf("Mode = %q, want ralph", fields.Mode)
	}
	if !fields.NoMerge {
		t.Error("NoMerge = false, want true")
	}
	if !fields.ReviewOnly {
		t.Error("ReviewOnly = false, want true")
	}
}

// TestDeferredOwnedLocalSlingPreservesRecordedConvoy is the ticket-1 regression:
// schedule → durable reconstruction → dispatch → Bead attachment, without calling
// executeSling directly.
func TestDeferredOwnedLocalSlingPreservesRecordedConvoy(t *testing.T) {
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	spawnOpts, createdConvoys := stubRawSlingCollaborators(t, townRoot)

	scheduled := completeRawIntentFields()
	persisted := beads.FormatSlingContextDescription(scheduled)
	reconstructed := beads.ParseSlingContextFields(persisted)
	if reconstructed == nil {
		t.Fatal("durable sling context did not reconstruct")
	}
	if reconstructed.Convoy != "hq-cv-scheduled" || !reconstructed.Owned {
		t.Fatalf("reconstructed convoy/owned = %q/%v", reconstructed.Convoy, reconstructed.Owned)
	}

	_, err := dispatchSingleBead(capacity.PendingBead{
		ID:         "gt-context",
		WorkBeadID: scheduled.WorkBeadID,
		TargetRig:  scheduled.TargetRig,
		Context:    reconstructed,
	}, townRoot, "test")
	if err != nil {
		t.Fatalf("dispatchSingleBead: %v", err)
	}

	if len(*createdConvoys) != 0 {
		t.Fatalf("deferred dispatch created duplicate convoy %v", *createdConvoys)
	}
	if spawnOpts.ResumeBranch != "feature/resume-me" {
		t.Errorf("ResumeBranch = %q, want feature/resume-me", spawnOpts.ResumeBranch)
	}
	if spawnOpts.Account != "work" {
		t.Errorf("Account = %q, want work", spawnOpts.Account)
	}
	if spawnOpts.Agent != "codex" {
		t.Errorf("Agent = %q, want codex", spawnOpts.Agent)
	}
	assertCompleteRawAttachment(t, readMutableBDDescription(t, descPath), "hq-cv-scheduled")
}

func TestLifecycleRawDirectSlingAttachesConvoyHookAndBead(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	spawnOpts, createdConvoys := stubRawSlingCollaborators(t, townRoot)

	outcome, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		RigName:          "gastown",
		Args:             "implement the thing",
		Vars:             []string{"feature=widget", "issue=gt-rawrollback"},
		Merge:            "local",
		ResumeBranch:     "feature/resume-me",
		Account:          "work",
		Agent:            "codex",
		Mode:             "ralph",
		NoMerge:          true,
		ReviewOnly:       true,
		HookRawBead:      true,
		Owned:            true,
		FormulaFailFatal: true,
		CallerContext:    "sling",
		NoBoot:           true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome == nil || !outcome.Success || outcome.PolecatName != "toast" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.ConvoyID == "" {
		t.Fatal("direct sling did not report a Convoy identity")
	}
	if len(*createdConvoys) != 1 || (*createdConvoys)[0] != outcome.ConvoyID {
		t.Fatalf("created convoys = %v, want [%s]", *createdConvoys, outcome.ConvoyID)
	}
	if spawnOpts.ResumeBranch != "feature/resume-me" {
		t.Errorf("ResumeBranch = %q", spawnOpts.ResumeBranch)
	}
	assertCompleteRawAttachment(t, readMutableBDDescription(t, descPath), outcome.ConvoyID)
}

func TestDirectAndDeferredRawSlingProduceEquivalentAttachment(t *testing.T) {
	directRoot, directRig, directDesc := setupMutableBDRawSlingTest(t, "Keep this body.")
	_, _ = stubRawSlingCollaborators(t, directRoot)
	direct, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		RigName:          "gastown",
		Merge:            "local",
		HookRawBead:      true,
		Owned:            true,
		FormulaFailFatal: true,
		NoBoot:           true,
		TownRoot:         directRoot,
		BeadsDir:         filepath.Join(directRig, ".beads"),
	})
	if err != nil {
		t.Fatalf("direct Execute: %v", err)
	}

	deferredRoot, _, deferredDesc := setupMutableBDRawSlingTest(t, "Keep this body.")
	_, created := stubRawSlingCollaborators(t, deferredRoot)
	_, err = dispatchSingleBead(capacity.PendingBead{
		ID:         "gt-context",
		WorkBeadID: "gt-rawrollback",
		TargetRig:  "gastown",
		Context: sling.ToContextFields(sling.Intent{
			BeadID:      "gt-rawrollback",
			RigName:     "gastown",
			Merge:       "local",
			Convoy:      direct.ConvoyID,
			HookRawBead: true,
			Owned:       true,
		}, "2026-08-14T00:00:00Z"),
	}, deferredRoot, "test")
	if err != nil {
		t.Fatalf("deferred dispatch: %v", err)
	}
	if len(*created) != 0 {
		t.Fatalf("deferred path created extra convoy %v", *created)
	}

	directFields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, directDesc)})
	deferredFields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, deferredDesc)})
	if directFields == nil || deferredFields == nil {
		t.Fatal("missing attachment fields")
	}
	if directFields.MergeStrategy != deferredFields.MergeStrategy || deferredFields.MergeStrategy != "local" {
		t.Errorf("merge mismatch direct=%q deferred=%q", directFields.MergeStrategy, deferredFields.MergeStrategy)
	}
	if !directFields.ConvoyOwned || !deferredFields.ConvoyOwned {
		t.Error("ownership mismatch")
	}
	if deferredFields.ConvoyID != direct.ConvoyID {
		t.Errorf("deferred ConvoyID = %q, want recorded %q", deferredFields.ConvoyID, direct.ConvoyID)
	}
}

func TestLifecycleFormulaSlingAttachesMolecule(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	_, _ = stubRawSlingCollaborators(t, townRoot)
	extendBDStubForFormula(t, townRoot)

	outcome, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		RigName:          "gastown",
		Formula:          "mol-polecat-work",
		Vars:             []string{"disks=3"},
		Merge:            "local",
		Owned:            true,
		FormulaFailFatal: true,
		NoBoot:           true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err != nil {
		t.Fatalf("formula Execute: %v", err)
	}
	if outcome.AttachedMolecule != "gt-wisp-xyz" {
		t.Errorf("AttachedMolecule = %q, want gt-wisp-xyz", outcome.AttachedMolecule)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, descPath)})
	if fields == nil {
		t.Fatal("no attachment fields")
	}
	if fields.AttachedMolecule != "gt-wisp-xyz" {
		t.Errorf("bead AttachedMolecule = %q", fields.AttachedMolecule)
	}
	if fields.AttachedFormula != "mol-polecat-work" {
		t.Errorf("AttachedFormula = %q", fields.AttachedFormula)
	}
	if !strings.Contains(fields.FormulaVars, "disks=3") {
		t.Errorf("FormulaVars missing disks=3: %q", fields.FormulaVars)
	}
}

func TestDeferredFormulaSlingMatchesDirectMoleculeAttachment(t *testing.T) {
	townRoot, _, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	_, created := stubRawSlingCollaborators(t, townRoot)
	extendBDStubForFormula(t, townRoot)

	_, err := dispatchSingleBead(capacity.PendingBead{
		ID:         "gt-context",
		WorkBeadID: "gt-rawrollback",
		TargetRig:  "gastown",
		Context: &capacity.SlingContextFields{
			WorkBeadID: "gt-rawrollback",
			TargetRig:  "gastown",
			Formula:    "mol-polecat-work",
			Vars:       "disks=3",
			Merge:      "local",
			Convoy:     "hq-cv-formula",
			Owned:      true,
		},
	}, townRoot, "test")
	if err != nil {
		t.Fatalf("deferred formula dispatch: %v", err)
	}
	if len(*created) != 0 {
		t.Fatalf("deferred formula created duplicate convoy %v", *created)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, descPath)})
	if fields == nil || fields.AttachedMolecule != "gt-wisp-xyz" {
		t.Fatalf("deferred formula molecule = %+v", fields)
	}
	if fields.ConvoyID != "hq-cv-formula" || !fields.ConvoyOwned {
		t.Fatalf("deferred formula convoy = %+v", fields)
	}
}

func TestLifecycleCompensatesHookFailureWithoutClosingReusedConvoy(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "no_merge: true\n\nKeep this body.")
	prevSpawn := spawnPolecatForSling
	prevHook := hookBeadWithRetryWithTownRootFn
	prevAdd := addTrackingRelationFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		hookBeadWithRetryWithTownRootFn = prevHook
		addTrackingRelationFn = prevAdd
	})
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
		}, nil
	}
	hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
		return errors.New("forced hook failure")
	}
	var created []string
	addTrackingRelationFn = func(gotTownRoot, convoyID, issueID string) error {
		created = append(created, convoyID)
		return nil
	}

	_, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		RigName:          "gastown",
		Convoy:           "hq-cv-existing",
		NoConvoy:         true,
		HookRawBead:      true,
		NoMerge:          true,
		ReviewOnly:       true,
		FormulaFailFatal: true,
		NoBoot:           true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if len(created) != 0 {
		t.Fatalf("compensation closed/recreated reused convoy: %v", created)
	}
	desc := readMutableBDDescription(t, descPath)
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("body lost:\n%s", desc)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil || !fields.NoMerge {
		t.Fatalf("prior no_merge was not restored: %+v\n%s", fields, desc)
	}
	if fields != nil && fields.ReviewOnly {
		t.Fatalf("failed attempt review_only leaked onto bead: %+v", fields)
	}
}

func TestLifecycleCompensatesEachDurableStage(t *testing.T) {
	stages := []struct {
		name  string
		setup func(t *testing.T, townRoot string)
	}{
		{
			name: "formula cook",
			setup: func(t *testing.T, townRoot string) {
				extendBDStubForFormula(t, townRoot)
				t.Setenv("BD_FAIL_COOK", "1")
			},
		},
		{
			name: "assignee lock",
			setup: func(t *testing.T, townRoot string) {
				prev := tryAcquireSlingAssigneeLockFn
				t.Cleanup(func() { tryAcquireSlingAssigneeLockFn = prev })
				tryAcquireSlingAssigneeLockFn = func(townRoot, targetAgent string) (func(), error) {
					return nil, errors.New("forced assignee lock failure")
				}
			},
		},
		{
			name: "hook",
			setup: func(t *testing.T, townRoot string) {
				hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
					return errors.New("forced hook failure")
				}
			},
		},
		{
			name: "bead fields",
			setup: func(t *testing.T, townRoot string) {
				t.Setenv("BD_FAIL_DESCRIPTION_UPDATE", "1")
			},
		},
	}

	for _, stage := range stages {
		t.Run(stage.name, func(t *testing.T) {
			townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
			prevSpawn := spawnPolecatForSling
			prevHook := hookBeadWithRetryWithTownRootFn
			prevRollback := rollbackSlingArtifactsFn
			t.Cleanup(func() {
				spawnPolecatForSling = prevSpawn
				hookBeadWithRetryWithTownRootFn = prevHook
				rollbackSlingArtifactsFn = prevRollback
			})
			spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
				return &SpawnedPolecatInfo{
					RigName:     rigName,
					PolecatName: "toast",
					ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
					Pane:        "%1",
				}, nil
			}
			hookBeadWithRetryWithTownRootFn = func(beadID, targetAgent, hookDir, townRoot string) error {
				return nil
			}
			rollbackCalled := false
			rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
				rollbackCalled = true
				if spawnInfo == nil || spawnInfo.PolecatName != "toast" {
					t.Fatalf("unexpected spawnInfo %+v", spawnInfo)
				}
			}
			stage.setup(t, townRoot)

			intent := sling.Intent{
				BeadID:           "gt-rawrollback",
				RigName:          "gastown",
				HookRawBead:      true,
				NoMerge:          true,
				ReviewOnly:       true,
				NoConvoy:         true,
				FormulaFailFatal: true,
				NoBoot:           true,
				TownRoot:         townRoot,
				BeadsDir:         filepath.Join(rigPath, ".beads"),
			}
			if stage.name == "formula cook" {
				intent.HookRawBead = false
				intent.Formula = "mol-polecat-work"
			}
			_, err := executeDeepSling(context.Background(), intent)
			if err == nil {
				t.Fatal("expected durable-stage failure")
			}
			if !rollbackCalled {
				t.Fatal("lifecycle did not compensate after Polecat creation")
			}
			if strings.Contains(readMutableBDDescription(t, descPath), "review_only: true") {
				t.Fatalf("stale review metadata remains after %s failure", stage.name)
			}
		})
	}
}

func TestDeferredDispatchFailureIdentifiesBeadAndRig(t *testing.T) {
	townRoot, _, _ := setupMutableBDRawSlingTest(t, "Keep this body.")
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = prevSpawn })
	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return nil, errors.New("spawn exploded")
	}

	_, err := dispatchSingleBead(capacity.PendingBead{
		ID:         "gt-context",
		WorkBeadID: "gt-rawrollback",
		TargetRig:  "gastown",
		Context: &capacity.SlingContextFields{
			WorkBeadID:  "gt-rawrollback",
			TargetRig:   "gastown",
			HookRawBead: true,
		},
	}, townRoot, "test")
	if err == nil {
		t.Fatal("expected dispatch failure")
	}
	if !strings.Contains(err.Error(), "gt-rawrollback") || !strings.Contains(err.Error(), "gastown") {
		t.Errorf("error %q should identify work bead and target rig", err)
	}
}

func extendBDStubForFormula(t *testing.T, townRoot string) {
	t.Helper()
	binDir := filepath.Join(townRoot, "bin")
	bdScript := `#!/bin/sh
set -eu
if [ "${1:-}" = "--allow-stale" ] && [ "${2:-}" = "version" ]; then
  echo "bd test"
  exit 0
fi
while [ "$#" -gt 0 ]; do
  case "$1" in
    --allow-stale) shift ;;
    *) break ;;
  esac
done
cmd="${1:-}"
if [ "$#" -gt 0 ]; then shift; fi
case "$cmd" in
  show)
    desc=""
    if [ -f "$BD_DESC_FILE" ]; then
      desc=$(awk 'BEGIN{first=1} {gsub(/\\/,"\\\\"); gsub(/"/,"\\\""); if(!first){printf "\\n"} printf "%s",$0; first=0}' "$BD_DESC_FILE")
    fi
    status="open"
    if [ -f "$BD_STATUS_FILE" ]; then status=$(cat "$BD_STATUS_FILE"); fi
    assignee=""
    if [ -f "$BD_ASSIGNEE_FILE" ]; then assignee=$(cat "$BD_ASSIGNEE_FILE"); fi
    printf '[{"id":"gt-rawrollback","title":"Test issue","status":"%s","assignee":"%s","description":"%s","dependencies":[]}]\n' "$status" "$assignee" "$desc"
    ;;
  update)
    for arg in "$@"; do
      case "$arg" in
        --description=*)
          if [ "${BD_FAIL_DESCRIPTION_UPDATE:-}" = "1" ]; then
            echo "forced description update failure" >&2
            exit 1
          fi
          printf "%s" "${arg#--description=}" > "$BD_DESC_FILE"
          ;;
        --status=*) printf "%s" "${arg#--status=}" > "$BD_STATUS_FILE" ;;
        --assignee=*) printf "%s" "${arg#--assignee=}" > "$BD_ASSIGNEE_FILE" ;;
      esac
    done
    ;;
  cook)
    if [ "${BD_FAIL_COOK:-}" = "1" ]; then
      echo "forced cook failure" >&2
      exit 1
    fi
    ;;
  formula)
    echo '{"name":"mol-polecat-work"}'
    ;;
  mol)
    sub="${1:-}"
    case "$sub" in
      bond)
        echo '{"result_id":"gt-wisp-xyz","id_mapping":{"mol-polecat-work":"gt-wisp-xyz"}}'
        ;;
      wisp)
        echo "legacy mol wisp should not be called" >&2
        exit 1
        ;;
    esac
    ;;
  create)
    ;;
  version)
    echo "bd test"
    ;;
esac
exit 0
`
	writeBDStub(t, binDir, bdScript, "")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestLifecycleNamedTargetHooksWithoutSpawningPolecat(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	workDir := filepath.Join(townRoot, "gastown", "crew", "toast")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatalf("mkdir workDir: %v", err)
	}

	spawned := false
	prevSpawn := spawnPolecatForSling
	prevResolve := resolveTargetAgentFn
	prevHook := hookBeadWithRetryFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		resolveTargetAgentFn = prevResolve
		hookBeadWithRetryFn = prevHook
	})
	spawnPolecatForSling = func(string, SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawned = true
		return nil, errors.New("named target must not spawn a polecat")
	}
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		if target != "gastown/crew/toast" {
			t.Errorf("resolve target = %q", target)
		}
		return "gastown/crew/toast", "%1", workDir, nil
	}
	hookBeadWithRetryFn = func(beadID, targetAgent, hookDir string) error {
		return nil
	}

	outcome, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		Target:           "gastown/crew/toast",
		HookRawBead:      true,
		NoConvoy:         true,
		NoBoot:           true,
		FormulaFailFatal: true,
		CallerContext:    "sling",
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err != nil {
		t.Fatalf("Execute named target: %v", err)
	}
	if spawned {
		t.Fatal("named target dispatched through polecat spawn")
	}
	if outcome == nil || !outcome.Success {
		t.Fatalf("outcome = %+v", outcome)
	}
	desc := readMutableBDDescription(t, descPath)
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("named sling dropped bead body:\n%s", desc)
	}
}

func TestLifecycleNamedTargetRejectsClosedBead(t *testing.T) {
	townRoot, rigPath, _ := setupMutableBDRawSlingTest(t, "Keep this body.")
	statusPath := filepath.Join(townRoot, "status.txt")
	if err := os.WriteFile(statusPath, []byte("closed"), 0644); err != nil {
		t.Fatal(err)
	}

	spawned := false
	prevSpawn := spawnPolecatForSling
	t.Cleanup(func() { spawnPolecatForSling = prevSpawn })
	spawnPolecatForSling = func(string, SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		spawned = true
		return nil, errors.New("closed named target must not spawn")
	}

	_, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:   "gt-rawrollback",
		Target:   "gastown/crew/toast",
		Force:    true,
		NoConvoy: true,
		NoBoot:   true,
		TownRoot: townRoot,
		BeadsDir: filepath.Join(rigPath, ".beads"),
	})
	if err == nil {
		t.Fatal("expected closed bead to be refused")
	}
	if !strings.Contains(err.Error(), "closed") {
		t.Fatalf("error = %v", err)
	}
	if spawned {
		t.Fatal("closed named target spawned a polecat")
	}
}

func TestLifecycleNamedTargetIdempotentNoOp(t *testing.T) {
	townRoot, rigPath, _ := setupMutableBDRawSlingTest(t, "Keep this body.")
	if err := os.WriteFile(filepath.Join(townRoot, "status.txt"), []byte("hooked"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "assignee.txt"), []byte("gastown/crew/toast"), 0644); err != nil {
		t.Fatal(err)
	}

	prevDead := isHookedAgentDeadFn
	prevResolve := resolveTargetAgentFn
	t.Cleanup(func() {
		isHookedAgentDeadFn = prevDead
		resolveTargetAgentFn = prevResolve
	})
	isHookedAgentDeadFn = func(string) bool { return false }
	resolveTargetAgentFn = func(string) (string, string, string, error) {
		t.Fatal("idempotent named sling must not resolve a target")
		return "", "", "", nil
	}

	outcome, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:   "gt-rawrollback",
		Target:   "gastown/crew/toast",
		NoConvoy: true,
		NoBoot:   true,
		TownRoot: townRoot,
		BeadsDir: filepath.Join(rigPath, ".beads"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome == nil || !outcome.NoOp || !outcome.Success {
		t.Fatalf("outcome = %+v, want successful no-op", outcome)
	}
}

func TestLifecycleNamedTargetDeadAgentAutoForce(t *testing.T) {
	townRoot, rigPath, _ := setupMutableBDRawSlingTest(t, "Keep this body.")
	workDir := filepath.Join(townRoot, "gastown", "crew", "toast")
	if err := os.MkdirAll(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "status.txt"), []byte("hooked"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "assignee.txt"), []byte("gastown/crew/old"), 0644); err != nil {
		t.Fatal(err)
	}

	prevDead := isHookedAgentDeadFn
	prevResolve := resolveTargetAgentFn
	prevHook := hookBeadWithRetryFn
	t.Cleanup(func() {
		isHookedAgentDeadFn = prevDead
		resolveTargetAgentFn = prevResolve
		hookBeadWithRetryFn = prevHook
	})
	isHookedAgentDeadFn = func(assignee string) bool { return assignee == "gastown/crew/old" }
	resolveTargetAgentFn = func(target string) (string, string, string, error) {
		return "gastown/crew/toast", "", workDir, nil
	}
	hookBeadWithRetryFn = func(string, string, string) error { return nil }

	outcome, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:      "gt-rawrollback",
		Target:      "gastown/crew/toast",
		HookRawBead: true,
		NoConvoy:    true,
		NoBoot:      true,
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome == nil || !outcome.Success || outcome.NoOp {
		t.Fatalf("outcome = %+v, want successful re-sling", outcome)
	}
}

func TestLifecycleNamedPolecatAssigneeLockFailureCompensates(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	prevSpawn := spawnPolecatForSling
	prevResolve := resolveTargetAgentFn
	prevLock := tryAcquireSlingAssigneeLockFn
	prevRollback := rollbackSlingArtifactsFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		resolveTargetAgentFn = prevResolve
		tryAcquireSlingAssigneeLockFn = prevLock
		rollbackSlingArtifactsFn = prevRollback
	})

	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
		}, nil
	}
	resolveTargetAgentFn = func(string) (string, string, string, error) {
		return "", "", "", errors.New("no live polecat")
	}
	tryAcquireSlingAssigneeLockFn = func(string, string) (func(), error) {
		return nil, errors.New("forced assignee lock failure")
	}
	var rolledBack *SpawnedPolecatInfo
	var rolledBackConvoy string
	rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
		rolledBack = spawnInfo
		rolledBackConvoy = convoyID
	}

	_, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		Target:           "gastown/polecats/toast",
		Create:           true,
		HookRawBead:      true,
		NoMerge:          true,
		ReviewOnly:       true,
		NoConvoy:         true,
		NoBoot:           true,
		FormulaFailFatal: true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err == nil {
		t.Fatal("expected assignee-lock failure")
	}
	if !strings.Contains(err.Error(), "serializing hook write") {
		t.Fatalf("error = %v, want assignee-lock failure", err)
	}
	if rolledBack == nil || rolledBack.PolecatName != "toast" {
		t.Fatalf("named sling left spawned polecat uncompensated: %+v", rolledBack)
	}
	if rolledBackConvoy != "" {
		t.Fatalf("reused/absent convoy was closed: %q", rolledBackConvoy)
	}
	desc := readMutableBDDescription(t, descPath)
	if strings.Contains(desc, "review_only: true") {
		t.Fatalf("failed attempt leaked review_only:\n%s", desc)
	}
}

func TestLifecycleNamedHookFailureClosesCreatedConvoyOnly(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	prevSpawn := spawnPolecatForSling
	prevResolve := resolveTargetAgentFn
	prevHook := hookBeadWithRetryFn
	prevHookTown := hookBeadWithRetryWithTownRootFn
	prevAdd := addTrackingRelationFn
	prevRollback := rollbackSlingArtifactsFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		resolveTargetAgentFn = prevResolve
		hookBeadWithRetryFn = prevHook
		hookBeadWithRetryWithTownRootFn = prevHookTown
		addTrackingRelationFn = prevAdd
		rollbackSlingArtifactsFn = prevRollback
	})

	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
		}, nil
	}
	resolveTargetAgentFn = func(string) (string, string, string, error) {
		return "", "", "", errors.New("no live polecat")
	}
	hookFail := errors.New("forced hook failure")
	hookBeadWithRetryFn = func(string, string, string) error { return hookFail }
	hookBeadWithRetryWithTownRootFn = func(string, string, string, string) error { return hookFail }
	var created []string
	addTrackingRelationFn = func(gotTownRoot, convoyID, issueID string) error {
		created = append(created, convoyID)
		return nil
	}
	var rolledBackConvoy string
	var rolledBackSpawn *SpawnedPolecatInfo
	rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
		rolledBackSpawn = spawnInfo
		rolledBackConvoy = convoyID
	}

	_, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		Target:           "gastown/polecats/toast",
		Create:           true,
		HookRawBead:      true,
		Owned:            true,
		Merge:            "local",
		NoBoot:           true,
		FormulaFailFatal: true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if len(created) != 1 {
		t.Fatalf("created convoys = %v, want one new convoy", created)
	}
	if rolledBackSpawn == nil || rolledBackSpawn.PolecatName != "toast" {
		t.Fatalf("hook failure did not compensate spawned polecat: %+v", rolledBackSpawn)
	}
	if rolledBackConvoy != created[0] {
		t.Fatalf("hook failure closed convoy %q, want created %q", rolledBackConvoy, created[0])
	}
	desc := readMutableBDDescription(t, descPath)
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("body lost:\n%s", desc)
	}
}

func TestLifecycleNamedHookFailureKeepsReusedConvoy(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "no_merge: true\n\nKeep this body.")
	prevSpawn := spawnPolecatForSling
	prevResolve := resolveTargetAgentFn
	prevHook := hookBeadWithRetryFn
	prevHookTown := hookBeadWithRetryWithTownRootFn
	prevAdd := addTrackingRelationFn
	prevRollback := rollbackSlingArtifactsFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		resolveTargetAgentFn = prevResolve
		hookBeadWithRetryFn = prevHook
		hookBeadWithRetryWithTownRootFn = prevHookTown
		addTrackingRelationFn = prevAdd
		rollbackSlingArtifactsFn = prevRollback
	})

	spawnPolecatForSling = func(rigName string, opts SlingSpawnOptions) (*SpawnedPolecatInfo, error) {
		return &SpawnedPolecatInfo{
			RigName:     rigName,
			PolecatName: "toast",
			ClonePath:   filepath.Join(townRoot, "gastown", "polecats", "toast"),
		}, nil
	}
	resolveTargetAgentFn = func(string) (string, string, string, error) {
		return "", "", "", errors.New("no live polecat")
	}
	hookFail := errors.New("forced hook failure")
	hookBeadWithRetryFn = func(string, string, string) error { return hookFail }
	hookBeadWithRetryWithTownRootFn = func(string, string, string, string) error { return hookFail }
	var created []string
	addTrackingRelationFn = func(gotTownRoot, convoyID, issueID string) error {
		created = append(created, convoyID)
		return nil
	}
	var rolledBackConvoy string
	rollbackSlingArtifactsFn = func(spawnInfo *SpawnedPolecatInfo, beadID, hookWorkDir, convoyID string) {
		rolledBackConvoy = convoyID
	}

	_, err := executeDeepSling(context.Background(), sling.Intent{
		BeadID:           "gt-rawrollback",
		Target:           "gastown/polecats/toast",
		Create:           true,
		Convoy:           "hq-cv-existing",
		HookRawBead:      true,
		NoMerge:          true,
		ReviewOnly:       true,
		NoBoot:           true,
		FormulaFailFatal: true,
		TownRoot:         townRoot,
		BeadsDir:         filepath.Join(rigPath, ".beads"),
	})
	if err == nil {
		t.Fatal("expected hook failure")
	}
	if len(created) != 0 {
		t.Fatalf("reused convoy was recreated: %v", created)
	}
	if rolledBackConvoy != "" {
		t.Fatalf("reused convoy was closed: %q", rolledBackConvoy)
	}
	desc := readMutableBDDescription(t, descPath)
	if !strings.Contains(desc, "Keep this body.") {
		t.Fatalf("body lost:\n%s", desc)
	}
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields == nil || !fields.NoMerge {
		t.Fatalf("prior no_merge was not restored: %+v\n%s", fields, desc)
	}
	if fields != nil && fields.ReviewOnly {
		t.Fatalf("failed attempt review_only leaked onto bead: %+v", fields)
	}
}
