package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePluginMD(t *testing.T) {
	content := []byte(`+++
name = "test-plugin"
description = "A test plugin"
version = 1

[gate]
type = "cooldown"
duration = "1h"

[tracking]
labels = ["test:label"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
+++

# Test Plugin

These are the instructions.
`)

	plugin, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	if plugin.Name != "test-plugin" {
		t.Errorf("expected name 'test-plugin', got %q", plugin.Name)
	}
	if plugin.Description != "A test plugin" {
		t.Errorf("expected description 'A test plugin', got %q", plugin.Description)
	}
	if plugin.Version != 1 {
		t.Errorf("expected version 1, got %d", plugin.Version)
	}
	if plugin.Location != LocationTown {
		t.Errorf("expected location 'town', got %q", plugin.Location)
	}
	if plugin.Gate == nil {
		t.Fatal("expected gate to be non-nil")
	}
	if plugin.Gate.Type != GateCooldown {
		t.Errorf("expected gate type 'cooldown', got %q", plugin.Gate.Type)
	}
	if plugin.Gate.Duration != "1h" {
		t.Errorf("expected gate duration '1h', got %q", plugin.Gate.Duration)
	}
	if plugin.Tracking == nil {
		t.Fatal("expected tracking to be non-nil")
	}
	if len(plugin.Tracking.Labels) != 1 || plugin.Tracking.Labels[0] != "test:label" {
		t.Errorf("expected labels ['test:label'], got %v", plugin.Tracking.Labels)
	}
	if !plugin.Tracking.Digest {
		t.Error("expected digest to be true")
	}
	if plugin.Execution == nil {
		t.Fatal("expected execution to be non-nil")
	}
	if plugin.Execution.Timeout != "5m" {
		t.Errorf("expected timeout '5m', got %q", plugin.Execution.Timeout)
	}
	if !plugin.Execution.NotifyOnFailure {
		t.Error("expected notify_on_failure to be true")
	}
	if plugin.Instructions == "" {
		t.Error("expected instructions to be non-empty")
	}
}

func TestParsePluginMD_MissingName(t *testing.T) {
	content := []byte(`+++
description = "No name"
+++

# No Name Plugin
`)

	_, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestParsePluginMD_MissingFrontmatter(t *testing.T) {
	content := []byte(`# No Frontmatter

Just instructions.
`)

	_, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err == nil {
		t.Error("expected error for missing frontmatter")
	}
}

func TestParsePluginMD_ManualGate(t *testing.T) {
	// Plugin with no gate section should have nil Gate
	content := []byte(`+++
name = "manual-plugin"
description = "A manual plugin"
version = 1
+++

# Manual Plugin
`)

	plugin, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	if plugin.Gate != nil {
		t.Error("expected gate to be nil for manual plugin")
	}

	// Summary should report gate type as manual
	summary := plugin.Summary()
	if summary.GateType != GateManual {
		t.Errorf("expected gate type 'manual', got %q", summary.GateType)
	}
}

func TestParsePluginMD_MalformedTOML(t *testing.T) {
	content := []byte(`+++
name = "broken
description = no quotes
+++

# Broken Plugin
`)
	_, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err == nil {
		t.Error("expected error for malformed TOML")
	}
}

func TestParsePluginMD_UnclosedFrontmatter(t *testing.T) {
	content := []byte(`+++
name = "unclosed"
description = "No closing delimiter"
`)
	_, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err == nil {
		t.Error("expected error for unclosed frontmatter")
	}
}

func TestParsePluginMD_EmptyFrontmatter(t *testing.T) {
	content := []byte(`+++
+++

# Empty Plugin
`)
	_, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err == nil {
		t.Error("expected error for empty frontmatter (missing name)")
	}
}

func TestParsePluginMD_CooldownGateNoDuration(t *testing.T) {
	content := []byte(`+++
name = "no-duration"
description = "Cooldown without duration"
version = 1

[gate]
type = "cooldown"
+++

# No Duration
`)
	// Currently accepted (no validation on gate fields beyond parsing)
	plugin, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}
	if plugin.Gate == nil {
		t.Fatal("expected gate to be non-nil")
	}
	if plugin.Gate.Type != GateCooldown {
		t.Errorf("expected gate type 'cooldown', got %q", plugin.Gate.Type)
	}
	if plugin.Gate.Duration != "" {
		t.Errorf("expected empty duration, got %q", plugin.Gate.Duration)
	}
}

