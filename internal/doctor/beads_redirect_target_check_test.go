package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBeadsRedirectTargetCheck_ValidTarget(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig with valid beads setup
	if err := os.MkdirAll(filepath.Join(rigBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect pointing to valid target
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK for valid redirect target, got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsRedirectTargetCheck_TargetDoesNotExist(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig structure but NO rig .beads directory
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect pointing to non-existent target
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for missing target, got %v: %s", result.Status, result.Message)
	}
	if len(result.Details) == 0 {
		t.Error("Expected details about broken target")
	}
}

func TestBeadsRedirectTargetCheck_TargetNoBeadsSetup(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig beads dir but with no dolt/, redirect, or config.yaml
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect pointing to empty beads dir
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for target with no beads setup, got %v: %s", result.Status, result.Message)
	}

	// Verify details mention "no beads setup"
	found := false
	for _, detail := range result.Details {
		if strings.Contains(detail, "no beads setup") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected detail mentioning 'no beads setup', got: %v", result.Details)
	}
}

func TestBeadsRedirectTargetCheck_PolecatBrokenTarget(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	polecatDir := filepath.Join(rigDir, "polecats", "polecat1")
	polecatBeadsDir := filepath.Join(polecatDir, ".beads")

	// Create rig structure but NO beads
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create polecat with .git (old flat structure) and redirect to non-existent target
	if err := os.MkdirAll(polecatBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatDir, ".git"), []byte("gitdir: /fake\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(polecatBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for polecat broken target, got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsRedirectTargetCheck_RefineryBrokenTarget(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	refineryDir := filepath.Join(rigDir, "refinery", "rig")
	refineryBeadsDir := filepath.Join(refineryDir, ".beads")

	// Create rig structure but NO beads
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create refinery with redirect to non-existent target
	if err := os.MkdirAll(refineryBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(refineryBeadsDir, "redirect"), []byte("../../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning for refinery broken target, got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsRedirectTargetCheck_NoRedirectFile(t *testing.T) {
	// Worktrees without redirect files should not be flagged (handled by other check)
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	crewDir := filepath.Join(rigDir, "crew", "worker1")

	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK for worktree without redirect, got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsRedirectTargetCheck_FixRecomputesRedirect(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig with valid beads setup (will be the fix target)
	if err := os.MkdirAll(filepath.Join(rigBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect pointing to wrong/broken path
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../nonexistent/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning before fix, got %v: %s", result.Status, result.Message)
	}

	// Fix
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify redirect was rewritten
	data, err := os.ReadFile(filepath.Join(crewBeadsDir, "redirect"))
	if err != nil {
		t.Fatalf("Redirect file missing after fix: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content != "../../.beads" {
		t.Errorf("Expected redirect to '../../.beads', got %q", content)
	}

	// Run again to verify clean
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK after fix, got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsRedirectTargetCheck_FixWithMayorBeads(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	mayorBeadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create mayor beads as canonical location
	if err := os.MkdirAll(filepath.Join(mayorBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect to non-existent rig .beads
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect (rig/.beads doesn't exist)
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning before fix, got %v: %s", result.Status, result.Message)
	}

	// Fix should redirect to mayor/rig/.beads
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify redirect now points to mayor/rig/.beads
	data, err := os.ReadFile(filepath.Join(crewBeadsDir, "redirect"))
	if err != nil {
		t.Fatalf("Redirect file missing after fix: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content != "../../mayor/rig/.beads" {
		t.Errorf("Expected redirect to '../../mayor/rig/.beads', got %q", content)
	}
}

func TestBeadsRedirectTargetCheck_FixUnfixable(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig structure but NO beads anywhere
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect to non-existent target
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning, got %v", result.Status)
	}

	// Fix should fail because no canonical beads location exists
	err := check.Fix(ctx)
	if err == nil {
		t.Error("Expected Fix to fail when no canonical beads exists")
	}
}

// Tracked beads architecture: rig/.beads redirects to mayor/rig/.beads. A
// worktree redirect that stops at rig/.beads forms a CHAIN, and bd does not
// follow chains — it prints "redirect chains not allowed, ignoring redirect in
// <rig>/.beads" and resolves against the intermediate directory, so lookups miss
// the real database ("issue not found" from a polecat worktree). The check must
// flag it and the fix must collapse it to the final destination (hq-szhze).
func TestBeadsRedirectTargetCheck_MayorRedirectChain(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	mayorBeadsDir := filepath.Join(rigDir, "mayor", "rig", ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create mayor beads (final canonical)
	if err := os.MkdirAll(filepath.Join(mayorBeadsDir, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create rig beads with redirect to mayor
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect to rig beads, which itself redirects to mayor.
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Fatalf("Expected StatusWarning for redirect chain, got %v: %s (details: %v)",
			result.Status, result.Message, result.Details)
	}
	if len(result.Details) != 1 || !strings.Contains(result.Details[0], "does not follow chains") {
		t.Errorf("Expected chain diagnosis in details, got %v", result.Details)
	}

	// Fix must rewrite the crew redirect to name mayor/rig/.beads directly.
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(crewBeadsDir, "redirect"))
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(string(data))
	if want := "../../mayor/rig/.beads"; got != want {
		t.Errorf("redirect after fix = %q, want %q", got, want)
	}

	// And the check must now pass: one hop, no further redirect.
	if result := check.Run(ctx); result.Status != StatusOK {
		t.Errorf("Expected StatusOK after fix, got %v: %s (details: %v)",
			result.Status, result.Message, result.Details)
	}
}

func TestBeadsRedirectTargetCheck_MultipleWorktrees(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")

	// Create rig but NO beads
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create multiple worktrees with broken redirects
	worktrees := []string{
		filepath.Join(rigDir, "crew", "worker1"),
		filepath.Join(rigDir, "crew", "worker2"),
		filepath.Join(rigDir, "polecats", "polecat1"),
	}

	for _, wt := range worktrees {
		beadsDir := filepath.Join(wt, ".beads")
		if err := os.MkdirAll(beadsDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(beadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Mark polecat as old-style flat worktree (has .git)
	if err := os.WriteFile(filepath.Join(rigDir, "polecats", "polecat1", ".git"), []byte("gitdir: /fake\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Expected StatusWarning, got %v: %s", result.Status, result.Message)
	}
	if len(result.Details) != 3 {
		t.Errorf("Expected 3 broken targets, got %d: %v", len(result.Details), result.Details)
	}
}

func TestBeadsRedirectTargetCheck_ValidConfigYaml(t *testing.T) {
	// Target with config.yaml should be considered valid
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig beads with only config.yaml (no dolt)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "config.yaml"), []byte("prefix: gt-\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK for target with config.yaml, got %v: %s", result.Status, result.Message)
	}
}

func TestBeadsRedirectTargetCheck_AbsolutePathRedirect(t *testing.T) {
	// Redirect files containing absolute paths should resolve correctly
	// without path-doubling (filepath.Join(wt, absPath) bug).
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create an absolute target with valid beads setup
	absTarget := filepath.Join(t.TempDir(), "canonical", ".beads")
	if err := os.MkdirAll(filepath.Join(absTarget, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with absolute-path redirect
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte(absTarget+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK for valid absolute redirect, got %v: %s (details: %v)",
			result.Status, result.Message, result.Details)
	}
}

func TestExtractRigName(t *testing.T) {
	tests := []struct {
		name         string
		townRoot     string
		worktreePath string
		want         string
	}{
		{
			name:         "normal crew path",
			townRoot:     "/town",
			worktreePath: "/town/myrig/crew/worker1",
			want:         "myrig",
		},
		{
			name:         "refinery path",
			townRoot:     "/town",
			worktreePath: "/town/myrig/refinery/rig",
			want:         "myrig",
		},
		{
			name:         "polecat path",
			townRoot:     "/town",
			worktreePath: "/town/myrig/polecats/polecat1",
			want:         "myrig",
		},
		{
			name:         "single component (rig root itself)",
			townRoot:     "/town",
			worktreePath: "/town/myrig",
			want:         "myrig",
		},
		{
			name:         "deeply nested path",
			townRoot:     "/town",
			worktreePath: "/town/myrig/a/b/c/d",
			want:         "myrig",
		},
		{
			name:         "worktree equals town root",
			townRoot:     "/town",
			worktreePath: "/town",
			want:         ".",
		},
		{
			name:         "unreachable relative path",
			townRoot:     "/other",
			worktreePath: "/town/myrig",
			want:         "..",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractRigName(tt.townRoot, tt.worktreePath)
			if got != tt.want {
				t.Errorf("extractRigName(%q, %q) = %q, want %q",
					tt.townRoot, tt.worktreePath, got, tt.want)
			}
		})
	}
}

func TestBeadsRedirectTargetCheck_FixWithMissingConfigYaml(t *testing.T) {
	// Target directory exists with metadata.json but no config.yaml/dolt/redirect.
	// Fix should create config.yaml from metadata defaults.
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig beads with only metadata.json (no config.yaml, no dolt/, no redirect)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadataJSON := `{"issue_prefix": "test", "dolt_database": "beads_test"}`
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "metadata.json"), []byte(metadataJSON), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with redirect to the rig beads dir (which has no beads setup yet)
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run should detect "no beads setup"
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Expected StatusWarning before fix, got %v: %s", result.Status, result.Message)
	}

	// Fix should create config.yaml from metadata.json
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify config.yaml was created
	configData, err := os.ReadFile(filepath.Join(rigBeadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml not created by fix: %v", err)
	}
	content := string(configData)
	if !strings.Contains(content, "prefix: test") {
		t.Errorf("Expected config.yaml to contain 'prefix: test', got: %s", content)
	}

	// Run again — should now be clean
	check2 := NewBeadsRedirectTargetCheck()
	result = check2.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Expected StatusOK after fix, got %v: %s (details: %v)",
			result.Status, result.Message, result.Details)
	}
}

func TestBeadsRedirectTargetCheck_FixMetadataRepairFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not enforce POSIX directory write bits for this chmod-based failure path")
	}

	// Target directory exists but metadata repair can't fix it (no metadata.json,
	// and the directory is writable so EnsureConfigYAML succeeds but with empty
	// fallback prefix — the important thing is the fallback path works).
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create rig beads dir that is not writable (so config.yaml creation fails)
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	// Make beads dir read-only so EnsureConfigYAMLFromMetadataIfMissing fails
	if err := os.Chmod(rigBeadsDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(rigBeadsDir, 0755) })

	// Create crew with redirect to the unwritable beads dir
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run should detect "no beads setup"
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Expected StatusWarning, got %v: %s", result.Status, result.Message)
	}

	// Fix should fail because: metadata repair fails (read-only dir) and
	// no canonical beads with dolt/redirect/config.yaml exists
	err := check.Fix(ctx)
	if err == nil {
		t.Error("Expected Fix to fail when metadata repair fails and no canonical beads exists")
	}
}

func TestBeadsRedirectTargetCheck_FixWithAbsoluteRigRedirect(t *testing.T) {
	// When rig/.beads/redirect contains an absolute path, the doctor fix
	// (recomputeRedirect) should pass it through as-is, not prepend upPath.
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "myrig")
	rigBeadsDir := filepath.Join(rigDir, ".beads")
	crewDir := filepath.Join(rigDir, "crew", "worker1")
	crewBeadsDir := filepath.Join(crewDir, ".beads")

	// Create an absolute canonical beads location
	absTarget := filepath.Join(t.TempDir(), "canonical", ".beads")
	if err := os.MkdirAll(filepath.Join(absTarget, "dolt"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create rig beads with absolute redirect to canonical location
	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte(absTarget+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rigDir, ".git"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create crew with a broken redirect
	if err := os.MkdirAll(crewBeadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(crewBeadsDir, "redirect"), []byte("../../nonexistent/.beads\n"), 0644); err != nil {
		t.Fatal(err)
	}

	check := NewBeadsRedirectTargetCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	// Run to detect
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Expected StatusWarning before fix, got %v: %s", result.Status, result.Message)
	}

	// Fix should rewrite redirect using the absolute rig redirect target
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	// Verify redirect is the absolute path (not "../../" + absolute path)
	data, err := os.ReadFile(filepath.Join(crewBeadsDir, "redirect"))
	if err != nil {
		t.Fatalf("Redirect file missing after fix: %v", err)
	}
	content := strings.TrimSpace(string(data))
	if content != absTarget {
		t.Errorf("Expected redirect to %q (absolute), got %q", absTarget, content)
	}
}
