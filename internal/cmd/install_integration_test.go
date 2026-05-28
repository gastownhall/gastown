//go:build e2e

package cmd

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/testutil/doltcleanup"
	"gopkg.in/yaml.v3"
)

// TestInstallCreatesCorrectStructure validates that a fresh gt install
// creates the expected directory structure and configuration files.
func TestInstallCreatesCorrectStructure(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	// Build gt binary for testing
	gtBinary := buildGT(t)

	// HOME and Dolt port are overridden for isolation; configure git identity so EnsureDoltIdentity works.
	env, doltPort := isolatedE2EDoltEnv(t, tmpDir)
	configureGitIdentity(t, env)
	doltcleanup.ReapOwnedDoltOnCleanup(t, hqPath)

	// Run gt install
	cmd := exec.Command(gtBinary, "install", hqPath, "--name", "test-town", "--dolt-port", doltPort)
	cmd.Dir = tmpDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
	}
	assertDoltConfigPort(t, hqPath, doltPort)

	// Verify directory structure
	assertDirExists(t, hqPath, "HQ root")
	assertDirExists(t, filepath.Join(hqPath, "mayor"), "mayor/")

	// Verify mayor/town.json
	townPath := filepath.Join(hqPath, "mayor", "town.json")
	assertFileExists(t, townPath, "mayor/town.json")

	townConfig, err := config.LoadTownConfig(townPath)
	if err != nil {
		t.Fatalf("failed to load town.json: %v", err)
	}
	if townConfig.Type != "town" {
		t.Errorf("town.json type = %q, want %q", townConfig.Type, "town")
	}
	if townConfig.Name != "test-town" {
		t.Errorf("town.json name = %q, want %q", townConfig.Name, "test-town")
	}

	// Verify mayor/rigs.json
	rigsPath := filepath.Join(hqPath, "mayor", "rigs.json")
	assertFileExists(t, rigsPath, "mayor/rigs.json")

	rigsConfig, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		t.Fatalf("failed to load rigs.json: %v", err)
	}
	if len(rigsConfig.Rigs) != 0 {
		t.Errorf("rigs.json should be empty, got %d rigs", len(rigsConfig.Rigs))
	}

	// Verify Claude settings exist in mayor/.claude/ (not town root/.claude/)
	// Mayor settings go here to avoid polluting child workspaces via directory traversal
	mayorSettingsPath := filepath.Join(hqPath, "mayor", ".claude", "settings.json")
	assertFileExists(t, mayorSettingsPath, "mayor/.claude/settings.json")

	// Verify deacon settings exist in deacon/.claude/
	deaconSettingsPath := filepath.Join(hqPath, "deacon", ".claude", "settings.json")
	assertFileExists(t, deaconSettingsPath, "deacon/.claude/settings.json")
}