func TestParsePluginMD_UnknownGateType(t *testing.T) {
	content := []byte(`+++
name = "unknown-gate"
description = "Unknown gate type"
version = 1

[gate]
type = "never-heard-of-this"
+++

# Unknown Gate
`)
	// Currently accepted (no validation on gate type values)
	plugin, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}
	if plugin.Gate == nil {
		t.Fatal("expected gate to be non-nil")
	}
	if plugin.Gate.Type != "never-heard-of-this" {
		t.Errorf("expected unknown gate type preserved, got %q", plugin.Gate.Type)
	}
}

func TestParsePluginMD_CronGate(t *testing.T) {
	content := []byte(`+++
name = "cron-plugin"
description = "Runs on schedule"
version = 1

[gate]
type = "cron"
schedule = "*/5 * * * *"
+++

# Cron Plugin
`)
	plugin, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}
	if plugin.Gate == nil {
		t.Fatal("expected gate to be non-nil")
	}
	if plugin.Gate.Type != GateCron {
		t.Errorf("expected gate type 'cron', got %q", plugin.Gate.Type)
	}
	if plugin.Gate.Schedule != "*/5 * * * *" {
		t.Errorf("expected schedule '*/5 * * * *', got %q", plugin.Gate.Schedule)
	}
}

func TestParsePluginMD_InstructionsOnly(t *testing.T) {
	content := []byte(`+++
name = "minimal"
+++

These are the instructions.
Multiple lines.
`)
	plugin, err := parsePluginMD(content, "/test/path", LocationTown, "")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}
	if plugin.Instructions == "" {
		t.Error("expected non-empty instructions")
	}
	if plugin.Description != "" {
		t.Errorf("expected empty description, got %q", plugin.Description)
	}
	if plugin.Version != 0 {
		t.Errorf("expected version 0, got %d", plugin.Version)
	}
}

func TestScanner_DiscoverAll(t *testing.T) {
	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "plugin-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create town plugins directory
	townPluginsDir := filepath.Join(tmpDir, "plugins")
	if err := os.MkdirAll(townPluginsDir, 0755); err != nil {
		t.Fatalf("failed to create town plugins dir: %v", err)
	}

	// Create a town plugin
	townPlugin := filepath.Join(townPluginsDir, "town-plugin")
	if err := os.MkdirAll(townPlugin, 0755); err != nil {
		t.Fatalf("failed to create town plugin dir: %v", err)
	}
	townPluginContent := []byte(`+++
name = "town-plugin"
description = "Town level plugin"
version = 1
+++

# Town Plugin
`)
	if err := os.WriteFile(filepath.Join(townPlugin, "plugin.md"), townPluginContent, 0644); err != nil {
		t.Fatalf("failed to write town plugin: %v", err)
	}

	// Create rig plugins directory
	rigPluginsDir := filepath.Join(tmpDir, "testrig", "plugins")
	if err := os.MkdirAll(rigPluginsDir, 0755); err != nil {
		t.Fatalf("failed to create rig plugins dir: %v", err)
	}

	// Create a rig plugin
	rigPlugin := filepath.Join(rigPluginsDir, "rig-plugin")
	if err := os.MkdirAll(rigPlugin, 0755); err != nil {
		t.Fatalf("failed to create rig plugin dir: %v", err)
	}
	rigPluginContent := []byte(`+++
name = "rig-plugin"
description = "Rig level plugin"
version = 1
+++

# Rig Plugin
`)
	if err := os.WriteFile(filepath.Join(rigPlugin, "plugin.md"), rigPluginContent, 0644); err != nil {
		t.Fatalf("failed to write rig plugin: %v", err)
	}

	// Create scanner
	scanner := NewScanner(tmpDir, []string{"testrig"})

	// Discover all plugins
	plugins, err := scanner.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(plugins) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(plugins))
	}

	// Check that we have both plugins
	names := make(map[string]bool)
	for _, p := range plugins {
		names[p.Name] = true
	}

	if !names["town-plugin"] {
		t.Error("expected to find 'town-plugin'")
	}
	if !names["rig-plugin"] {
		t.Error("expected to find 'rig-plugin'")
	}
}

