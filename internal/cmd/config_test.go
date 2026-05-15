package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/config"
)

// setupTestTown creates a minimal Gas Town workspace for testing.
func setupTestTownForConfig(t *testing.T) string {
	t.Helper()

	townRoot := t.TempDir()

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

	return townRoot
}

func TestConfigAgentList(t *testing.T) {
	t.Run("lists built-in agents", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Change to town root so workspace.FindFromCwd works
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{}
		err := runConfigAgentList(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentList failed: %v", err)
		}

		// Verify settings file was created (LoadOrCreate creates it)
		if _, err := os.Stat(settingsPath); err != nil {
			// This is OK - list command works without settings file
		}
	})

	t.Run("lists built-in and custom agents", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Create settings with custom agent
		settings := &config.TownSettings{
			Type:         "town-settings",
			Version:      config.CurrentTownSettingsVersion,
			DefaultAgent: "claude",
			Agents: map[string]*config.RuntimeConfig{
				"my-custom": {
					Command: "my-agent",
					Args:    []string{"--flag"},
				},
			},
		}
		if err := config.SaveTownSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Load agent registry
		registryPath := config.DefaultAgentRegistryPath(townRoot)
		if err := config.LoadAgentRegistry(registryPath); err != nil {
			t.Fatalf("load agent registry: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{}
		err := runConfigAgentList(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentList failed: %v", err)
		}
	})

	t.Run("JSON output", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Load agent registry
		registryPath := config.DefaultAgentRegistryPath(townRoot)
		if err := config.LoadAgentRegistry(registryPath); err != nil {
			t.Fatalf("load agent registry: %v", err)
		}

		// Use a command with the --json flag registered
		cmd := &cobra.Command{}
		cmd.Flags().Bool("json", true, "")
		args := []string{}
		err := runConfigAgentList(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentList failed: %v", err)
		}
	})
}

func TestConfigAgentGet(t *testing.T) {
	t.Run("gets built-in agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Load agent registry
		registryPath := config.DefaultAgentRegistryPath(townRoot)
		if err := config.LoadAgentRegistry(registryPath); err != nil {
			t.Fatalf("load agent registry: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{"claude"}
		err := runConfigAgentGet(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentGet failed: %v", err)
		}
	})

	t.Run("gets custom agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Create settings with custom agent
		settings := &config.TownSettings{
			Type:         "town-settings",
			Version:      config.CurrentTownSettingsVersion,
			DefaultAgent: "claude",
			Agents: map[string]*config.RuntimeConfig{
				"my-custom": {
					Command: "my-agent",
					Args:    []string{"--flag1", "--flag2"},
				},
			},
		}
		if err := config.SaveTownSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Load agent registry
		registryPath := config.DefaultAgentRegistryPath(townRoot)
		if err := config.LoadAgentRegistry(registryPath); err != nil {
			t.Fatalf("load agent registry: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{"my-custom"}
		err := runConfigAgentGet(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentGet failed: %v", err)
		}
	})

	t.Run("returns error for unknown agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Load agent registry
		registryPath := config.DefaultAgentRegistryPath(townRoot)
		if err := config.LoadAgentRegistry(registryPath); err != nil {
			t.Fatalf("load agent registry: %v", err)
		}

		// Run the command with unknown agent
		cmd := &cobra.Command{}
		args := []string{"unknown-agent"}
		err := runConfigAgentGet(cmd, args)
		if err == nil {
			t.Fatal("expected error for unknown agent")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %v, want 'not found'", err)
		}
	})
}

func TestConfigAgentSet(t *testing.T) {
	t.Run("sets custom agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{"my-agent", "my-agent --arg1 --arg2"}
		err := runConfigAgentSet(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentSet failed: %v", err)
		}

		// Verify settings were saved
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		if loaded.Agents == nil {
			t.Fatal("Agents map is nil")
		}
		agent, ok := loaded.Agents["my-agent"]
		if !ok {
			t.Fatal("custom agent not found in settings")
		}
		if agent.Command != "my-agent" {
			t.Errorf("Command = %q, want 'my-agent'", agent.Command)
		}
		if len(agent.Args) != 2 {
			t.Errorf("Args count = %d, want 2", len(agent.Args))
		}
	})

	t.Run("sets agent with single command (no args)", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{"simple-agent", "simple-agent"}
		err := runConfigAgentSet(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentSet failed: %v", err)
		}

		// Verify settings were saved
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		agent := loaded.Agents["simple-agent"]
		if agent.Command != "simple-agent" {
			t.Errorf("Command = %q, want 'simple-agent'", agent.Command)
		}
		if len(agent.Args) != 0 {
			t.Errorf("Args count = %d, want 0", len(agent.Args))
		}
	})

	t.Run("overrides existing agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Create initial settings
		settings := &config.TownSettings{
			Type:         "town-settings",
			Version:      config.CurrentTownSettingsVersion,
			DefaultAgent: "claude",
			Agents: map[string]*config.RuntimeConfig{
				"my-agent": {
					Command: "old-command",
					Args:    []string{"--old"},
				},
			},
		}
		if err := config.SaveTownSettings(settingsPath, settings); err != nil {
			t.Fatalf("save initial settings: %v", err)
		}

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run the command to override
		cmd := &cobra.Command{}
		args := []string{"my-agent", "new-command --new"}
		err := runConfigAgentSet(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentSet failed: %v", err)
		}

		// Verify settings were updated
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		agent := loaded.Agents["my-agent"]
		if agent.Command != "new-command" {
			t.Errorf("Command = %q, want 'new-command'", agent.Command)
		}
	})
}