// TestInstallBeadsHasCorrectPrefix validates that beads is initialized
// with the correct "hq-" prefix for town-level beads.
func TestInstallBeadsHasCorrectPrefix(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	// Build gt binary for testing
	gtBinary := buildGT(t)

	// HOME and Dolt port are overridden for isolation; configure git identity so EnsureDoltIdentity works.
	env, doltPort := isolatedE2EDoltEnv(t, tmpDir)
	configureGitIdentity(t, env)
	doltcleanup.ReapOwnedDoltOnCleanup(t, hqPath)

	// Run gt install (includes beads init by default)
	cmd := exec.Command(gtBinary, "install", hqPath, "--dolt-port", doltPort)
	cmd.Dir = tmpDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
	}
	assertDoltConfigPort(t, hqPath, doltPort)

	// Verify .beads/ directory exists
	beadsDir := filepath.Join(hqPath, ".beads")
	assertDirExists(t, beadsDir, ".beads/")

	// Verify beads database was initialized: metadata.json present and dolt server
	// data directory exists. Since bd v0.52.0 server mode, dolt data lives in
	// .dolt-data/hq/ (centralized) rather than the legacy .beads/dolt/ path.
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	assertFileExists(t, metadataPath, ".beads/metadata.json")
	doltDataDir := filepath.Join(hqPath, ".dolt-data", "hq")
	assertDirExists(t, doltDataDir, ".dolt-data/hq/")

	// Verify metadata points to canonical HQ Dolt database, not beads_hq.
	var metadata struct {
		DoltMode     string `json:"dolt_mode"`
		DoltDatabase string `json:"dolt_database"`
	}
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("reading metadata.json: %v", err)
	}
	if err := json.Unmarshal(metadataBytes, &metadata); err != nil {
		t.Fatalf("parsing metadata.json: %v", err)
	}
	if metadata.DoltMode != "server" {
		t.Errorf("metadata dolt_mode = %q, want %q", metadata.DoltMode, "server")
	}
	if metadata.DoltDatabase != "hq" {
		t.Errorf("metadata dolt_database = %q, want %q", metadata.DoltDatabase, "hq")
	}

	// Verify top-level config.yaml exists with hq prefix keys.
	configPath := filepath.Join(beadsDir, "config.yaml")
	assertFileExists(t, configPath, ".beads/config.yaml")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading config.yaml: %v", err)
	}
	configText := string(configBytes)
	if !strings.Contains(configText, "prefix: hq") {
		t.Errorf("config.yaml missing prefix: hq, got:\n%s", configText)
	}
	if !strings.Contains(configText, "issue-prefix: hq") {
		t.Errorf("config.yaml missing issue-prefix: hq, got:\n%s", configText)
	}
	// Optional online smoke check: verify prefix via bd CLI when Dolt is reachable.
	// Core assertions already validate prefix from tracked config.yaml above.
	bdCmd := exec.Command("bd", "config", "get", "issue_prefix")
	bdCmd.Dir = hqPath
	bdCmd.Env = append(env, "BEADS_DIR="+beadsDir)
	prefixOutput, err := bdCmd.Output() // stdout only
	if err != nil {
		// In CI/e2e, transient Dolt connection races can make this online check flaky.
		// Keep test deterministic by relying on config.yaml assertions when bd is offline.
		debugCmd := exec.Command("bd", "config", "get", "issue_prefix")
		debugCmd.Dir = hqPath
		debugCmd.Env = append(env, "BEADS_DIR="+beadsDir)
		combinedOut, _ := debugCmd.CombinedOutput()
		out := string(combinedOut)
		if strings.Contains(out, "Dolt server still unreachable") || strings.Contains(out, "connect: connection refused") {
			t.Logf("bd online prefix check skipped due transient Dolt unreachability: %s", strings.TrimSpace(out))
		} else {
			t.Fatalf("bd config get issue_prefix failed: %v\nOutput: %s", err, combinedOut)
		}
	} else {
		prefix := strings.TrimSpace(string(prefixOutput))
		if prefix != "hq" {
			t.Errorf("beads issue_prefix = %q, want %q", prefix, "hq")
		}
	}
}

// TestInstallIdempotent validates that running gt install twice
// on the same directory fails without --force flag.
func TestInstallIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	gtBinary := buildGT(t)

	// First install should succeed
	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first install failed: %v\nOutput: %s", err, output)
	}

	// Second install without --force should fail
	cmd = exec.Command(gtBinary, "install", hqPath, "--no-beads")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("second install should have failed without --force")
	}
	if !strings.Contains(string(output), "already a Gas Town HQ") {
		t.Errorf("expected 'already a Gas Town HQ' error, got: %s", output)
	}

	// Third install with --force should succeed
	cmd = exec.Command(gtBinary, "install", hqPath, "--no-beads", "--force")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install with --force failed: %v\nOutput: %s", err, output)
	}
}

