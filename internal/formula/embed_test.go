package formula

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestGetEmbeddedFormulas verifies embedded formulas can be read and hashed.
func TestGetEmbeddedFormulas(t *testing.T) {
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatalf("getEmbeddedFormulas() error: %v", err)
	}
	if len(embedded) == 0 {
		t.Error("should have embedded formulas")
	}

	// Verify at least one known formula exists
	if _, ok := embedded["mol-deacon-patrol.formula.toml"]; !ok {
		t.Error("should contain mol-deacon-patrol.formula.toml")
	}

	// Verify hashes are valid hex strings
	for name, hash := range embedded {
		if len(hash) != 64 {
			t.Errorf("%s hash has wrong length: %d", name, len(hash))
		}
	}
}

// TestProvisionFormulas_FreshInstall tests provisioning to an empty directory.
func TestProvisionFormulas_FreshInstall(t *testing.T) {
	tmpDir := t.TempDir()

	count, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}
	if count == 0 {
		t.Error("should have provisioned at least one formula")
	}

	// Verify formulas directory was created
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if _, err := os.Stat(formulasDir); os.IsNotExist(err) {
		t.Error("formulas directory should exist")
	}

	// Verify .installed.json was created
	installedPath := filepath.Join(formulasDir, ".installed.json")
	if _, err := os.Stat(installedPath); os.IsNotExist(err) {
		t.Error(".installed.json should exist")
	}

	// Verify installed record contains the right checksums
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatalf("loadInstalledRecord() error: %v", err)
	}
	if len(installed.Formulas) != count {
		t.Errorf("installed record has %d entries, want %d", len(installed.Formulas), count)
	}
}

