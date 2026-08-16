package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/sling"
)

func stubSlingSpawnAndHook(t *testing.T, townRoot string) {
	t.Helper()
	prevSpawn := spawnPolecatForSling
	prevHook := hookBeadWithRetryWithTownRootFn
	t.Cleanup(func() {
		spawnPolecatForSling = prevSpawn
		hookBeadWithRetryWithTownRootFn = prevHook
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
}

func executeRawSling(t *testing.T, townRoot, rigPath string, merge string) *SlingResult {
	t.Helper()
	result, err := executeSling(context.Background(), sling.Intent{
		BeadID:      "gt-rawrollback",
		RigName:     "gastown",
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
		HookRawBead: true,
		Merge:       merge,
		NoConvoy:    true,
		NoBoot:      true,
	})
	if err != nil {
		t.Fatalf("executeSling: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("executeSling result not successful: %+v", result)
	}
	return result
}

func TestExecuteSlingPersistsCLILocalMergeOnIssue(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "Keep this body.")
	stubSlingSpawnAndHook(t, townRoot)

	executeRawSling(t, townRoot, rigPath, "local")

	fields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, descPath)})
	if fields == nil || fields.MergeStrategy != "local" {
		t.Fatalf("MergeStrategy = %v, want local", fields)
	}
}

func TestExecuteSlingInfersLocalMergeFromIssueProse(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "DO NOT push to GitHub — local commit only")
	stubSlingSpawnAndHook(t, townRoot)

	executeRawSling(t, townRoot, rigPath, "")

	fields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, descPath)})
	if fields == nil || fields.MergeStrategy != "local" {
		t.Fatalf("MergeStrategy = %v, want local from issue prose", fields)
	}
}

func TestExecuteSlingReusesStoredLocalMergeWithoutFlag(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "merge_strategy: local\n\nOrdinary follow-up work.")
	stubSlingSpawnAndHook(t, townRoot)

	executeRawSling(t, townRoot, rigPath, "")

	fields := beads.ParseAttachmentFields(&beads.Issue{Description: readMutableBDDescription(t, descPath)})
	if fields == nil || fields.MergeStrategy != "local" {
		t.Fatalf("MergeStrategy = %v, want stored local reused on re-dispatch", fields)
	}
}

func TestExecuteSlingAppliesStoredLocalMergeToNewConvoy(t *testing.T) {
	townRoot, rigPath, _ := setupMutableBDRawSlingTest(t, "merge_strategy: local\n\nOrdinary follow-up work.")
	stubSlingSpawnAndHook(t, townRoot)

	createDescPath := filepath.Join(townRoot, "convoy-create-desc.txt")
	t.Setenv("BD_CREATE_DESC_FILE", createDescPath)

	prevAddTracking := addTrackingRelationFn
	t.Cleanup(func() { addTrackingRelationFn = prevAddTracking })
	addTrackingRelationFn = func(gotTownRoot, convoyID, issueID string) error {
		return nil
	}

	result, err := executeSling(context.Background(), sling.Intent{
		BeadID:      "gt-rawrollback",
		RigName:     "gastown",
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
		HookRawBead: true,
		NoBoot:      true,
	})
	if err != nil {
		t.Fatalf("executeSling: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("executeSling result not successful: %+v", result)
	}

	createDesc, err := os.ReadFile(createDescPath)
	if err != nil {
		t.Fatalf("convoy create description was not recorded: %v", err)
	}
	if !strings.Contains(string(createDesc), "Merge: local") {
		t.Fatalf("new convoy description missing Merge: local\n%s", createDesc)
	}
}

func TestExecuteSlingFailsClosedWhenLocalMergePersistFails(t *testing.T) {
	townRoot, rigPath, descPath := setupMutableBDRawSlingTest(t, "DO NOT push to GitHub — local commit only")
	stubSlingSpawnAndHook(t, townRoot)
	t.Setenv("BD_FAIL_DESCRIPTION_UPDATE", "1")

	result, err := executeSling(context.Background(), sling.Intent{
		BeadID:      "gt-rawrollback",
		RigName:     "gastown",
		TownRoot:    townRoot,
		BeadsDir:    filepath.Join(rigPath, ".beads"),
		HookRawBead: true,
		NoConvoy:    true,
		NoBoot:      true,
	})
	if err == nil {
		t.Fatal("expected persist failure from executeSling")
	}
	if result != nil && result.Success {
		t.Fatalf("dispatch reported success after persist failure: %+v", result)
	}
	desc := readMutableBDDescription(t, descPath)
	fields := beads.ParseAttachmentFields(&beads.Issue{Description: desc})
	if fields != nil && fields.MergeStrategy == "local" {
		t.Fatalf("failed persist still wrote merge_strategy=local:\n%s", desc)
	}
}

func TestDoneSkipPushForLocalStrategy(t *testing.T) {
	localIssue := &beads.Issue{Description: "merge_strategy: local\n"}
	mrIssue := &beads.Issue{Description: "merge_strategy: mr\n"}

	tests := []struct {
		name     string
		convoy   *ConvoyInfo
		issue    *beads.Issue
		wantSkip bool
	}{
		{
			name:     "convoy local",
			convoy:   &ConvoyInfo{ID: "hq-cv-1", MergeStrategy: "local"},
			wantSkip: true,
		},
		{
			name:     "issue local with no convoy",
			issue:    localIssue,
			wantSkip: true,
		},
		{
			name:     "issue local survives re-dispatch convoy without flag",
			convoy:   &ConvoyInfo{ID: "hq-cv-2", MergeStrategy: "mr"},
			issue:    localIssue,
			wantSkip: true,
		},
		{
			name:     "in-flight prose survives convoy without local",
			convoy:   &ConvoyInfo{ID: "hq-cv-4", MergeStrategy: "mr"},
			issue:    &beads.Issue{Description: "DO NOT push to GitHub — local commit only"},
			wantSkip: true,
		},
		{
			name:     "issue prose local with no stored field",
			issue:    &beads.Issue{Title: "secret work", Description: "DO NOT push to GitHub — local commit only"},
			wantSkip: true,
		},
		{
			name:     "neither local",
			convoy:   &ConvoyInfo{ID: "hq-cv-3", MergeStrategy: "mr"},
			issue:    mrIssue,
			wantSkip: false,
		},
		{
			name:     "empty convoy and ordinary issue",
			wantSkip: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := doneSkipPushForLocalStrategy(tt.convoy, tt.issue)
			if got != tt.wantSkip {
				t.Fatalf("doneSkipPushForLocalStrategy() = %v, want %v", got, tt.wantSkip)
			}
		})
	}
}