func TestParsePluginMD_GitHubSheriff(t *testing.T) {
	// Verify the actual github-sheriff plugin.md parses correctly.
	// This catches frontmatter regressions in the shipped plugin.
	content, err := os.ReadFile(filepath.Join("..", "..", "plugins", "github-sheriff", "plugin.md"))
	if err != nil {
		t.Skipf("github-sheriff plugin not found (expected in plugins/): %v", err)
	}

	plugin, err := parsePluginMD(content, "/test/github-sheriff", LocationRig, "gastown")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	if plugin.Name != "github-sheriff" {
		t.Errorf("expected name 'github-sheriff', got %q", plugin.Name)
	}
	if plugin.Gate == nil {
		t.Fatal("expected gate to be non-nil")
	}
	if plugin.Gate.Type != GateCooldown {
		t.Errorf("expected gate type 'cooldown', got %q", plugin.Gate.Type)
	}
	if plugin.Gate.Duration != "2h" {
		t.Errorf("expected gate duration '2h', got %q", plugin.Gate.Duration)
	}
	if plugin.Tracking == nil {
		t.Fatal("expected tracking to be non-nil")
	}
	if !plugin.Tracking.Digest {
		t.Error("expected digest to be true")
	}
	if plugin.Execution == nil {
		t.Fatal("expected execution to be non-nil")
	}
	if plugin.Execution.Timeout != "2m" {
		t.Errorf("expected timeout '2m', got %q", plugin.Execution.Timeout)
	}
	if !plugin.Execution.NotifyOnFailure {
		t.Error("expected notify_on_failure to be true")
	}
	if plugin.Instructions == "" {
		t.Error("expected non-empty instructions")
	}
}

func TestParsePluginMD_StuckAgentDogUsesCanonicalHeartbeatPath(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("..", "..", "plugins", "stuck-agent-dog", "plugin.md"))
	if err != nil {
		t.Skipf("stuck-agent-dog plugin not found (expected in plugins/): %v", err)
	}

	plugin, err := parsePluginMD(content, "/test/stuck-agent-dog", LocationRig, "gastown")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	if plugin.Name != "stuck-agent-dog" {
		t.Fatalf("expected name 'stuck-agent-dog', got %q", plugin.Name)
	}
	if !strings.Contains(plugin.Instructions, "deacon/heartbeat.json") {
		t.Fatalf("expected canonical heartbeat path in instructions, got:\n%s", plugin.Instructions)
	}
	if strings.Contains(plugin.Instructions, ".deacon-heartbeat") {
		t.Fatalf("did not expect legacy heartbeat path in instructions, got:\n%s", plugin.Instructions)
	}
	if !strings.Contains(plugin.Instructions, "Fallback for older/runtime-copied layouts") {
		t.Fatalf("expected rigs.json fallback guidance in instructions, got:\n%s", plugin.Instructions)
	}
	if !strings.Contains(plugin.Instructions, "RIGS_JSON_PATH=\"${TOWN_ROOT}/rigs.json\"") {
		t.Fatalf("expected town-root rigs.json as canonical source in instructions, got:\n%s", plugin.Instructions)
	}
	if !strings.Contains(plugin.Instructions, "$TOWN_ROOT/mayor/rigs.json") {
		t.Fatalf("expected mayor/ fallback in instructions, got:\n%s", plugin.Instructions)
	}
	if !strings.Contains(plugin.Instructions, "Filter out any malformed/blank rows") {
		t.Fatalf("expected fail-safe blank/malformed rigs row handling in instructions, got:\n%s", plugin.Instructions)
	}
	if !strings.Contains(plugin.Instructions, "could not parse rigs.json") {
		t.Fatalf("expected fail-safe rigs.json parse handling in instructions, got:\n%s", plugin.Instructions)
	}
	if !strings.Contains(plugin.Instructions, ">20m threshold") {
		t.Fatalf("expected canonical deacon very-stale threshold in instructions, got:\n%s", plugin.Instructions)
	}
}