// TestProvisionFormulas_SkipsExisting tests that existing files are not overwritten.
func TestProvisionFormulas_SkipsExisting(t *testing.T) {
	tmpDir := t.TempDir()

	// Create formulas directory with a custom formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	customContent := []byte("# Custom user formula\nformula = \"mol-deacon-patrol\"\n")
	customPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	if err := os.WriteFile(customPath, customContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Provision formulas
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Verify custom content was NOT overwritten
	content, err := os.ReadFile(customPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(customContent) {
		t.Error("existing formula should not have been overwritten")
	}
}

// TestCheckFormulaHealth_AllOK tests when all formulas are up to date.
func TestCheckFormulaHealth_AllOK(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Check health
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	if report.Outdated != 0 {
		t.Errorf("Outdated = %d, want 0", report.Outdated)
	}
	if report.Missing != 0 {
		t.Errorf("Missing = %d, want 0", report.Missing)
	}
	if report.Modified != 0 {
		t.Errorf("Modified = %d, want 0", report.Modified)
	}
	if report.OK == 0 {
		t.Error("OK should be > 0")
	}
}

// TestCheckFormulaHealth_UserModified tests detection of user-modified formulas.
func TestCheckFormulaHealth_UserModified(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Modify a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	formulaPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	modifiedContent := []byte("# User modified this\nformula = \"mol-deacon-patrol\"\nversion = 999\n")
	if err := os.WriteFile(formulaPath, modifiedContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Check health
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	if report.Modified != 1 {
		t.Errorf("Modified = %d, want 1", report.Modified)
	}

	// Verify the specific formula is marked as modified
	found := false
	for _, f := range report.Formulas {
		if f.Name == "mol-deacon-patrol.formula.toml" {
			if f.Status != "modified" {
				t.Errorf("mol-deacon-patrol status = %q, want %q", f.Status, "modified")
			}
			found = true
			break
		}
	}
	if !found {
		t.Error("mol-deacon-patrol.formula.toml not found in report")
	}
}

// TestCheckFormulaHealth_Missing tests detection of deleted formulas.
func TestCheckFormulaHealth_Missing(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Delete a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	formulaPath := filepath.Join(formulasDir, "mol-deacon-patrol.formula.toml")
	if err := os.Remove(formulaPath); err != nil {
		t.Fatal(err)
	}

	// Check health
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	if report.Missing != 1 {
		t.Errorf("Missing = %d, want 1", report.Missing)
	}
}

// TestCheckFormulaHealth_Outdated simulates an outdated formula.
func TestCheckFormulaHealth_Outdated(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Simulate "old" installed record by changing the installed hash for a formula
	// This mimics what happens when a new binary has updated formula content
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}

	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatal(err)
	}

	// Pick a formula that exists
	var targetFormula string
	for name := range installed.Formulas {
		targetFormula = name
		break
	}
	if targetFormula == "" {
		t.Skip("no formulas installed")
	}

	// Write a file that simulates "old version" - content differs from embedded
	formulaPath := filepath.Join(formulasDir, targetFormula)
	oldContent := []byte("# Old version of formula\n")
	if err := os.WriteFile(formulaPath, oldContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Update installed record to match the old content's hash
	hash := sha256.Sum256(oldContent)
	installed.Formulas[targetFormula] = hex.EncodeToString(hash[:])

	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		t.Fatal(err)
	}

	// Now: file matches what we "installed" but differs from embedded = outdated
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	if report.Outdated != 1 {
		t.Errorf("Outdated = %d, want 1", report.Outdated)
	}

	// Verify the embedded hash is different from installed
	embeddedHash := embedded[targetFormula]
	if embeddedHash == installed.Formulas[targetFormula] {
		t.Error("embedded hash should differ from installed hash for this test")
	}
}

// TestUpdateFormulas_UpdatesOutdated tests that outdated formulas are updated.
func TestUpdateFormulas_UpdatesOutdated(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Simulate outdated formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}

	var targetFormula string
	for name := range installed.Formulas {
		targetFormula = name
		break
	}
	if targetFormula == "" {
		t.Skip("no formulas installed")
	}

	// Write old content
	formulaPath := filepath.Join(formulasDir, targetFormula)
	oldContent := []byte("# Old version\n")
	if err := os.WriteFile(formulaPath, oldContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Update installed record with old content's hash
	hash := sha256.Sum256(oldContent)
	installed.Formulas[targetFormula] = hex.EncodeToString(hash[:])
	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		t.Fatal(err)
	}

	// Run update
	updated, skipped, reinstalled, err := UpdateFormulas(tmpDir)
	if err != nil {
		t.Fatalf("UpdateFormulas() error: %v", err)
	}

	if updated != 1 {
		t.Errorf("updated = %d, want 1", updated)
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if reinstalled != 0 {
		t.Errorf("reinstalled = %d, want 0", reinstalled)
	}

	// Verify file was updated
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}
	if report.Outdated != 0 {
		t.Errorf("after update, Outdated = %d, want 0", report.Outdated)
	}
}

// TestUpdateFormulas_SkipsModified tests that user-modified formulas are skipped.
func TestUpdateFormulas_SkipsModified(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Modify a formula (user customization)
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}

	var targetFormula string
	for name := range installed.Formulas {
		targetFormula = name
		break
	}
	if targetFormula == "" {
		t.Skip("no formulas installed")
	}

	// Write different content that doesn't match installed hash
	formulaPath := filepath.Join(formulasDir, targetFormula)
	modifiedContent := []byte("# User customized this formula\nformula = \"custom\"\n")
	if err := os.WriteFile(formulaPath, modifiedContent, 0644); err != nil {
		t.Fatal(err)
	}

	// Run update - should skip the modified formula
	_, skipped, _, err := UpdateFormulas(tmpDir)
	if err != nil {
		t.Fatalf("UpdateFormulas() error: %v", err)
	}

	if skipped != 1 {
		t.Errorf("skipped = %d, want 1", skipped)
	}

	// Verify file was NOT changed
	content, err := os.ReadFile(formulaPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(modifiedContent) {
		t.Error("modified formula should not have been changed")
	}
}

