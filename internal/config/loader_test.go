package config

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/constants"
)

// skipIfAgentBinaryMissing skips the test if any of the specified agent binaries
// are not found in PATH. This allows tests that depend on specific agents to be
// skipped in environments where those agents aren't installed.
func skipIfAgentBinaryMissing(t *testing.T, agents ...string) {
	t.Helper()
	for _, agent := range agents {
		if _, err := exec.LookPath(agent); err != nil {
			t.Skipf("skipping test: agent binary %q not found in PATH", agent)
		}
	}
}

func writeAgentStub(t *testing.T, binDir, name string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		path := filepath.Join(binDir, name+".cmd")
		if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 0\r\n"), 0644); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
		return
	}

	path := filepath.Join(binDir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write %s stub: %v", name, err)
	}
}

// isClaudeCommand checks if a command is claude (either "claude" or a path ending in "/claude").
// This handles the case where resolveClaudePath returns the full path to the claude binary.
// Also handles Windows paths with .exe extension.
func isClaudeCommand(cmd string) bool {
	base := filepath.Base(cmd)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	return base == "claude"
}

func TestTownConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "town.json")

	original := &TownConfig{
		Type:      "town",
		Version:   1,
		Name:      "test-town",
		CreatedAt: time.Now().Truncate(time.Second),
	}

	if err := SaveTownConfig(path, original); err != nil {
		t.Fatalf("SaveTownConfig: %v", err)
	}

	loaded, err := LoadTownConfig(path)
	if err != nil {
		t.Fatalf("LoadTownConfig: %v", err)
	}

	if loaded.Name != original.Name {
		t.Errorf("Name = %q, want %q", loaded.Name, original.Name)
	}
	if loaded.Type != original.Type {
		t.Errorf("Type = %q, want %q", loaded.Type, original.Type)
	}
}

func TestRigsConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "rigs.json")

	original := &RigsConfig{
		Version: 1,
		Rigs: map[string]RigEntry{
			"gastown": {
				GitURL:    "git@github.com:steveyegge/gastown.git",
				LocalRepo: "/tmp/local-repo",
				AddedAt:   time.Now().Truncate(time.Second),
				BeadsConfig: &BeadsConfig{
					Repo:   "local",
					Prefix: "gt-",
				},
			},
		},
	}

	if err := SaveRigsConfig(path, original); err != nil {
		t.Fatalf("SaveRigsConfig: %v", err)
	}

	loaded, err := LoadRigsConfig(path)
	if err != nil {
		t.Fatalf("LoadRigsConfig: %v", err)
	}

	if len(loaded.Rigs) != 1 {
		t.Errorf("Rigs count = %d, want 1", len(loaded.Rigs))
	}

	rig, ok := loaded.Rigs["gastown"]
	if !ok {
		t.Fatal("missing 'gastown' rig")
	}
	if rig.BeadsConfig == nil || rig.BeadsConfig.Prefix != "gt-" {
		t.Errorf("BeadsConfig.Prefix = %v, want 'gt-'", rig.BeadsConfig)
	}
	if rig.LocalRepo != "/tmp/local-repo" {
		t.Errorf("LocalRepo = %q, want %q", rig.LocalRepo, "/tmp/local-repo")
	}
}

func TestLoadTownConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadTownConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestValidationErrors(t *testing.T) {
	t.Parallel()
	// Missing name
	tc := &TownConfig{Type: "town", Version: 1}
	if err := validateTownConfig(tc); err == nil {
		t.Error("expected error for missing name")
	}

	// Wrong type
	tc = &TownConfig{Type: "wrong", Version: 1, Name: "test"}
	if err := validateTownConfig(tc); err == nil {
		t.Error("expected error for wrong type")
	}
}

func TestRigConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	original := NewRigConfig("gastown", "git@github.com:test/gastown.git")
	original.CreatedAt = time.Now().Truncate(time.Second)
	original.Beads = &BeadsConfig{Prefix: "gt-"}
	original.LocalRepo = "/tmp/local-repo"

	if err := SaveRigConfig(path, original); err != nil {
		t.Fatalf("SaveRigConfig: %v", err)
	}

	loaded, err := LoadRigConfig(path)
	if err != nil {
		t.Fatalf("LoadRigConfig: %v", err)
	}

	if loaded.Type != "rig" {
		t.Errorf("Type = %q, want 'rig'", loaded.Type)
	}
	if loaded.Version != CurrentRigConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentRigConfigVersion)
	}
	if loaded.Name != "gastown" {
		t.Errorf("Name = %q, want 'gastown'", loaded.Name)
	}
	if loaded.GitURL != "git@github.com:test/gastown.git" {
		t.Errorf("GitURL = %q, want expected URL", loaded.GitURL)
	}
	if loaded.LocalRepo != "/tmp/local-repo" {
		t.Errorf("LocalRepo = %q, want %q", loaded.LocalRepo, "/tmp/local-repo")
	}
	if loaded.Beads == nil || loaded.Beads.Prefix != "gt-" {
		t.Error("Beads.Prefix not preserved")
	}
}

func TestRigSettingsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings", "config.json")

	original := NewRigSettings()

	if err := SaveRigSettings(path, original); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	loaded, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	if loaded.Type != "rig-settings" {
		t.Errorf("Type = %q, want 'rig-settings'", loaded.Type)
	}
	if loaded.MergeQueue == nil {
		t.Fatal("MergeQueue is nil")
	}
	if !loaded.MergeQueue.Enabled {
		t.Error("MergeQueue.Enabled = false, want true")
	}
}

func TestRigSettingsWithCustomMergeQueue(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	original := &RigSettings{
		Type:    "rig-settings",
		Version: 1,
		MergeQueue: &MergeQueueConfig{
			Enabled:                          true,
			IntegrationBranchPolecatEnabled:  boolPtr(false),
			IntegrationBranchRefineryEnabled: boolPtr(false),
			OnConflict:                       OnConflictAutoRebase,
			RunTests:                         boolPtr(true),
			TestCommand:                      "make test",
			DeleteMergedBranches:             boolPtr(false),
			RetryFlakyTests:                  3,
			PollInterval:                     "1m",
			MaxConcurrent:                    2,
		},
	}

	if err := SaveRigSettings(path, original); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	loaded, err := LoadRigSettings(path)
	if err != nil {
		t.Fatalf("LoadRigSettings: %v", err)
	}

	mq := loaded.MergeQueue
	if mq.OnConflict != OnConflictAutoRebase {
		t.Errorf("OnConflict = %q, want %q", mq.OnConflict, OnConflictAutoRebase)
	}
	if mq.TestCommand != "make test" {
		t.Errorf("TestCommand = %q, want 'make test'", mq.TestCommand)
	}
	if mq.RetryFlakyTests != 3 {
		t.Errorf("RetryFlakyTests = %d, want 3", mq.RetryFlakyTests)
	}
}

func TestRigConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *RigConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: &RigConfig{
				Type:    "rig",
				Version: 1,
				Name:    "test-rig",
			},
			wantErr: false,
		},
		{
			name: "missing name",
			config: &RigConfig{
				Type:    "rig",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "wrong type",
			config: &RigConfig{
				Type:    "wrong",
				Version: 1,
				Name:    "test",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRigConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRigConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRigSettingsValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		settings *RigSettings
		wantErr  bool
	}{
		{
			name: "valid settings",
			settings: &RigSettings{
				Type:       "rig-settings",
				Version:    1,
				MergeQueue: DefaultMergeQueueConfig(),
			},
			wantErr: false,
		},
		{
			name: "valid settings without merge queue",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
			},
			wantErr: false,
		},
		{
			name: "wrong type",
			settings: &RigSettings{
				Type:    "wrong",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "invalid on_conflict",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					OnConflict: "invalid",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid poll_interval",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					PollInterval: "not-a-duration",
				},
			},
			wantErr: true,
		},
		{
			name: "invalid stale_claim_timeout",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					StaleClaimTimeout: "not-a-duration",
				},
			},
			wantErr: true,
		},
		{
			name: "zero stale_claim_timeout",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					StaleClaimTimeout: "0s",
				},
			},
			wantErr: true,
		},
		{
			name: "negative stale_claim_timeout",
			settings: &RigSettings{
				Type:    "rig-settings",
				Version: 1,
				MergeQueue: &MergeQueueConfig{
					StaleClaimTimeout: "-5m",
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRigSettings(tt.settings)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateRigSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestDefaultMergeQueueConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultMergeQueueConfig()

	if !cfg.Enabled {
		t.Error("Enabled should be true by default")
	}
	if !cfg.IsPolecatIntegrationEnabled() {
		t.Error("IsPolecatIntegrationEnabled should be true by default")
	}
	if !cfg.IsRefineryIntegrationEnabled() {
		t.Error("IsRefineryIntegrationEnabled should be true by default")
	}
	if cfg.OnConflict != OnConflictAssignBack {
		t.Errorf("OnConflict = %q, want %q", cfg.OnConflict, OnConflictAssignBack)
	}
	if !cfg.IsRunTestsEnabled() {
		t.Error("IsRunTestsEnabled should be true by default")
	}
	if cfg.TestCommand != "" {
		t.Errorf("TestCommand = %q, want empty (language-agnostic default)", cfg.TestCommand)
	}
	if !cfg.IsDeleteMergedBranchesEnabled() {
		t.Error("IsDeleteMergedBranchesEnabled should be true by default")
	}
	if cfg.RetryFlakyTests != 1 {
		t.Errorf("RetryFlakyTests = %d, want 1", cfg.RetryFlakyTests)
	}
	if cfg.PollInterval != "30s" {
		t.Errorf("PollInterval = %q, want '30s'", cfg.PollInterval)
	}
	if cfg.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent = %d, want 1", cfg.MaxConcurrent)
	}
	if cfg.StaleClaimTimeout != "30m" {
		t.Errorf("StaleClaimTimeout = %q, want '30m'", cfg.StaleClaimTimeout)
	}
}

func TestLoadRigConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadRigConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadRigSettingsNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadRigSettings("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadRepoSettings(t *testing.T) {
	t.Parallel()

	t.Run("returns nil when file missing", func(t *testing.T) {
		t.Parallel()
		settings, err := LoadRepoSettings("/nonexistent/repo")
		if err != nil {
			t.Fatalf("expected nil error, got: %v", err)
		}
		if settings != nil {
			t.Fatal("expected nil settings for missing file")
		}
	})

	t.Run("loads valid repo settings", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		gsDir := filepath.Join(dir, ".gastown")
		if err := os.MkdirAll(gsDir, 0755); err != nil {
			t.Fatal(err)
		}
		data := []byte(`{
			"type": "rig-settings",
			"version": 1,
			"merge_queue": {
				"test_command": "./scripts/ci/api.sh",
				"build_command": "dotnet build"
			}
		}`)
		if err := os.WriteFile(filepath.Join(gsDir, "settings.json"), data, 0644); err != nil {
			t.Fatal(err)
		}

		settings, err := LoadRepoSettings(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if settings == nil {
			t.Fatal("expected non-nil settings")
		}
		if settings.MergeQueue == nil {
			t.Fatal("expected non-nil MergeQueue")
		}
		if settings.MergeQueue.TestCommand != "./scripts/ci/api.sh" {
			t.Errorf("expected test_command='./scripts/ci/api.sh', got %q", settings.MergeQueue.TestCommand)
		}
		if settings.MergeQueue.BuildCommand != "dotnet build" {
			t.Errorf("expected build_command='dotnet build', got %q", settings.MergeQueue.BuildCommand)
		}
	})
}

func TestMergeSettingsCommand(t *testing.T) {
	t.Parallel()

	t.Run("nil inputs returns nil", func(t *testing.T) {
		t.Parallel()
		result := MergeSettingsCommand(nil, nil)
		if result != nil {
			t.Fatal("expected nil")
		}
	})

	t.Run("repo only", func(t *testing.T) {
		t.Parallel()
		repo := &MergeQueueConfig{TestCommand: "repo-test", BuildCommand: "repo-build"}
		result := MergeSettingsCommand(repo, nil)
		if result.TestCommand != "repo-test" {
			t.Errorf("expected 'repo-test', got %q", result.TestCommand)
		}
	})

	t.Run("local overrides repo", func(t *testing.T) {
		t.Parallel()
		repo := &MergeQueueConfig{TestCommand: "repo-test", BuildCommand: "repo-build", LintCommand: "repo-lint"}
		local := &MergeQueueConfig{TestCommand: "local-test"}
		result := MergeSettingsCommand(repo, local)
		if result.TestCommand != "local-test" {
			t.Errorf("expected 'local-test', got %q", result.TestCommand)
		}
		if result.BuildCommand != "repo-build" {
			t.Errorf("expected 'repo-build' (not overridden), got %q", result.BuildCommand)
		}
		if result.LintCommand != "repo-lint" {
			t.Errorf("expected 'repo-lint' (not overridden), got %q", result.LintCommand)
		}
	})

	t.Run("local only", func(t *testing.T) {
		t.Parallel()
		local := &MergeQueueConfig{TestCommand: "local-test"}
		result := MergeSettingsCommand(nil, local)
		if result.TestCommand != "local-test" {
			t.Errorf("expected 'local-test', got %q", result.TestCommand)
		}
	})
}

func TestMayorConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "config.json")

	original := NewMayorConfig()
	original.Theme = &TownThemeConfig{
		Disabled: true,
		Name:     "forest",
		Custom: &CustomTheme{
			BG: "#111111",
			FG: "#eeeeee",
		},
		RoleDefaults: map[string]string{
			"witness": "rust",
		},
	}

	if err := SaveMayorConfig(path, original); err != nil {
		t.Fatalf("SaveMayorConfig: %v", err)
	}

	loaded, err := LoadMayorConfig(path)
	if err != nil {
		t.Fatalf("LoadMayorConfig: %v", err)
	}

	if loaded.Type != "mayor-config" {
		t.Errorf("Type = %q, want 'mayor-config'", loaded.Type)
	}
	if loaded.Version != CurrentMayorConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentMayorConfigVersion)
	}
	if loaded.Theme == nil || loaded.Theme.RoleDefaults["witness"] != "rust" {
		t.Error("Theme.RoleDefaults not preserved")
	}
	if loaded.Theme == nil || !loaded.Theme.Disabled {
		t.Error("Theme.Disabled not preserved")
	}
	if loaded.Theme == nil || loaded.Theme.Name != "forest" {
		t.Error("Theme.Name not preserved")
	}
	if loaded.Theme == nil || loaded.Theme.Custom == nil || loaded.Theme.Custom.BG != "#111111" || loaded.Theme.Custom.FG != "#eeeeee" {
		t.Error("Theme.Custom not preserved")
	}
}

func TestLoadMayorConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadMayorConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestAccountsConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "accounts.json")

	original := NewAccountsConfig()
	original.Accounts["yegge"] = Account{
		Email:       "steve.yegge@gmail.com",
		Description: "Personal account",
		ConfigDir:   "~/.claude-accounts/yegge",
	}
	original.Accounts["ghosttrack"] = Account{
		Email:       "steve@ghosttrack.com",
		Description: "Business account",
		ConfigDir:   "~/.claude-accounts/ghosttrack",
	}
	original.Default = "ghosttrack"

	if err := SaveAccountsConfig(path, original); err != nil {
		t.Fatalf("SaveAccountsConfig: %v", err)
	}

	loaded, err := LoadAccountsConfig(path)
	if err != nil {
		t.Fatalf("LoadAccountsConfig: %v", err)
	}

	if loaded.Version != CurrentAccountsVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentAccountsVersion)
	}
	if len(loaded.Accounts) != 2 {
		t.Errorf("Accounts count = %d, want 2", len(loaded.Accounts))
	}
	if loaded.Default != "ghosttrack" {
		t.Errorf("Default = %q, want 'ghosttrack'", loaded.Default)
	}

	yegge := loaded.GetAccount("yegge")
	if yegge == nil {
		t.Fatal("GetAccount('yegge') returned nil")
	}
	if yegge.Email != "steve.yegge@gmail.com" {
		t.Errorf("yegge.Email = %q, want 'steve.yegge@gmail.com'", yegge.Email)
	}

	defAcct := loaded.GetDefaultAccount()
	if defAcct == nil {
		t.Fatal("GetDefaultAccount() returned nil")
	}
	if defAcct.Email != "steve@ghosttrack.com" {
		t.Errorf("default.Email = %q, want 'steve@ghosttrack.com'", defAcct.Email)
	}
}

func TestAccountsConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *AccountsConfig
		wantErr bool
	}{
		{
			name:    "valid empty config",
			config:  NewAccountsConfig(),
			wantErr: false,
		},
		{
			name: "valid config with accounts",
			config: &AccountsConfig{
				Version: 1,
				Accounts: map[string]Account{
					"test": {Email: "test@example.com", ConfigDir: "~/.claude-accounts/test"},
				},
				Default: "test",
			},
			wantErr: false,
		},
		{
			name: "default refers to nonexistent account",
			config: &AccountsConfig{
				Version: 1,
				Accounts: map[string]Account{
					"test": {Email: "test@example.com", ConfigDir: "~/.claude-accounts/test"},
				},
				Default: "nonexistent",
			},
			wantErr: true,
		},
		{
			name: "account missing config_dir",
			config: &AccountsConfig{
				Version: 1,
				Accounts: map[string]Account{
					"test": {Email: "test@example.com"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAccountsConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAccountsConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadAccountsConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadAccountsConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestMessagingConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "config", "messaging.json")

	original := NewMessagingConfig()
	original.Lists["oncall"] = []string{"mayor/", "gastown/witness"}
	original.Lists["cleanup"] = []string{"gastown/witness", "deacon/"}
	original.Queues["work/gastown"] = QueueConfig{
		Workers:   []string{"gastown/polecats/*"},
		MaxClaims: 5,
	}
	original.Announces["alerts"] = AnnounceConfig{
		Readers:     []string{"@town"},
		RetainCount: 100,
	}
	original.NudgeChannels["workers"] = []string{"gastown/polecats/*", "gastown/crew/*"}
	original.NudgeChannels["witnesses"] = []string{"*/witness"}

	if err := SaveMessagingConfig(path, original); err != nil {
		t.Fatalf("SaveMessagingConfig: %v", err)
	}

	loaded, err := LoadMessagingConfig(path)
	if err != nil {
		t.Fatalf("LoadMessagingConfig: %v", err)
	}

	if loaded.Type != "messaging" {
		t.Errorf("Type = %q, want 'messaging'", loaded.Type)
	}
	if loaded.Version != CurrentMessagingVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentMessagingVersion)
	}

	// Check lists
	if len(loaded.Lists) != 2 {
		t.Errorf("Lists count = %d, want 2", len(loaded.Lists))
	}
	if oncall, ok := loaded.Lists["oncall"]; !ok || len(oncall) != 2 {
		t.Error("oncall list not preserved")
	}

	// Check queues
	if len(loaded.Queues) != 1 {
		t.Errorf("Queues count = %d, want 1", len(loaded.Queues))
	}
	if q, ok := loaded.Queues["work/gastown"]; !ok || q.MaxClaims != 5 {
		t.Error("queue not preserved")
	}

	// Check announces
	if len(loaded.Announces) != 1 {
		t.Errorf("Announces count = %d, want 1", len(loaded.Announces))
	}
	if a, ok := loaded.Announces["alerts"]; !ok || a.RetainCount != 100 {
		t.Error("announce not preserved")
	}

	// Check nudge channels
	if len(loaded.NudgeChannels) != 2 {
		t.Errorf("NudgeChannels count = %d, want 2", len(loaded.NudgeChannels))
	}
	if workers, ok := loaded.NudgeChannels["workers"]; !ok || len(workers) != 2 {
		t.Error("workers nudge channel not preserved")
	}
	if witnesses, ok := loaded.NudgeChannels["witnesses"]; !ok || len(witnesses) != 1 {
		t.Error("witnesses nudge channel not preserved")
	}
}

func TestMessagingConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *MessagingConfig
		wantErr bool
	}{
		{
			name:    "valid empty config",
			config:  NewMessagingConfig(),
			wantErr: false,
		},
		{
			name: "valid config with lists",
			config: &MessagingConfig{
				Type:    "messaging",
				Version: 1,
				Lists: map[string][]string{
					"oncall": {"mayor/", "gastown/witness"},
				},
			},
			wantErr: false,
		},
		{
			name: "wrong type",
			config: &MessagingConfig{
				Type:    "wrong",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "future version rejected",
			config: &MessagingConfig{
				Type:    "messaging",
				Version: 999,
			},
			wantErr: true,
		},
		{
			name: "list with no recipients",
			config: &MessagingConfig{
				Version: 1,
				Lists: map[string][]string{
					"empty": {},
				},
			},
			wantErr: true,
		},
		{
			name: "queue with no workers",
			config: &MessagingConfig{
				Version: 1,
				Queues: map[string]QueueConfig{
					"work": {Workers: []string{}},
				},
			},
			wantErr: true,
		},
		{
			name: "queue with negative max_claims",
			config: &MessagingConfig{
				Version: 1,
				Queues: map[string]QueueConfig{
					"work": {Workers: []string{"worker/"}, MaxClaims: -1},
				},
			},
			wantErr: true,
		},
		{
			name: "announce with no readers",
			config: &MessagingConfig{
				Version: 1,
				Announces: map[string]AnnounceConfig{
					"alerts": {Readers: []string{}},
				},
			},
			wantErr: true,
		},
		{
			name: "announce with negative retain_count",
			config: &MessagingConfig{
				Version: 1,
				Announces: map[string]AnnounceConfig{
					"alerts": {Readers: []string{"@town"}, RetainCount: -1},
				},
			},
			wantErr: true,
		},
		{
			name: "valid config with nudge channels",
			config: &MessagingConfig{
				Type:    "messaging",
				Version: 1,
				NudgeChannels: map[string][]string{
					"workers": {"gastown/polecats/*", "gastown/crew/*"},
				},
			},
			wantErr: false,
		},
		{
			name: "nudge channel with no recipients",
			config: &MessagingConfig{
				Version: 1,
				NudgeChannels: map[string][]string{
					"empty": {},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMessagingConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateMessagingConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadMessagingConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadMessagingConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestLoadMessagingConfigMalformedJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "messaging.json")

	// Write malformed JSON
	if err := os.WriteFile(path, []byte("{not valid json"), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	_, err := LoadMessagingConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestLoadOrCreateMessagingConfig(t *testing.T) {
	t.Parallel()
	// Test creating default when not found
	config, err := LoadOrCreateMessagingConfig("/nonexistent/path.json")
	if err != nil {
		t.Fatalf("LoadOrCreateMessagingConfig: %v", err)
	}
	if config == nil {
		t.Fatal("expected non-nil config")
	}
	if config.Version != CurrentMessagingVersion {
		t.Errorf("Version = %d, want %d", config.Version, CurrentMessagingVersion)
	}

	// Test loading existing
	dir := t.TempDir()
	path := filepath.Join(dir, "messaging.json")
	original := NewMessagingConfig()
	original.Lists["test"] = []string{"mayor/"}
	if err := SaveMessagingConfig(path, original); err != nil {
		t.Fatalf("SaveMessagingConfig: %v", err)
	}

	loaded, err := LoadOrCreateMessagingConfig(path)
	if err != nil {
		t.Fatalf("LoadOrCreateMessagingConfig: %v", err)
	}
	if _, ok := loaded.Lists["test"]; !ok {
		t.Error("existing config not loaded")
	}
}

func TestMessagingConfigPath(t *testing.T) {
	t.Parallel()
	path := MessagingConfigPath("/home/user/gt")
	expected := "/home/user/gt/config/messaging.json"
	if filepath.ToSlash(path) != expected {
		t.Errorf("MessagingConfigPath = %q, want %q", path, expected)
	}
}

func TestRuntimeConfigDefaults(t *testing.T) {
	t.Parallel()
	rc := DefaultRuntimeConfig()
	if rc.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", rc.Provider, "claude")
	}
	if !isClaudeCommand(rc.Command) {
		t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
	}
	if len(rc.Args) != 1 || rc.Args[0] != "--dangerously-skip-permissions" {
		t.Errorf("Args = %v, want [--dangerously-skip-permissions]", rc.Args)
	}
	if rc.Session == nil || rc.Session.SessionIDEnv != "CLAUDE_SESSION_ID" {
		t.Errorf("SessionIDEnv = %q, want %q", rc.Session.SessionIDEnv, "CLAUDE_SESSION_ID")
	}
}

func TestRuntimeConfigBuildCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rc           *RuntimeConfig
		wantContains []string // Parts the command should contain
		isClaudeCmd  bool     // Whether command should be claude (or path to claude)
	}{
		{
			name:         "nil config uses defaults",
			rc:           nil,
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
		{
			name:         "default config",
			rc:           DefaultRuntimeConfig(),
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
		{
			name:         "custom command",
			rc:           &RuntimeConfig{Command: "aider", Args: []string{"--no-git"}},
			wantContains: []string{"aider", "--no-git"},
			isClaudeCmd:  false,
		},
		{
			name:         "multiple args",
			rc:           &RuntimeConfig{Command: "claude", Args: []string{"--model", "opus", "--no-confirm"}},
			wantContains: []string{"--model", "opus", "--no-confirm"},
			isClaudeCmd:  true,
		},
		{
			name:         "empty command uses default",
			rc:           &RuntimeConfig{Command: "", Args: nil},
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rc.BuildCommand()
			// Check command contains expected parts
			for _, part := range tt.wantContains {
				if !strings.Contains(got, part) {
					t.Errorf("BuildCommand() = %q, should contain %q", got, part)
				}
			}
			// Check if command starts with claude (or path to claude)
			if tt.isClaudeCmd {
				parts := strings.Fields(got)
				if len(parts) > 0 && !isClaudeCommand(parts[0]) {
					t.Errorf("BuildCommand() = %q, command should be claude or path to claude", got)
				}
			}
		})
	}
}

func TestRuntimeConfigBuildCommandWithPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		rc           *RuntimeConfig
		prompt       string
		wantContains []string // Parts the command should contain
		isClaudeCmd  bool     // Whether command should be claude (or path to claude)
	}{
		{
			name:         "no prompt",
			rc:           DefaultRuntimeConfig(),
			prompt:       "",
			wantContains: []string{"--dangerously-skip-permissions"},
			isClaudeCmd:  true,
		},
		{
			name:         "with prompt",
			rc:           DefaultRuntimeConfig(),
			prompt:       "gt prime",
			wantContains: []string{"--dangerously-skip-permissions", `"gt prime"`},
			isClaudeCmd:  true,
		},
		{
			name:         "prompt with quotes",
			rc:           DefaultRuntimeConfig(),
			prompt:       `Hello "world"`,
			wantContains: []string{"--dangerously-skip-permissions", `"Hello \"world\""`},
			isClaudeCmd:  true,
		},
		{
			name:         "config initial prompt used if no override",
			rc:           &RuntimeConfig{Command: "aider", Args: []string{}, InitialPrompt: "/help"},
			prompt:       "",
			wantContains: []string{"aider", `"/help"`},
			isClaudeCmd:  false,
		},
		{
			name:         "override takes precedence over config",
			rc:           &RuntimeConfig{Command: "aider", Args: []string{}, InitialPrompt: "/help"},
			prompt:       "custom prompt",
			wantContains: []string{"aider", `"custom prompt"`},
			isClaudeCmd:  false,
		},
		{
			name:         "copilot uses -i flag for prompt",
			rc:           &RuntimeConfig{Command: "copilot", Args: []string{"--yolo"}, PromptMode: "arg"},
			prompt:       "test prompt",
			wantContains: []string{"copilot", "--yolo", "-i", `"test prompt"`},
			isClaudeCmd:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.rc.BuildCommandWithPrompt(tt.prompt)
			// Check command contains expected parts
			for _, part := range tt.wantContains {
				if !strings.Contains(got, part) {
					t.Errorf("BuildCommandWithPrompt(%q) = %q, should contain %q", tt.prompt, got, part)
				}
			}
			// Check if command starts with claude (or path to claude)
			if tt.isClaudeCmd {
				parts := strings.Fields(got)
				if len(parts) > 0 && !isClaudeCommand(parts[0]) {
					t.Errorf("BuildCommandWithPrompt(%q) = %q, command should be claude or path to claude", tt.prompt, got)
				}
			}
		})
	}
}

func TestBuildAgentStartupCommand(t *testing.T) {
	// BuildAgentStartupCommand auto-detects town root from cwd when rigPath is empty.
	// Use a temp directory to ensure we exercise the fallback default config path.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmpWD := t.TempDir()
	if err := os.Chdir(tmpWD); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	// Test without rig config (uses defaults)
	// New signature: (role, rig, townRoot, rigPath, prompt)
	cmd := BuildAgentStartupCommand("witness", "gastown", "", "", "")

	// Should contain environment variables (via 'exec env') and claude command
	if !strings.Contains(cmd, "exec env") {
		t.Error("expected 'exec env' in command")
	}
	if !strings.Contains(cmd, "GT_ROLE=gastown/witness") {
		t.Error("expected GT_ROLE=gastown/witness in command")
	}
	if !strings.Contains(cmd, "BD_ACTOR=gastown/witness") {
		t.Error("expected BD_ACTOR in command")
	}
	parts := strings.Fields(cmd)
	if len(parts) < 2 || !isClaudeCommand(parts[len(parts)-2]) || parts[len(parts)-1] != "--dangerously-skip-permissions" {
		t.Error("expected claude command in output")
	}
}

func TestBuildPolecatStartupCommand(t *testing.T) {
	t.Parallel()
	cmd := BuildPolecatStartupCommand("gastown", "toast", "", "")

	if !strings.Contains(cmd, "GT_ROLE=gastown/polecats/toast") {
		t.Error("expected GT_ROLE=gastown/polecats/toast in command")
	}
	if !strings.Contains(cmd, "GT_RIG=gastown") {
		t.Error("expected GT_RIG=gastown in command")
	}
	if !strings.Contains(cmd, "GT_POLECAT=toast") {
		t.Error("expected GT_POLECAT=toast in command")
	}
	if !strings.Contains(cmd, "BD_ACTOR=gastown/polecats/toast") {
		t.Error("expected BD_ACTOR in command")
	}
}

func TestBuildCrewStartupCommand(t *testing.T) {
	t.Parallel()
	cmd := BuildCrewStartupCommand("gastown", "max", "", "")

	if !strings.Contains(cmd, "GT_ROLE=gastown/crew/max") {
		t.Error("expected GT_ROLE=gastown/crew/max in command")
	}
	if !strings.Contains(cmd, "GT_RIG=gastown") {
		t.Error("expected GT_RIG=gastown in command")
	}
	if !strings.Contains(cmd, "GT_CREW=max") {
		t.Error("expected GT_CREW=max in command")
	}
	if !strings.Contains(cmd, "BD_ACTOR=gastown/crew/max") {
		t.Error("expected BD_ACTOR in command")
	}
}

func TestResolveAgentConfigWithOverride(t *testing.T) {
	ResetRegistryForTesting()
	t.Cleanup(ResetRegistryForTesting)
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Town settings: default agent is gemini, plus a custom alias.
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	townSettings.Agents["claude-haiku"] = &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "haiku", "--dangerously-skip-permissions"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Rig settings: prefer codex unless overridden.
	rigSettings := NewRigSettings()
	rigSettings.Agent = "codex"
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("no override uses rig agent", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "codex" {
			t.Fatalf("name = %q, want %q", name, "codex")
		}
		if rc.Command != "codex" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "codex")
		}
	})

	t.Run("override uses built-in preset", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "gemini")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "gemini" {
			t.Fatalf("name = %q, want %q", name, "gemini")
		}
		if rc.Command != "gemini" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "gemini")
		}
	})

	t.Run("override uses custom agent alias", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "claude-haiku")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "claude-haiku" {
			t.Fatalf("name = %q, want %q", name, "claude-haiku")
		}
		if !isClaudeCommand(rc.Command) {
			t.Fatalf("rc.Command = %q, want claude or path ending in /claude", rc.Command)
		}
		got := rc.BuildCommand()
		// Check command includes expected flags (path to claude may vary)
		if !strings.Contains(got, "--model haiku") || !strings.Contains(got, "--dangerously-skip-permissions") {
			t.Fatalf("BuildCommand() = %q, want command with --model haiku and --dangerously-skip-permissions", got)
		}
	})

	t.Run("override uses custom codex hooks alias", func(t *testing.T) {
		townSettings := NewTownSettings()
		townSettings.Agents["codex-worker-hooks"] = &RuntimeConfig{
			Command:    "codex",
			Args:       []string{"--dangerously-bypass-approvals-and-sandbox"},
			PromptMode: "arg",
			Hooks: &RuntimeHooksConfig{
				Provider:     "codex",
				Dir:          ".codex",
				SettingsFile: "hooks.json",
			},
		}
		if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
			t.Fatalf("SaveTownSettings: %v", err)
		}

		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "codex-worker-hooks")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "codex-worker-hooks" {
			t.Fatalf("name = %q, want %q", name, "codex-worker-hooks")
		}
		if rc.Command != "codex" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "codex")
		}
		if rc.PromptMode != "arg" {
			t.Fatalf("rc.PromptMode = %q, want %q", rc.PromptMode, "arg")
		}
		if rc.Hooks == nil {
			t.Fatal("expected hooks config")
		}
		if rc.Hooks.Provider != "codex" || rc.Hooks.Dir != ".codex" || rc.Hooks.SettingsFile != "hooks.json" {
			t.Fatalf("unexpected hooks config: %+v", rc.Hooks)
		}
		args := rc.BuildArgsWithPrompt("start here")
		if len(args) == 0 || args[len(args)-1] != "start here" {
			t.Fatalf("BuildArgsWithPrompt should append prompt positionally, got %v", args)
		}
	})

	t.Run("unknown override errors", func(t *testing.T) {
		_, _, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "nope-not-an-agent")
		if err == nil {
			t.Fatal("expected error for unknown agent override")
		}
	})

	t.Run("override with subcommand", func(t *testing.T) {
		rc, name, err := ResolveAgentConfigWithOverride(townRoot, rigPath, "opencode acp")
		if err != nil {
			t.Fatalf("ResolveAgentConfigWithOverride: %v", err)
		}
		if name != "opencode" {
			t.Fatalf("name = %q, want %q", name, "opencode")
		}
		if rc.Command != "opencode" {
			t.Fatalf("rc.Command = %q, want %q", rc.Command, "opencode")
		}
		// Verify "acp" was appended to Args
		found := false
		for _, arg := range rc.Args {
			if arg == "acp" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("rc.Args = %v, want it to contain %q", rc.Args, "acp")
		}
	})
}

func TestBuildPolecatStartupCommandWithAgentOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// The rig settings file must exist for resolver calls that load it.
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildPolecatStartupCommandWithAgentOverride("testrig", "toast", rigPath, "", "gemini")
	if err != nil {
		t.Fatalf("BuildPolecatStartupCommandWithAgentOverride: %v", err)
	}
	if !strings.Contains(cmd, "GT_ROLE=testrig/polecats/toast") {
		t.Fatalf("expected GT_ROLE export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_RIG=testrig") {
		t.Fatalf("expected GT_RIG export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_POLECAT=toast") {
		t.Fatalf("expected GT_POLECAT export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "gemini --approval-mode yolo") {
		t.Fatalf("expected gemini command in output: %q", cmd)
	}
}

func TestBuildAgentStartupCommandWithAgentOverride(t *testing.T) {
	townRoot := t.TempDir()

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("WriteFile town.json: %v", err)
	}

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	originalWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(originalWd) })
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	t.Run("empty override uses default agent", func(t *testing.T) {
		// New signature: (role, rig, townRoot, rigPath, prompt, agentOverride)
		cmd, err := BuildAgentStartupCommandWithAgentOverride("mayor", "", "", "", "", "")
		if err != nil {
			t.Fatalf("BuildAgentStartupCommandWithAgentOverride: %v", err)
		}
		if !strings.Contains(cmd, "GT_ROLE=mayor") {
			t.Fatalf("expected GT_ROLE export in command: %q", cmd)
		}
		if !strings.Contains(cmd, "BD_ACTOR=mayor") {
			t.Fatalf("expected BD_ACTOR export in command: %q", cmd)
		}
		if !strings.Contains(cmd, "gemini --approval-mode yolo") {
			t.Fatalf("expected gemini command in output: %q", cmd)
		}
	})

	t.Run("override switches agent", func(t *testing.T) {
		// New signature: (role, rig, townRoot, rigPath, prompt, agentOverride)
		cmd, err := BuildAgentStartupCommandWithAgentOverride("mayor", "", "", "", "", "codex")
		if err != nil {
			t.Fatalf("BuildAgentStartupCommandWithAgentOverride: %v", err)
		}
		if !strings.Contains(cmd, "codex") {
			t.Fatalf("expected codex command in output: %q", cmd)
		}
	})
}

func TestBuildCrewStartupCommandWithAgentOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildCrewStartupCommandWithAgentOverride("testrig", "max", rigPath, "gt prime", "gemini")
	if err != nil {
		t.Fatalf("BuildCrewStartupCommandWithAgentOverride: %v", err)
	}
	if !strings.Contains(cmd, "GT_ROLE=testrig/crew/max") {
		t.Fatalf("expected GT_ROLE export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_RIG=testrig") {
		t.Fatalf("expected GT_RIG export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_CREW=max") {
		t.Fatalf("expected GT_CREW export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "BD_ACTOR=testrig/crew/max") {
		t.Fatalf("expected BD_ACTOR export in command: %q", cmd)
	}
	if !strings.Contains(cmd, "gemini --approval-mode yolo") {
		t.Fatalf("expected gemini command in output: %q", cmd)
	}
}

func TestBuildStartupCommand_UsesRigAgentWhenRigPathProvided(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	rigSettings.Agent = "codex"
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{"GT_ROLE": "witness"}, rigPath, "")
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected rig agent (codex) in command: %q", cmd)
	}
	if strings.Contains(cmd, "gemini --approval-mode yolo") {
		t.Fatalf("did not expect town default agent in command: %q", cmd)
	}
}

func TestBuildStartupCommand_UsesRoleAgentsFromTownSettings(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	binDir := t.TempDir()
	for _, name := range []string{"gemini", "codex"} {
		if runtime.GOOS == "windows" {
			path := filepath.Join(binDir, name+".cmd")
			if err := os.WriteFile(path, []byte("@echo off\r\nexit /b 0\r\n"), 0644); err != nil {
				t.Fatalf("write %s stub: %v", name, err)
			}
			continue
		}
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Configure town settings with role_agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "gemini",
		constants.RoleWitness:  "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings (no agent override)
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("refinery role gets gemini from role_agents", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleRefinery}, rigPath, "")
		if !strings.Contains(cmd, "gemini") {
			t.Fatalf("expected gemini for refinery role, got: %q", cmd)
		}
	})

	t.Run("witness role gets codex from role_agents", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleWitness}, rigPath, "")
		if !strings.Contains(cmd, "codex") {
			t.Fatalf("expected codex for witness role, got: %q", cmd)
		}
	})

	t.Run("crew role falls back to default_agent (not in role_agents)", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleCrew}, rigPath, "")
		if !strings.Contains(cmd, "claude") {
			t.Fatalf("expected claude fallback for crew role, got: %q", cmd)
		}
	})

	t.Run("no role falls back to default resolution", func(t *testing.T) {
		cmd := BuildStartupCommand(map[string]string{}, rigPath, "")
		if !strings.Contains(cmd, "claude") {
			t.Fatalf("expected claude for no role, got: %q", cmd)
		}
	})
}

func TestBuildStartupCommand_RigRoleAgentsOverridesTownRoleAgents(t *testing.T) {
	skipIfAgentBinaryMissing(t, "gemini", "codex")
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Town settings has witness = gemini
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleWitness: "gemini",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Rig settings overrides witness to codex
	rigSettings := NewRigSettings()
	rigSettings.RoleAgents = map[string]string{
		constants.RoleWitness: "codex",
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{"GT_ROLE": constants.RoleWitness}, rigPath, "")
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected codex from rig role_agents override, got: %q", cmd)
	}
	if strings.Contains(cmd, "gemini") {
		t.Fatalf("did not expect town role_agents (gemini) in command: %q", cmd)
	}
}

func TestBuildAgentStartupCommand_UsesRoleAgents(t *testing.T) {
	skipIfAgentBinaryMissing(t, "codex")
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure town settings with role_agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// BuildAgentStartupCommand passes role via GT_ROLE env var (compound format)
	cmd := BuildAgentStartupCommand(constants.RoleRefinery, "testrig", townRoot, rigPath, "")
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("expected codex for refinery role, got: %q", cmd)
	}
	if !strings.Contains(cmd, "GT_ROLE=testrig/refinery") {
		t.Fatalf("expected GT_ROLE=testrig/refinery in command: %q", cmd)
	}
}

func TestBuildAgentStartupCommand_DogUsesRoleAgents(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude-opus"
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-opus": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "opus"},
		},
		"claude-haiku": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "haiku"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		"dog": "claude-haiku",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	cmd := BuildAgentStartupCommand("dog", "", townRoot, "", "")
	if !strings.Contains(cmd, "GT_ROLE=dog") {
		t.Fatalf("expected GT_ROLE=dog in command, got: %q", cmd)
	}
	if !strings.Contains(cmd, "--model haiku") {
		t.Fatalf("expected --model haiku from role_agents[dog], got: %q", cmd)
	}
	if strings.Contains(cmd, "--model opus") {
		t.Fatalf("did not expect --model opus (default_agent) for dog role, got: %q", cmd)
	}
}

func TestValidateAgentConfig(t *testing.T) {
	t.Parallel()

	t.Run("valid built-in agent", func(t *testing.T) {
		// claude is a built-in preset and binary should exist
		err := ValidateAgentConfig("claude", nil, nil)
		// Note: This may fail if claude binary is not installed, which is expected
		if err != nil && !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("unexpected error for claude: %v", err)
		}
	})

	t.Run("invalid agent name", func(t *testing.T) {
		err := ValidateAgentConfig("nonexistent-agent-xyz", nil, nil)
		if err == nil {
			t.Error("expected error for nonexistent agent")
		}
		if !strings.Contains(err.Error(), "not found in config or built-in presets") {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("custom agent with missing binary", func(t *testing.T) {
		townSettings := NewTownSettings()
		townSettings.Agents = map[string]*RuntimeConfig{
			"my-custom-agent": {
				Command: "nonexistent-binary-xyz123",
				Args:    []string{"--some-flag"},
			},
		}
		err := ValidateAgentConfig("my-custom-agent", townSettings, nil)
		if err == nil {
			t.Error("expected error for missing binary")
		}
		if !strings.Contains(err.Error(), "not found in PATH") {
			t.Errorf("unexpected error message: %v", err)
		}
	})
}

func TestResolveRoleAgentConfig_FallsBackOnInvalidAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure town settings with an invalid agent for refinery
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "nonexistent-agent-xyz", // Invalid agent
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Should fall back to default (claude) when agent is invalid
	rc := ResolveRoleAgentConfig(constants.RoleRefinery, townRoot, rigPath)
	// Command can be "claude" or a resolved platform-specific claude binary path.
	if !isClaudeCommand(rc.Command) {
		t.Errorf("expected fallback to claude or path ending in /claude, got: %s", rc.Command)
	}
}

func TestGetRuntimeCommand_UsesRigAgentWhenRigPathProvided(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	rigSettings.Agent = "codex"
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := GetRuntimeCommand(rigPath)
	if !strings.HasPrefix(cmd, "codex") {
		t.Fatalf("GetRuntimeCommand() = %q, want prefix %q", cmd, "codex")
	}
}

func TestExpectedPaneCommands(t *testing.T) {
	t.Parallel()
	t.Run("claude maps to node and claude", func(t *testing.T) {
		got := ExpectedPaneCommands(&RuntimeConfig{Command: "claude"})
		want := []string{"node", "claude"}
		if len(got) != 2 || got[0] != "node" || got[1] != "claude" {
			t.Fatalf("ExpectedPaneCommands(claude) = %v, want %v", got, want)
		}
	})

	t.Run("codex maps to executable", func(t *testing.T) {
		got := ExpectedPaneCommands(&RuntimeConfig{Command: "codex"})
		if len(got) != 1 || got[0] != "codex" {
			t.Fatalf("ExpectedPaneCommands(codex) = %v, want %v", got, []string{"codex"})
		}
	})
}

func TestResolveRoleAgentConfigFromRigSettings(t *testing.T) {
	t.Parallel()
	// Create temp town with rig containing custom runtime config
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}

	settings := NewRigSettings()
	settings.Runtime = &RuntimeConfig{
		Command:  "aider",
		Provider: "aider",
		Args:     []string{"--no-git", "--model", "claude-3"},
	}
	if err := SaveRigSettings(filepath.Join(settingsDir, "config.json"), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	// Load and verify using ResolveRoleAgentConfig
	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	if rc.Command != "aider" {
		t.Errorf("Command = %q, want %q", rc.Command, "aider")
	}
	if len(rc.Args) != 3 {
		t.Errorf("Args = %v, want 3 args", rc.Args)
	}

	cmd := rc.BuildCommand()
	if cmd != "aider --no-git --model claude-3" {
		t.Errorf("BuildCommand() = %q, want %q", cmd, "aider --no-git --model claude-3")
	}
}

func TestResolveRoleAgentConfigFallsBackToDefaults(t *testing.T) {
	t.Parallel()
	// Non-existent paths should use defaults
	rc := ResolveRoleAgentConfig("polecat", "/nonexistent/town", "/nonexistent/rig")
	if !isClaudeCommand(rc.Command) {
		t.Errorf("Command = %q, want claude or path ending in /claude (default)", rc.Command)
	}
}

func TestResolveWorkerAgentConfig_WorkerSpecificOverridesRole(t *testing.T) {
	// Cannot use t.Parallel — uses t.Setenv
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	// Create a fake codex binary so ValidateAgentConfig passes
	binDir := t.TempDir()
	writeAgentStub(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	settings := NewRigSettings()
	settings.RoleAgents = map[string]string{constants.RoleCrew: "claude"}
	settings.WorkerAgents = map[string]string{"denali": "codex"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	rc := ResolveWorkerAgentConfig("denali", townRoot, rigPath)
	if rc.Provider != "codex" && !strings.Contains(rc.Command, "codex") {
		t.Errorf("expected codex for worker denali, got provider=%q command=%q", rc.Provider, rc.Command)
	}
}

func TestResolveWorkerAgentConfig_FallsBackToRoleAgents(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	settings := NewRigSettings()
	settings.RoleAgents = map[string]string{constants.RoleCrew: "claude"}
	settings.WorkerAgents = map[string]string{"denali": "codex"} // only denali is overridden
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	// "glacier" is not in worker_agents — should fall through to role_agents["crew"] = claude
	rc := ResolveWorkerAgentConfig("glacier", townRoot, rigPath)
	if !isClaudeCommand(rc.Command) {
		t.Errorf("expected claude fallback for glacier (not in worker_agents), got command=%q", rc.Command)
	}
}

func TestResolveWorkerAgentConfig_EmptyWorkerNameFallsBackToRole(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	settings := NewRigSettings()
	settings.WorkerAgents = map[string]string{"denali": "codex"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	// Empty worker name should fall back to crew role resolution (claude default)
	rc := ResolveWorkerAgentConfig("", townRoot, rigPath)
	if !isClaudeCommand(rc.Command) {
		t.Errorf("expected claude for empty worker name, got command=%q", rc.Command)
	}
}

func TestBuildStartupCommand_WorkerAgentsViaCrew(t *testing.T) {
	// Cannot use t.Parallel — uses t.Setenv
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	// Create a fake codex binary
	binDir := t.TempDir()
	writeAgentStub(t, binDir, "codex")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	settings := NewRigSettings()
	settings.WorkerAgents = map[string]string{"denali": "codex"}
	if err := SaveRigSettings(RigSettingsPath(rigPath), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	t.Run("crew worker with worker_agents entry uses codex", func(t *testing.T) {
		envVars := map[string]string{
			"GT_ROLE": constants.RoleCrew,
			"GT_CREW": "denali",
		}
		cmd := BuildStartupCommand(envVars, rigPath, "")
		if !strings.Contains(cmd, "codex") {
			t.Errorf("expected codex for crew worker denali, got: %q", cmd)
		}
	})

	t.Run("crew worker without worker_agents entry falls back to default", func(t *testing.T) {
		envVars := map[string]string{
			"GT_ROLE": constants.RoleCrew,
			"GT_CREW": "glacier",
		}
		cmd := BuildStartupCommand(envVars, rigPath, "")
		if strings.Contains(cmd, "codex") {
			t.Errorf("expected non-codex for crew worker glacier (not in worker_agents), got: %q", cmd)
		}
	})

	t.Run("crew role without GT_CREW falls back to role resolution", func(t *testing.T) {
		envVars := map[string]string{
			"GT_ROLE": constants.RoleCrew,
		}
		cmd := BuildStartupCommand(envVars, rigPath, "")
		if strings.Contains(cmd, "codex") {
			t.Errorf("expected non-codex when GT_CREW not set, got: %q", cmd)
		}
	})
}

func TestResolveWorkerAgentConfig_TownCrewAgents(t *testing.T) {
	// Cannot use t.Parallel — uses t.Setenv
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")

	// Create fake agent binaries (needs .exe on Windows for exec.LookPath).
	// Both codex and claude stubs are needed: codex for town crew_agents,
	// claude for the rig worker_agents override subtest.
	binDir := t.TempDir()
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	for _, name := range []string{"codex", "claude"} {
		stubPath := filepath.Join(binDir, name+ext)
		if err := os.WriteFile(stubPath, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	// Set up town settings with crew_agents but NO rig worker_agents
	townSettings := NewTownSettings()
	townSettings.CrewAgents = map[string]string{"bob": "codex"}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("saving town settings: %v", err)
	}

	// Save rig settings without worker_agents
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("saving rig settings: %v", err)
	}

	t.Run("town crew_agents resolves for named worker", func(t *testing.T) {
		rc := ResolveWorkerAgentConfig("bob", townRoot, rigPath)
		if rc.Provider != "codex" && !strings.Contains(rc.Command, "codex") {
			t.Errorf("expected codex for crew worker bob via town crew_agents, got provider=%q command=%q", rc.Provider, rc.Command)
		}
	})

	t.Run("worker not in town crew_agents falls through to defaults", func(t *testing.T) {
		rc := ResolveWorkerAgentConfig("alice", townRoot, rigPath)
		if !isClaudeCommand(rc.Command) {
			t.Errorf("expected claude fallback for alice (not in crew_agents), got command=%q", rc.Command)
		}
	})

	t.Run("rig worker_agents takes priority over town crew_agents", func(t *testing.T) {
		// Add rig-level worker_agents that should override town crew_agents
		rigSettings2 := NewRigSettings()
		rigSettings2.WorkerAgents = map[string]string{"bob": "claude"}
		if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings2); err != nil {
			t.Fatalf("saving rig settings: %v", err)
		}
		rc := ResolveWorkerAgentConfig("bob", townRoot, rigPath)
		if !isClaudeCommand(rc.Command) {
			t.Errorf("expected claude for bob (rig worker_agents should override town crew_agents), got command=%q", rc.Command)
		}
	})
}

func TestIsClaudeAgent(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		rc       *RuntimeConfig
		expected bool
	}{
		{"empty provider and command (defaults)", &RuntimeConfig{}, true},
		{"explicit claude provider", &RuntimeConfig{Provider: "claude", Command: "anything"}, true},
		{"explicit codex provider", &RuntimeConfig{Provider: "codex", Command: "claude"}, false},
		{"bare claude command", &RuntimeConfig{Command: "claude"}, true},
		{"path to claude binary", &RuntimeConfig{Command: "/usr/local/bin/claude"}, true},
		{"aider command no provider", &RuntimeConfig{Command: "aider"}, false},
		{"generic provider", &RuntimeConfig{Provider: "generic"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClaudeAgent(tt.rc); got != tt.expected {
				t.Errorf("isClaudeAgent(%+v) = %v, want %v", tt.rc, got, tt.expected)
			}
		})
	}
}

func TestWithRoleSettingsFlag_SkipsNonClaude(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}

	// Configure aider (non-Claude agent) for polecat role
	settings := NewRigSettings()
	settings.Runtime = &RuntimeConfig{
		Command: "aider",
		Args:    []string{"--no-git", "--model", "claude-3"},
	}
	if err := SaveRigSettings(filepath.Join(settingsDir, "config.json"), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	// Should NOT contain --settings since aider is not a Claude agent
	for _, arg := range rc.Args {
		if arg == "--settings" {
			t.Errorf("non-Claude agent 'aider' should not get --settings flag, but Args = %v", rc.Args)
			break
		}
	}
}

func TestWithRoleSettingsFlag_InjectsForClaude(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "myrig")
	settingsDir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("creating settings dir: %v", err)
	}

	// Default config (Claude agent) for polecat role
	settings := NewRigSettings()
	if err := SaveRigSettings(filepath.Join(settingsDir, "config.json"), settings); err != nil {
		t.Fatalf("saving settings: %v", err)
	}

	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	// Should contain --settings since default agent is Claude
	found := false
	for _, arg := range rc.Args {
		if arg == "--settings" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("default Claude agent should get --settings flag for polecat role, but Args = %v", rc.Args)
	}
}

func TestRoleSettingsDir(t *testing.T) {
	t.Parallel()
	rigPath := "/fake/rig"
	tests := []struct {
		role string
		want string
	}{
		{"crew", filepath.Join(rigPath, "crew")},
		{"witness", filepath.Join(rigPath, "witness")},
		{"refinery", filepath.Join(rigPath, "refinery")},
		{"polecat", filepath.Join(rigPath, "polecats")},
		{"mayor", ""},
		{"deacon", ""},
		{"", ""},
	}
	for _, tt := range tests {
		got := RoleSettingsDir(tt.role, rigPath)
		if got != tt.want {
			t.Errorf("RoleSettingsDir(%q, %q) = %q, want %q", tt.role, rigPath, got, tt.want)
		}
	}
}

func TestDaemonPatrolConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "mayor", "daemon.json")

	original := NewDaemonPatrolConfig()
	original.Patrols["custom"] = PatrolConfig{
		Enabled:  true,
		Interval: "10m",
		Agent:    "custom-agent",
	}

	if err := SaveDaemonPatrolConfig(path, original); err != nil {
		t.Fatalf("SaveDaemonPatrolConfig: %v", err)
	}

	loaded, err := LoadDaemonPatrolConfig(path)
	if err != nil {
		t.Fatalf("LoadDaemonPatrolConfig: %v", err)
	}

	if loaded.Type != "daemon-patrol-config" {
		t.Errorf("Type = %q, want 'daemon-patrol-config'", loaded.Type)
	}
	if loaded.Version != CurrentDaemonPatrolConfigVersion {
		t.Errorf("Version = %d, want %d", loaded.Version, CurrentDaemonPatrolConfigVersion)
	}
	if loaded.Heartbeat == nil || !loaded.Heartbeat.Enabled {
		t.Error("Heartbeat not preserved")
	}
	if len(loaded.Patrols) != 4 {
		t.Errorf("Patrols count = %d, want 4", len(loaded.Patrols))
	}
	if custom, ok := loaded.Patrols["custom"]; !ok || custom.Agent != "custom-agent" {
		t.Error("custom patrol not preserved")
	}
}

func TestDaemonPatrolConfigValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		config  *DaemonPatrolConfig
		wantErr bool
	}{
		{
			name:    "valid default config",
			config:  NewDaemonPatrolConfig(),
			wantErr: false,
		},
		{
			name: "valid minimal config",
			config: &DaemonPatrolConfig{
				Type:    "daemon-patrol-config",
				Version: 1,
			},
			wantErr: false,
		},
		{
			name: "wrong type",
			config: &DaemonPatrolConfig{
				Type:    "wrong",
				Version: 1,
			},
			wantErr: true,
		},
		{
			name: "future version rejected",
			config: &DaemonPatrolConfig{
				Type:    "daemon-patrol-config",
				Version: 999,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateDaemonPatrolConfig(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateDaemonPatrolConfig() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDaemonPatrolConfigNotFound(t *testing.T) {
	t.Parallel()
	_, err := LoadDaemonPatrolConfig("/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestDaemonPatrolConfigPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		townRoot string
		expected string
	}{
		{"/home/user/gt", "/home/user/gt/mayor/daemon.json"},
		{"/var/lib/gastown", "/var/lib/gastown/mayor/daemon.json"},
		{"/tmp/test-workspace", "/tmp/test-workspace/mayor/daemon.json"},
		{"~/gt", "~/gt/mayor/daemon.json"},
	}

	for _, tt := range tests {
		t.Run(tt.townRoot, func(t *testing.T) {
			path := DaemonPatrolConfigPath(tt.townRoot)
			if filepath.ToSlash(path) != filepath.ToSlash(tt.expected) {
				t.Errorf("DaemonPatrolConfigPath(%q) = %q, want %q", tt.townRoot, path, tt.expected)
			}
		})
	}
}

func TestEnsureDaemonPatrolConfig(t *testing.T) {
	t.Parallel()
	t.Run("creates config if missing", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "mayor"), 0755); err != nil {
			t.Fatalf("creating mayor dir: %v", err)
		}

		err := EnsureDaemonPatrolConfig(dir)
		if err != nil {
			t.Fatalf("EnsureDaemonPatrolConfig: %v", err)
		}

		path := DaemonPatrolConfigPath(dir)
		loaded, err := LoadDaemonPatrolConfig(path)
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}
		if loaded.Type != "daemon-patrol-config" {
			t.Errorf("Type = %q, want 'daemon-patrol-config'", loaded.Type)
		}
		if len(loaded.Patrols) != 3 {
			t.Errorf("Patrols count = %d, want 3 (deacon, witness, refinery)", len(loaded.Patrols))
		}
	})

	t.Run("preserves existing config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "mayor", "daemon.json")

		existing := &DaemonPatrolConfig{
			Type:    "daemon-patrol-config",
			Version: 1,
			Patrols: map[string]PatrolConfig{
				"custom-only": {Enabled: true, Agent: "custom"},
			},
		}
		if err := SaveDaemonPatrolConfig(path, existing); err != nil {
			t.Fatalf("SaveDaemonPatrolConfig: %v", err)
		}

		err := EnsureDaemonPatrolConfig(dir)
		if err != nil {
			t.Fatalf("EnsureDaemonPatrolConfig: %v", err)
		}

		loaded, err := LoadDaemonPatrolConfig(path)
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}
		if len(loaded.Patrols) != 1 {
			t.Errorf("Patrols count = %d, want 1 (should preserve existing)", len(loaded.Patrols))
		}
		if _, ok := loaded.Patrols["custom-only"]; !ok {
			t.Error("existing custom patrol was overwritten")
		}
	})

}

func TestNewDaemonPatrolConfig(t *testing.T) {
	t.Parallel()
	cfg := NewDaemonPatrolConfig()

	if cfg.Type != "daemon-patrol-config" {
		t.Errorf("Type = %q, want 'daemon-patrol-config'", cfg.Type)
	}
	if cfg.Version != CurrentDaemonPatrolConfigVersion {
		t.Errorf("Version = %d, want %d", cfg.Version, CurrentDaemonPatrolConfigVersion)
	}
	if cfg.Heartbeat == nil {
		t.Fatal("Heartbeat is nil")
	}
	if !cfg.Heartbeat.Enabled {
		t.Error("Heartbeat.Enabled should be true by default")
	}
	if cfg.Heartbeat.Interval != "3m" {
		t.Errorf("Heartbeat.Interval = %q, want '3m'", cfg.Heartbeat.Interval)
	}
	if len(cfg.Patrols) != 3 {
		t.Errorf("Patrols count = %d, want 3", len(cfg.Patrols))
	}

	for _, name := range []string{"deacon", "witness", "refinery"} {
		patrol, ok := cfg.Patrols[name]
		if !ok {
			t.Errorf("missing %s patrol", name)
			continue
		}
		if !patrol.Enabled {
			t.Errorf("%s patrol should be enabled by default", name)
		}
		if patrol.Agent != name {
			t.Errorf("%s patrol Agent = %q, want %q", name, patrol.Agent, name)
		}
	}
}

func TestAddRigToDaemonPatrols(t *testing.T) {
	t.Parallel()

	t.Run("adds rig to witness and refinery", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Write a daemon.json with existing rigs
		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "heartbeat": {"enabled": true, "interval": "3m"},
  "patrols": {
    "witness": {"enabled": true, "interval": "5m", "agent": "witness", "rigs": ["gastown"]},
    "refinery": {"enabled": true, "interval": "5m", "agent": "refinery", "rigs": ["gastown"]},
    "deacon": {"enabled": true, "interval": "5m", "agent": "deacon"}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		// Reload and verify
		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 2 || witness.Rigs[0] != "gastown" || witness.Rigs[1] != "newrig" {
			t.Errorf("witness rigs = %v, want [gastown newrig]", witness.Rigs)
		}

		refinery := cfg.Patrols["refinery"]
		if len(refinery.Rigs) != 2 || refinery.Rigs[0] != "gastown" || refinery.Rigs[1] != "newrig" {
			t.Errorf("refinery rigs = %v, want [gastown newrig]", refinery.Rigs)
		}

		// Deacon should be untouched
		deacon := cfg.Patrols["deacon"]
		if len(deacon.Rigs) != 0 {
			t.Errorf("deacon rigs = %v, want empty", deacon.Rigs)
		}
	})

	t.Run("skips duplicate rig", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown", "beads"]},
    "refinery": {"enabled": true, "rigs": ["gastown", "beads"]}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "gastown"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 2 {
			t.Errorf("witness rigs = %v, want [gastown beads] (no duplicate)", witness.Rigs)
		}
	})

	t.Run("preserves dolt_server fields", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "dolt_server": {"enabled": true, "port": 3307, "host": "127.0.0.1", "data_dir": "/tmp/dolt"},
    "witness": {"enabled": true, "rigs": ["gastown"]},
    "refinery": {"enabled": true, "rigs": ["gastown"]}
  }
}`
		path := filepath.Join(mayorDir, "daemon.json")
		if err := os.WriteFile(path, []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		// Read raw JSON to verify dolt_server fields are preserved
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}

		var patrols map[string]json.RawMessage
		if err := json.Unmarshal(raw["patrols"], &patrols); err != nil {
			t.Fatal(err)
		}

		var doltServer map[string]interface{}
		if err := json.Unmarshal(patrols["dolt_server"], &doltServer); err != nil {
			t.Fatal(err)
		}

		if doltServer["port"].(float64) != 3307 {
			t.Errorf("dolt_server port = %v, want 3307", doltServer["port"])
		}
		if doltServer["host"].(string) != "127.0.0.1" {
			t.Errorf("dolt_server host = %v, want 127.0.0.1", doltServer["host"])
		}
		if doltServer["data_dir"].(string) != "/tmp/dolt" {
			t.Errorf("dolt_server data_dir = %v, want /tmp/dolt", doltServer["data_dir"])
		}
	})

	t.Run("no-op when daemon.json missing", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}
		// Should not create the file
		if _, err := os.Stat(filepath.Join(townRoot, "mayor", "daemon.json")); !os.IsNotExist(err) {
			t.Error("expected daemon.json to not exist")
		}
	})

	t.Run("no-op when no patrols section", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{"type": "daemon-patrol-config", "version": 1}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}
	})
}

func TestRemoveRigFromDaemonPatrols(t *testing.T) {
	t.Parallel()

	t.Run("removes rig from witness and refinery", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown", "beads", "myrig"]},
    "refinery": {"enabled": true, "rigs": ["gastown", "beads", "myrig"]},
    "deacon": {"enabled": true}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 2 || witness.Rigs[0] != "gastown" || witness.Rigs[1] != "myrig" {
			t.Errorf("witness rigs = %v, want [gastown myrig]", witness.Rigs)
		}

		refinery := cfg.Patrols["refinery"]
		if len(refinery.Rigs) != 2 || refinery.Rigs[0] != "gastown" || refinery.Rigs[1] != "myrig" {
			t.Errorf("refinery rigs = %v, want [gastown myrig]", refinery.Rigs)
		}
	})

	t.Run("no-op when rig not present", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown"]},
    "refinery": {"enabled": true, "rigs": ["gastown"]}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "nonexistent"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 1 || witness.Rigs[0] != "gastown" {
			t.Errorf("witness rigs = %v, want [gastown]", witness.Rigs)
		}
	})

	t.Run("preserves dolt_server fields", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "dolt_server": {"enabled": true, "port": 3307, "host": "127.0.0.1"},
    "witness": {"enabled": true, "rigs": ["gastown", "beads"]},
    "refinery": {"enabled": true, "rigs": ["gastown", "beads"]}
  }
}`
		path := filepath.Join(mayorDir, "daemon.json")
		if err := os.WriteFile(path, []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}

		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatal(err)
		}

		var patrols map[string]json.RawMessage
		if err := json.Unmarshal(raw["patrols"], &patrols); err != nil {
			t.Fatal(err)
		}

		var doltServer map[string]interface{}
		if err := json.Unmarshal(patrols["dolt_server"], &doltServer); err != nil {
			t.Fatal(err)
		}

		if doltServer["port"].(float64) != 3307 {
			t.Errorf("dolt_server port = %v, want 3307", doltServer["port"])
		}
	})

	t.Run("no-op when daemon.json missing", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}
	})

	t.Run("no-op when no patrols section", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{"type": "daemon-patrol-config", "version": 1}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		if err := RemoveRigFromDaemonPatrols(townRoot, "beads"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}
	})

	t.Run("roundtrip add then remove", func(t *testing.T) {
		t.Parallel()
		townRoot := t.TempDir()
		mayorDir := filepath.Join(townRoot, "mayor")
		if err := os.MkdirAll(mayorDir, 0755); err != nil {
			t.Fatal(err)
		}

		daemonJSON := `{
  "type": "daemon-patrol-config",
  "version": 1,
  "patrols": {
    "witness": {"enabled": true, "rigs": ["gastown"]},
    "refinery": {"enabled": true, "rigs": ["gastown"]}
  }
}`
		if err := os.WriteFile(filepath.Join(mayorDir, "daemon.json"), []byte(daemonJSON), 0644); err != nil {
			t.Fatal(err)
		}

		// Add a rig
		if err := AddRigToDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("AddRigToDaemonPatrols: %v", err)
		}

		// Remove it
		if err := RemoveRigFromDaemonPatrols(townRoot, "newrig"); err != nil {
			t.Fatalf("RemoveRigFromDaemonPatrols: %v", err)
		}

		cfg, err := LoadDaemonPatrolConfig(DaemonPatrolConfigPath(townRoot))
		if err != nil {
			t.Fatalf("LoadDaemonPatrolConfig: %v", err)
		}

		witness := cfg.Patrols["witness"]
		if len(witness.Rigs) != 1 || witness.Rigs[0] != "gastown" {
			t.Errorf("witness rigs = %v, want [gastown]", witness.Rigs)
		}

		refinery := cfg.Patrols["refinery"]
		if len(refinery.Rigs) != 1 || refinery.Rigs[0] != "gastown" {
			t.Errorf("refinery rigs = %v, want [gastown]", refinery.Rigs)
		}
	})
}

func TestSaveTownSettings(t *testing.T) {
	t.Parallel()
	t.Run("saves valid town settings", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "settings", "config.json")

		settings := &TownSettings{
			Type:         "town-settings",
			Version:      CurrentTownSettingsVersion,
			DefaultAgent: "gemini",
			Agents: map[string]*RuntimeConfig{
				"my-agent": {
					Command: "my-agent",
					Args:    []string{"--arg1", "--arg2"},
				},
			},
		}

		err := SaveTownSettings(settingsPath, settings)
		if err != nil {
			t.Fatalf("SaveTownSettings failed: %v", err)
		}

		// Verify file exists
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("reading settings file: %v", err)
		}

		// Verify it contains expected content
		content := string(data)
		if !strings.Contains(content, `"type": "town-settings"`) {
			t.Errorf("missing type field")
		}
		if !strings.Contains(content, `"default_agent": "gemini"`) {
			t.Errorf("missing default_agent field")
		}
		if !strings.Contains(content, `"my-agent"`) {
			t.Errorf("missing custom agent")
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "deeply", "nested", "settings", "config.json")

		settings := NewTownSettings()

		err := SaveTownSettings(settingsPath, settings)
		if err != nil {
			t.Fatalf("SaveTownSettings failed: %v", err)
		}

		// Verify file exists
		if _, err := os.Stat(settingsPath); err != nil {
			t.Errorf("settings file not created: %v", err)
		}
	})

	t.Run("rejects invalid type", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "config.json")

		settings := &TownSettings{
			Type:    "invalid-type",
			Version: CurrentTownSettingsVersion,
		}

		err := SaveTownSettings(settingsPath, settings)
		if err == nil {
			t.Error("expected error for invalid type")
		}
	})

	t.Run("rejects unsupported version", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "config.json")

		settings := &TownSettings{
			Type:    "town-settings",
			Version: CurrentTownSettingsVersion + 100,
		}

		err := SaveTownSettings(settingsPath, settings)
		if err == nil {
			t.Error("expected error for unsupported version")
		}
	})

	t.Run("roundtrip save and load", func(t *testing.T) {
		tmpDir := t.TempDir()
		settingsPath := filepath.Join(tmpDir, "config.json")

		original := &TownSettings{
			Type:         "town-settings",
			Version:      CurrentTownSettingsVersion,
			DefaultAgent: "codex",
			Agents: map[string]*RuntimeConfig{
				"custom-1": {
					Command: "custom-agent",
					Args:    []string{"--flag"},
				},
			},
		}

		err := SaveTownSettings(settingsPath, original)
		if err != nil {
			t.Fatalf("SaveTownSettings failed: %v", err)
		}

		loaded, err := LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("LoadOrCreateTownSettings failed: %v", err)
		}

		if loaded.Type != original.Type {
			t.Errorf("Type = %q, want %q", loaded.Type, original.Type)
		}
		if loaded.Version != original.Version {
			t.Errorf("Version = %d, want %d", loaded.Version, original.Version)
		}
		if loaded.DefaultAgent != original.DefaultAgent {
			t.Errorf("DefaultAgent = %q, want %q", loaded.DefaultAgent, original.DefaultAgent)
		}

		if len(loaded.Agents) != len(original.Agents) {
			t.Errorf("Agents count = %d, want %d", len(loaded.Agents), len(original.Agents))
		}
	})
}

func TestGetDefaultFormula(t *testing.T) {
	t.Parallel()
	t.Run("returns empty string for nonexistent rig", func(t *testing.T) {
		result := GetDefaultFormula("/nonexistent/path")
		if result != "" {
			t.Errorf("GetDefaultFormula() = %q, want empty string", result)
		}
	})

	t.Run("returns empty string when no workflow config", func(t *testing.T) {
		dir := t.TempDir()
		settings := NewRigSettings()
		if err := SaveRigSettings(RigSettingsPath(dir), settings); err != nil {
			t.Fatalf("SaveRigSettings: %v", err)
		}

		result := GetDefaultFormula(dir)
		if result != "" {
			t.Errorf("GetDefaultFormula() = %q, want empty string", result)
		}
	})

	t.Run("returns default formula when configured", func(t *testing.T) {
		dir := t.TempDir()
		settings := NewRigSettings()
		settings.Workflow = &WorkflowConfig{
			DefaultFormula: "shiny",
		}
		if err := SaveRigSettings(RigSettingsPath(dir), settings); err != nil {
			t.Fatalf("SaveRigSettings: %v", err)
		}

		result := GetDefaultFormula(dir)
		if result != "shiny" {
			t.Errorf("GetDefaultFormula() = %q, want %q", result, "shiny")
		}
	})
}

// TestLookupAgentConfigWithRigSettings verifies that lookupAgentConfig checks
// rig-level agents first, then town-level agents, then built-ins.
func TestLookupAgentConfigWithRigSettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		rigSettings     *RigSettings
		townSettings    *TownSettings
		expectedCommand string
		expectedFrom    string
	}{
		{
			name: "rig-custom-agent",
			rigSettings: &RigSettings{
				Agent: "default-rig-agent",
				Agents: map[string]*RuntimeConfig{
					"rig-custom-agent": {
						Command: "custom-rig-cmd",
						Args:    []string{"--rig-flag"},
					},
				},
			},
			townSettings: &TownSettings{
				Agents: map[string]*RuntimeConfig{
					"town-custom-agent": {
						Command: "custom-town-cmd",
						Args:    []string{"--town-flag"},
					},
				},
			},
			expectedCommand: "custom-rig-cmd",
			expectedFrom:    "rig",
		},
		{
			name: "town-custom-agent",
			rigSettings: &RigSettings{
				Agents: map[string]*RuntimeConfig{
					"other-rig-agent": {
						Command: "other-rig-cmd",
					},
				},
			},
			townSettings: &TownSettings{
				Agents: map[string]*RuntimeConfig{
					"town-custom-agent": {
						Command: "custom-town-cmd",
						Args:    []string{"--town-flag"},
					},
				},
			},
			expectedCommand: "custom-town-cmd",
			expectedFrom:    "town",
		},
		{
			name:            "unknown-agent",
			rigSettings:     nil,
			townSettings:    nil,
			expectedCommand: "claude",
			expectedFrom:    "builtin",
		},
		{
			name: "claude",
			rigSettings: &RigSettings{
				Agent: "claude",
			},
			townSettings: &TownSettings{
				Agents: map[string]*RuntimeConfig{
					"claude": {
						Command: "custom-claude",
					},
				},
			},
			expectedCommand: "custom-claude",
			expectedFrom:    "town",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rc := lookupAgentConfig(tt.name, tt.townSettings, tt.rigSettings)

			if rc == nil {
				t.Errorf("lookupAgentConfig(%s) returned nil", tt.name)
			}

			// For claude commands, allow either "claude" or path ending in /claude
			if tt.expectedCommand == "claude" {
				if !isClaudeCommand(rc.Command) {
					t.Errorf("lookupAgentConfig(%s).Command = %s, want claude or path ending in /claude", tt.name, rc.Command)
				}
			} else if rc.Command != tt.expectedCommand {
				t.Errorf("lookupAgentConfig(%s).Command = %s, want %s", tt.name, rc.Command, tt.expectedCommand)
			}
		})
	}
}