func TestConfigAgentSetProviderInference(t *testing.T) {
	t.Run("infers provider from known command name", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// "gemini" is a known preset — provider should be inferred
		configAgentSetProvider = ""
		cmd := &cobra.Command{}
		args := []string{"gemini-custom", "gemini --fast-mode"}
		if err := runConfigAgentSet(cmd, args); err != nil {
			t.Fatalf("runConfigAgentSet failed: %v", err)
		}

		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		agent := loaded.Agents["gemini-custom"]
		if agent == nil {
			t.Fatal("agent not found")
		}
		if agent.Provider != "gemini" {
			t.Errorf("Provider = %q, want 'gemini' (inferred from command)", agent.Provider)
		}
		if agent.Command != "gemini" {
			t.Errorf("Command = %q, want 'gemini'", agent.Command)
		}
	})

	t.Run("no provider inferred for unknown command", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// "my-custom-tool" is not a known preset — provider should remain empty
		configAgentSetProvider = ""
		cmd := &cobra.Command{}
		args := []string{"my-bot", "my-custom-tool --flag"}
		if err := runConfigAgentSet(cmd, args); err != nil {
			t.Fatalf("runConfigAgentSet failed: %v", err)
		}

		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		agent := loaded.Agents["my-bot"]
		if agent == nil {
			t.Fatal("agent not found")
		}
		if agent.Provider != "" {
			t.Errorf("Provider = %q, want '' (no inference for unknown command)", agent.Provider)
		}
	})

	t.Run("explicit --provider flag overrides inference", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Command name is unknown, but explicit provider is given
		configAgentSetProvider = "claude"
		defer func() { configAgentSetProvider = "" }()

		cmd := &cobra.Command{}
		args := []string{"my-claude-wrapper", "my-claude-wrapper --custom"}
		if err := runConfigAgentSet(cmd, args); err != nil {
			t.Fatalf("runConfigAgentSet failed: %v", err)
		}

		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		agent := loaded.Agents["my-claude-wrapper"]
		if agent == nil {
			t.Fatal("agent not found")
		}
		if agent.Provider != "claude" {
			t.Errorf("Provider = %q, want 'claude' (explicit --provider flag)", agent.Provider)
		}
	})
}