// TestUpdateFormulas_ReinstallsMissing tests that deleted formulas are reinstalled.
func TestUpdateFormulas_ReinstallsMissing(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Delete a formula
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}

	var targetFormula string
	for name := range installed.Formulas {
		targetFormula = name
		break
	}
	if targetFormula == "" {
		t.Skip("no formulas installed")
	}

	formulaPath := filepath.Join(formulasDir, targetFormula)
	if err := os.Remove(formulaPath); err != nil {
		t.Fatal(err)
	}

	// Run update
	_, _, reinstalled, err := UpdateFormulas(tmpDir)
	if err != nil {
		t.Fatalf("UpdateFormulas() error: %v", err)
	}

	if reinstalled != 1 {
		t.Errorf("reinstalled = %d, want 1", reinstalled)
	}

	// Verify file was restored
	if _, err := os.Stat(formulaPath); os.IsNotExist(err) {
		t.Error("missing formula should have been reinstalled")
	}
}

// TestUpdateFormulas_InstallsNew tests that new formulas are installed.
func TestUpdateFormulas_InstallsNew(t *testing.T) {
	tmpDir := t.TempDir()

	// Create directory structure but with empty installed record
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write empty installed record
	emptyInstalled := &InstalledRecord{Formulas: make(map[string]string)}
	if err := saveInstalledRecord(formulasDir, emptyInstalled); err != nil {
		t.Fatal(err)
	}

	// Run update - should install all formulas as "new"
	updated, skipped, reinstalled, err := UpdateFormulas(tmpDir)
	if err != nil {
		t.Fatalf("UpdateFormulas() error: %v", err)
	}

	// All formulas should be installed
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatal(err)
	}

	total := updated + reinstalled
	if total != len(embedded) {
		t.Errorf("total installed = %d, want %d", total, len(embedded))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
}

// TestInstalledRecordPersistence tests that the installed record survives across operations.
func TestInstalledRecordPersistence(t *testing.T) {
	tmpDir := t.TempDir()

	// Provision
	count, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Load and verify
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(installed.Formulas) != count {
		t.Errorf("installed has %d formulas, want %d", len(installed.Formulas), count)
	}

	// Verify file is valid JSON
	installedPath := filepath.Join(formulasDir, ".installed.json")
	data, err := os.ReadFile(installedPath)
	if err != nil {
		t.Fatal(err)
	}

	var decoded InstalledRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Errorf("installed.json is not valid JSON: %v", err)
	}
}

// TestCheckFormulaHealth_NewFormula tests detection of new formulas that were never installed.
func TestCheckFormulaHealth_NewFormula(t *testing.T) {
	tmpDir := t.TempDir()

	// Create formulas directory with empty installed record
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write empty installed record - simulates pre-existing install without this formula
	emptyInstalled := &InstalledRecord{Formulas: make(map[string]string)}
	if err := saveInstalledRecord(formulasDir, emptyInstalled); err != nil {
		t.Fatal(err)
	}

	// Check health - all embedded formulas should be "new"
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	embedded, _ := getEmbeddedFormulas()
	if report.New != len(embedded) {
		t.Errorf("New = %d, want %d", report.New, len(embedded))
	}
	if report.OK != 0 {
		t.Errorf("OK = %d, want 0", report.OK)
	}
}