// TestFillRuntimeDefaults tests the fillRuntimeDefaults function comprehensively.
func TestFillRuntimeDefaults(t *testing.T) {
	t.Parallel()

	t.Run("preserves all fields", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Provider:      "codex",
			Command:       "opencode",
			Args:          []string{"-m", "gpt-5"},
			Env:           map[string]string{"OPENCODE_PERMISSION": `{"*":"allow"}`},
			InitialPrompt: "test prompt",
			PromptMode:    "none",
			ResolvedAgent: "opencode",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "OPENCODE_SESSION_ID",
			},
			Hooks: &RuntimeHooksConfig{
				Provider: "opencode",
			},
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"opencode", "node"},
			},
			Instructions: &RuntimeInstructionsConfig{
				File: "OPENCODE.md",
			},
		}

		result := fillRuntimeDefaults(input)

		if result.Provider != input.Provider {
			t.Errorf("Provider: got %q, want %q", result.Provider, input.Provider)
		}
		if result.Command != input.Command {
			t.Errorf("Command: got %q, want %q", result.Command, input.Command)
		}
		if len(result.Args) != len(input.Args) {
			t.Errorf("Args: got %v, want %v", result.Args, input.Args)
		}
		if result.Env["OPENCODE_PERMISSION"] != input.Env["OPENCODE_PERMISSION"] {
			t.Errorf("Env: got %v, want %v", result.Env, input.Env)
		}
		if result.InitialPrompt != input.InitialPrompt {
			t.Errorf("InitialPrompt: got %q, want %q", result.InitialPrompt, input.InitialPrompt)
		}
		if result.PromptMode != input.PromptMode {
			t.Errorf("PromptMode: got %q, want %q", result.PromptMode, input.PromptMode)
		}
		if result.Session == nil || result.Session.SessionIDEnv != input.Session.SessionIDEnv {
			t.Errorf("Session: got %+v, want %+v", result.Session, input.Session)
		}
		if result.Hooks == nil || result.Hooks.Provider != input.Hooks.Provider {
			t.Errorf("Hooks: got %+v, want %+v", result.Hooks, input.Hooks)
		}
		if result.Tmux == nil || len(result.Tmux.ProcessNames) != len(input.Tmux.ProcessNames) {
			t.Errorf("Tmux: got %+v, want %+v", result.Tmux, input.Tmux)
		}
		if result.Instructions == nil || result.Instructions.File != input.Instructions.File {
			t.Errorf("Instructions: got %+v, want %+v", result.Instructions, input.Instructions)
		}
		if result.ResolvedAgent != input.ResolvedAgent {
			t.Errorf("ResolvedAgent: got %q, want %q", result.ResolvedAgent, input.ResolvedAgent)
		}
	})

	t.Run("nil input returns defaults", func(t *testing.T) {
		t.Parallel()
		result := fillRuntimeDefaults(nil)

		if result == nil {
			t.Fatal("fillRuntimeDefaults(nil) returned nil")
		}
		if result.Command == "" {
			t.Error("Command should have default value")
		}
	})

	t.Run("empty command defaults to claude", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "",
			Args:    []string{"--custom-flag"},
		}

		result := fillRuntimeDefaults(input)

		// Use isClaudeCommand to handle resolved paths (e.g., /opt/homebrew/bin/claude)
		if !isClaudeCommand(result.Command) {
			t.Errorf("Command: got %q, want claude or path ending in claude", result.Command)
		}
		// Args should be preserved, not overwritten
		if len(result.Args) != 1 || result.Args[0] != "--custom-flag" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("nil args defaults to skip-permissions", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Args:    nil,
		}

		result := fillRuntimeDefaults(input)

		if result.Args == nil || len(result.Args) == 0 {
			t.Error("Args should have default value")
		}
		if result.Args[0] != "--dangerously-skip-permissions" {
			t.Errorf("Args: got %v, want [--dangerously-skip-permissions]", result.Args)
		}
	})

	t.Run("empty args slice is preserved", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Args:    []string{}, // Explicitly empty, not nil
		}

		result := fillRuntimeDefaults(input)

		// Empty slice means "no args", not "use defaults"
		// This is intentional per RuntimeConfig docs
		if result.Args == nil {
			t.Error("Empty Args slice should be preserved as empty, not nil")
		}
	})

	t.Run("env map is copied not shared", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Env:     map[string]string{"KEY": "value"},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's env
		result.Env["NEW_KEY"] = "new_value"

		// Original should be unchanged
		if _, ok := input.Env["NEW_KEY"]; ok {
			t.Error("Env map was not copied - modifications affect original")
		}
	})

	t.Run("prompt_mode none is preserved for custom agents", func(t *testing.T) {
		t.Parallel()
		// This is the specific bug that was fixed - opencode needs prompt_mode: "none"
		// to prevent the startup beacon from being passed as an argument
		input := &RuntimeConfig{
			Provider:   "opencode",
			Command:    "opencode",
			Args:       []string{"-m", "gpt-5"},
			PromptMode: "none",
		}

		result := fillRuntimeDefaults(input)

		if result.PromptMode != "none" {
			t.Errorf("PromptMode: got %q, want %q - custom prompt_mode was not preserved", result.PromptMode, "none")
		}
	})

	t.Run("args slice is deep copied not shared", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Args:    []string{"original-arg"},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's args
		result.Args[0] = "modified-arg"

		// Original should be unchanged
		if input.Args[0] != "original-arg" {
			t.Errorf("Args slice was not deep copied - modifications affect original: got %q, want %q",
				input.Args[0], "original-arg")
		}
	})

	t.Run("session struct is deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "ORIGINAL_SESSION_ID",
				ConfigDirEnv: "ORIGINAL_CONFIG_DIR",
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's session
		result.Session.SessionIDEnv = "MODIFIED_SESSION_ID"

		// Original should be unchanged
		if input.Session.SessionIDEnv != "ORIGINAL_SESSION_ID" {
			t.Errorf("Session struct was not deep copied - modifications affect original: got %q, want %q",
				input.Session.SessionIDEnv, "ORIGINAL_SESSION_ID")
		}
	})

	t.Run("hooks struct is deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Hooks: &RuntimeHooksConfig{
				Provider:     "original-provider",
				Dir:          "original-dir",
				SettingsFile: "original-file",
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's hooks
		result.Hooks.Provider = "modified-provider"

		// Original should be unchanged
		if input.Hooks.Provider != "original-provider" {
			t.Errorf("Hooks struct was not deep copied - modifications affect original: got %q, want %q",
				input.Hooks.Provider, "original-provider")
		}
	})

	t.Run("tmux struct and process_names are deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames:      []string{"original-process"},
				ReadyPromptPrefix: "original-prefix",
				ReadyDelayMs:      5000,
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's tmux
		result.Tmux.ProcessNames[0] = "modified-process"
		result.Tmux.ReadyPromptPrefix = "modified-prefix"

		// Original should be unchanged
		if input.Tmux.ProcessNames[0] != "original-process" {
			t.Errorf("Tmux.ProcessNames was not deep copied - modifications affect original: got %q, want %q",
				input.Tmux.ProcessNames[0], "original-process")
		}
		if input.Tmux.ReadyPromptPrefix != "original-prefix" {
			t.Errorf("Tmux struct was not deep copied - modifications affect original: got %q, want %q",
				input.Tmux.ReadyPromptPrefix, "original-prefix")
		}
	})

	t.Run("instructions struct is deep copied", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "opencode",
			Instructions: &RuntimeInstructionsConfig{
				File: "ORIGINAL.md",
			},
		}

		result := fillRuntimeDefaults(input)

		// Modify result's instructions
		result.Instructions.File = "MODIFIED.md"

		// Original should be unchanged
		if input.Instructions.File != "ORIGINAL.md" {
			t.Errorf("Instructions struct was not deep copied - modifications affect original: got %q, want %q",
				input.Instructions.File, "ORIGINAL.md")
		}
	})

	t.Run("nil nested structs are auto-filled from preset for known agents", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			// All nested structs left nil
		}

		result := fillRuntimeDefaults(input)

		// Hooks is auto-filled for known agents (claude, opencode) to ensure
		// EnsureSettingsForRole creates the correct settings files.
		if result.Hooks == nil {
			t.Error("Hooks should be auto-filled for claude command")
		} else if result.Hooks.Provider != "claude" {
			t.Errorf("Hooks.Provider = %q, want %q", result.Hooks.Provider, "claude")
		}
		// Session is auto-filled from preset so handoffs can propagate GT_SESSION_ID_ENV.
		if result.Session == nil {
			t.Error("Session should be auto-filled for claude command")
		} else if result.Session.SessionIDEnv != "CLAUDE_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want CLAUDE_SESSION_ID", result.Session.SessionIDEnv)
		}
		// Tmux is auto-filled from preset so WaitForRuntimeReady uses prompt detection.
		if result.Tmux == nil {
			t.Error("Tmux should be auto-filled for claude command")
		} else if result.Tmux.ReadyPromptPrefix != "❯ " {
			t.Errorf("Tmux.ReadyPromptPrefix = %q, want \"❯ \"", result.Tmux.ReadyPromptPrefix)
		}
		// Instructions is auto-filled from preset when nil.
		if result.Instructions == nil {
			t.Error("Instructions should be auto-filled for claude command")
		} else if result.Instructions.File != "CLAUDE.md" {
			t.Errorf("Instructions.File = %q, want CLAUDE.md", result.Instructions.File)
		}
	})

	t.Run("partial nested struct is copied without defaults", func(t *testing.T) {
		t.Parallel()
		// User defines partial Tmux config - only ProcessNames, no other fields
		input := &RuntimeConfig{
			Command: "opencode",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"opencode"},
				// ReadyPromptPrefix and ReadyDelayMs left at zero values
			},
		}

		result := fillRuntimeDefaults(input)

		// ProcessNames should be copied
		if len(result.Tmux.ProcessNames) != 1 || result.Tmux.ProcessNames[0] != "opencode" {
			t.Errorf("Tmux.ProcessNames not copied correctly: got %v", result.Tmux.ProcessNames)
		}
		// Zero values should remain zero (fillRuntimeDefaults doesn't fill nested defaults)
		if result.Tmux.ReadyDelayMs != 0 {
			t.Errorf("Tmux.ReadyDelayMs should be 0 (unfilled), got %d", result.Tmux.ReadyDelayMs)
		}
	})

	t.Run("pi command gets hooks and tmux defaults", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "pi",
		}

		result := fillRuntimeDefaults(input)

		// Hooks should be auto-filled for pi
		if result.Hooks == nil {
			t.Fatal("Hooks should be auto-filled for pi command")
		}
		if result.Hooks.Provider != "pi" {
			t.Errorf("Hooks.Provider = %q, want pi", result.Hooks.Provider)
		}
		if result.Hooks.Dir != ".pi/extensions" {
			t.Errorf("Hooks.Dir = %q, want .pi/extensions", result.Hooks.Dir)
		}
		if result.Hooks.SettingsFile != "gastown-hooks.js" {
			t.Errorf("Hooks.SettingsFile = %q, want gastown-hooks.js", result.Hooks.SettingsFile)
		}

		// Tmux should be auto-filled for pi
		if result.Tmux == nil {
			t.Fatal("Tmux should be auto-filled for pi command")
		}
		if len(result.Tmux.ProcessNames) != 3 {
			t.Errorf("Tmux.ProcessNames length = %d, want 3", len(result.Tmux.ProcessNames))
		}
		expectedNames := []string{"pi", "node", "bun"}
		for i, want := range expectedNames {
			if i < len(result.Tmux.ProcessNames) && result.Tmux.ProcessNames[i] != want {
				t.Errorf("Tmux.ProcessNames[%d] = %q, want %q", i, result.Tmux.ProcessNames[i], want)
			}
		}
		if result.Tmux.ReadyDelayMs != 8000 {
			t.Errorf("Tmux.ReadyDelayMs = %d, want 8000", result.Tmux.ReadyDelayMs)
		}

		// PromptMode should be "arg" for pi (from preset)
		if result.PromptMode != "arg" {
			t.Errorf("PromptMode = %q, want arg", result.PromptMode)
		}
	})

	t.Run("pi preserves user-specified hooks", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "pi",
			Hooks: &RuntimeHooksConfig{
				Provider:     "custom",
				Dir:          "custom-dir",
				SettingsFile: "custom.js",
			},
		}

		result := fillRuntimeDefaults(input)

		// User-specified hooks should be preserved
		if result.Hooks.Provider != "custom" {
			t.Errorf("Hooks.Provider = %q, want custom (user-specified)", result.Hooks.Provider)
		}
	})

	t.Run("pi preserves user-specified tmux", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "pi",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"custom-pi"},
				ReadyDelayMs: 5000,
			},
		}

		result := fillRuntimeDefaults(input)

		// User-specified tmux should be preserved
		if result.Tmux.ProcessNames[0] != "custom-pi" {
			t.Errorf("Tmux.ProcessNames[0] = %q, want custom-pi (user-specified)", result.Tmux.ProcessNames[0])
		}
		if result.Tmux.ReadyDelayMs != 5000 {
			t.Errorf("Tmux.ReadyDelayMs = %d, want 5000 (user-specified)", result.Tmux.ReadyDelayMs)
		}
	})

	t.Run("custom claude agent inherits Session and Tmux from preset", func(t *testing.T) {
		t.Parallel()
		// Simulates: gt config agent set claude-opus 'claude --model claude-opus-4-6'
		input := &RuntimeConfig{
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "claude-opus-4-6"},
		}

		result := fillRuntimeDefaults(input)

		if result.Session == nil {
			t.Fatal("Session should be auto-filled for claude command")
		}
		if result.Session.SessionIDEnv != "CLAUDE_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want CLAUDE_SESSION_ID", result.Session.SessionIDEnv)
		}
		if result.Tmux == nil {
			t.Fatal("Tmux should be auto-filled for claude command")
		}
		if result.Tmux.ReadyPromptPrefix != "❯ " {
			t.Errorf("Tmux.ReadyPromptPrefix = %q, want \"❯ \"", result.Tmux.ReadyPromptPrefix)
		}
		found := false
		for _, n := range result.Tmux.ProcessNames {
			if n == "claude" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tmux.ProcessNames = %v, want to contain \"claude\"", result.Tmux.ProcessNames)
		}
		if len(result.Args) < 2 || result.Args[len(result.Args)-1] != "claude-opus-4-6" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("explicit Session config is not overridden by preset", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "MY_CUSTOM_SESSION_ID",
			},
		}

		result := fillRuntimeDefaults(input)

		if result.Session.SessionIDEnv != "MY_CUSTOM_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want MY_CUSTOM_SESSION_ID (user-specified)", result.Session.SessionIDEnv)
		}
	})

	t.Run("explicit Tmux config is not overridden by preset", func(t *testing.T) {
		t.Parallel()
		input := &RuntimeConfig{
			Command: "claude",
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"my-claude-wrapper"},
			},
		}

		result := fillRuntimeDefaults(input)

		if len(result.Tmux.ProcessNames) != 1 || result.Tmux.ProcessNames[0] != "my-claude-wrapper" {
			t.Errorf("Tmux.ProcessNames = %v, want [my-claude-wrapper] (user-specified)", result.Tmux.ProcessNames)
		}
	})
}

// TestFillRuntimeDefaultsPresetMerging verifies preset defaults are merged
// into custom agent configs based on the Provider field or inferred command name.
func TestFillRuntimeDefaultsPresetMerging(t *testing.T) {
	t.Parallel()

	t.Run("custom agent with provider=gemini gets session defaults", func(t *testing.T) {
		t.Parallel()
		// Custom agent using a different binary but declaring gemini as provider
		input := &RuntimeConfig{
			Provider: "gemini",
			Command:  "gemini-custom",
			Args:     []string{"--fast-mode"},
		}

		result := fillRuntimeDefaults(input)

		// Session should be auto-filled from gemini preset
		if result.Session == nil {
			t.Fatal("Session should be auto-filled from gemini preset")
		}
		if result.Session.SessionIDEnv != "GEMINI_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want GEMINI_SESSION_ID", result.Session.SessionIDEnv)
		}
		// Tmux should be auto-filled from gemini preset
		if result.Tmux == nil {
			t.Fatal("Tmux should be auto-filled from gemini preset")
		}
		found := false
		for _, name := range result.Tmux.ProcessNames {
			if name == "gemini" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Tmux.ProcessNames should contain 'gemini', got %v", result.Tmux.ProcessNames)
		}
		// User-specified Args should be preserved
		if len(result.Args) != 1 || result.Args[0] != "--fast-mode" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("custom agent infers preset from command name", func(t *testing.T) {
		t.Parallel()
		// No provider set, but command matches a known preset
		input := &RuntimeConfig{
			Command: "gemini",
			Args:    []string{"--approval-mode", "custom"},
		}

		result := fillRuntimeDefaults(input)

		// Should get gemini preset defaults
		if result.Session == nil {
			t.Fatal("Session should be auto-filled from gemini preset (inferred from command)")
		}
		if result.Session.SessionIDEnv != "GEMINI_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv = %q, want GEMINI_SESSION_ID", result.Session.SessionIDEnv)
		}
		// Args should be preserved (user override)
		if len(result.Args) != 2 || result.Args[0] != "--approval-mode" {
			t.Errorf("Args should be preserved: got %v", result.Args)
		}
	})

	t.Run("preset defaults not applied when fields already set", func(t *testing.T) {
		t.Parallel()
		// All fields explicitly set — preset should not override
		input := &RuntimeConfig{
			Provider: "claude",
			Command:  "custom-claude",
			Session: &RuntimeSessionConfig{
				SessionIDEnv: "MY_SESSION_ID",
			},
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"my-process"},
			},
			Instructions: &RuntimeInstructionsConfig{
				File: "MY.md",
			},
			PromptMode: "none",
		}

		result := fillRuntimeDefaults(input)

		// User-set fields should not be overridden by preset
		if result.Session.SessionIDEnv != "MY_SESSION_ID" {
			t.Errorf("Session.SessionIDEnv overridden: got %q, want MY_SESSION_ID", result.Session.SessionIDEnv)
		}
		if len(result.Tmux.ProcessNames) != 1 || result.Tmux.ProcessNames[0] != "my-process" {
			t.Errorf("Tmux.ProcessNames overridden: got %v, want [my-process]", result.Tmux.ProcessNames)
		}
		if result.Instructions.File != "MY.md" {
			t.Errorf("Instructions.File overridden: got %q, want MY.md", result.Instructions.File)
		}
		if result.PromptMode != "none" {
			t.Errorf("PromptMode overridden: got %q, want none", result.PromptMode)
		}
	})
}

