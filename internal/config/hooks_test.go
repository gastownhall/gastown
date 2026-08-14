// Test Hook Configuration Validation
//
// These tests ensure Claude Code hook configurations are correct across Gas Town.
// Specifically, they validate that:
// - All SessionStart hooks with `gt prime` include the `--hook` flag
// - The registry.toml includes all required roles for session-prime
//
// These tests exist because hook misconfiguration causes seance to fail
// (predecessor sessions become undiscoverable).

package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// ClaudeSettings represents the structure of .claude/settings.json
type ClaudeSettings struct {
	Hooks map[string][]HookEntry `json:"hooks"`
}

type HookEntry struct {
	Matcher string       `json:"matcher"`
	Hooks   []HookAction `json:"hooks"`
}

type HookAction struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// HookRegistry represents the structure of hooks/registry.toml
type HookRegistry struct {
	Hooks map[string]RegistryHook `toml:"hooks"`
}

type RegistryHook struct {
	Description string   `toml:"description"`
	Event       string   `toml:"event"`
	Matchers    []string `toml:"matchers"`
	Command     string   `toml:"command"`
	Roles       []string `toml:"roles"`
	Scope       string   `toml:"scope"`
	Enabled     bool     `toml:"enabled"`
}

// findTownRoot walks up from cwd to find the Gas Town root.
// We look for hooks/registry.toml as the unique marker (mayor/ exists at multiple levels).
func findTownRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		// hooks/registry.toml is unique to the town root
		registryPath := filepath.Join(dir, "hooks", "registry.toml")
		if _, err := os.Stat(registryPath); err == nil {
			return dir, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

// TestSessionStartHooksHaveHookFlag ensures all SessionStart hooks with
// `gt prime` include the `--hook` flag. Without this flag, sessions won't
// emit session_start events and seance can't discover predecessor sessions.
func TestSessionStartHooksHaveHookFlag(t *testing.T) {
	townRoot, err := findTownRoot()
	if err != nil {
		t.Skip("Not running inside Gas Town directory structure")
	}

	var settingsFiles []string

	err = filepath.Walk(townRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip inaccessible paths
		}
		if info.Name() == "settings.json" && strings.Contains(path, ".claude") {
			settingsFiles = append(settingsFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk directory: %v", err)
	}

	if len(settingsFiles) == 0 {
		t.Skip("No .claude/settings.json files found")
	}

	var failures []string

	for _, path := range settingsFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Logf("Warning: failed to read %s: %v", path, err)
			continue
		}

		var settings ClaudeSettings
		if err := json.Unmarshal(data, &settings); err != nil {
			t.Logf("Warning: failed to parse %s: %v", path, err)
			continue
		}

		sessionStartHooks, ok := settings.Hooks["SessionStart"]
		if !ok {
			continue // No SessionStart hooks in this file
		}

		for _, entry := range sessionStartHooks {
			for _, hook := range entry.Hooks {
				cmd := hook.Command
				// Check if command contains "gt prime" but not "--hook"
				if strings.Contains(cmd, "gt prime") && !strings.Contains(cmd, "--hook") {
					relPath, _ := filepath.Rel(townRoot, path)
					failures = append(failures, relPath)
				}
			}
		}
	}

	if len(failures) > 0 {
		t.Errorf("SessionStart hooks missing --hook flag in gt prime command:\n  %s\n\n"+
			"The --hook flag is required for seance to discover predecessor sessions.\n"+
			"Fix by changing 'gt prime' to 'gt prime --hook' in these files.",
			strings.Join(failures, "\n  "))
	}
}