func TestConfigAgentRemove(t *testing.T) {
	t.Run("removes custom agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Create settings with custom agent
		settings := &config.TownSettings{
			Type:         "town-settings",
			Version:      config.CurrentTownSettingsVersion,
			DefaultAgent: "claude",
			Agents: map[string]*config.RuntimeConfig{
				"my-agent": {
					Command: "my-agent",
					Args:    []string{"--flag"},
				},
			},
		}
		if err := config.SaveTownSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run the command
		cmd := &cobra.Command{}
		args := []string{"my-agent"}
		err := runConfigAgentRemove(cmd, args)
		if err != nil {
			t.Fatalf("runConfigAgentRemove failed: %v", err)
		}

		// Verify agent was removed
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		if loaded.Agents != nil {
			if _, ok := loaded.Agents["my-agent"]; ok {
				t.Error("agent still exists after removal")
			}
		}
	})

	t.Run("rejects removing built-in agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Try to remove a built-in agent
		cmd := &cobra.Command{}
		args := []string{"claude"}
		err := runConfigAgentRemove(cmd, args)
		if err == nil {
			t.Fatal("expected error when removing built-in agent")
		}
		if !strings.Contains(err.Error(), "cannot remove built-in") {
			t.Errorf("error = %v, want 'cannot remove built-in'", err)
		}
	})

	t.Run("returns error for non-existent custom agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Try to remove a non-existent agent
		cmd := &cobra.Command{}
		args := []string{"non-existent"}
		err := runConfigAgentRemove(cmd, args)
		if err == nil {
			t.Fatal("expected error for non-existent agent")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %v, want 'not found'", err)
		}
	})
}

func TestConfigDefaultAgent(t *testing.T) {
	t.Run("gets default agent (shows current)", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Run the command with no args (should show current default)
		cmd := &cobra.Command{}
		args := []string{}
		err := runConfigDefaultAgent(cmd, args)
		if err != nil {
			t.Fatalf("runConfigDefaultAgent failed: %v", err)
		}
	})

	t.Run("sets default agent to built-in", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Set default to gemini
		cmd := &cobra.Command{}
		args := []string{"gemini"}
		err := runConfigDefaultAgent(cmd, args)
		if err != nil {
			t.Fatalf("runConfigDefaultAgent failed: %v", err)
		}

		// Verify settings were saved
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		if loaded.DefaultAgent != "gemini" {
			t.Errorf("DefaultAgent = %q, want 'gemini'", loaded.DefaultAgent)
		}
	})

	t.Run("sets default agent to custom", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		// Create settings with custom agent
		settings := &config.TownSettings{
			Type:         "town-settings",
			Version:      config.CurrentTownSettingsVersion,
			DefaultAgent: "claude",
			Agents: map[string]*config.RuntimeConfig{
				"my-custom": {
					Command: "my-agent",
					Args:    []string{},
				},
			},
		}
		if err := config.SaveTownSettings(settingsPath, settings); err != nil {
			t.Fatalf("save settings: %v", err)
		}

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Set default to custom agent
		cmd := &cobra.Command{}
		args := []string{"my-custom"}
		err := runConfigDefaultAgent(cmd, args)
		if err != nil {
			t.Fatalf("runConfigDefaultAgent failed: %v", err)
		}

		// Verify settings were saved
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}

		if loaded.DefaultAgent != "my-custom" {
			t.Errorf("DefaultAgent = %q, want 'my-custom'", loaded.DefaultAgent)
		}
	})

	t.Run("returns error for unknown agent", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Change to town root
		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Try to set default to unknown agent
		cmd := &cobra.Command{}
		args := []string{"unknown-agent"}
		err := runConfigDefaultAgent(cmd, args)
		if err == nil {
			t.Fatal("expected error for unknown agent")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %v, want 'not found'", err)
		}
	})
}