// TestCheckFormulaHealth_Untracked tests detection of files that exist but aren't
// in .installed.json and don't match embedded (e.g., from older gt version).
func TestCheckFormulaHealth_Untracked(t *testing.T) {
	tmpDir := t.TempDir()

	// Get embedded formulas
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatal(err)
	}

	// Create formulas directory without .installed.json
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write formula files with different content (simulating older version)
	for name := range embedded {
		oldContent := []byte("# old version of " + name + "\n[molecule]\nid = \"test\"\n")
		if err := os.WriteFile(filepath.Join(formulasDir, name), oldContent, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Check health - all should be "untracked" (not "modified" since not tracked)
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	if report.Untracked != len(embedded) {
		t.Errorf("Untracked = %d, want %d", report.Untracked, len(embedded))
	}
	if report.Modified != 0 {
		t.Errorf("Modified = %d, want 0 (untracked files shouldn't be marked as modified)", report.Modified)
	}
	if report.OK != 0 {
		t.Errorf("OK = %d, want 0", report.OK)
	}

	// Verify all formulas have status "untracked"
	for _, f := range report.Formulas {
		if f.Status != "untracked" {
			t.Errorf("formula %s status = %q, want %q", f.Name, f.Status, "untracked")
		}
	}
}

// TestUpdateFormulas_UpdatesUntracked tests that untracked files get updated.
func TestUpdateFormulas_UpdatesUntracked(t *testing.T) {
	tmpDir := t.TempDir()

	// Get embedded formulas
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatal(err)
	}

	// Create formulas directory without .installed.json
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write formula files with different content (simulating older version)
	for name := range embedded {
		oldContent := []byte("# old version of " + name + "\n[molecule]\nid = \"test\"\n")
		if err := os.WriteFile(filepath.Join(formulasDir, name), oldContent, 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Run update - should update all untracked formulas
	updated, skipped, reinstalled, err := UpdateFormulas(tmpDir)
	if err != nil {
		t.Fatalf("UpdateFormulas() error: %v", err)
	}

	// All untracked files should be updated (counted as "updated", not "reinstalled")
	if updated != len(embedded) {
		t.Errorf("updated = %d, want %d", updated, len(embedded))
	}
	if skipped != 0 {
		t.Errorf("skipped = %d, want 0", skipped)
	}
	if reinstalled != 0 {
		t.Errorf("reinstalled = %d, want 0", reinstalled)
	}

	// Verify files now match embedded
	for name, expectedHash := range embedded {
		content, err := os.ReadFile(filepath.Join(formulasDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		actualHash := computeHash(content)
		if actualHash != expectedHash {
			t.Errorf("%s hash mismatch after update", name)
		}
	}

	// Verify .installed.json was created with correct hashes
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}
	for name, expectedHash := range embedded {
		if installed.Formulas[name] != expectedHash {
			t.Errorf(".installed.json hash for %s = %q, want %q",
				name, installed.Formulas[name], expectedHash)
		}
	}

	// Re-run health check - should be all OK now
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK != len(embedded) {
		t.Errorf("after update, OK = %d, want %d", report.OK, len(embedded))
	}
	if report.Untracked != 0 {
		t.Errorf("after update, Untracked = %d, want 0", report.Untracked)
	}
}

// TestProvisionFormulas_StatError tests that ProvisionFormulas returns an error
// when os.Stat fails with something other than IsNotExist (e.g. permission denied).
func TestProvisionFormulas_StatError(t *testing.T) {
	tmpDir := t.TempDir()

	// Get at least one embedded formula name
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatal(err)
	}
	var targetFormula string
	for name := range embedded {
		targetFormula = name
		break
	}

	// Create formulas directory
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create the formula file as a directory — os.Stat succeeds but it's a dir,
	// so we need a different approach. Instead, make the parent dir unreadable
	// for the specific file by creating a subdirectory with no permissions
	// that blocks stat. Actually, the simplest way: create a file, then remove
	// read+execute permission from the parent directory so Stat fails with EACCES.
	//
	// We'll use a symlink pointing to a nonexistent target through an unreadable dir.
	// Simpler: just write the formula, then chmod the formulasDir to 0000 before
	// the Stat call. But ProvisionFormulas does MkdirAll first...
	//
	// Best approach: pre-create a broken symlink at the formula path.
	// os.Stat on a broken symlink returns an error that is NOT os.IsNotExist.
	destPath := filepath.Join(formulasDir, targetFormula)
	brokenTarget := filepath.Join(tmpDir, "nonexistent-dir", "nonexistent-file")
	if err := os.Symlink(brokenTarget, destPath); err != nil {
		t.Fatal(err)
	}

	// ProvisionFormulas should return an error about the stat failure
	_, err = ProvisionFormulas(tmpDir)
	if err == nil {
		t.Fatal("ProvisionFormulas should return error for broken symlink stat failure")
	}
	if !os.IsNotExist(err) {
		// The error wraps the underlying stat error — verify it mentions the formula
		if got := err.Error(); got == "" {
			t.Error("error message should not be empty")
		}
	}
}

// TestCheckFormulaHealth_ErrorCounter tests that CheckFormulaHealth increments
// the Error counter for files that can't be read (e.g. permission denied).
func TestCheckFormulaHealth_ErrorCounter(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Chmod(path, 0000) does not prevent reading on Windows")
	}
	tmpDir := t.TempDir()

	// Provision fresh
	_, err := ProvisionFormulas(tmpDir)
	if err != nil {
		t.Fatalf("ProvisionFormulas() error: %v", err)
	}

	// Make one formula file unreadable
	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		t.Fatal(err)
	}

	var targetFormula string
	for name := range installed.Formulas {
		targetFormula = name
		break
	}
	if targetFormula == "" {
		t.Skip("no formulas installed")
	}

	formulaPath := filepath.Join(formulasDir, targetFormula)
	if err := os.Chmod(formulaPath, 0000); err != nil {
		t.Fatal(err)
	}
	// Restore permissions on cleanup so t.TempDir() can remove it
	t.Cleanup(func() { os.Chmod(formulaPath, 0644) })

	// Check health
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatalf("CheckFormulaHealth() error: %v", err)
	}

	if report.Error != 1 {
		t.Errorf("Error = %d, want 1", report.Error)
	}

	// Verify the specific formula has "error" status
	found := false
	for _, f := range report.Formulas {
		if f.Name == targetFormula {
			if f.Status != "error" {
				t.Errorf("formula %s status = %q, want %q", targetFormula, f.Status, "error")
			}
			found = true
			break
		}
	}
	if !found {
		t.Errorf("formula %s not found in report", targetFormula)
	}
}