// TestLookupAgentConfigPreservesCustomFields verifies that custom agents
// have all their settings preserved through the lookup chain.
func TestLookupAgentConfigPreservesCustomFields(t *testing.T) {
	t.Parallel()

	townSettings := &TownSettings{
		Type:         "town-settings",
		Version:      1,
		DefaultAgent: "claude",
		Agents: map[string]*RuntimeConfig{
			"opencode-mayor": {
				Command:    "opencode",
				Args:       []string{"-m", "gpt-5"},
				PromptMode: "none",
				Env:        map[string]string{"OPENCODE_PERMISSION": `{"*":"allow"}`},
				Tmux: &RuntimeTmuxConfig{
					ProcessNames: []string{"opencode", "node"},
				},
			},
		},
	}

	rc := lookupAgentConfig("opencode-mayor", townSettings, nil)

	if rc == nil {
		t.Fatal("lookupAgentConfig returned nil for custom agent")
	}
	if rc.PromptMode != "none" {
		t.Errorf("PromptMode: got %q, want %q - setting was lost in lookup chain", rc.PromptMode, "none")
	}
	if rc.Command != "opencode" {
		t.Errorf("Command: got %q, want %q", rc.Command, "opencode")
	}
	if rc.Env["OPENCODE_PERMISSION"] != `{"*":"allow"}` {
		t.Errorf("Env was not preserved: got %v", rc.Env)
	}
	if rc.Tmux == nil || len(rc.Tmux.ProcessNames) != 2 {
		t.Errorf("Tmux.ProcessNames not preserved: got %+v", rc.Tmux)
	}
}

// TestBuildCommandWithPromptRespectsPromptModeNone verifies that when PromptMode
// is "none", the prompt is not appended to the command.
func TestBuildCommandWithPromptRespectsPromptModeNone(t *testing.T) {
	t.Parallel()

	rc := &RuntimeConfig{
		Command:    "opencode",
		Args:       []string{"-m", "gpt-5"},
		PromptMode: "none",
	}

	// Build command with a prompt that should be ignored
	cmd := rc.BuildCommandWithPrompt("This prompt should not appear")

	if strings.Contains(cmd, "This prompt should not appear") {
		t.Errorf("prompt_mode=none should prevent prompt from being added, got: %s", cmd)
	}
	if !strings.HasPrefix(cmd, "opencode") {
		t.Errorf("Command should start with opencode, got: %s", cmd)
	}
}

// captureStderr redirects os.Stderr for the duration of fn and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = r.Close()
	return buf.String()
}

// TestBuildCommandWithPromptWarnsOnDroppedPrompt verifies that when PromptMode
// is "none" and a non-empty prompt is provided, a warning is emitted to stderr.
// This makes the misconfiguration self-diagnosing (issue #3803).
func TestBuildCommandWithPromptWarnsOnDroppedPrompt(t *testing.T) {
	rc := &RuntimeConfig{
		Command:    "claude",
		Args:       []string{"--dangerously-skip-permissions"},
		PromptMode: "none",
	}

	var cmd string
	stderr := captureStderr(t, func() {
		cmd = rc.BuildCommandWithPrompt("[GAS TOWN] deacon <- daemon • patrol")
	})

	if strings.Contains(cmd, "GAS TOWN") {
		t.Errorf("prompt_mode=none should prevent prompt from appearing in command, got: %s", cmd)
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("expected a warning on stderr when prompt is dropped, got: %q", stderr)
	}
	if !strings.Contains(stderr, "prompt_mode") {
		t.Errorf("warning should mention prompt_mode, got: %q", stderr)
	}
	if !strings.Contains(stderr, `"claude"`) {
		t.Errorf("warning should include the agent command name, got: %q", stderr)
	}
}

// TestBuildCommandWithPromptNoWarnOnEmptyPrompt verifies that no warning is
// emitted when the prompt is empty (that is the normal PromptMode:"none" use case).
func TestBuildCommandWithPromptNoWarnOnEmptyPrompt(t *testing.T) {
	rc := &RuntimeConfig{
		Command:    "codex",
		Args:       []string{},
		PromptMode: "none",
	}

	stderr := captureStderr(t, func() {
		_ = rc.BuildCommandWithPrompt("")
	})

	if stderr != "" {
		t.Errorf("no warning expected when prompt is empty, got: %q", stderr)
	}
}

// TestBuildArgsWithPromptWarnsOnDroppedPrompt verifies the parallel warning in
// BuildArgsWithPrompt when PromptMode is "none" and a non-empty prompt is provided.
func TestBuildArgsWithPromptWarnsOnDroppedPrompt(t *testing.T) {
	rc := &RuntimeConfig{
		Command:    "claude",
		Args:       []string{"--dangerously-skip-permissions"},
		PromptMode: "none",
	}

	var args []string
	stderr := captureStderr(t, func() {
		args = rc.BuildArgsWithPrompt("[GAS TOWN] deacon <- daemon • patrol")
	})

	for _, arg := range args {
		if strings.Contains(arg, "GAS TOWN") {
			t.Errorf("prompt_mode=none should prevent prompt appearing in args, got: %v", args)
		}
	}
	if !strings.Contains(stderr, "warning:") {
		t.Errorf("expected a warning on stderr when prompt is dropped, got: %q", stderr)
	}
}

// TestRoleAgentConfigWithCustomAgent tests role-based agent resolution with
// custom agents that have special settings like prompt_mode: "none".
//
// This test mirrors manual verification using settings/config.json:
//
//	{
//	  "type": "town-settings",
//	  "version": 1,
//	  "default_agent": "claude-opus",
//	  "agents": {
//	    "amp-yolo": {
//	      "command": "amp",
//	      "args": ["--dangerously-allow-all"]
//	    },
//	    "opencode-mayor": {
//	      "command": "opencode",
//	      "args": ["-m", "openai/gpt-5.2-codex"],
//	      "prompt_mode": "none",
//	      "process_names": ["opencode", "node"],
//	      "env": {
//	        "OPENCODE_PERMISSION": "{\"*\":\"allow\"}"
//	      }
//	    }
//	  },
//	  "role_agents": {
//	    "crew": "claude-sonnet",
//	    "deacon": "claude-haiku",
//	    "mayor": "opencode-mayor",
//	    "polecat": "claude-opus",
//	    "refinery": "claude-opus",
//	    "witness": "claude-sonnet"
//	  }
//	}
//
// Manual test procedure:
//  1. Set role_agents.mayor to each agent (claude, gemini, codex, cursor, auggie, amp, opencode)
//  2. Run: gt start
//  3. Verify mayor starts with correct agent config
//  4. Run: GT_NUKE_ACKNOWLEDGED=1 gt down --nuke
//  5. Repeat for all 7 built-in agents
func TestRoleAgentConfigWithCustomAgent(t *testing.T) {
	skipIfAgentBinaryMissing(t, "opencode", "claude")
	t.Parallel()

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create town settings mirroring the manual test config
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude-opus"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor:    "opencode-mayor",
		constants.RoleDeacon:   "claude-haiku",
		constants.RolePolecat:  "claude-opus",
		constants.RoleRefinery: "claude-opus",
		constants.RoleWitness:  "claude-sonnet",
		constants.RoleCrew:     "claude-sonnet",
	}
	townSettings.Agents = map[string]*RuntimeConfig{
		"opencode-mayor": {
			Command:    "opencode",
			Args:       []string{"-m", "openai/gpt-5.2-codex"},
			PromptMode: "none",
			Env:        map[string]string{"OPENCODE_PERMISSION": `{"*":"allow"}`},
			Tmux: &RuntimeTmuxConfig{
				ProcessNames: []string{"opencode", "node"},
			},
		},
		"amp-yolo": {
			Command: "amp",
			Args:    []string{"--dangerously-allow-all"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create minimal rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Test mayor role gets opencode-mayor with prompt_mode: none
	t.Run("mayor gets opencode-mayor config", func(t *testing.T) {
		rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
		if rc == nil {
			t.Fatal("ResolveRoleAgentConfig returned nil for mayor")
		}
		if rc.Command != "opencode" {
			t.Errorf("Command: got %q, want %q", rc.Command, "opencode")
		}
		if rc.PromptMode != "none" {
			t.Errorf("PromptMode: got %q, want %q - critical for opencode", rc.PromptMode, "none")
		}
		if rc.Env["OPENCODE_PERMISSION"] != `{"*":"allow"}` {
			t.Errorf("Env not preserved: got %v", rc.Env)
		}

		// Verify startup beacon is NOT added to command
		cmd := rc.BuildCommandWithPrompt("[GAS TOWN] mayor <- human • cold-start")
		if strings.Contains(cmd, "GAS TOWN") {
			t.Errorf("prompt_mode=none should prevent beacon, got: %s", cmd)
		}
	})

	// Test other roles get their configured agents
	t.Run("deacon gets claude-haiku", func(t *testing.T) {
		rc := ResolveRoleAgentConfig(constants.RoleDeacon, townRoot, rigPath)
		if rc == nil {
			t.Fatal("ResolveRoleAgentConfig returned nil for deacon")
		}
		// claude-haiku is a built-in preset
		if !strings.Contains(rc.Command, "claude") && rc.Command != "claude" {
			t.Errorf("Command: got %q, want claude-based command", rc.Command)
		}
	})

	t.Run("polecat gets claude-opus", func(t *testing.T) {
		rc := ResolveRoleAgentConfig(constants.RolePolecat, townRoot, rigPath)
		if rc == nil {
			t.Fatal("ResolveRoleAgentConfig returned nil for polecat")
		}
		if !strings.Contains(rc.Command, "claude") && rc.Command != "claude" {
			t.Errorf("Command: got %q, want claude-based command", rc.Command)
		}
	})
}

// TestMultipleAgentTypes tests that various built-in agent presets work correctly.
// NOTE: Only these are actual built-in presets: claude, gemini, codex, cursor, auggie, amp, opencode.
// Variants like "claude-opus", "claude-haiku", "claude-sonnet" are NOT built-in - they need
// to be defined as custom agents in TownSettings.Agents if specific model selection is needed.
func TestMultipleAgentTypes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name          string
		agentName     string
		expectCommand string
		isBuiltIn     bool // true if this is an actual built-in preset
	}{
		{
			name:          "claude built-in preset",
			agentName:     "claude",
			expectCommand: "claude",
			isBuiltIn:     true,
		},
		{
			name:          "codex built-in preset",
			agentName:     "codex",
			expectCommand: "codex",
			isBuiltIn:     true,
		},
		{
			name:          "gemini built-in preset",
			agentName:     "gemini",
			expectCommand: "gemini",
			isBuiltIn:     true,
		},
		{
			name:          "amp built-in preset",
			agentName:     "amp",
			expectCommand: "amp",
			isBuiltIn:     true,
		},
		{
			name:          "opencode built-in preset",
			agentName:     "opencode",
			expectCommand: "opencode",
			isBuiltIn:     true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// Skip if agent binary not installed (prevents flaky CI failures)
			skipIfAgentBinaryMissing(t, tc.agentName)

			// Verify it's actually a built-in preset
			if tc.isBuiltIn {
				preset := GetAgentPresetByName(tc.agentName)
				if preset == nil {
					t.Errorf("%s should be a built-in preset but GetAgentPresetByName returned nil", tc.agentName)
					return
				}
			}

			townRoot := t.TempDir()
			rigPath := filepath.Join(townRoot, "testrig")

			townSettings := NewTownSettings()
			townSettings.DefaultAgent = "claude"
			townSettings.RoleAgents = map[string]string{
				constants.RoleMayor: tc.agentName,
			}
			if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
				t.Fatalf("SaveTownSettings: %v", err)
			}

			rigSettings := NewRigSettings()
			if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
				t.Fatalf("SaveRigSettings: %v", err)
			}

			rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
			if rc == nil {
				t.Fatalf("ResolveRoleAgentConfig returned nil for %s", tc.agentName)
			}

			// Allow path-based commands (e.g., /opt/homebrew/bin/claude)
			if !strings.Contains(rc.Command, tc.expectCommand) {
				t.Errorf("Command: got %q, want command containing %q", rc.Command, tc.expectCommand)
			}
		})
	}
}

// TestCustomClaudeVariants tests that Claude model variants (opus, sonnet, haiku) need
// to be explicitly defined as custom agents since they are NOT built-in presets.
func TestCustomClaudeVariants(t *testing.T) {
	skipIfAgentBinaryMissing(t, "claude")
	t.Parallel()

	// Verify that claude-opus/sonnet/haiku are NOT built-in presets
	variants := []string{"claude-opus", "claude-sonnet", "claude-haiku"}
	for _, variant := range variants {
		if preset := GetAgentPresetByName(variant); preset != nil {
			t.Errorf("%s should NOT be a built-in preset (only 'claude' is), but GetAgentPresetByName returned non-nil", variant)
		}
	}

	// Test that custom claude variants work when explicitly defined
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor:  "claude-opus",
		constants.RoleDeacon: "claude-haiku",
	}
	// Define the custom variants
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-opus": {
			Command: "claude",
			Args:    []string{"--model", "claude-opus-4", "--dangerously-skip-permissions"},
		},
		"claude-haiku": {
			Command: "claude",
			Args:    []string{"--model", "claude-haiku-3", "--dangerously-skip-permissions"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Test claude-opus custom agent
	rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for claude-opus")
	}
	if !strings.Contains(rc.Command, "claude") {
		t.Errorf("claude-opus Command: got %q, want claude", rc.Command)
	}
	foundModel := false
	for _, arg := range rc.Args {
		if arg == "claude-opus-4" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Errorf("claude-opus Args should contain model flag: got %v", rc.Args)
	}

	// Test claude-haiku custom agent
	rc = ResolveRoleAgentConfig(constants.RoleDeacon, townRoot, rigPath)
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for claude-haiku")
	}
	foundModel = false
	for _, arg := range rc.Args {
		if arg == "claude-haiku-3" {
			foundModel = true
			break
		}
	}
	if !foundModel {
		t.Errorf("claude-haiku Args should contain model flag: got %v", rc.Args)
	}
}

// TestCustomAgentWithAmp tests custom agent configuration for amp.
// This mirrors the manual test: amp-yolo started successfully with custom args.
func TestCustomAgentWithAmp(t *testing.T) {
	skipIfAgentBinaryMissing(t, "amp")
	t.Parallel()

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleMayor: "amp-yolo",
	}
	townSettings.Agents = map[string]*RuntimeConfig{
		"amp-yolo": {
			Command: "amp",
			Args:    []string{"--dangerously-allow-all"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	rc := ResolveRoleAgentConfig(constants.RoleMayor, townRoot, rigPath)
	if rc == nil {
		t.Fatal("ResolveRoleAgentConfig returned nil for amp-yolo")
	}

	if rc.Command != "amp" {
		t.Errorf("Command: got %q, want %q", rc.Command, "amp")
	}
	if len(rc.Args) != 1 || rc.Args[0] != "--dangerously-allow-all" {
		t.Errorf("Args: got %v, want [--dangerously-allow-all]", rc.Args)
	}

	// Verify command generation
	cmd := rc.BuildCommand()
	if !strings.Contains(cmd, "amp") {
		t.Errorf("BuildCommand should contain amp, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--dangerously-allow-all") {
		t.Errorf("BuildCommand should contain custom args, got: %s", cmd)
	}
}

func TestResolveRoleAgentConfig(t *testing.T) {
	skipIfAgentBinaryMissing(t, "gemini", "codex")
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create town settings with role-specific agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		"mayor":   "claude", // mayor uses default claude
		"witness": "gemini", // witness uses gemini
		"polecat": "codex",  // polecats use codex
	}
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-haiku": {
			Command: "claude",
			Args:    []string{"--model", "haiku", "--dangerously-skip-permissions"},
		},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create rig settings that override some roles
	rigSettings := NewRigSettings()
	rigSettings.Agent = "gemini" // default for this rig
	rigSettings.RoleAgents = map[string]string{
		"witness": "claude-haiku", // override witness to use haiku
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("rig RoleAgents overrides town RoleAgents", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("witness", townRoot, rigPath)
		// Should get claude-haiku from rig's RoleAgents
		if !isClaudeCommand(rc.Command) {
			t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
		}
		cmd := rc.BuildCommand()
		if !strings.Contains(cmd, "--model haiku") {
			t.Errorf("BuildCommand() = %q, should contain --model haiku", cmd)
		}
	})

	t.Run("town RoleAgents used when rig has no override", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
		// Should get codex from town's RoleAgents (rig doesn't override polecat)
		if rc.Command != "codex" {
			t.Errorf("Command = %q, want %q", rc.Command, "codex")
		}
	})

	t.Run("falls back to default agent when role not in RoleAgents", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("crew", townRoot, rigPath)
		// crew is not in any RoleAgents, should use rig's default agent (gemini)
		if rc.Command != "gemini" {
			t.Errorf("Command = %q, want %q", rc.Command, "gemini")
		}
	})

	t.Run("town-level role (no rigPath) uses town RoleAgents", func(t *testing.T) {
		rc := ResolveRoleAgentConfig("mayor", townRoot, "")
		// mayor is in town's RoleAgents and may resolve to a platform-specific claude binary path.
		if !isClaudeCommand(rc.Command) {
			t.Errorf("Command = %q, want claude or path ending in /claude", rc.Command)
		}
	})
}

func TestResolveRoleAgentName(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create town settings with role-specific agents
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		"witness": "gemini",
		"polecat": "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create rig settings
	rigSettings := NewRigSettings()
	rigSettings.Agent = "amp"
	rigSettings.RoleAgents = map[string]string{
		"witness": "cursor", // override witness
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	t.Run("rig role-specific agent", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("witness", townRoot, rigPath)
		if name != "cursor" {
			t.Errorf("name = %q, want %q", name, "cursor")
		}
		if !isRoleSpecific {
			t.Error("isRoleSpecific = false, want true")
		}
	})

	t.Run("town role-specific agent", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("polecat", townRoot, rigPath)
		if name != "codex" {
			t.Errorf("name = %q, want %q", name, "codex")
		}
		if !isRoleSpecific {
			t.Error("isRoleSpecific = false, want true")
		}
	})

	t.Run("falls back to rig default agent", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("crew", townRoot, rigPath)
		if name != "amp" {
			t.Errorf("name = %q, want %q", name, "amp")
		}
		if isRoleSpecific {
			t.Error("isRoleSpecific = true, want false")
		}
	})

	t.Run("falls back to town default agent when no rig path", func(t *testing.T) {
		name, isRoleSpecific := ResolveRoleAgentName("refinery", townRoot, "")
		if name != "claude" {
			t.Errorf("name = %q, want %q", name, "claude")
		}
		if isRoleSpecific {
			t.Error("isRoleSpecific = true, want false")
		}
	})
}

func TestRoleAgentsRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	townSettingsPath := filepath.Join(dir, "settings", "config.json")
	rigSettingsPath := filepath.Join(dir, "rig", "settings", "config.json")

	// Test TownSettings with RoleAgents
	t.Run("town settings with role_agents", func(t *testing.T) {
		original := NewTownSettings()
		original.RoleAgents = map[string]string{
			"mayor":   "claude-opus",
			"witness": "claude-haiku",
			"polecat": "claude-sonnet",
		}

		if err := SaveTownSettings(townSettingsPath, original); err != nil {
			t.Fatalf("SaveTownSettings: %v", err)
		}

		loaded, err := LoadOrCreateTownSettings(townSettingsPath)
		if err != nil {
			t.Fatalf("LoadOrCreateTownSettings: %v", err)
		}

		if len(loaded.RoleAgents) != 3 {
			t.Errorf("RoleAgents count = %d, want 3", len(loaded.RoleAgents))
		}
		if loaded.RoleAgents["mayor"] != "claude-opus" {
			t.Errorf("RoleAgents[mayor] = %q, want %q", loaded.RoleAgents["mayor"], "claude-opus")
		}
		if loaded.RoleAgents["witness"] != "claude-haiku" {
			t.Errorf("RoleAgents[witness] = %q, want %q", loaded.RoleAgents["witness"], "claude-haiku")
		}
		if loaded.RoleAgents["polecat"] != "claude-sonnet" {
			t.Errorf("RoleAgents[polecat] = %q, want %q", loaded.RoleAgents["polecat"], "claude-sonnet")
		}
	})

	// Test RigSettings with RoleAgents
	t.Run("rig settings with role_agents", func(t *testing.T) {
		original := NewRigSettings()
		original.RoleAgents = map[string]string{
			"witness": "gemini",
			"crew":    "codex",
		}

		if err := SaveRigSettings(rigSettingsPath, original); err != nil {
			t.Fatalf("SaveRigSettings: %v", err)
		}

		loaded, err := LoadRigSettings(rigSettingsPath)
		if err != nil {
			t.Fatalf("LoadRigSettings: %v", err)
		}

		if len(loaded.RoleAgents) != 2 {
			t.Errorf("RoleAgents count = %d, want 2", len(loaded.RoleAgents))
		}
		if loaded.RoleAgents["witness"] != "gemini" {
			t.Errorf("RoleAgents[witness] = %q, want %q", loaded.RoleAgents["witness"], "gemini")
		}
		if loaded.RoleAgents["crew"] != "codex" {
			t.Errorf("RoleAgents[crew] = %q, want %q", loaded.RoleAgents["crew"], "codex")
		}
	})
}

// Escalation config tests

func TestEscalationConfigRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "settings", "escalation.json")

	original := &EscalationConfig{
		Type:    "escalation",
		Version: CurrentEscalationVersion,
		Routes: map[string][]string{
			SeverityLow:      {"bead"},
			SeverityMedium:   {"bead", "mail:mayor"},
			SeverityHigh:     {"bead", "mail:mayor", "email:human"},
			SeverityCritical: {"bead", "mail:mayor", "email:human", "sms:human"},
		},
		Contacts: EscalationContacts{
			HumanEmail: "test@example.com",
			HumanSMS:   "+15551234567",
		},
		StaleThreshold:   "2h",
		MaxReescalations: intPtr(3),
	}

	if err := SaveEscalationConfig(path, original); err != nil {
		t.Fatalf("SaveEscalationConfig: %v", err)
	}

	loaded, err := LoadEscalationConfig(path)
	if err != nil {
		t.Fatalf("LoadEscalationConfig: %v", err)
	}

	if loaded.Type != original.Type {
		t.Errorf("Type = %q, want %q", loaded.Type, original.Type)
	}
	if loaded.Version != original.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, original.Version)
	}
	if loaded.StaleThreshold != original.StaleThreshold {
		t.Errorf("StaleThreshold = %q, want %q", loaded.StaleThreshold, original.StaleThreshold)
	}
	if *loaded.MaxReescalations != *original.MaxReescalations {
		t.Errorf("MaxReescalations = %d, want %d", *loaded.MaxReescalations, *original.MaxReescalations)
	}
	if loaded.Contacts.HumanEmail != original.Contacts.HumanEmail {
		t.Errorf("Contacts.HumanEmail = %q, want %q", loaded.Contacts.HumanEmail, original.Contacts.HumanEmail)
	}
	if loaded.Contacts.HumanSMS != original.Contacts.HumanSMS {
		t.Errorf("Contacts.HumanSMS = %q, want %q", loaded.Contacts.HumanSMS, original.Contacts.HumanSMS)
	}

	// Check routes
	for severity, actions := range original.Routes {
		loadedActions := loaded.Routes[severity]
		if len(loadedActions) != len(actions) {
			t.Errorf("Routes[%s] len = %d, want %d", severity, len(loadedActions), len(actions))
			continue
		}
		for i, action := range actions {
			if loadedActions[i] != action {
				t.Errorf("Routes[%s][%d] = %q, want %q", severity, i, loadedActions[i], action)
			}
		}
	}
}

func TestEscalationConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := NewEscalationConfig()

	if cfg.Type != "escalation" {
		t.Errorf("Type = %q, want %q", cfg.Type, "escalation")
	}
	if cfg.Version != CurrentEscalationVersion {
		t.Errorf("Version = %d, want %d", cfg.Version, CurrentEscalationVersion)
	}
	if cfg.StaleThreshold != "4h" {
		t.Errorf("StaleThreshold = %q, want %q", cfg.StaleThreshold, "4h")
	}
	if cfg.MaxReescalations == nil || *cfg.MaxReescalations != 2 {
		t.Errorf("MaxReescalations = %v, want %d", cfg.MaxReescalations, 2)
	}

	// Check default routes
	if len(cfg.Routes) != 4 {
		t.Errorf("Routes count = %d, want 4", len(cfg.Routes))
	}
	if len(cfg.Routes[SeverityLow]) != 1 || cfg.Routes[SeverityLow][0] != "bead" {
		t.Errorf("Routes[low] = %v, want [bead]", cfg.Routes[SeverityLow])
	}
	if len(cfg.Routes[SeverityCritical]) != 4 {
		t.Errorf("Routes[critical] len = %d, want 4", len(cfg.Routes[SeverityCritical]))
	}
}

func TestEscalationConfigValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  *EscalationConfig
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			config: &EscalationConfig{
				Type:    "escalation",
				Version: 1,
				Routes: map[string][]string{
					SeverityLow: {"bead"},
				},
			},
			wantErr: false,
		},
		{
			name: "invalid type",
			config: &EscalationConfig{
				Type:    "wrong-type",
				Version: 1,
			},
			wantErr: true,
			errMsg:  "invalid config type",
		},
		{
			name: "unsupported version",
			config: &EscalationConfig{
				Type:    "escalation",
				Version: 999,
			},
			wantErr: true,
			errMsg:  "unsupported config version",
		},
		{
			name: "invalid stale threshold",
			config: &EscalationConfig{
				Type:           "escalation",
				Version:        1,
				StaleThreshold: "not-a-duration",
			},
			wantErr: true,
			errMsg:  "invalid stale_threshold",
		},
		{
			name: "invalid severity key",
			config: &EscalationConfig{
				Type:    "escalation",
				Version: 1,
				Routes: map[string][]string{
					"invalid-severity": {"bead"},
				},
			},
			wantErr: true,
			errMsg:  "unknown severity",
		},
		{
			name: "negative max reescalations",
			config: &EscalationConfig{
				Type:             "escalation",
				Version:          1,
				MaxReescalations: intPtr(-1),
			},
			wantErr: true,
			errMsg:  "max_reescalations must be non-negative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateEscalationConfig(tt.config)
			if tt.wantErr {
				if err == nil {
					t.Errorf("validateEscalationConfig() expected error containing %q, got nil", tt.errMsg)
				} else if !strings.Contains(err.Error(), tt.errMsg) {
					t.Errorf("validateEscalationConfig() error = %v, want error containing %q", err, tt.errMsg)
				}
			} else {
				if err != nil {
					t.Errorf("validateEscalationConfig() unexpected error: %v", err)
				}
			}
		})
	}
}

func TestEscalationConfigGetStaleThreshold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *EscalationConfig
		expected time.Duration
	}{
		{
			name:     "default when empty",
			config:   &EscalationConfig{},
			expected: 4 * time.Hour,
		},
		{
			name: "2 hours",
			config: &EscalationConfig{
				StaleThreshold: "2h",
			},
			expected: 2 * time.Hour,
		},
		{
			name: "30 minutes",
			config: &EscalationConfig{
				StaleThreshold: "30m",
			},
			expected: 30 * time.Minute,
		},
		{
			name: "invalid duration falls back to default",
			config: &EscalationConfig{
				StaleThreshold: "invalid",
			},
			expected: 4 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetStaleThreshold()
			if got != tt.expected {
				t.Errorf("GetStaleThreshold() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestEscalationConfigGetRouteForSeverity(t *testing.T) {
	t.Parallel()

	cfg := &EscalationConfig{
		Routes: map[string][]string{
			SeverityLow:    {"bead"},
			SeverityMedium: {"bead", "mail:mayor"},
		},
	}

	tests := []struct {
		severity string
		expected []string
	}{
		{SeverityLow, []string{"bead"}},
		{SeverityMedium, []string{"bead", "mail:mayor"}},
		{SeverityHigh, []string{"bead", "mail:mayor"}},     // fallback for missing
		{SeverityCritical, []string{"bead", "mail:mayor"}}, // fallback for missing
	}

	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			got := cfg.GetRouteForSeverity(tt.severity)
			if len(got) != len(tt.expected) {
				t.Errorf("GetRouteForSeverity(%s) len = %d, want %d", tt.severity, len(got), len(tt.expected))
				return
			}
			for i, action := range tt.expected {
				if got[i] != action {
					t.Errorf("GetRouteForSeverity(%s)[%d] = %q, want %q", tt.severity, i, got[i], action)
				}
			}
		})
	}
}

func TestEscalationConfigGetMaxReescalations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		config   *EscalationConfig
		expected int
	}{
		{
			name:     "default when nil",
			config:   &EscalationConfig{},
			expected: 2,
		},
		{
			name: "explicit zero means never re-escalate",
			config: &EscalationConfig{
				MaxReescalations: intPtr(0),
			},
			expected: 0,
		},
		{
			name: "custom value",
			config: &EscalationConfig{
				MaxReescalations: intPtr(5),
			},
			expected: 5,
		},
		{
			name: "negative returns negative (should not happen after validation)",
			config: &EscalationConfig{
				MaxReescalations: intPtr(-1),
			},
			expected: -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.GetMaxReescalations()
			if got != tt.expected {
				t.Errorf("GetMaxReescalations() = %d, want %d", got, tt.expected)
			}
		})
	}
}

func TestLoadOrCreateEscalationConfig(t *testing.T) {
	t.Parallel()

	t.Run("creates default when not found", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings", "escalation.json")

		cfg, err := LoadOrCreateEscalationConfig(path)
		if err != nil {
			t.Fatalf("LoadOrCreateEscalationConfig: %v", err)
		}

		if cfg.Type != "escalation" {
			t.Errorf("Type = %q, want %q", cfg.Type, "escalation")
		}
		if len(cfg.Routes) != 4 {
			t.Errorf("Routes count = %d, want 4", len(cfg.Routes))
		}
	})

	t.Run("loads existing config", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "settings", "escalation.json")

		// Create a config first
		original := &EscalationConfig{
			Type:           "escalation",
			Version:        1,
			StaleThreshold: "1h",
			Routes: map[string][]string{
				SeverityLow: {"bead"},
			},
		}
		if err := SaveEscalationConfig(path, original); err != nil {
			t.Fatalf("SaveEscalationConfig: %v", err)
		}

		// Load it
		cfg, err := LoadOrCreateEscalationConfig(path)
		if err != nil {
			t.Fatalf("LoadOrCreateEscalationConfig: %v", err)
		}

		if cfg.StaleThreshold != "1h" {
			t.Errorf("StaleThreshold = %q, want %q", cfg.StaleThreshold, "1h")
		}
	})
}

func TestEscalationConfigPath(t *testing.T) {
	t.Parallel()

	path := EscalationConfigPath("/home/user/gt")
	expected := "/home/user/gt/settings/escalation.json"
	if filepath.ToSlash(path) != expected {
		t.Errorf("EscalationConfigPath = %q, want %q", path, expected)
	}
}

func TestBuildStartupCommandWithAgentOverride_PriorityOverRoleAgents(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure town settings with role_agents: refinery = codex
	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.RoleAgents = map[string]string{
		constants.RoleRefinery: "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Create empty rig settings
	rigSettings := NewRigSettings()
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// agentOverride = "gemini" should take priority over role_agents[refinery] = "codex"
	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleRefinery},
		rigPath,
		"",
		"gemini", // explicit override
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "gemini") {
		t.Errorf("expected gemini (override) in command, got: %q", cmd)
	}
	if strings.Contains(cmd, "codex") {
		t.Errorf("did not expect codex (role_agents) when override is set: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_IncludesGTRoot(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should include GT_ROOT in export
	expected := "GT_ROOT=" + ShellQuote(townRoot)
	if !strings.Contains(cmd, expected) {
		t.Errorf("expected %s in command, got: %q", expected, cmd)
	}
}

func TestQuoteForShell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple string",
			input: "hello",
			want:  `"hello"`,
		},
		{
			name:  "string with double quote",
			input: `say "hello"`,
			want:  `"say \"hello\""`,
		},
		{
			name:  "string with backslash",
			input: `path\to\file`,
			want:  `"path\\to\\file"`,
		},
		{
			name:  "string with backtick",
			input: "run `cmd`",
			want:  "\"run \\`cmd\\`\"",
		},
		{
			name:  "string with dollar sign",
			input: "cost is $100",
			want:  `"cost is \$100"`,
		},
		{
			name:  "variable expansion prevented",
			input: "$HOME/path",
			want:  `"\$HOME/path"`,
		},
		{
			name:  "empty string",
			input: "",
			want:  `""`,
		},
		{
			name:  "combined special chars",
			input: "`$HOME`",
			want:  "\"\\`\\$HOME\\`\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := quoteForShell(tt.input)
			if got != tt.want {
				t.Errorf("quoteForShell(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildStartupCommandWithAgentOverride_SetsGTAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should include GT_AGENT=gemini in export so handoff can preserve it
	if !strings.Contains(cmd, "GT_AGENT=gemini") {
		t.Errorf("expected GT_AGENT=gemini in command, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_SetsGTProcessNames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should include GT_PROCESS_NAMES with gemini's process names
	if !strings.Contains(cmd, "GT_PROCESS_NAMES=gemini") {
		t.Errorf("expected GT_PROCESS_NAMES=gemini in command, got: %q", cmd)
	}
}

func TestBuildStartupCommand_SetsGTProcessNames(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
	)

	// Default agent is claude — GT_PROCESS_NAMES should include node,claude
	if !strings.Contains(cmd, "GT_PROCESS_NAMES=") {
		t.Errorf("expected GT_PROCESS_NAMES in command, got: %q", cmd)
	}
}

// TestBuildStartupCommandWithAgentOverride_UsesOverrideWhenNoTownRoot tests that
// agentOverride is respected even when findTownRootFromCwd fails.
// This is a regression test for the bug where `gt deacon start --agent codex`
// would still launch Claude if run from outside the town directory.
func TestBuildStartupCommandWithAgentOverride_UsesOverrideWhenNoTownRoot(t *testing.T) {
	t.Parallel()
	ResetRegistryForTesting()

	// Change to a directory that is definitely NOT in a Gas Town workspace
	// by using a temp directory with no mayor/town.json
	tmpDir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldWd)
	})

	// Call with rigPath="" (like deacon does) and agentOverride="codex"
	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "deacon"},
		"",      // rigPath is empty for town-level roles
		"",      // no prompt
		"codex", // agent override
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// Should use codex, NOT claude (the default)
	if !strings.Contains(cmd, "codex") {
		t.Errorf("expected command to contain 'codex' but got: %q", cmd)
	}
	if strings.Contains(cmd, "claude") {
		t.Errorf("expected command to NOT contain 'claude' but got: %q", cmd)
	}
	// Should have the codex permissive approval/sandbox flag.
	if !strings.Contains(cmd, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("expected command to contain '--dangerously-bypass-approvals-and-sandbox' (codex flag) but got: %q", cmd)
	}
	// Should set GT_AGENT=codex
	if !strings.Contains(cmd, "GT_AGENT=codex") {
		t.Errorf("expected command to contain 'GT_AGENT=codex' but got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_GTAgentFromResolvedAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create necessary config files
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": constants.RoleWitness},
		rigPath,
		"",
		"", // No override — should still get GT_AGENT from resolved agent
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	// GT_AGENT should be set from the resolved agent for liveness detection,
	// even when no explicit override is used.
	if !strings.Contains(cmd, "GT_AGENT=") {
		t.Errorf("expected GT_AGENT in command for liveness detection, got: %q", cmd)
	}
}

// TestBuildStartupCommand_RoleAgentsSetGTAgent verifies that when a non-Claude agent
// is configured via role_agents, GT_AGENT is set in the startup command.
// Without this, IsAgentAlive falls back to ["node", "claude"] and witness patrol
// auto-nukes polecats running non-Claude agents. See: fix/gt-agent-role-agents.
func TestBuildStartupCommand_RoleAgentsSetGTAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure opencode as the polecat agent via role_agents.
	// Define it as a custom agent so the test doesn't depend on the
	// opencode binary being in PATH (ValidateAgentConfig calls exec.LookPath).
	townSettings := NewTownSettings()
	townSettings.Agents["opencode"] = &RuntimeConfig{
		Command: "opencode",
	}
	townSettings.RoleAgents = map[string]string{
		"polecat": "opencode",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildPolecatStartupCommand("testrig", "furiosa", rigPath, "do work")

	// GT_AGENT must be set to "opencode" so IsAgentAlive detects the process
	if !strings.Contains(cmd, "GT_AGENT=opencode") {
		t.Errorf("expected GT_AGENT=opencode in command, got: %q", cmd)
	}
}

// TestBuildStartupCommand_RoleAgentsCustomAgentSetGTAgent verifies that custom
// agents defined in town settings and used via role_agents also get GT_AGENT set.
func TestBuildStartupCommand_RoleAgentsCustomAgentSetGTAgent(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Configure a custom agent "codex" mapped to opencode
	townSettings := NewTownSettings()
	townSettings.Agents["codex"] = &RuntimeConfig{
		Command: "opencode",
		Args:    []string{"-m", "openai/gpt-5.3-codex"},
	}
	townSettings.RoleAgents = map[string]string{
		"polecat": "codex",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildPolecatStartupCommand("testrig", "furiosa", rigPath, "do work")

	// GT_AGENT must be set to the custom agent name "codex"
	if !strings.Contains(cmd, "GT_AGENT=codex") {
		t.Errorf("expected GT_AGENT=codex in command, got: %q", cmd)
	}
}

// TestBuildStartupCommand_UsesGTRootFromEnvVars verifies that when rigPath is empty
// but GT_ROOT is provided in envVars, the function uses GT_ROOT to resolve town
// settings and respects role_agents configuration. This is the path hit when the
// daemon spawns town-level agents (deacon, mayor) where rigPath is always empty.
// Fixes #433
func TestBuildStartupCommand_UsesGTRootFromEnvVars(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-sonnet": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		constants.RoleDeacon: "claude-sonnet",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	envVars := map[string]string{
		"GT_ROLE": constants.RoleDeacon,
		"GT_ROOT": townRoot,
	}
	cmd := BuildStartupCommand(envVars, "", "")

	if !strings.Contains(cmd, "--model sonnet") {
		t.Errorf("expected --model sonnet from role_agents[deacon], got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_UsesGTRootFromEnvVars(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()

	townSettings := NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.Agents = map[string]*RuntimeConfig{
		"claude-sonnet": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		constants.RoleDeacon: "claude-sonnet",
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	envVars := map[string]string{
		"GT_ROLE": constants.RoleDeacon,
		"GT_ROOT": townRoot,
	}
	cmd, err := BuildStartupCommandWithAgentOverride(envVars, "", "", "")
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "--model sonnet") {
		t.Errorf("expected --model sonnet from role_agents[deacon], got: %q", cmd)
	}
}

// TestMergeQueueConfig_PartialJSON_BoolDefaults verifies that omitted *bool fields
// in a partial merge_queue JSON config deserialize to nil (not false), and that the
// nil-safe accessor methods return the correct defaults.
//
// This is a regression test for: "Partial merge_queue config silently disables
// refinery tests — omitted booleans deserialize to Go zero values (false)".
// The *bool pointer approach prevents this, and this test locks in that guarantee.
func TestMergeQueueConfig_PartialJSON_BoolDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		// Expected accessor results when *bool fields are omitted (nil)
		wantRunTests            bool
		wantDeleteMerged        bool
		wantPolecatIntegration  bool
		wantRefineryIntegration bool
		wantAutoLand            bool
	}{
		{
			name: "minimal config — all *bool fields omitted",
			json: `{"enabled": true, "on_conflict": "assign_back"}`,
			// nil *bool → accessor defaults
			wantRunTests:            true,
			wantDeleteMerged:        true,
			wantPolecatIntegration:  true,
			wantRefineryIntegration: true,
			wantAutoLand:            false,
		},
		{
			name: "explicit false — should be respected",
			json: `{
				"enabled": true,
				"on_conflict": "assign_back",
				"run_tests": false,
				"delete_merged_branches": false,
				"integration_branch_polecat_enabled": false,
				"integration_branch_refinery_enabled": false,
				"integration_branch_auto_land": false
			}`,
			wantRunTests:            false,
			wantDeleteMerged:        false,
			wantPolecatIntegration:  false,
			wantRefineryIntegration: false,
			wantAutoLand:            false,
		},
		{
			name: "explicit true — should be respected",
			json: `{
				"enabled": true,
				"on_conflict": "assign_back",
				"run_tests": true,
				"delete_merged_branches": true,
				"integration_branch_polecat_enabled": true,
				"integration_branch_refinery_enabled": true,
				"integration_branch_auto_land": true
			}`,
			wantRunTests:            true,
			wantDeleteMerged:        true,
			wantPolecatIntegration:  true,
			wantRefineryIntegration: true,
			wantAutoLand:            true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg MergeQueueConfig
			if err := json.Unmarshal([]byte(tt.json), &cfg); err != nil {
				t.Fatalf("json.Unmarshal: %v", err)
			}

			if got := cfg.IsRunTestsEnabled(); got != tt.wantRunTests {
				t.Errorf("IsRunTestsEnabled() = %v, want %v", got, tt.wantRunTests)
			}
			if got := cfg.IsDeleteMergedBranchesEnabled(); got != tt.wantDeleteMerged {
				t.Errorf("IsDeleteMergedBranchesEnabled() = %v, want %v", got, tt.wantDeleteMerged)
			}
			if got := cfg.IsPolecatIntegrationEnabled(); got != tt.wantPolecatIntegration {
				t.Errorf("IsPolecatIntegrationEnabled() = %v, want %v", got, tt.wantPolecatIntegration)
			}
			if got := cfg.IsRefineryIntegrationEnabled(); got != tt.wantRefineryIntegration {
				t.Errorf("IsRefineryIntegrationEnabled() = %v, want %v", got, tt.wantRefineryIntegration)
			}
			if got := cfg.IsIntegrationBranchAutoLandEnabled(); got != tt.wantAutoLand {
				t.Errorf("IsIntegrationBranchAutoLandEnabled() = %v, want %v", got, tt.wantAutoLand)
			}
		})
	}
}

