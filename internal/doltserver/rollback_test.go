package doltserver

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFindBackups_Empty(t *testing.T) {
	townRoot := t.TempDir()

	backups, err := FindBackups(townRoot)
	if err != nil {
		t.Fatalf("FindBackups failed: %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("expected 0 backups, got %d", len(backups))
	}
}

func TestFindBackups_SortedNewestFirst(t *testing.T) {
	townRoot := t.TempDir()

	// Create backups in non-chronological order
	for _, ts := range []string{"20260101-120000", "20260103-120000", "20260102-120000"} {
		dir := filepath.Join(townRoot, "migration-backup-"+ts)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	backups, err := FindBackups(townRoot)
	if err != nil {
		t.Fatalf("FindBackups failed: %v", err)
	}
	if len(backups) != 3 {
		t.Fatalf("expected 3 backups, got %d", len(backups))
	}

	if backups[0].Timestamp != "20260103-120000" {
		t.Errorf("first backup timestamp = %q, want 20260103-120000", backups[0].Timestamp)
	}
	if backups[1].Timestamp != "20260102-120000" {
		t.Errorf("second backup timestamp = %q, want 20260102-120000", backups[1].Timestamp)
	}
	if backups[2].Timestamp != "20260101-120000" {
		t.Errorf("third backup timestamp = %q, want 20260101-120000", backups[2].Timestamp)
	}
}

func TestFindBackups_LoadsMetadata(t *testing.T) {
	townRoot := t.TempDir()

	dir := filepath.Join(townRoot, "migration-backup-20260207-143022")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	meta := map[string]interface{}{
		"created_at": "2026-02-07T14:30:22Z",
		"town_root":  townRoot,
	}
	data, _ := json.Marshal(meta)
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	backups, err := FindBackups(townRoot)
	if err != nil {
		t.Fatalf("FindBackups failed: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("expected 1 backup, got %d", len(backups))
	}
	if backups[0].Metadata == nil {
		t.Fatal("expected metadata to be loaded")
	}
	if backups[0].Metadata["created_at"] != "2026-02-07T14:30:22Z" {
		t.Errorf("created_at = %v, want 2026-02-07T14:30:22Z", backups[0].Metadata["created_at"])
	}
}

func TestFindBackups_IgnoresNonBackupDirs(t *testing.T) {
	townRoot := t.TempDir()

	// Create some non-backup directories
	for _, name := range []string{"gastown", ".beads", "migration-backup-", "not-a-backup"} {
		if err := os.MkdirAll(filepath.Join(townRoot, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	// And one real backup
	if err := os.MkdirAll(filepath.Join(townRoot, "migration-backup-20260101-000000"), 0755); err != nil {
		t.Fatal(err)
	}

	backups, err := FindBackups(townRoot)
	if err != nil {
		t.Fatalf("FindBackups failed: %v", err)
	}
	if len(backups) != 1 {
		t.Errorf("expected 1 backup, got %d", len(backups))
	}
}

func TestRestoreFromBackup_FormulaStyle(t *testing.T) {
	townRoot := t.TempDir()

	// Create the backup in formula style: town-beads/ and <rigname>-beads/
	backupDir := filepath.Join(townRoot, "migration-backup-20260207-143022")

	// Town-level backup
	townBackupBeads := filepath.Join(backupDir, "town-beads")
	if err := os.MkdirAll(townBackupBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBackupBeads, "metadata.json"),
		[]byte(`{"database": "beads.db", "backend": "sqlite"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Rig-level backup
	rigBackupBeads := filepath.Join(backupDir, "gastown-beads")
	if err := os.MkdirAll(rigBackupBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBackupBeads, "metadata.json"),
		[]byte(`{"database": "beads.db", "backend": "sqlite"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create existing (post-migration) .beads dirs that will be replaced
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "metadata.json"),
		[]byte(`{"backend": "dolt", "dolt_mode": "server"}`), 0644); err != nil {
		t.Fatal(err)
	}

	rigBeads := filepath.Join(townRoot, "gastown", ".beads")
	if err := os.MkdirAll(rigBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"),
		[]byte(`{"backend": "dolt", "dolt_mode": "server"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Perform rollback
	result, err := RestoreFromBackup(townRoot, backupDir)
	if err != nil {
		t.Fatalf("RestoreFromBackup failed: %v", err)
	}

	// Verify results
	if !result.RestoredTown {
		t.Error("expected RestoredTown to be true")
	}
	if len(result.RestoredRigs) != 1 || result.RestoredRigs[0] != "gastown" {
		t.Errorf("RestoredRigs = %v, want [gastown]", result.RestoredRigs)
	}

	// Verify metadata.json was reset to pre-migration state
	data, err := os.ReadFile(filepath.Join(townRoot, ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("reading restored town metadata: %v", err)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parsing restored metadata: %v", err)
	}
	if meta["backend"] != "sqlite" {
		t.Errorf("town metadata backend = %v, want sqlite (pre-migration)", meta["backend"])
	}

	// Verify rig metadata was reset
	data, err = os.ReadFile(filepath.Join(townRoot, "gastown", ".beads", "metadata.json"))
	if err != nil {
		t.Fatalf("reading restored rig metadata: %v", err)
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parsing restored rig metadata: %v", err)
	}
	if meta["backend"] != "sqlite" {
		t.Errorf("rig metadata backend = %v, want sqlite (pre-migration)", meta["backend"])
	}
}

func TestRestoreFromBackup_TestBackupStyle(t *testing.T) {
	townRoot := t.TempDir()

	// Create the backup in test-backup style: rigs/<rigname>/.beads
	backupDir := filepath.Join(townRoot, "migration-backup-20260207-143022")

	// Town-level backup
	townBackupBeads := filepath.Join(backupDir, "town-beads")
	if err := os.MkdirAll(townBackupBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townBackupBeads, "metadata.json"),
		[]byte(`{"backend": "sqlite"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Rig-level backup in rigs/ subdirectory
	rigBackup := filepath.Join(backupDir, "rigs", "gastown", ".beads")
	if err := os.MkdirAll(rigBackup, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBackup, "metadata.json"),
		[]byte(`{"backend": "sqlite"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create existing dirs
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "gastown", ".beads"), 0755); err != nil {
		t.Fatal(err)
	}

	result, err := RestoreFromBackup(townRoot, backupDir)
	if err != nil {
		t.Fatalf("RestoreFromBackup failed: %v", err)
	}

	if !result.RestoredTown {
		t.Error("expected RestoredTown to be true")
	}
	if len(result.RestoredRigs) != 1 || result.RestoredRigs[0] != "gastown" {
		t.Errorf("RestoredRigs = %v, want [gastown]", result.RestoredRigs)
	}
}

func TestRestoreFromBackup_NoBackup(t *testing.T) {
	townRoot := t.TempDir()

	_, err := RestoreFromBackup(townRoot, filepath.Join(townRoot, "nonexistent"))
	if err == nil {
		t.Fatal("expected error for nonexistent backup")
	}
}

func TestRestoreFromBackup_CreatesMissingParentDirs(t *testing.T) {
	townRoot := t.TempDir()

	// Create backup with a rig that doesn't exist yet in town
	backupDir := filepath.Join(townRoot, "migration-backup-20260207-143022")
	rigBackup := filepath.Join(backupDir, "newrig-beads")
	if err := os.MkdirAll(rigBackup, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBackup, "metadata.json"),
		[]byte(`{"backend": "sqlite"}`), 0644); err != nil {
		t.Fatal(err)
	}

	// The newrig/ directory doesn't exist in townRoot yet
	result, err := RestoreFromBackup(townRoot, backupDir)
	if err != nil {
		t.Fatalf("RestoreFromBackup failed: %v", err)
	}

	if len(result.RestoredRigs) != 1 || result.RestoredRigs[0] != "newrig" {
		t.Errorf("RestoredRigs = %v, want [newrig]", result.RestoredRigs)
	}

	// Verify the directory was created
	if _, err := os.Stat(filepath.Join(townRoot, "newrig", ".beads", "metadata.json")); err != nil {
		t.Errorf("expected restored beads dir to exist: %v", err)
	}
}

// Directory-copy behaviour now lives with its implementation in
// internal/fsutil (see TestCopyDirReproducesTree and siblings). The
// end-to-end restore paths above exercise it through RestoreFromBackup.