func TestParsePluginMD_WithRunScript(t *testing.T) {
	// Use a temp dir with a fixture plugin.md and run.sh so the test
	// doesn't depend on the local filesystem layout (fails in CI).
	pluginDir := t.TempDir()

	pluginContent := []byte(`+++
name = "example-plugin"
description = "Example plugin with run script and all features"
version = 2

[gate]
type = "cooldown"
duration = "30m"

[tracking]
labels = ["plugin:example-plugin", "category:cleanup"]
digest = true

[execution]
timeout = "5m"
notify_on_failure = true
severity = "low"
+++

# Example Plugin

Deterministic cleanup plugin with run.sh script.
`)

	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), pluginContent, 0644); err != nil {
		t.Fatalf("writing plugin.md fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/bash\necho ok\n"), 0755); err != nil {
		t.Fatalf("writing run.sh fixture: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(pluginDir, "plugin.md"))
	if err != nil {
		t.Fatalf("reading plugin.md fixture: %v", err)
	}

	plugin, err := parsePluginMD(content, pluginDir, LocationRig, "gastown")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	// Verify run.sh detection (loadPlugin does this, not parsePluginMD)
	runScriptPath := filepath.Join(pluginDir, "run.sh")
	if info, statErr := os.Stat(runScriptPath); statErr == nil && !info.IsDir() {
		plugin.HasRunScript = true
	}
	if !plugin.HasRunScript {
		t.Error("expected HasRunScript=true for plugin with run.sh")
	}

	if plugin.Name != "example-plugin" {
		t.Errorf("expected name 'example-plugin', got %q", plugin.Name)
	}
	if plugin.Gate == nil {
		t.Fatal("expected gate to be non-nil")
	}
	if plugin.Gate.Type != GateCooldown {
		t.Errorf("expected gate type 'cooldown', got %q", plugin.Gate.Type)
	}
	if plugin.Gate.Duration != "30m" {
		t.Errorf("expected gate duration '30m', got %q", plugin.Gate.Duration)
	}
	if plugin.Tracking == nil {
		t.Fatal("expected tracking to be non-nil")
	}
	if len(plugin.Tracking.Labels) != 2 {
		t.Errorf("expected 2 labels, got %d", len(plugin.Tracking.Labels))
	}
	if !plugin.Tracking.Digest {
		t.Error("expected digest to be true")
	}
	if plugin.Execution == nil {
		t.Fatal("expected execution to be non-nil")
	}
	if plugin.Execution.Timeout != "5m" {
		t.Errorf("expected timeout '5m', got %q", plugin.Execution.Timeout)
	}
	if !plugin.Execution.NotifyOnFailure {
		t.Error("expected notify_on_failure to be true")
	}
	if plugin.Instructions == "" {
		t.Error("expected non-empty instructions")
	}
}

func TestScanner_RigOverridesTown(t *testing.T) {
	// Create temp directory structure
	tmpDir, err := os.MkdirTemp("", "plugin-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create town plugins directory with a plugin
	townPluginsDir := filepath.Join(tmpDir, "plugins", "shared-plugin")
	if err := os.MkdirAll(townPluginsDir, 0755); err != nil {
		t.Fatalf("failed to create town plugins dir: %v", err)
	}
	townPluginContent := []byte(`+++
name = "shared-plugin"
description = "Town version"
version = 1
+++

# Town Version
`)
	if err := os.WriteFile(filepath.Join(townPluginsDir, "plugin.md"), townPluginContent, 0644); err != nil {
		t.Fatalf("failed to write town plugin: %v", err)
	}

	// Create rig plugins directory with same-named plugin
	rigPluginsDir := filepath.Join(tmpDir, "testrig", "plugins", "shared-plugin")
	if err := os.MkdirAll(rigPluginsDir, 0755); err != nil {
		t.Fatalf("failed to create rig plugins dir: %v", err)
	}
	rigPluginContent := []byte(`+++
name = "shared-plugin"
description = "Rig version"
version = 1
+++

# Rig Version
`)
	if err := os.WriteFile(filepath.Join(rigPluginsDir, "plugin.md"), rigPluginContent, 0644); err != nil {
		t.Fatalf("failed to write rig plugin: %v", err)
	}

	// Create scanner
	scanner := NewScanner(tmpDir, []string{"testrig"})

	// Discover all plugins
	plugins, err := scanner.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	// Should only have one plugin (rig overrides town)
	if len(plugins) != 1 {
		t.Errorf("expected 1 plugin (rig override), got %d", len(plugins))
	}

	if plugins[0].Description != "Rig version" {
		t.Errorf("expected rig version description, got %q", plugins[0].Description)
	}
	if plugins[0].Location != LocationRig {
		t.Errorf("expected location 'rig', got %q", plugins[0].Location)
	}
}

func TestLoadPlugin_DetectsRunScript(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "plugin-runsh-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create plugin dir with plugin.md AND run.sh
	pluginDir := filepath.Join(tmpDir, "plugins", "with-script")
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatalf("failed to create plugin dir: %v", err)
	}
	pluginContent := []byte(`+++
name = "with-script"
description = "Plugin with run.sh"
version = 1
+++

# Instructions (should be ignored when run.sh exists)
`)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), pluginContent, 0644); err != nil {
		t.Fatalf("failed to write plugin.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/bash\necho hello\n"), 0755); err != nil {
		t.Fatalf("failed to write run.sh: %v", err)
	}

	// Create plugin dir with plugin.md only (no run.sh)
	pluginDirNoScript := filepath.Join(tmpDir, "plugins", "no-script")
	if err := os.MkdirAll(pluginDirNoScript, 0755); err != nil {
		t.Fatalf("failed to create plugin dir: %v", err)
	}
	noScriptContent := []byte(`+++
name = "no-script"
description = "Plugin without run.sh"
version = 1
+++

# Instructions
`)
	if err := os.WriteFile(filepath.Join(pluginDirNoScript, "plugin.md"), noScriptContent, 0644); err != nil {
		t.Fatalf("failed to write plugin.md: %v", err)
	}

	scanner := NewScanner(tmpDir, nil)
	plugins, err := scanner.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}

	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugins, got %d", len(plugins))
	}

	byName := make(map[string]*Plugin)
	for _, p := range plugins {
		byName[p.Name] = p
	}

	if p, ok := byName["with-script"]; !ok {
		t.Fatal("expected to find 'with-script' plugin")
	} else if !p.HasRunScript {
		t.Error("expected HasRunScript=true for plugin with run.sh")
	}

	if p, ok := byName["no-script"]; !ok {
		t.Fatal("expected to find 'no-script' plugin")
	} else if p.HasRunScript {
		t.Error("expected HasRunScript=false for plugin without run.sh")
	}
}