// TestMergeQueueConfig_PartialJSON_NilPointers verifies that omitted *bool fields
// deserialize to nil, not to a pointer to false. This is the underlying mechanism
// that makes the accessor defaults work.
func TestMergeQueueConfig_PartialJSON_NilPointers(t *testing.T) {
	t.Parallel()

	partialJSON := `{"enabled": true, "on_conflict": "assign_back"}`
	var cfg MergeQueueConfig
	if err := json.Unmarshal([]byte(partialJSON), &cfg); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if cfg.RunTests != nil {
		t.Errorf("RunTests should be nil when omitted, got %v", *cfg.RunTests)
	}
	if cfg.DeleteMergedBranches != nil {
		t.Errorf("DeleteMergedBranches should be nil when omitted, got %v", *cfg.DeleteMergedBranches)
	}
	if cfg.IntegrationBranchPolecatEnabled != nil {
		t.Errorf("IntegrationBranchPolecatEnabled should be nil when omitted, got %v", *cfg.IntegrationBranchPolecatEnabled)
	}
	if cfg.IntegrationBranchRefineryEnabled != nil {
		t.Errorf("IntegrationBranchRefineryEnabled should be nil when omitted, got %v", *cfg.IntegrationBranchRefineryEnabled)
	}
	if cfg.IntegrationBranchAutoLand != nil {
		t.Errorf("IntegrationBranchAutoLand should be nil when omitted, got %v", *cfg.IntegrationBranchAutoLand)
	}
}

// --- Ephemeral Cost Tier Tests ---

func TestTryResolveFromEphemeralTier(t *testing.T) {
	t.Run("no env var returns not handled", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "")
		rc, handled := tryResolveFromEphemeralTier("witness")
		if handled {
			t.Error("expected handled=false when GT_COST_TIER not set")
		}
		if rc != nil {
			t.Errorf("expected nil rc when GT_COST_TIER not set, got %+v", rc)
		}
	})

	t.Run("invalid tier returns not handled", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "premium")
		rc, handled := tryResolveFromEphemeralTier("witness")
		if handled {
			t.Error("expected handled=false for invalid tier")
		}
		if rc != nil {
			t.Errorf("expected nil rc for invalid tier, got %+v", rc)
		}
	})

	t.Run("budget tier witness gets haiku", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "budget")
		rc, handled := tryResolveFromEphemeralTier("witness")
		if !handled {
			t.Fatal("expected handled=true for witness in budget tier")
		}
		if rc == nil {
			t.Fatal("expected RuntimeConfig for witness in budget tier")
		}
		if !isClaudeCommand(rc.Command) {
			t.Errorf("Command = %q, want claude", rc.Command)
		}
		found := false
		for i, arg := range rc.Args {
			if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "haiku" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Args %v missing --model haiku", rc.Args)
		}
	})

	t.Run("economy tier polecat returns handled with nil rc (use default)", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "economy")
		rc, handled := tryResolveFromEphemeralTier("polecat")
		if !handled {
			t.Error("expected handled=true for polecat in economy tier (tier manages this role)")
		}
		if rc != nil {
			t.Errorf("expected nil rc for polecat in economy tier (should use default), got %+v", rc)
		}
	})

	t.Run("economy tier mayor gets sonnet", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "economy")
		rc, handled := tryResolveFromEphemeralTier("mayor")
		if !handled {
			t.Fatal("expected handled=true for mayor in economy tier")
		}
		if rc == nil {
			t.Fatal("expected RuntimeConfig for mayor in economy tier")
		}
		found := false
		for i, arg := range rc.Args {
			if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "sonnet[1m]" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Args %v missing --model sonnet[1m]", rc.Args)
		}
	})

	t.Run("standard tier returns handled with nil rc for all roles", func(t *testing.T) {
		t.Setenv("GT_COST_TIER", "standard")
		for _, role := range []string{"mayor", "deacon", "witness", "refinery", "polecat", "crew"} {
			rc, handled := tryResolveFromEphemeralTier(role)
			if !handled {
				t.Errorf("standard tier should return handled=true for %s", role)
			}
			if rc != nil {
				t.Errorf("standard tier should return nil rc for %s (use default), got %+v", role, rc)
			}
		}
	})
}

func TestResolveRoleAgentConfig_WithEphemeralTier(t *testing.T) {
	townRoot := t.TempDir()

	// Create minimal town settings
	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	t.Setenv("GT_COST_TIER", "budget")

	rc := ResolveRoleAgentConfig("witness", townRoot, "")
	if rc == nil {
		t.Fatal("expected RuntimeConfig for witness with ephemeral budget tier")
	}
	if !isClaudeCommand(rc.Command) {
		t.Errorf("Command = %q, want claude", rc.Command)
	}
	found := false
	for i, arg := range rc.Args {
		if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "haiku" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Args %v missing --model haiku for budget witness", rc.Args)
	}
}

func TestResolveRoleAgentConfig_EphemeralOverridesPersistent(t *testing.T) {
	townRoot := t.TempDir()

	// Create town settings with economy tier persisted
	townSettings := NewTownSettings()
	if err := ApplyCostTier(townSettings, TierEconomy); err != nil {
		t.Fatalf("ApplyCostTier: %v", err)
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Set ephemeral to budget — should override
	t.Setenv("GT_COST_TIER", "budget")

	// witness is sonnet in economy, haiku in budget
	rc := ResolveRoleAgentConfig("witness", townRoot, "")
	if rc == nil {
		t.Fatal("expected RuntimeConfig for witness")
	}
	found := false
	for i, arg := range rc.Args {
		if arg == "--model" && i+1 < len(rc.Args) && rc.Args[i+1] == "haiku" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ephemeral budget should override persistent economy; witness Args %v missing --model haiku", rc.Args)
	}
}

func TestResolveRoleAgentConfig_EphemeralStandardSkipsPersisted(t *testing.T) {
	townRoot := t.TempDir()

	// Create town settings with budget tier persisted (haiku for witness)
	townSettings := NewTownSettings()
	if err := ApplyCostTier(townSettings, TierBudget); err != nil {
		t.Fatalf("ApplyCostTier: %v", err)
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Set ephemeral to standard — should skip persisted budget config
	t.Setenv("GT_COST_TIER", "standard")

	// polecat was claude-sonnet in budget, should now use default (opus/claude)
	rc := ResolveRoleAgentConfig("polecat", townRoot, "")
	if rc == nil {
		t.Fatal("expected RuntimeConfig for polecat")
	}
	// Should be default claude (opus), NOT claude-sonnet from stale budget config
	for i, arg := range rc.Args {
		if arg == "--model" && i+1 < len(rc.Args) {
			model := rc.Args[i+1]
			if model == "sonnet" || model == "sonnet[1m]" || model == "haiku" {
				t.Errorf("ephemeral standard should not use stale budget model; got --model %s", model)
			}
		}
	}
}

func TestResolveRoleAgentConfig_EphemeralRespectsNonClaudeOverride(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := t.TempDir()

	// Create rig settings with gemini as witness (non-Claude agent)
	rigSettingsDir := filepath.Join(rigPath, ".settings")
	if err := os.MkdirAll(rigSettingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rigSettings := &RigSettings{
		Type:    "rig-settings",
		Version: 1,
		RoleAgents: map[string]string{
			"witness": "gemini",
		},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Create town settings with gemini agent defined
	townSettings := NewTownSettings()
	townSettings.Agents["gemini"] = &RuntimeConfig{
		Command: "gemini",
		Args:    []string{},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Set ephemeral budget tier — should NOT override the gemini witness
	t.Setenv("GT_COST_TIER", "budget")

	rc := ResolveRoleAgentConfig("witness", townRoot, rigPath)
	if rc == nil {
		t.Fatal("expected RuntimeConfig for witness")
	}
	// Should still be gemini, not claude-haiku from budget tier
	if rc.Command != "gemini" {
		t.Errorf("expected gemini for witness (non-Claude rig override), got Command=%q", rc.Command)
	}
}

func TestResolveRoleAgentConfig_EphemeralDefaultPreservesNonClaudeOverride(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := t.TempDir()

	// Create rig settings with gemini as polecat (non-Claude agent)
	rigSettingsDir := filepath.Join(rigPath, ".settings")
	if err := os.MkdirAll(rigSettingsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	rigSettings := &RigSettings{
		Type:    "rig-settings",
		Version: 1,
		RoleAgents: map[string]string{
			"polecat": "gemini",
		},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// Create town settings with gemini agent defined
	townSettings := NewTownSettings()
	townSettings.Agents["gemini"] = &RuntimeConfig{
		Command: "gemini",
		Args:    []string{},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	// Economy tier maps polecat to "" (use default) — should NOT override gemini
	t.Setenv("GT_COST_TIER", "economy")

	rc := ResolveRoleAgentConfig("polecat", townRoot, rigPath)
	if rc == nil {
		t.Fatal("expected RuntimeConfig for polecat")
	}
	// Should still be gemini, not default claude — the nil-rc path must
	// preserve explicit non-Claude overrides
	if rc.Command != "gemini" {
		t.Errorf("expected gemini for polecat (non-Claude rig override with tier default), got Command=%q", rc.Command)
	}
}

func TestBuildStartupCommand_ExecWrapper(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create a rig settings with exec_wrapper configured
	rigSettings := NewRigSettings()
	rigSettings.Runtime = &RuntimeConfig{
		Command:     "claude",
		ExecWrapper: []string{"exitbox", "run", "--profile=gastown-polecat", "--"},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd := BuildStartupCommand(map[string]string{"GT_ROLE": "polecat"}, rigPath, "hello")

	// Must contain exec wrapper tokens
	if !strings.Contains(cmd, "exitbox run --profile=gastown-polecat --") {
		t.Errorf("expected exec wrapper in command, got: %q", cmd)
	}

	// The wrapper + agent command should appear as a contiguous sequence
	// "exitbox run --profile=gastown-polecat -- claude"
	if !strings.Contains(cmd, "exitbox run --profile=gastown-polecat -- claude") {
		t.Errorf("expected wrapper immediately before claude command, got: %q", cmd)
	}

	// Env vars (exec env ...) must appear before the wrapper
	envIdx := strings.Index(cmd, "exec env")
	wrapperIdx := strings.Index(cmd, "exitbox run")
	if envIdx == -1 || wrapperIdx == -1 || envIdx >= wrapperIdx {
		t.Errorf("expected 'exec env' before wrapper, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_ExecWrapper(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	// Create rig settings with exec_wrapper
	rigSettings := NewRigSettings()
	rigSettings.Runtime = &RuntimeConfig{
		Command:     "claude",
		ExecWrapper: []string{"daytona", "exec", "furiosa-ws", "--"},
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "polecat"},
		rigPath, "hello", "",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "daytona exec furiosa-ws --") {
		t.Errorf("expected exec wrapper in command, got: %q", cmd)
	}

	// The wrapper + agent command should appear as a contiguous sequence
	if !strings.Contains(cmd, "daytona exec furiosa-ws -- claude") {
		t.Errorf("expected wrapper immediately before claude command, got: %q", cmd)
	}
}

// --- Tests for GH#3153: --agent override skips --settings flag ---

func TestWithRoleSettingsFlag_IdempotencyGuard(t *testing.T) {
	t.Parallel()
	rigPath := "/fake/town/myrig"
	rc := &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--dangerously-skip-permissions", "--settings", "/already/set/.claude/settings.json"},
	}

	before := len(rc.Args)
	result := withRoleSettingsFlag(rc, "polecat", rigPath)

	if len(result.Args) != before {
		t.Errorf("idempotency guard failed: expected %d args, got %d — Args = %v", before, len(result.Args), result.Args)
	}
	count := 0
	for _, arg := range result.Args {
		if arg == "--settings" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 --settings flag, got %d — Args = %v", count, result.Args)
	}
}

func TestBuildStartupCommandWithAgentOverride_SettingsFlagForClaudeOverride(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.Agents["claude-sonnet"] = &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "sonnet", "--dangerously-skip-permissions"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "testrig/polecats/toast"},
		rigPath,
		"",
		"claude-sonnet",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "--settings") {
		t.Errorf("Claude override on polecat role should include --settings, got: %q", cmd)
	}
	expectedPath := filepath.Join(rigPath, "polecats", ".claude", "settings.json")
	if !strings.Contains(cmd, expectedPath) {
		t.Errorf("expected settings path %q in command, got: %q", expectedPath, cmd)
	}
}

func TestBuildPolecatStartupCommandWithAgentOverride_IncludesSettingsFlag(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	townSettings.Agents["claude-sonnet"] = &RuntimeConfig{
		Command: "claude",
		Args:    []string{"--model", "sonnet", "--dangerously-skip-permissions"},
	}
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildPolecatStartupCommandWithAgentOverride("testrig", "toast", rigPath, "", "claude-sonnet")
	if err != nil {
		t.Fatalf("BuildPolecatStartupCommandWithAgentOverride: %v", err)
	}

	if !strings.Contains(cmd, "--settings") {
		t.Errorf("polecat with Claude override must get --settings for hooks to fire, got: %q", cmd)
	}
	expectedPath := filepath.Join(rigPath, "polecats", ".claude", "settings.json")
	if !strings.Contains(cmd, expectedPath) {
		t.Errorf("expected settings path %q in command, got: %q", expectedPath, cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_NoSettingsFlagForNonClaude(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "testrig/polecats/toast"},
		rigPath,
		"",
		"gemini",
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	if strings.Contains(cmd, "--settings") {
		t.Errorf("non-Claude override (gemini) should NOT get --settings, got: %q", cmd)
	}
}

func TestBuildStartupCommandWithAgentOverride_NoDoubleSettingsOnNonOverridePath(t *testing.T) {
	t.Parallel()
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "testrig")

	townSettings := NewTownSettings()
	if err := SaveTownSettings(TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := SaveRigSettings(RigSettingsPath(rigPath), NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	// No override — ResolveRoleAgentConfig already adds --settings for polecat role.
	// The new withRoleSettingsFlag call in BuildStartupCommandWithAgentOverride should
	// be a no-op (idempotency guard), not double-add.
	cmd, err := BuildStartupCommandWithAgentOverride(
		map[string]string{"GT_ROLE": "testrig/polecats/toast"},
		rigPath,
		"",
		"", // no override
	)
	if err != nil {
		t.Fatalf("BuildStartupCommandWithAgentOverride: %v", err)
	}

	count := strings.Count(cmd, "--settings")
	if count > 1 {
		t.Errorf("expected at most 1 --settings flag (idempotency guard), got %d — cmd: %q", count, cmd)
	}
	if count == 0 {
		t.Errorf("default Claude agent on polecat role should still get --settings, got: %q", cmd)
	}
}