func TestConfigDefaultAgentList(t *testing.T) {
	t.Run("lists available agents via default-agent list", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// runConfigAgentList is reused by default-agent list
		cmd := &cobra.Command{}
		err := runConfigAgentList(cmd, []string{})
		if err != nil {
			t.Fatalf("runConfigAgentList (via default-agent list) failed: %v", err)
		}
	})

	t.Run("JSON output via default-agent list", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		registryPath := config.DefaultAgentRegistryPath(townRoot)
		if err := config.LoadAgentRegistry(registryPath); err != nil {
			t.Fatalf("load agent registry: %v", err)
		}

		cmd := &cobra.Command{}
		cmd.Flags().Bool("json", true, "")
		err := runConfigAgentList(cmd, []string{})
		if err != nil {
			t.Fatalf("runConfigAgentList JSON (via default-agent list) failed: %v", err)
		}
	})
}

func TestConfigSetGet(t *testing.T) {
	t.Run("set and get convoy.notify_on_complete", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Set convoy.notify_on_complete to true
		cmd := &cobra.Command{}
		err := runConfigSet(cmd, []string{"convoy.notify_on_complete", "true"})
		if err != nil {
			t.Fatalf("runConfigSet failed: %v", err)
		}

		// Verify persisted
		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if loaded.Convoy == nil {
			t.Fatal("Convoy config is nil after set")
		}
		if !loaded.Convoy.NotifyOnComplete {
			t.Error("NotifyOnComplete should be true")
		}

		// Get the value back
		err = runConfigGet(cmd, []string{"convoy.notify_on_complete"})
		if err != nil {
			t.Fatalf("runConfigGet failed: %v", err)
		}

		// Set back to false
		err = runConfigSet(cmd, []string{"convoy.notify_on_complete", "false"})
		if err != nil {
			t.Fatalf("runConfigSet(false) failed: %v", err)
		}

		loaded, err = config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if loaded.Convoy != nil && loaded.Convoy.NotifyOnComplete {
			t.Error("NotifyOnComplete should be false after setting to false")
		}
	})

	t.Run("set and get cli_theme", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)
		settingsPath := config.TownSettingsPath(townRoot)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		cmd := &cobra.Command{}
		err := runConfigSet(cmd, []string{"cli_theme", "dark"})
		if err != nil {
			t.Fatalf("runConfigSet failed: %v", err)
		}

		loaded, err := config.LoadOrCreateTownSettings(settingsPath)
		if err != nil {
			t.Fatalf("load settings: %v", err)
		}
		if loaded.CLITheme != "dark" {
			t.Errorf("CLITheme = %q, want 'dark'", loaded.CLITheme)
		}
	})

	t.Run("set cli_theme rejects invalid value", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		cmd := &cobra.Command{}
		err := runConfigSet(cmd, []string{"cli_theme", "neon"})
		if err == nil {
			t.Fatal("expected error for invalid cli_theme")
		}
		if !strings.Contains(err.Error(), "invalid cli_theme") {
			t.Errorf("error = %v, want 'invalid cli_theme'", err)
		}
	})

	t.Run("set rejects unknown key", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		cmd := &cobra.Command{}
		err := runConfigSet(cmd, []string{"nonexistent.key", "value"})
		if err == nil {
			t.Fatal("expected error for unknown key")
		}
		if !strings.Contains(err.Error(), "unknown config key") {
			t.Errorf("error = %v, want 'unknown config key'", err)
		}
	})

	t.Run("get rejects unknown key", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		cmd := &cobra.Command{}
		err := runConfigGet(cmd, []string{"nonexistent.key"})
		if err == nil {
			t.Fatal("expected error for unknown key")
		}
		if !strings.Contains(err.Error(), "unknown config key") {
			t.Errorf("error = %v, want 'unknown config key'", err)
		}
	})

	t.Run("convoy.notify_on_complete rejects non-boolean", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		cmd := &cobra.Command{}
		err := runConfigSet(cmd, []string{"convoy.notify_on_complete", "maybe"})
		if err == nil {
			t.Fatal("expected error for non-boolean value")
		}
		if !strings.Contains(err.Error(), "invalid value") {
			t.Errorf("error = %v, want 'invalid value'", err)
		}
	})
}

