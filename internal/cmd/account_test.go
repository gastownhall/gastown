package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/spf13/cobra"
)

// setupTestTownForAccount creates a minimal Gas Town workspace with accounts.
func setupTestTownForAccount(t *testing.T) (townRoot string, accountsDir string) {
	t.Helper()

	townRoot = t.TempDir()

	// Create mayor directory with required files
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// Create town.json
	townConfig := &config.TownConfig{
		Type:       "town",
		Version:    config.CurrentTownVersion,
		Name:       "test-town",
		PublicName: "Test Town",
		CreatedAt:  time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	townConfigPath := filepath.Join(mayorDir, "town.json")
	if err := config.SaveTownConfig(townConfigPath, townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	// Create empty rigs.json
	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs:    make(map[string]config.RigEntry),
	}
	rigsPath := filepath.Join(mayorDir, "rigs.json")
	if err := config.SaveRigsConfig(rigsPath, rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	// Create accounts directory
	accountsDir = filepath.Join(t.TempDir(), "claude-accounts")
	if err := os.MkdirAll(accountsDir, 0755); err != nil {
		t.Fatalf("mkdir accounts: %v", err)
	}

	return townRoot, accountsDir
}

func setTestHome(t *testing.T, fakeHome string) {
	t.Helper()

	t.Setenv("HOME", fakeHome)

	if runtime.GOOS != "windows" {
		return
	}

	t.Setenv("USERPROFILE", fakeHome)

	drive := filepath.VolumeName(fakeHome)
	if drive == "" {
		return
	}

	t.Setenv("HOMEDRIVE", drive)
	t.Setenv("HOMEPATH", strings.TrimPrefix(fakeHome, drive))
}

func TestAccountSwitch(t *testing.T) {
	t.Run("switch between accounts", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink tests require elevated privileges on Windows")
		}
		townRoot, accountsDir := setupTestTownForAccount(t)

		// Create fake home directory for ~/.claude
		fakeHome := t.TempDir()
		setTestHome(t, fakeHome)

		// Create account config directories
		workConfigDir := filepath.Join(accountsDir, "work")
		personalConfigDir := filepath.Join(accountsDir, "personal")
		if err := os.MkdirAll(workConfigDir, 0755); err != nil {
			t.Fatalf("mkdir work config: %v", err)
		}
		if err := os.MkdirAll(personalConfigDir, 0755); err != nil {
			t.Fatalf("mkdir personal config: %v", err)
		}

		// Create accounts.json with two accounts
		accountsPath := filepath.Join(townRoot, "mayor", "accounts.json")
		accountsCfg := config.NewAccountsConfig()
		accountsCfg.Accounts["work"] = config.Account{
			Email:     "steve@work.com",
			ConfigDir: workConfigDir,
		}
		accountsCfg.Accounts["personal"] = config.Account{
			Email:     "steve@personal.com",
			ConfigDir: personalConfigDir,
		}
		accountsCfg.Default = "work"
		if err := config.SaveAccountsConfig(accountsPath, accountsCfg); err != nil {
			t.Fatalf("save accounts.json: %v", err)
		}

		// Create initial symlink to work account
		claudeDir := filepath.Join(fakeHome, ".claude")
		if err := os.Symlink(workConfigDir, claudeDir); err != nil {
			t.Fatalf("create symlink: %v", err)
		}

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run switch to personal
		cmd := &cobra.Command{}
		err := runAccountSwitch(cmd, []string{"personal"})
		if err != nil {
			t.Fatalf("runAccountSwitch failed: %v", err)
		}

		// Verify symlink points to personal
		target, err := os.Readlink(claudeDir)
		if err != nil {
			t.Fatalf("readlink: %v", err)
		}
		if target != personalConfigDir {
			t.Errorf("symlink target = %q, want %q", target, personalConfigDir)
		}

		// Verify default was updated
		loadedCfg, err := config.LoadAccountsConfig(accountsPath)
		if err != nil {
			t.Fatalf("load accounts: %v", err)
		}
		if loadedCfg.Default != "personal" {
			t.Errorf("default = %q, want 'personal'", loadedCfg.Default)
		}
	})

	t.Run("already on target account", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink tests require elevated privileges on Windows")
		}
		townRoot, accountsDir := setupTestTownForAccount(t)

		fakeHome := t.TempDir()
		setTestHome(t, fakeHome)

		workConfigDir := filepath.Join(accountsDir, "work")
		if err := os.MkdirAll(workConfigDir, 0755); err != nil {
			t.Fatalf("mkdir work config: %v", err)
		}

		accountsPath := filepath.Join(townRoot, "mayor", "accounts.json")
		accountsCfg := config.NewAccountsConfig()
		accountsCfg.Accounts["work"] = config.Account{
			Email:     "steve@work.com",
			ConfigDir: workConfigDir,
		}
		accountsCfg.Default = "work"
		if err := config.SaveAccountsConfig(accountsPath, accountsCfg); err != nil {
			t.Fatalf("save accounts.json: %v", err)
		}

		// Create symlink already pointing to work
		claudeDir := filepath.Join(fakeHome, ".claude")
		if err := os.Symlink(workConfigDir, claudeDir); err != nil {
			t.Fatalf("create symlink: %v", err)
		}

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Switch to work (should be no-op)
		cmd := &cobra.Command{}
		err := runAccountSwitch(cmd, []string{"work"})
		if err != nil {
			t.Fatalf("runAccountSwitch failed: %v", err)
		}

		// Symlink should still point to work
		target, err := os.Readlink(claudeDir)
		if err != nil {
			t.Fatalf("readlink: %v", err)
		}
		if target != workConfigDir {
			t.Errorf("symlink target = %q, want %q", target, workConfigDir)
		}
	})

	t.Run("nonexistent account", func(t *testing.T) {
		townRoot, accountsDir := setupTestTownForAccount(t)

		fakeHome := t.TempDir()
		setTestHome(t, fakeHome)

		workConfigDir := filepath.Join(accountsDir, "work")
		if err := os.MkdirAll(workConfigDir, 0755); err != nil {
			t.Fatalf("mkdir work config: %v", err)
		}

		accountsPath := filepath.Join(townRoot, "mayor", "accounts.json")
		accountsCfg := config.NewAccountsConfig()
		accountsCfg.Accounts["work"] = config.Account{
			Email:     "steve@work.com",
			ConfigDir: workConfigDir,
		}
		accountsCfg.Default = "work"
		if err := config.SaveAccountsConfig(accountsPath, accountsCfg); err != nil {
			t.Fatalf("save accounts.json: %v", err)
		}

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Switch to nonexistent account
		cmd := &cobra.Command{}
		err := runAccountSwitch(cmd, []string{"nonexistent"})
		if err == nil {
			t.Fatal("expected error for nonexistent account")
		}
	})

	t.Run("real directory gets moved", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlink tests require elevated privileges on Windows")
		}
		townRoot, accountsDir := setupTestTownForAccount(t)

		fakeHome := t.TempDir()
		setTestHome(t, fakeHome)

		workConfigDir := filepath.Join(accountsDir, "work")
		personalConfigDir := filepath.Join(accountsDir, "personal")
		// Don't create workConfigDir - it will be created by moving ~/.claude
		if err := os.MkdirAll(personalConfigDir, 0755); err != nil {
			t.Fatalf("mkdir personal config: %v", err)
		}

		accountsPath := filepath.Join(townRoot, "mayor", "accounts.json")
		accountsCfg := config.NewAccountsConfig()
		accountsCfg.Accounts["work"] = config.Account{
			Email:     "steve@work.com",
			ConfigDir: workConfigDir,
		}
		accountsCfg.Accounts["personal"] = config.Account{
			Email:     "steve@personal.com",
			ConfigDir: personalConfigDir,
		}
		accountsCfg.Default = "work"
		if err := config.SaveAccountsConfig(accountsPath, accountsCfg); err != nil {
			t.Fatalf("save accounts.json: %v", err)
		}

		// Create ~/.claude as a real directory with a marker file
		claudeDir := filepath.Join(fakeHome, ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatalf("mkdir .claude: %v", err)
		}
		markerFile := filepath.Join(claudeDir, "marker.txt")
		if err := os.WriteFile(markerFile, []byte("test"), 0644); err != nil {
			t.Fatalf("write marker: %v", err)
		}

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Switch to personal
		cmd := &cobra.Command{}
		err := runAccountSwitch(cmd, []string{"personal"})
		if err != nil {
			t.Fatalf("runAccountSwitch failed: %v", err)
		}

		// Verify ~/.claude is now a symlink to personal
		fileInfo, err := os.Lstat(claudeDir)
		if err != nil {
			t.Fatalf("lstat .claude: %v", err)
		}
		if fileInfo.Mode()&os.ModeSymlink == 0 {
			t.Error("~/.claude is not a symlink")
		}

		target, err := os.Readlink(claudeDir)
		if err != nil {
			t.Fatalf("readlink: %v", err)
		}
		if target != personalConfigDir {
			t.Errorf("symlink target = %q, want %q", target, personalConfigDir)
		}

		// Verify original content was moved to work config dir
		movedMarker := filepath.Join(workConfigDir, "marker.txt")
		if _, err := os.Stat(movedMarker); err != nil {
			t.Errorf("marker file not moved to work config dir: %v", err)
		}
	})
}