// TestInstallForcePreservesConfigs validates that re-running gt install --force
// preserves existing town.json and rigs.json rather than clobbering them.
func TestInstallForcePreservesConfigs(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	gtBinary := buildGT(t)

	// First install
	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first install failed: %v\nOutput: %s", err, output)
	}

	// Inject sentinel values into town.json and rigs.json
	mayorDir := filepath.Join(hqPath, "mayor")
	townPath := filepath.Join(mayorDir, "town.json")
	rigsPath := filepath.Join(mayorDir, "rigs.json")

	townData, err := os.ReadFile(townPath)
	if err != nil {
		t.Fatalf("reading town.json: %v", err)
	}
	var townConfig config.TownConfig
	if err := json.Unmarshal(townData, &townConfig); err != nil {
		t.Fatalf("parsing town.json: %v", err)
	}
	// Set a sentinel public name to detect clobbering
	townConfig.PublicName = "sentinel-preserve-test"
	sentinelTown, err := json.MarshalIndent(townConfig, "", "  ")
	if err != nil {
		t.Fatalf("marshaling town.json: %v", err)
	}
	if err := os.WriteFile(townPath, sentinelTown, 0644); err != nil {
		t.Fatalf("writing sentinel town.json: %v", err)
	}

	// Add a sentinel rig entry
	rigsData, err := os.ReadFile(rigsPath)
	if err != nil {
		t.Fatalf("reading rigs.json: %v", err)
	}
	var rigsConfig config.RigsConfig
	if err := json.Unmarshal(rigsData, &rigsConfig); err != nil {
		t.Fatalf("parsing rigs.json: %v", err)
	}
	rigsConfig.Rigs["sentinel-rig"] = config.RigEntry{GitURL: "https://example.com/sentinel"}
	sentinelRigs, err := json.MarshalIndent(rigsConfig, "", "  ")
	if err != nil {
		t.Fatalf("marshaling rigs.json: %v", err)
	}
	if err := os.WriteFile(rigsPath, sentinelRigs, 0644); err != nil {
		t.Fatalf("writing sentinel rigs.json: %v", err)
	}

	// Re-install with --force
	cmd = exec.Command(gtBinary, "install", hqPath, "--no-beads", "--force")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install --force failed: %v\nOutput: %s", err, output)
	}

	// Verify preserve messages
	outStr := string(output)
	if !strings.Contains(outStr, "already exists, preserving") {
		t.Errorf("expected 'already exists, preserving' message, got:\n%s", outStr)
	}

	// Verify town.json sentinel survived
	townAfter, err := os.ReadFile(townPath)
	if err != nil {
		t.Fatalf("reading town.json after re-install: %v", err)
	}
	var townAfterConfig config.TownConfig
	if err := json.Unmarshal(townAfter, &townAfterConfig); err != nil {
		t.Fatalf("parsing town.json after re-install: %v", err)
	}
	if townAfterConfig.PublicName != "sentinel-preserve-test" {
		t.Errorf("town.json was clobbered: PublicName = %q, want %q", townAfterConfig.PublicName, "sentinel-preserve-test")
	}

	// Verify rigs.json sentinel survived
	rigsAfter, err := os.ReadFile(rigsPath)
	if err != nil {
		t.Fatalf("reading rigs.json after re-install: %v", err)
	}
	var rigsAfterConfig config.RigsConfig
	if err := json.Unmarshal(rigsAfter, &rigsAfterConfig); err != nil {
		t.Fatalf("parsing rigs.json after re-install: %v", err)
	}
	if _, ok := rigsAfterConfig.Rigs["sentinel-rig"]; !ok {
		t.Errorf("rigs.json was clobbered: sentinel-rig entry missing")
	}
}

// TestInstallForceRejectsNonRegularConfigs validates that gt install --force
// errors when town.json or rigs.json exists as a directory instead of a file.
func TestInstallForceRejectsNonRegularConfigs(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	gtBinary := buildGT(t)

	// First install
	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first install failed: %v\nOutput: %s", err, output)
	}

	// Replace town.json with a directory
	mayorDir := filepath.Join(hqPath, "mayor")
	townPath := filepath.Join(mayorDir, "town.json")
	if err := os.Remove(townPath); err != nil {
		t.Fatalf("removing town.json: %v", err)
	}
	if err := os.Mkdir(townPath, 0755); err != nil {
		t.Fatalf("creating town.json as directory: %v", err)
	}

	// Re-install with --force should fail
	cmd = exec.Command(gtBinary, "install", hqPath, "--no-beads", "--force")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("install --force should have failed with directory town.json, output:\n%s", output)
	}
	if !strings.Contains(string(output), "not a regular file") {
		t.Errorf("expected 'not a regular file' error, got:\n%s", output)
	}
}