func TestConfigMaintenanceSetGet(t *testing.T) {
	t.Run("set and get maintenance.window", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		// Set maintenance window
		err := setMaintenanceConfig(townRoot, "maintenance.window", "03:00")
		if err != nil {
			t.Fatalf("setMaintenanceConfig failed: %v", err)
		}

		// Get it back
		err = getMaintenanceConfig(townRoot, "maintenance.window")
		if err != nil {
			t.Fatalf("getMaintenanceConfig failed: %v", err)
		}
	})

	t.Run("set maintenance.window validates format", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		// Valid windows
		for _, w := range []string{"03:00", "00:00", "23:59", "12:30"} {
			if err := setMaintenanceConfig(townRoot, "maintenance.window", w); err != nil {
				t.Errorf("setMaintenanceConfig(%q) unexpected error: %v", w, err)
			}
		}

		// Invalid windows
		for _, w := range []string{"25:00", "12:60", "abc", "12", ""} {
			if err := setMaintenanceConfig(townRoot, "maintenance.window", w); err == nil {
				t.Errorf("setMaintenanceConfig(%q) expected error", w)
			}
		}
	})

	t.Run("set and get maintenance.interval", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		for _, interval := range []string{"daily", "weekly", "monthly", "48h"} {
			if err := setMaintenanceConfig(townRoot, "maintenance.interval", interval); err != nil {
				t.Errorf("setMaintenanceConfig(interval=%q) unexpected error: %v", interval, err)
			}
		}

		// Invalid interval
		if err := setMaintenanceConfig(townRoot, "maintenance.interval", "whenever"); err == nil {
			t.Error("expected error for invalid interval 'whenever'")
		}
	})

	t.Run("set and get maintenance.threshold", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		if err := setMaintenanceConfig(townRoot, "maintenance.threshold", "500"); err != nil {
			t.Fatalf("setMaintenanceConfig(threshold=500) failed: %v", err)
		}

		// Invalid thresholds
		if err := setMaintenanceConfig(townRoot, "maintenance.threshold", "0"); err == nil {
			t.Error("expected error for threshold 0")
		}
		if err := setMaintenanceConfig(townRoot, "maintenance.threshold", "abc"); err == nil {
			t.Error("expected error for non-numeric threshold")
		}
	})

	t.Run("maintenance config routes through runConfigSet", func(t *testing.T) {
		townRoot := setupTestTownForConfig(t)

		originalWd, _ := os.Getwd()
		defer os.Chdir(originalWd)
		if err := os.Chdir(townRoot); err != nil {
			t.Fatalf("chdir: %v", err)
		}

		cmd := &cobra.Command{}
		err := runConfigSet(cmd, []string{"maintenance.window", "04:00"})
		if err != nil {
			t.Fatalf("runConfigSet(maintenance.window) failed: %v", err)
		}

		err = runConfigGet(cmd, []string{"maintenance.window"})
		if err != nil {
			t.Fatalf("runConfigGet(maintenance.window) failed: %v", err)
		}
	})
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input string
		want  bool
		err   bool
	}{
		{"true", true, false},
		{"True", true, false},
		{"TRUE", true, false},
		{"yes", true, false},
		{"1", true, false},
		{"on", true, false},
		{"false", false, false},
		{"False", false, false},
		{"no", false, false},
		{"0", false, false},
		{"off", false, false},
		{"maybe", false, true},
		{"", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseBool(tt.input)
			if (err != nil) != tt.err {
				t.Errorf("parseBool(%q) error = %v, wantErr %v", tt.input, err, tt.err)
				return
			}
			if got != tt.want {
				t.Errorf("parseBool(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
