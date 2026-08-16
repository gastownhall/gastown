package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jonbaldie/gastown/internal/templates"
)

func TestTownIdentity(t *testing.T) {
	content := templates.TownIdentity()

	// Must contain the Gas Town header
	if content == "" {
		t.Fatal("templates.TownIdentity returned empty string")
	}
	if content[0:10] != "# Gas Town" {
		t.Errorf("expected content to start with '# Gas Town', got: %q", content[:10])
	}

	// Must contain identity anchoring instructions
	if !contains(content, "Do NOT adopt an identity") {
		t.Error("identity text should contain identity anchoring warning")
	}
	if !contains(content, "GT_ROLE") {
		t.Error("identity text should reference GT_ROLE environment variable")
	}
}

func TestUpgradeAgentsMD_CreatesMissingFile(t *testing.T) {
	tmpDir := t.TempDir()

	upgradeDryRun = false
	upgradeVerbose = false

	result := upgradeAgentsMD(tmpDir)

	if runtime.GOOS == "windows" {
		if result.changed < 1 {
			t.Errorf("expected at least 1 change for new AGENTS.md, got %d", result.changed)
		}
	} else if result.changed != 2 {
		t.Errorf("expected 2 changes for new AGENTS.md + CLAUDE.md symlink, got %d", result.changed)
	}

	agentsPath := filepath.Join(tmpDir, "AGENTS.md")
	data, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("AGENTS.md not created: %v", err)
	}

	expected := templates.TownIdentity()
	if string(data) != expected {
		t.Error("AGENTS.md content doesn't match expected template")
	}

	info, err := os.Lstat(agentsPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("AGENTS.md should be a regular file")
	}

	if runtime.GOOS != "windows" {
		target, err := os.Readlink(filepath.Join(tmpDir, "CLAUDE.md"))
		if err != nil {
			t.Fatalf("CLAUDE.md symlink not created: %v", err)
		}
		if target != "AGENTS.md" {
			t.Errorf("CLAUDE.md symlink target = %q, want %q", target, "AGENTS.md")
		}
	}
}

func TestUpgradeAgentsMD_UpToDate(t *testing.T) {
	tmpDir := t.TempDir()

	expected := templates.TownIdentity()
	if err := os.WriteFile(filepath.Join(tmpDir, "AGENTS.md"), []byte(expected), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("AGENTS.md", filepath.Join(tmpDir, "CLAUDE.md")); err != nil {
		t.Fatal(err)
	}

	upgradeDryRun = false
	upgradeVerbose = false

	result := upgradeAgentsMD(tmpDir)

	if result.changed != 0 {
		t.Errorf("expected 0 changes for up-to-date pair, got %d", result.changed)
	}
}

func TestUpgradeAgentsMD_FlipsOldSymlinkDirection(t *testing.T) {
	tmpDir := t.TempDir()
	expected := templates.TownIdentity()
	if err := os.WriteFile(filepath.Join(tmpDir, "CLAUDE.md"), []byte(expected), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("CLAUDE.md", filepath.Join(tmpDir, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}

	upgradeDryRun = false
	upgradeVerbose = false
	result := upgradeAgentsMD(tmpDir)
	if result.changed == 0 {
		t.Fatal("expected pair flip to report a change")
	}

	info, err := os.Lstat(filepath.Join(tmpDir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("AGENTS.md should be a regular file after flip")
	}
	target, err := os.Readlink(filepath.Join(tmpDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md should be a symlink: %v", err)
	}
	if target != "AGENTS.md" {
		t.Errorf("CLAUDE.md target = %q, want AGENTS.md", target)
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != expected {
		t.Error("identity text was lost during flip")
	}
}

func TestCreateTownRootAgentMDs_WritesAgentsCanonical(t *testing.T) {
	tmpDir := t.TempDir()
	created, err := createTownRootAgentMDs(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected town-root pair to be created")
	}
	got, err := os.ReadFile(filepath.Join(tmpDir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != templates.TownIdentity() {
		t.Error("AGENTS.md missing identity text")
	}
	target, err := os.Readlink(filepath.Join(tmpDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md should be a symlink: %v", err)
	}
	if target != "AGENTS.md" {
		t.Errorf("CLAUDE.md target = %q, want AGENTS.md", target)
	}
}

func TestUpgradeAgentsMD_DryRun(t *testing.T) {
	tmpDir := t.TempDir()

	upgradeDryRun = true
	upgradeVerbose = false

	result := upgradeAgentsMD(tmpDir)

	if result.changed < 1 {
		t.Errorf("expected at least 1 change in dry-run mode, got %d", result.changed)
	}

	// Verify file was NOT created
	claudePath := filepath.Join(tmpDir, "CLAUDE.md")
	if _, err := os.Stat(claudePath); !os.IsNotExist(err) {
		t.Error("dry-run should not create CLAUDE.md")
	}

	// Reset
	upgradeDryRun = false
}

func TestUpgradeDaemonConfig_CreatesMissing(t *testing.T) {
	tmpDir := t.TempDir()

	// Create mayor directory (required by DaemonPatrolConfigPath)
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	upgradeDryRun = false
	upgradeVerbose = false

	result := upgradeDaemonConfig(tmpDir)

	if result.changed != 1 {
		t.Errorf("expected 1 change for new daemon.json, got %d", result.changed)
	}

	// Verify file exists
	daemonPath := filepath.Join(mayorDir, "daemon.json")
	if _, err := os.Stat(daemonPath); err != nil {
		t.Errorf("daemon.json not created: %v", err)
	}
}

func TestUpgradeDaemonConfig_ExistingValid(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create a valid daemon.json
	daemonPath := filepath.Join(mayorDir, "daemon.json")
	content := `{
		"type": "daemon-patrol-config",
		"version": 1,
		"heartbeat": {"enabled": true, "interval": "3m"},
		"patrols": {}
	}`
	if err := os.WriteFile(daemonPath, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	upgradeDryRun = false
	upgradeVerbose = false

	result := upgradeDaemonConfig(tmpDir)

	if result.changed != 0 {
		t.Errorf("expected 0 changes for existing daemon.json, got %d", result.changed)
	}
}

func TestUpgradeCommandRegistered(t *testing.T) {
	// Verify the upgrade command is registered in rootCmd
	found := false
	for _, cmd := range rootCmd.Commands() {
		if cmd.Name() == "upgrade" {
			found = true
			break
		}
	}
	if !found {
		t.Error("upgrade command not registered with rootCmd")
	}
}

func TestUpgradeBeadsExempt(t *testing.T) {
	if !beadsExemptCommands["upgrade"] {
		t.Error("upgrade should be in beadsExemptCommands")
	}
}

func TestUpgradeBranchCheckExempt(t *testing.T) {
	if !branchCheckExemptCommands["upgrade"] {
		t.Error("upgrade should be in branchCheckExemptCommands")
	}
}

// contains is already declared in mq_test.go in this package,
// so we reuse it here.