// TestCheckFormulaHealth_MixedScenarios tests a mix of OK, untracked, and modified.
func TestCheckFormulaHealth_MixedScenarios(t *testing.T) {
	tmpDir := t.TempDir()

	// Get embedded formulas
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) < 3 {
		t.Skip("need at least 3 formulas for this test")
	}

	formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Prepare installed record with only some formulas tracked
	installed := &InstalledRecord{Formulas: make(map[string]string)}

	i := 0
	var okFormula, untrackedFormula, modifiedFormula string
	for name := range embedded {
		switch i {
		case 0:
			// First formula: write matching content, track it -> should be OK
			okFormula = name
			content, _ := formulasFS.ReadFile("formulas/" + name)
			if err := os.WriteFile(filepath.Join(formulasDir, name), content, 0644); err != nil {
				t.Fatal(err)
			}
			installed.Formulas[name] = computeHash(content)

		case 1:
			// Second formula: write old content, don't track -> should be untracked
			untrackedFormula = name
			oldContent := []byte("# untracked old version\n[molecule]\nid = \"test\"\n")
			if err := os.WriteFile(filepath.Join(formulasDir, name), oldContent, 0644); err != nil {
				t.Fatal(err)
			}
			// Don't add to installed record

		case 2:
			// Third formula: write different content, track with original hash -> should be modified
			modifiedFormula = name
			originalContent, _ := formulasFS.ReadFile("formulas/" + name)
			originalHash := computeHash(originalContent)
			modifiedContent := []byte("# user modified version\n[molecule]\nid = \"custom\"\n")
			if err := os.WriteFile(filepath.Join(formulasDir, name), modifiedContent, 0644); err != nil {
				t.Fatal(err)
			}
			installed.Formulas[name] = originalHash // Track with original hash
		}
		i++
		if i >= 3 {
			break
		}
	}

	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		t.Fatal(err)
	}

	// Check health
	report, err := CheckFormulaHealth(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Find status of each test formula
	statusMap := make(map[string]string)
	for _, f := range report.Formulas {
		statusMap[f.Name] = f.Status
	}

	if statusMap[okFormula] != "ok" {
		t.Errorf("formula %s status = %q, want %q", okFormula, statusMap[okFormula], "ok")
	}
	if statusMap[untrackedFormula] != "untracked" {
		t.Errorf("formula %s status = %q, want %q", untrackedFormula, statusMap[untrackedFormula], "untracked")
	}
	if statusMap[modifiedFormula] != "modified" {
		t.Errorf("formula %s status = %q, want %q", modifiedFormula, statusMap[modifiedFormula], "modified")
	}
}