// TestRegistrySessionPrimeIncludesAllRoles ensures the session-prime hook
// in registry.toml includes all worker roles that need session discovery.
func TestRegistrySessionPrimeIncludesAllRoles(t *testing.T) {
	townRoot, err := findTownRoot()
	if err != nil {
		t.Skip("Not running inside Gas Town directory structure")
	}

	registryPath := filepath.Join(townRoot, "hooks", "registry.toml")
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Skipf("hooks/registry.toml not found: %v", err)
	}

	var registry HookRegistry
	if err := toml.Unmarshal(data, &registry); err != nil {
		t.Fatalf("Failed to parse registry.toml: %v", err)
	}

	sessionPrime, ok := registry.Hooks["session-prime"]
	if !ok {
		t.Fatal("session-prime hook not found in registry.toml")
	}

	// All roles that should be able to use seance
	requiredRoles := []string{"crew", "polecat", "witness", "refinery", "mayor", "deacon"}

	roleSet := make(map[string]bool)
	for _, role := range sessionPrime.Roles {
		roleSet[role] = true
	}

	var missingRoles []string
	for _, role := range requiredRoles {
		if !roleSet[role] {
			missingRoles = append(missingRoles, role)
		}
	}

	if len(missingRoles) > 0 {
		t.Errorf("session-prime hook missing roles: %v\n\n"+
			"Current roles: %v\n"+
			"All roles need session-prime for seance to discover their predecessor sessions.",
			missingRoles, sessionPrime.Roles)
	}

	// Also verify the command has --hook
	if !strings.Contains(sessionPrime.Command, "--hook") {
		t.Errorf("session-prime command missing --hook flag:\n  %s\n\n"+
			"The --hook flag is required for seance to discover predecessor sessions.",
			sessionPrime.Command)
	}
}

// --- Cross-agent settings validation ---
//
// These tests ensure all agent settings templates (not just Claude) maintain
// required invariants. As new agents are added (gemini, copilot, etc.),
// this catches misconfiguration in their embedded templates.

// agentSettingsPattern describes where to find settings files for each agent.
type agentSettingsPattern struct {
	dirGlob  string // e.g. ".gemini"
	fileName string // e.g. "settings.json"
	format   string // "json" or "md"
}

// knownAgentPatterns lists all agent settings that should be validated.
// Add new agents here as they're integrated.
var knownAgentPatterns = []agentSettingsPattern{
	{".claude", "settings.json", "json"},
	{".gemini", "settings.json", "json"},
	// copilot uses .copilot/copilot-instructions.md (markdown, not JSON hooks)
	// opencode uses .opencode/plugin/gastown.js (JS, not JSON hooks)
}

// TestAllAgentSessionStartHooksHaveHookFlag validates that across all agent
// settings files in the town, any SessionStart hook containing "gt prime"
// includes the "--hook" flag. This extends TestSessionStartHooksHaveHookFlag
// to cover non-Claude agents.
func TestAllAgentSessionStartHooksHaveHookFlag(t *testing.T) {
	townRoot, err := findTownRoot()
	if err != nil {
		t.Skip("Not running inside Gas Town directory structure")
	}

	var failures []string

	for _, pattern := range knownAgentPatterns {
		if pattern.format != "json" {
			continue // Only JSON settings have structured hooks
		}

		err := filepath.Walk(townRoot, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			// Match agent settings dir + file name
			if info.Name() != pattern.fileName {
				return nil
			}
			if !strings.Contains(path, pattern.dirGlob) {
				return nil
			}

			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil
			}

			var settings ClaudeSettings // Same JSON structure for gemini hooks
			if jsonErr := json.Unmarshal(data, &settings); jsonErr != nil {
				return nil
			}

			sessionStartHooks, ok := settings.Hooks["SessionStart"]
			if !ok {
				return nil
			}

			for _, entry := range sessionStartHooks {
				for _, hook := range entry.Hooks {
					if strings.Contains(hook.Command, "gt prime") && !strings.Contains(hook.Command, "--hook") {
						relPath, _ := filepath.Rel(townRoot, path)
						failures = append(failures, relPath)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Logf("Warning: walk error for pattern %s: %v", pattern.dirGlob, err)
		}
	}

	if len(failures) > 0 {
		t.Errorf("SessionStart hooks missing --hook flag in gt prime command:\n  %s\n\n"+
			"The --hook flag is required for seance to discover predecessor sessions.\n"+
			"This validation covers all JSON-based agent settings (Claude, Gemini, etc.).",
			strings.Join(failures, "\n  "))
	}
}