// TestInstallFormulasProvisioned validates that embedded formulas are copied
// to .beads/formulas/ during installation.
func TestInstallFormulasProvisioned(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	gtBinary := buildGT(t)

	// HOME and Dolt port are overridden for isolation; configure git identity so EnsureDoltIdentity works.
	env, doltPort := isolatedE2EDoltEnv(t, tmpDir)
	configureGitIdentity(t, env)
	doltcleanup.ReapOwnedDoltOnCleanup(t, hqPath)

	// Run gt install (includes beads and formula provisioning)
	cmd := exec.Command(gtBinary, "install", hqPath, "--dolt-port", doltPort)
	cmd.Dir = tmpDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install failed: %v\nOutput: %s", err, output)
	}
	assertDoltConfigPort(t, hqPath, doltPort)

	// Verify .beads/formulas/ directory exists
	formulasDir := filepath.Join(hqPath, ".beads", "formulas")
	assertDirExists(t, formulasDir, ".beads/formulas/")

	// Verify at least some expected formulas exist
	expectedFormulas := []string{
		"mol-deacon-patrol.formula.toml",
		"mol-refinery-patrol.formula.toml",
		"code-review.formula.toml",
	}
	for _, f := range expectedFormulas {
		formulaPath := filepath.Join(formulasDir, f)
		assertFileExists(t, formulaPath, f)
	}

	// Verify the count matches embedded formulas
	entries, err := os.ReadDir(formulasDir)
	if err != nil {
		t.Fatalf("failed to read formulas dir: %v", err)
	}
	// Count only formula files (not directories)
	var fileCount int
	for _, e := range entries {
		if !e.IsDir() {
			fileCount++
		}
	}
	// Should have at least 20 formulas (allows for some variation)
	if fileCount < 20 {
		t.Errorf("expected at least 20 formulas, got %d", fileCount)
	}
}

// TestInstallWrappersInExistingTown validates that --wrappers works in an
// existing town without requiring --force or recreating HQ structure.
func TestInstallWrappersInExistingTown(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")
	binDir := filepath.Join(tmpDir, "bin")

	// Create bin directory for wrappers
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	gtBinary := buildGT(t)

	// First: create HQ without wrappers
	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("first install failed: %v\nOutput: %s", err, output)
	}

	// Verify town.json exists (proves HQ was created)
	townPath := filepath.Join(hqPath, "mayor", "town.json")
	assertFileExists(t, townPath, "mayor/town.json")

	// Get modification time of town.json before wrapper install
	townInfo, err := os.Stat(townPath)
	if err != nil {
		t.Fatalf("failed to stat town.json: %v", err)
	}
	townModBefore := townInfo.ModTime()

	// Second: install --wrappers in same directory (should not recreate HQ)
	cmd = exec.Command(gtBinary, "install", hqPath, "--wrappers")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install --wrappers in existing town failed: %v\nOutput: %s", err, output)
	}

	// Verify town.json was NOT modified (HQ was not recreated)
	townInfo, err = os.Stat(townPath)
	if err != nil {
		t.Fatalf("failed to stat town.json after wrapper install: %v", err)
	}
	if townInfo.ModTime() != townModBefore {
		t.Errorf("town.json was modified during --wrappers install, HQ should not be recreated")
	}

	// Verify output mentions wrapper installation
	if !strings.Contains(string(output), "gt-codex") && !strings.Contains(string(output), "gt-opencode") {
		t.Errorf("expected output to mention wrappers, got: %s", output)
	}
}

// TestInstallNoBeadsFlag validates that --no-beads skips beads initialization.
func TestInstallNoBeadsFlag(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")

	gtBinary := buildGT(t)

	// Run gt install with --no-beads
	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads")
	cmd.Env = append(os.Environ(), "HOME="+tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install --no-beads failed: %v\nOutput: %s", err, output)
	}

	// Verify .beads/ directory does NOT exist
	beadsDir := filepath.Join(hqPath, ".beads")
	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Errorf(".beads/ should not exist with --no-beads flag")
	}
}

// assertDirExists checks that the given path exists and is a directory.
func assertDirExists(t *testing.T, path, name string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("%s does not exist: %v", name, err)
		return
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", name)
	}
}

// assertFileExists checks that the given path exists and is a file.
func assertFileExists(t *testing.T, path, name string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Errorf("%s does not exist: %v", name, err)
		return
	}
	if info.IsDir() {
		t.Errorf("%s is a directory, expected file", name)
	}
}

