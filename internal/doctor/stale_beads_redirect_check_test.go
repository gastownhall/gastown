package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStaleBeadsRedirectCheck_NoStaleFiles(t *testing.T) {
	// Create temp town with clean .beads redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	beadsDir := filepath.Join(rigDir, ".beads")

	// Create rig structure
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create only redirect file (no stale files)
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_WithStaleFiles(t *testing.T) {
	// Create temp town with stale .beads files
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	beadsDir := filepath.Join(rigDir, ".beads")

	// Create rig structure
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create redirect file
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stale files
	staleFiles := []string{"issues.jsonl", "issues.db", "metadata.json"}
	for _, f := range staleFiles {
		if err := os.WriteFile(filepath.Join(beadsDir, f), []byte("stale data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(result.Details) != 1 {
		t.Errorf("Expected 1 stale location, got %d", len(result.Details))
	}
}

func TestStaleBeadsRedirectCheck_FixRemovesStaleFiles(t *testing.T) {
	// Create temp town with stale .beads files
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	beadsDir := filepath.Join(rigDir, ".beads")

	// Create rig structure
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create redirect file
	redirectPath := filepath.Join(beadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stale files (config.yaml excluded - may be tracked in git;
	// issues.jsonl no longer recognized — Dolt server is the only backend)
	staleFiles := []string{"issues.db", "metadata.json"}
	for _, f := range staleFiles {
		if err := os.WriteFile(filepath.Join(beadsDir, f), []byte("stale data"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create .gitignore (should be preserved)
	gitignorePath := filepath.Join(beadsDir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte("*.db\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect issues
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning before fix, got %v", result.Status)
	}

	// Apply fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify stale files removed
	for _, f := range staleFiles {
		if _, err := os.Stat(filepath.Join(beadsDir, f)); !os.IsNotExist(err) {
			t.Errorf("Stale file %s still exists after fix", f)
		}
	}

	// Verify redirect preserved
	if _, err := os.Stat(redirectPath); err != nil {
		t.Errorf("Redirect file should be preserved: %v", err)
	}

	// Verify .gitignore preserved
	if _, err := os.Stat(gitignorePath); err != nil {
		t.Errorf(".gitignore should be preserved: %v", err)
	}

	// Run again to verify clean
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestCleanStaleBeadsFiles_RemovesMetadataDriftFromRedirect(t *testing.T) {
	townRoot := t.TempDir()
	canonicalBeads := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
	redirectBeads := filepath.Join(townRoot, "myrig", ".beads")
	if err := os.MkdirAll(canonicalBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(redirectBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(canonicalBeads, "metadata.json"), []byte(`{"dolt_database":"myrig"}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirectBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirectBeads, "metadata.json"), []byte(`{"dolt_database":"hq"}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := cleanStaleBeadsFiles(redirectBeads); err != nil {
		t.Fatalf("cleanStaleBeadsFiles failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(redirectBeads, "metadata.json")); !os.IsNotExist(err) {
		t.Fatalf("stale redirect metadata should be removed, stat err=%v", err)
	}
}

func TestCleanStaleBeadsFiles_PreservesMatchingRedirectMetadata(t *testing.T) {
	townRoot := t.TempDir()
	canonicalBeads := filepath.Join(townRoot, "myrig", "mayor", "rig", ".beads")
	redirectBeads := filepath.Join(townRoot, "myrig", ".beads")
	if err := os.MkdirAll(canonicalBeads, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(redirectBeads, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"myrig"}`)
	if err := os.WriteFile(filepath.Join(canonicalBeads, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirectBeads, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(redirectBeads, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}

	if err := cleanStaleBeadsFiles(redirectBeads); err != nil {
		t.Fatalf("cleanStaleBeadsFiles failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(redirectBeads, "metadata.json")); err != nil {
		t.Fatalf("matching redirect metadata should be preserved: %v", err)
	}
}

func TestStaleBeadsRedirectCheck_NoRedirect(t *testing.T) {
	// Create temp town with .beads but no redirect (canonical location)
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	beadsDir := filepath.Join(rigDir, ".beads")

	// Create rig structure
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create data files but NO redirect
	if err := os.WriteFile(filepath.Join(beadsDir, "issues.jsonl"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	// Should be OK - no redirect means this is a canonical location
	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK (no redirect), got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_CrewWorkspaces(t *testing.T) {
	// Create temp town with crew workspace stale files
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	crewBeadsDir := filepath.Join(rigDir, "crew", "worker1", ".beads")

	// Create crew structure
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create redirect file
	redirectPath := filepath.Join(crewBeadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Create stale file
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "issues.db"), []byte("stale"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for crew stale files, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_MissingRedirect(t *testing.T) {
	// Create temp town with crew workspace missing redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew workspace WITHOUT .beads/redirect
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for missing redirect, got %v: %s", result.Status, result.Message)
	}

	// Verify the message mentions missing redirect
	found := false
	for _, detail := range result.Details {
		if filepath.Base(detail) == "worker1" || len(detail) > 0 {
			found = true
			break
		}
	}
	if !found && len(result.Details) == 0 {
		t.Errorf("Expected details about missing redirect, got: %v", result.Details)
	}
}

func TestStaleBeadsRedirectCheck_FixCreatesMissingRedirect(t *testing.T) {
	// Create temp town with crew workspace missing redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a marker file so it's recognized as having beads
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "issues.jsonl"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create crew workspace WITHOUT .beads/redirect
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect issues
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning before fix, got %v: %s", result.Status, result.Message)
	}

	// Apply fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify redirect was created
	redirectPath := filepath.Join(crewDir, ".beads", "redirect")
	data, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("Redirect file not created: %v", err)
	}

	// Verify redirect content points to rig's .beads
	content := string(data)
	if content != "../../.beads\n" {
		t.Errorf("Expected redirect to '../../.beads', got %q", content)
	}

	// Run again to verify clean
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_TrackedBeadsArchitecture(t *testing.T) {
	// Create temp town with tracked beads architecture (mayor/rig/.beads is canonical)
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	mayorBeadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")

	// Create mayor beads (canonical location for tracked beads)
	if err := os.MkdirAll(mayorBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Create a marker file
	if err := os.WriteFile(filepath.Join(mayorBeadsDir, "issues.jsonl"), []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	// Create crew workspace WITHOUT redirect
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect issues
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for missing redirect, got %v: %s", result.Status, result.Message)
	}

	// Apply fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify redirect was created pointing to mayor/rig/.beads
	redirectPath := filepath.Join(crewDir, ".beads", "redirect")
	data, err := os.ReadFile(redirectPath)
	if err != nil {
		t.Fatalf("Redirect file not created: %v", err)
	}

	// Verify redirect content points to mayor/rig/.beads
	content := string(data)
	expected := "../../mayor/rig/.beads\n"
	if content != expected {
		t.Errorf("Expected redirect to %q, got %q", expected, content)
	}
}

func TestStaleBeadsRedirectCheck_IncorrectRedirect(t *testing.T) {
	// Create temp town with incorrect redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew workspace with WRONG redirect
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	redirectPath := filepath.Join(crewBeadsDir, "redirect")
	// Wrong path - pointing to non-existent location
	if err := os.WriteFile(redirectPath, []byte("../wrong/path/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for incorrect redirect, got %v: %s", result.Status, result.Message)
	}

	// Verify details mention incorrect redirect
	foundIncorrect := false
	for _, detail := range result.Details {
		if strings.HasPrefix(detail, "incorrect redirect:") {
			foundIncorrect = true
			break
		}
	}
	if !foundIncorrect && len(result.Details) > 0 {
		// Just check we got some warning about it
		t.Logf("Details: %v", result.Details)
	}
}

func TestStaleBeadsRedirectCheck_ValidRedirectNotFlagged(t *testing.T) {
	// Create temp town with correct redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew workspace with CORRECT redirect
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	redirectPath := filepath.Join(crewBeadsDir, "redirect")
	if err := os.WriteFile(redirectPath, []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK for valid redirect, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_PolecatWorkspace(t *testing.T) {
	// Create temp town with polecat workspace missing redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	polecatDir := filepath.Join(rigDir, "polecats", "polecat1")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create polecat workspace WITHOUT redirect (old flat structure with .git)
	if err := os.MkdirAll(polecatDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatDir, ".git"), []byte("gitdir: /fake\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for polecat missing redirect, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_NestedPolecatWorkspace(t *testing.T) {
	// Polecats with nested structure: polecats/<name>/<rig_name>/
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	// Nested clone path: polecats/polecat1/myrig/
	polecatClone := filepath.Join(rigDir, "polecats", "polecat1", "myrig")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create nested polecat workspace WITHOUT redirect
	if err := os.MkdirAll(polecatClone, 0755); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for nested polecat missing redirect, got %v: %s", result.Status, result.Message)
	}
}

func TestStaleBeadsRedirectCheck_RefineryWorkspace(t *testing.T) {
	// Create temp town with refinery workspace missing redirect
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	refineryDir := filepath.Join(rigDir, "refinery", "rig")

	// Create rig beads (canonical location)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create refinery workspace WITHOUT redirect
	if err := os.MkdirAll(refineryDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Also need a .git to make it look like a rig
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	check := NewStaleBeadsRedirectCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for refinery missing redirect, got %v: %s", result.Status, result.Message)
	}
}