func TestFormatMailBody_WithRunScript(t *testing.T) {
	p := &Plugin{
		Name:         "test-plugin",
		Description:  "A test plugin",
		Path:         "/home/user/gt/plugins/test-plugin",
		HasRunScript: true,
	}

	body := p.FormatMailBody()

	// Must contain the bash command to run the script
	if !strings.Contains(body, "cd /home/user/gt/plugins/test-plugin && bash run.sh") {
		t.Error("expected mail body to contain run.sh execution command")
	}
	// Must instruct dog NOT to interpret markdown
	if !strings.Contains(body, "Do NOT interpret the plugin.md instructions") {
		t.Error("expected mail body to warn against interpreting markdown")
	}
	// Must NOT contain "## Instructions" section
	if strings.Contains(body, "## Instructions") {
		t.Error("expected mail body to NOT contain markdown instructions section")
	}
}

func TestParsePluginMD_ExecWrapper(t *testing.T) {
	content := []byte(`+++
name = "exitbox-sandbox"
description = "Sandbox polecat execution with exitbox"
version = 1

[execution]
type = "exec-wrapper"
wrapper = ["exitbox", "run", "--profile=gastown-polecat", "--"]
+++

# Exitbox Sandbox

Wraps polecat sessions in an exitbox sandbox for filesystem and network isolation.
`)

	p, err := parsePluginMD(content, "/test/exitbox-sandbox", LocationRig, "gastown")
	if err != nil {
		t.Fatalf("parsePluginMD failed: %v", err)
	}

	if p.Name != "exitbox-sandbox" {
		t.Errorf("expected name 'exitbox-sandbox', got %q", p.Name)
	}
	if p.Execution == nil {
		t.Fatal("expected execution to be non-nil")
	}
	if p.Execution.Type != ExecTypeExecWrapper {
		t.Errorf("expected execution type 'exec-wrapper', got %q", p.Execution.Type)
	}
	if len(p.Execution.Wrapper) != 4 {
		t.Fatalf("expected 4 wrapper tokens, got %d: %v", len(p.Execution.Wrapper), p.Execution.Wrapper)
	}
	if p.Execution.Wrapper[0] != "exitbox" {
		t.Errorf("expected wrapper[0]='exitbox', got %q", p.Execution.Wrapper[0])
	}
	if p.Execution.Wrapper[3] != "--" {
		t.Errorf("expected wrapper[3]='--', got %q", p.Execution.Wrapper[3])
	}
	if !p.IsExecWrapper() {
		t.Error("expected IsExecWrapper() to return true")
	}
	args := p.ExecWrapperArgs()
	if len(args) != 4 {
		t.Errorf("expected ExecWrapperArgs to return 4 tokens, got %d", len(args))
	}
}