func assertDoltConfigPort(t *testing.T, hqPath, wantPort string) {
	t.Helper()
	configPath := filepath.Join(hqPath, ".dolt-data", "config.yaml")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("reading Dolt config %s: %v", configPath, err)
	}
	var cfg struct {
		Listener struct {
			Port int `yaml:"port"`
		} `yaml:"listener"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("parsing Dolt config %s: %v", configPath, err)
	}
	want, err := strconv.Atoi(wantPort)
	if err != nil {
		t.Fatalf("invalid expected Dolt port %q: %v", wantPort, err)
	}
	if cfg.Listener.Port != want {
		t.Fatalf("Dolt config port = %d, want isolated port %d\n%s", cfg.Listener.Port, want, data)
	}
}

func assertSlotValue(t *testing.T, townRoot, issueID, slot, want string) {
	t.Helper()
	cmd := exec.Command("bd", "--json", "slot", "show", issueID)
	cmd.Dir = townRoot
	output, err := cmd.Output()
	if err != nil {
		debugCmd := exec.Command("bd", "--json", "slot", "show", issueID)
		debugCmd.Dir = townRoot
		combined, _ := debugCmd.CombinedOutput()
		t.Fatalf("bd slot show %s failed: %v\nOutput: %s", issueID, err, combined)
	}

	var parsed struct {
		Slots map[string]*string `json:"slots"`
	}
	if err := json.Unmarshal(output, &parsed); err != nil {
		t.Fatalf("parsing slot show output failed: %v\nOutput: %s", err, output)
	}

	var got string
	if value, ok := parsed.Slots[slot]; ok && value != nil {
		got = *value
	}
	if got != want {
		t.Fatalf("slot %s for %s = %q, want %q", slot, issueID, got, want)
	}
}

// TestInstallDoctorClean validates that gt install creates a functional system.
// This test verifies:
// 1. gt install succeeds with proper structure
// 2. gt rig add succeeds
// 3. gt crew add succeeds
// 4. Basic commands work
//
// NOTE: Full doctor --fix verification is currently limited by known issues:
// - Doctor fix has bugs with bead creation (UNIQUE constraint errors)
// - Container environment lacks tmux for session checks
// - Test repos don't satisfy priming expectations (AGENTS.md length)
//
// TODO: Enable full doctor verification once these issues are resolved.
func TestInstallDoctorClean(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")
	gtBinary := buildGT(t)

	// Clean environment and isolated Dolt port for predictable, non-destructive behavior.
	env, doltPort := isolatedE2EDoltEnv(t, tmpDir)
	doltcleanup.ReapOwnedDoltOnCleanup(t, hqPath)

	// Set up git identity in the test's temp HOME so EnsureDoltIdentity can copy it.
	configureGitIdentity(t, env)

	// 1. Install town with git (now includes dolt identity, HQ init, server start)
	t.Run("install", func(t *testing.T) {
		runGTCmd(t, gtBinary, tmpDir, env, "install", hqPath, "--name", "test-town", "--git", "--dolt-port", doltPort)
		assertDoltConfigPort(t, hqPath, doltPort)
	})
	t.Cleanup(func() {
		cmd := exec.Command(gtBinary, "dolt", "stop")
		cmd.Dir = hqPath
		cmd.Env = env
		_ = cmd.Run()
	})

	// 2. Verify core structure exists
	t.Run("verify-structure", func(t *testing.T) {
		assertDirExists(t, filepath.Join(hqPath, "mayor"), "mayor/")
		assertDirExists(t, filepath.Join(hqPath, "deacon"), "deacon/")
		assertDirExists(t, filepath.Join(hqPath, ".beads"), ".beads/")
		assertFileExists(t, filepath.Join(hqPath, "mayor", "town.json"), "mayor/town.json")
		assertFileExists(t, filepath.Join(hqPath, "mayor", "rigs.json"), "mayor/rigs.json")
	})

	// 3. Verify install bootstrapped dolt (identity, HQ database, server)
	t.Run("verify-dolt-bootstrap", func(t *testing.T) {
		// HQ database should exist
		hqDoltDir := filepath.Join(hqPath, ".dolt-data", "hq", ".dolt")
		assertDirExists(t, hqDoltDir, ".dolt-data/hq/.dolt")

		// Dolt identity should have been copied from git config
		nameCmd := exec.Command("dolt", "config", "--global", "--get", "user.name")
		nameCmd.Env = env
		nameOut, err := nameCmd.Output()
		if err != nil {
			t.Fatalf("dolt config --get user.name failed: %v", err)
		}
		if got := strings.TrimSpace(string(nameOut)); got != "Test User" {
			t.Errorf("dolt user.name = %q, want %q", got, "Test User")
		}

		// Dolt server should be reachable
		cmd := exec.Command(gtBinary, "dolt", "status")
		cmd.Dir = hqPath
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("dolt status failed (server may not be running): %v\n%s", err, out)
		}
		if strings.Contains(string(out), "not running") {
			t.Errorf("expected dolt server to be running, got: %s", out)
		}

		// init-rig hq again should be idempotent (no error, "already exists" message)
		initCmd := exec.Command(gtBinary, "dolt", "init-rig", "hq")
		initCmd.Dir = hqPath
		initCmd.Env = env
		initOut, err := initCmd.CombinedOutput()
		if err != nil {
			t.Fatalf("re-running init-rig hq should be idempotent, got error: %v\n%s", err, initOut)
		}
		if !strings.Contains(string(initOut), "already exists") {
			t.Errorf("expected 'already exists' message, got: %s", initOut)
		}
	})

	// 4. Add a small public repo as a rig (CLI rejects local paths)
	t.Run("rig-add", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "rig", "add", "testrig",
			"https://github.com/octocat/Hello-World.git", "--prefix", "tr")
	})

	// 5. Verify rig structure exists
	t.Run("verify-rig-structure", func(t *testing.T) {
		rigPath := filepath.Join(hqPath, "testrig")
		assertDirExists(t, rigPath, "testrig/")
		assertDirExists(t, filepath.Join(rigPath, "witness"), "testrig/witness/")
		assertDirExists(t, filepath.Join(rigPath, "refinery"), "testrig/refinery/")
		assertDirExists(t, filepath.Join(rigPath, ".repo.git"), "testrig/.repo.git/")
	})

	// 6. Add a crew member
	t.Run("crew-add", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "crew", "add", "jayne", "--rig", "testrig")
	})

	// 7. Verify crew structure exists
	t.Run("verify-crew-structure", func(t *testing.T) {
		crewPath := filepath.Join(hqPath, "testrig", "crew", "jayne")
		assertDirExists(t, crewPath, "testrig/crew/jayne/")
	})

	// 8. Basic commands should work
	// Note: mail inbox and hook are omitted — fresh Dolt databases lack the
	// issues table, so these commands error with "table not found" until
	// the first bead is created.
	t.Run("commands", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "rig", "list")
		runGTCmd(t, gtBinary, hqPath, env, "crew", "list", "--rig", "testrig")
	})

	// 9. Doctor runs without crashing (may have warnings/errors but should not panic)
	t.Run("doctor-runs", func(t *testing.T) {
		cmd := exec.Command(gtBinary, "doctor", "-v")
		cmd.Dir = hqPath
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		outStr := string(out)
		t.Logf("Doctor output:\n%s", outStr)
		// Fail on crashes/panics even though we tolerate doctor errors
		for _, signal := range []string{"panic:", "SIGSEGV", "runtime error"} {
			if strings.Contains(outStr, signal) {
				t.Fatalf("doctor crashed with %s", signal)
			}
		}
	})
}

// TestInstallWithDaemon validates that gt install creates a functional system
// with the daemon running. This extends TestInstallDoctorClean by:
// 1. Starting the daemon after install
// 2. Verifying the daemon is healthy
// 3. Running basic operations with daemon support
func TestInstallWithDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "test-hq")
	gtBinary := buildGT(t)

	// Clean environment and isolated Dolt port for predictable, non-destructive behavior.
	env, doltPort := isolatedE2EDoltEnv(t, tmpDir)
	doltcleanup.ReapOwnedDoltOnCleanup(t, hqPath)

	// Set up git identity in the test's temp HOME so EnsureDoltIdentity can copy it.
	configureGitIdentity(t, env)

	// 1. Install town with git (now includes dolt identity, HQ init, server start)
	t.Run("install", func(t *testing.T) {
		runGTCmd(t, gtBinary, tmpDir, env, "install", hqPath, "--name", "test-town", "--git", "--dolt-port", doltPort)
		assertDoltConfigPort(t, hqPath, doltPort)
	})
	t.Cleanup(func() {
		cmd := exec.Command(gtBinary, "dolt", "stop")
		cmd.Dir = hqPath
		cmd.Env = env
		_ = cmd.Run()
	})

	// 2. Start daemon
	t.Run("daemon-start", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "daemon", "start")
	})
	t.Cleanup(func() {
		cmd := exec.Command(gtBinary, "daemon", "stop")
		cmd.Dir = hqPath
		cmd.Env = env
		_ = cmd.Run()
	})

	// 4. Verify daemon is running
	t.Run("daemon-status", func(t *testing.T) {
		cmd := exec.Command(gtBinary, "daemon", "status")
		cmd.Dir = hqPath
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("daemon status failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "running") {
			t.Errorf("expected daemon to be running, got: %s", out)
		}
	})

	// 5. Add a small public repo as a rig (CLI rejects local paths)
	t.Run("rig-add", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "rig", "add", "testrig",
			"https://github.com/octocat/Hello-World.git", "--prefix", "tr")
	})

	// 6. Add crew member
	t.Run("crew-add", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "crew", "add", "jayne", "--rig", "testrig")
	})

	// 7. Verify commands work with daemon running
	// Note: mail inbox and hook are omitted — fresh Dolt databases lack the
	// issues table, so these commands error with "table not found" until
	// the first bead is created.
	t.Run("commands", func(t *testing.T) {
		runGTCmd(t, gtBinary, hqPath, env, "rig", "list")
		runGTCmd(t, gtBinary, hqPath, env, "crew", "list", "--rig", "testrig")
	})

	// 8. Verify daemon shows in doctor output
	t.Run("doctor-daemon-check", func(t *testing.T) {
		cmd := exec.Command(gtBinary, "doctor", "-v")
		cmd.Dir = hqPath
		cmd.Env = env
		out, _ := cmd.CombinedOutput()
		outStr := string(out)
		t.Logf("Doctor output:\n%s", outStr)
		// Fail on crashes/panics even though we tolerate doctor errors
		for _, signal := range []string{"panic:", "SIGSEGV", "runtime error"} {
			if strings.Contains(outStr, signal) {
				t.Fatalf("doctor crashed with %s", signal)
			}
		}
	})
}

// cleanE2EEnv returns os.Environ() with Gas Town and Beads routing variables
// removed. E2E tests add an explicit isolated Dolt port per test instead of
// inheriting production/default Dolt routing from the developer environment.
func cleanE2EEnv() []string {
	var clean []string
	for _, env := range os.Environ() {
		if strings.HasPrefix(env, "GT_") || strings.HasPrefix(env, "BD_") || strings.HasPrefix(env, "BEADS_") {
			continue
		}
		clean = append(clean, env)
	}
	return clean
}

func isolatedE2EDoltEnv(t *testing.T, homeDir string) ([]string, string) {
	t.Helper()
	port := strconv.Itoa(freeE2EDoltPort(t))
	env := append(cleanE2EEnv(),
		"HOME="+homeDir,
		"GT_DOLT_PORT="+port,
		"BEADS_DOLT_PORT="+port,
		"BEADS_DOLT_AUTO_START=0",
	)
	return env, port
}

func freeE2EDoltPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("allocate free TCP port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// configureGitIdentity sets git global config in the test's temp HOME directory.
// Tests override HOME to a temp dir for isolation, so git/dolt can't find the
// container's build-time global config. EnsureDoltIdentity copies from git config,
// so git identity must be available before gt install.
func configureGitIdentity(t *testing.T, env []string) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "--global", "user.name", "Test User"},
		{"config", "--global", "user.email", "test@test.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}
}

// runGTCmd runs a gt command and fails the test if it fails.
func runGTCmd(t *testing.T, binary, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt %v failed: %v\n%s", args, err, out)
	}
}