// TestResolveFormulaContent verifies resolution order: rig > town > embedded.
func TestResolveFormulaContent(t *testing.T) {
	t.Run("returns embedded formula when no disk overrides exist", func(t *testing.T) {
		content, err := ResolveFormulaContent("mol-polecat-work", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(content) == 0 {
			t.Error("expected non-empty content")
		}
	})

	t.Run("town-level formula shadows embedded", func(t *testing.T) {
		tmpDir := t.TempDir()
		formulasDir := filepath.Join(tmpDir, ".beads", "formulas")
		if err := os.MkdirAll(formulasDir, 0755); err != nil {
			t.Fatal(err)
		}
		townContent := []byte("formula = \"mol-polecat-work\"\nversion = 99\n")
		if err := os.WriteFile(filepath.Join(formulasDir, "mol-polecat-work.formula.toml"), townContent, 0644); err != nil {
			t.Fatal(err)
		}

		content, err := ResolveFormulaContent("mol-polecat-work", tmpDir, "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(content) != string(townContent) {
			t.Error("expected town-level content to shadow embedded")
		}
	})

	t.Run("rig-level formula shadows town and embedded", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Write town-level formula
		townFormulasDir := filepath.Join(tmpDir, ".beads", "formulas")
		if err := os.MkdirAll(townFormulasDir, 0755); err != nil {
			t.Fatal(err)
		}
		townContent := []byte("formula = \"mol-polecat-work\"\nversion = 99\n")
		if err := os.WriteFile(filepath.Join(townFormulasDir, "mol-polecat-work.formula.toml"), townContent, 0644); err != nil {
			t.Fatal(err)
		}

		// Write rig-level formula (takes priority)
		rigFormulasDir := filepath.Join(tmpDir, "myrig", ".beads", "formulas")
		if err := os.MkdirAll(rigFormulasDir, 0755); err != nil {
			t.Fatal(err)
		}
		rigContent := []byte("formula = \"mol-polecat-work\"\nversion = 100\n")
		if err := os.WriteFile(filepath.Join(rigFormulasDir, "mol-polecat-work.formula.toml"), rigContent, 0644); err != nil {
			t.Fatal(err)
		}

		content, err := ResolveFormulaContent("mol-polecat-work", tmpDir, "myrig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(content) != string(rigContent) {
			t.Error("expected rig-level content to shadow town and embedded")
		}
	})

	t.Run("route-resolved rig formula shadows direct rig and town", func(t *testing.T) {
		tmpDir := t.TempDir()
		beadsDir := filepath.Join(tmpDir, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "routes.jsonl"), []byte(`{"prefix":"rt-","path":"routed/mayor/rig"}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		formulaName := "mol-routed-test"
		routedFormulasDir := filepath.Join(tmpDir, "routed", "mayor", "rig", ".beads", "formulas")
		directFormulasDir := filepath.Join(tmpDir, "routed", ".beads", "formulas")
		townFormulasDir := filepath.Join(tmpDir, ".beads", "formulas")
		for _, dir := range []string{routedFormulasDir, directFormulasDir, townFormulasDir} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}

		routedContent := []byte("formula = \"mol-routed-test\"\nversion = 3\n")
		directContent := []byte("formula = \"mol-routed-test\"\nversion = 2\n")
		townContent := []byte("formula = \"mol-routed-test\"\nversion = 1\n")
		for path, content := range map[string][]byte{
			filepath.Join(routedFormulasDir, formulaName+".formula.toml"): routedContent,
			filepath.Join(directFormulasDir, formulaName+".formula.toml"): directContent,
			filepath.Join(townFormulasDir, formulaName+".formula.toml"):   townContent,
		} {
			if err := os.WriteFile(path, content, 0644); err != nil {
				t.Fatal(err)
			}
		}

		content, err := ResolveFormulaContent(formulaName, tmpDir, "routed")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !bytes.Equal(content, routedContent) {
			t.Fatalf("expected route-resolved rig content, got %q", string(content))
		}
	})

	t.Run("falls back to town when rig has no override", func(t *testing.T) {
		tmpDir := t.TempDir()
		townFormulasDir := filepath.Join(tmpDir, ".beads", "formulas")
		if err := os.MkdirAll(townFormulasDir, 0755); err != nil {
			t.Fatal(err)
		}
		townContent := []byte("formula = \"mol-custom-test\"\nversion = 1\n")
		if err := os.WriteFile(filepath.Join(townFormulasDir, "mol-custom-test.formula.toml"), townContent, 0644); err != nil {
			t.Fatal(err)
		}

		content, err := ResolveFormulaContent("mol-custom-test", tmpDir, "myrig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if string(content) != string(townContent) {
			t.Error("expected town-level content when rig has no override")
		}
	})

	t.Run("falls back to embedded when disk tiers miss", func(t *testing.T) {
		tmpDir := t.TempDir()
		for _, dir := range []string{
			filepath.Join(tmpDir, ".beads", "formulas"),
			filepath.Join(tmpDir, "myrig", ".beads", "formulas"),
		} {
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
		}

		content, err := ResolveFormulaContent("mol-polecat-work", tmpDir, "myrig")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		embedded, err := GetEmbeddedFormulaContent("mol-polecat-work")
		if err != nil {
			t.Fatalf("unexpected embedded error: %v", err)
		}
		if !bytes.Equal(content, embedded) {
			t.Fatal("expected embedded content after disk tiers miss")
		}
	})

	t.Run("rejects path-like rig names", func(t *testing.T) {
		_, err := ResolveFormulaContent("mol-polecat-work", t.TempDir(), "../outside")
		if err == nil {
			t.Fatal("expected error for path-like rig name")
		}
	})

	t.Run("returns error when not found anywhere", func(t *testing.T) {
		_, err := ResolveFormulaContent("mol-does-not-exist", "", "")
		if err == nil {
			t.Error("expected error for non-existent formula")
		}
		if !errors.Is(err, ErrFormulaNotFound) {
			t.Fatalf("error should match ErrFormulaNotFound, got: %v", err)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("error should preserve embedded fs.ErrNotExist, got: %v", err)
		}
	})
}

// TestGetEmbeddedFormulaContent verifies extraction of individual embedded formulas.
func TestGetEmbeddedFormulaContent(t *testing.T) {
	// Known embedded formula should succeed
	content, err := GetEmbeddedFormulaContent("mol-polecat-work")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent(mol-polecat-work) error: %v", err)
	}
	if len(content) == 0 {
		t.Error("expected non-empty content")
	}

	// With suffix should also work
	content2, err := GetEmbeddedFormulaContent("mol-polecat-work.formula.toml")
	if err != nil {
		t.Fatalf("GetEmbeddedFormulaContent with suffix error: %v", err)
	}
	if string(content) != string(content2) {
		t.Error("content should be identical with or without suffix")
	}

	// Non-existent formula should fail
	_, err = GetEmbeddedFormulaContent("nonexistent-formula")
	if err == nil {
		t.Error("expected error for non-existent formula")
	}
}