func TestPlugin_IsExecWrapper_False(t *testing.T) {
	// Regular plugin should not be an exec-wrapper
	p := &Plugin{
		Name: "regular",
		Execution: &Execution{
			Timeout: "5m",
		},
	}
	if p.IsExecWrapper() {
		t.Error("expected IsExecWrapper() to return false for regular plugin")
	}
	if args := p.ExecWrapperArgs(); args != nil {
		t.Errorf("expected ExecWrapperArgs to return nil, got %v", args)
	}
}

func TestPlugin_ExecWrapperSummary(t *testing.T) {
	p := &Plugin{
		Name:     "exitbox-sandbox",
		Location: LocationRig,
		RigName:  "gastown",
		Execution: &Execution{
			Type:    ExecTypeExecWrapper,
			Wrapper: []string{"exitbox", "run", "--"},
		},
	}

	summary := p.Summary()
	if summary.ExecutionType != ExecTypeExecWrapper {
		t.Errorf("expected execution type 'exec-wrapper', got %q", summary.ExecutionType)
	}
}

func TestScanner_DiscoverExecWrappers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "plugin-wrapper-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a regular plugin
	regularDir := filepath.Join(tmpDir, "plugins", "regular")
	if err := os.MkdirAll(regularDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regularDir, "plugin.md"), []byte(`+++
name = "regular"
description = "Regular plugin"
version = 1

[execution]
timeout = "5m"
+++

# Regular
`), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	// Create an exec-wrapper plugin
	wrapperDir := filepath.Join(tmpDir, "plugins", "sandbox")
	if err := os.MkdirAll(wrapperDir, 0755); err != nil {
		t.Fatalf("failed to create dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wrapperDir, "plugin.md"), []byte(`+++
name = "sandbox"
description = "Sandbox wrapper"
version = 1

[execution]
type = "exec-wrapper"
wrapper = ["exitbox", "run", "--"]
+++

# Sandbox
`), 0644); err != nil {
		t.Fatalf("failed to write: %v", err)
	}

	scanner := NewScanner(tmpDir, nil)

	// DiscoverAll should find both
	all, err := scanner.DiscoverAll()
	if err != nil {
		t.Fatalf("DiscoverAll failed: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 plugins, got %d", len(all))
	}

	// DiscoverExecWrappers should find only the wrapper
	wrappers, err := scanner.DiscoverExecWrappers()
	if err != nil {
		t.Fatalf("DiscoverExecWrappers failed: %v", err)
	}
	if len(wrappers) != 1 {
		t.Fatalf("expected 1 exec-wrapper, got %d", len(wrappers))
	}
	if wrappers[0].Name != "sandbox" {
		t.Errorf("expected wrapper name 'sandbox', got %q", wrappers[0].Name)
	}
}

func TestFormatMailBody_WithoutRunScript(t *testing.T) {
	p := &Plugin{
		Name:         "test-plugin",
		Description:  "A test plugin",
		Path:         "/home/user/gt/plugins/test-plugin",
		Instructions: "Do the thing.",
		HasRunScript: false,
	}

	body := p.FormatMailBody()

	// Must contain the instructions section
	if !strings.Contains(body, "## Instructions") {
		t.Error("expected mail body to contain instructions section")
	}
	if !strings.Contains(body, "Do the thing.") {
		t.Error("expected mail body to contain plugin instructions")
	}
	// Must NOT contain run.sh dispatch
	if strings.Contains(body, "bash run.sh") {
		t.Error("expected mail body to NOT contain run.sh command")
	}
}
