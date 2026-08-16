package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/spf13/cobra"
)

func setupTestTownForCrewList(t *testing.T, rigs map[string][]string) string {
	t.Helper()

	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	townConfig := &config.TownConfig{
		Type:       "town",
		Version:    config.CurrentTownVersion,
		Name:       "test-town",
		PublicName: "Test Town",
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	if err := config.SaveTownConfig(filepath.Join(mayorDir, "town.json"), townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	rigsConfig := &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    make(map[string]config.RigEntry),
	}

	for rigName, crewNames := range rigs {
		rigsConfig.Rigs[rigName] = config.RigEntry{
			GitURL:  "https://example.com/" + rigName + ".git",
			AddedAt: time.Now(),
		}

		rigPath := filepath.Join(townRoot, rigName)
		crewDir := filepath.Join(rigPath, "crew")
		if err := os.MkdirAll(crewDir, 0755); err != nil {
			t.Fatalf("mkdir crew dir: %v", err)
		}
		for _, crewName := range crewNames {
			if err := os.MkdirAll(filepath.Join(crewDir, crewName), 0755); err != nil {
				t.Fatalf("mkdir crew worker: %v", err)
			}
		}
	}

	if err := config.SaveRigsConfig(filepath.Join(mayorDir, "rigs.json"), rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	return townRoot
}

func TestRunCrewList_PositionalRigArg(t *testing.T) {
	townRoot := setupTestTownForCrewList(t, map[string][]string{
		"rig-a": {"alice"},
		"rig-b": {"bob"},
	})

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	t.Run("positional arg sets rig filter", func(t *testing.T) {
		crewRig = ""
		crewListAll = false
		crewJSON = true
		defer func() {
			crewRig = ""
			crewJSON = false
		}()

		output := captureStdout(t, func() {
			if err := runCrewList(&cobra.Command{}, []string{"rig-a"}); err != nil {
				t.Fatalf("runCrewList error: %v", err)
			}
		})

		var items []CrewListItem
		if err := json.Unmarshal([]byte(output), &items); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("expected 1 crew worker, got %d", len(items))
		}
		if items[0].Rig != "rig-a" {
			t.Errorf("expected rig-a, got %s", items[0].Rig)
		}
	})

	t.Run("positional arg conflicts with --rig flag", func(t *testing.T) {
		crewRig = "rig-b"
		crewListAll = false
		defer func() { crewRig = "" }()

		err := runCrewList(&cobra.Command{}, []string{"rig-a"})
		if err == nil {
			t.Fatal("expected error for positional arg + --rig flag")
		}
		if !strings.Contains(err.Error(), "cannot specify both") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("positional arg with --all errors", func(t *testing.T) {
		crewRig = ""
		crewListAll = true
		defer func() { crewListAll = false }()

		err := runCrewList(&cobra.Command{}, []string{"rig-a"})
		if err == nil {
			t.Fatal("expected error for positional arg + --all")
		}
	})
}

func TestRunCrewList_AllWithRigErrors(t *testing.T) {
	townRoot := setupTestTownForCrewList(t, map[string][]string{"rig-a": {"alice"}})

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	crewListAll = true
	crewRig = "rig-a"
	defer func() {
		crewListAll = false
		crewRig = ""
	}()

	err := runCrewList(&cobra.Command{}, nil)
	if err == nil {
		t.Fatal("expected error for --all with --rig, got nil")
	}
}

func TestRunCrewList_AllAggregatesJSON(t *testing.T) {
	townRoot := setupTestTownForCrewList(t, map[string][]string{
		"rig-a": {"alice"},
		"rig-b": {"bob"},
	})

	originalWd, _ := os.Getwd()
	defer os.Chdir(originalWd)
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	crewListAll = true
	crewJSON = true
	crewRig = ""
	defer func() {
		crewListAll = false
		crewJSON = false
	}()

	output := captureStdout(t, func() {
		if err := runCrewList(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runCrewList failed: %v", err)
		}
	})

	var items []CrewListItem
	if err := json.Unmarshal([]byte(output), &items); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 crew workers, got %d", len(items))
	}

	rigs := map[string]bool{}
	for _, item := range items {
		rigs[item.Rig] = true
	}
	if !rigs["rig-a"] || !rigs["rig-b"] {
		t.Fatalf("expected crew from rig-a and rig-b, got: %#v", rigs)
	}
}
