package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ParseDurationOrDefault ---

func TestParseDurationOrDefault(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		fallback time.Duration
		want     time.Duration
	}{
		{"valid seconds", "15s", 0, 15 * time.Second},
		{"valid minutes", "5m", 0, 5 * time.Minute},
		{"valid hours", "2h", 0, 2 * time.Hour},
		{"valid milliseconds", "500ms", 0, 500 * time.Millisecond},
		{"valid composite", "1m30s", 0, 90 * time.Second},
		{"empty string returns fallback", "", 42 * time.Second, 42 * time.Second},
		{"invalid string returns fallback", "not-a-duration", 7 * time.Second, 7 * time.Second},
		{"negative duration parses", "-5s", 10 * time.Second, -5 * time.Second},
		{"zero duration parses", "0s", 10 * time.Second, 0},
		{"bare number returns fallback", "15", 3 * time.Second, 3 * time.Second},
		{"whitespace returns fallback", "  ", 1 * time.Second, 1 * time.Second},
		{"zero fallback with empty", "", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ParseDurationOrDefault(tt.input, tt.fallback)
			if got != tt.want {
				t.Errorf("ParseDurationOrDefault(%q, %v) = %v, want %v",
					tt.input, tt.fallback, got, tt.want)
			}
		})
	}
}

// --- Default*Config functions ---

func TestDefaultWebTimeoutsConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultWebTimeoutsConfig()

	if cfg == nil {
		t.Fatal("DefaultWebTimeoutsConfig() returned nil")
	}

	tests := []struct {
		name     string
		field    string
		fallback time.Duration
		want     time.Duration
	}{
		{"CmdTimeout", cfg.CmdTimeout, 0, 15 * time.Second},
		{"GhCmdTimeout", cfg.GhCmdTimeout, 0, 10 * time.Second},
		{"TmuxCmdTimeout", cfg.TmuxCmdTimeout, 0, 2 * time.Second},
		{"FetchTimeout", cfg.FetchTimeout, 0, 8 * time.Second},
		{"DefaultRunTimeout", cfg.DefaultRunTimeout, 0, 30 * time.Second},
		{"MaxRunTimeout", cfg.MaxRunTimeout, 0, 120 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseDurationOrDefault(tt.field, tt.fallback)
			if got != tt.want {
				t.Errorf("default %s: ParseDurationOrDefault(%q, %v) = %v, want %v",
					tt.name, tt.field, tt.fallback, got, tt.want)
			}
		})
	}
}

func TestDefaultWorkerStatusConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultWorkerStatusConfig()

	if cfg == nil {
		t.Fatal("DefaultWorkerStatusConfig() returned nil")
	}

	stale := ParseDurationOrDefault(cfg.StaleThreshold, 0)
	if stale != 5*time.Minute {
		t.Errorf("StaleThreshold = %v, want 5m", stale)
	}
	stuck := ParseDurationOrDefault(cfg.StuckThreshold, 0)
	if stuck != 30*time.Minute {
		t.Errorf("StuckThreshold = %v, want 30m", stuck)
	}
	// stale < stuck invariant
	if stale >= stuck {
		t.Errorf("StaleThreshold (%v) must be < StuckThreshold (%v)", stale, stuck)
	}
	hbFresh := ParseDurationOrDefault(cfg.HeartbeatFreshThreshold, 0)
	if hbFresh != 5*time.Minute {
		t.Errorf("HeartbeatFreshThreshold = %v, want 5m", hbFresh)
	}
	mayorActive := ParseDurationOrDefault(cfg.MayorActiveThreshold, 0)
	if mayorActive != 5*time.Minute {
		t.Errorf("MayorActiveThreshold = %v, want 5m", mayorActive)
	}
}

func TestDefaultFeedCuratorConfig(t *testing.T) {
	t.Parallel()
	cfg := DefaultFeedCuratorConfig()

	if cfg == nil {
		t.Fatal("DefaultFeedCuratorConfig() returned nil")
	}

	dedupe := ParseDurationOrDefault(cfg.DoneDedupeWindow, 0)
	if dedupe != 10*time.Second {
		t.Errorf("DoneDedupeWindow = %v, want 10s", dedupe)
	}
	agg := ParseDurationOrDefault(cfg.SlingAggregateWindow, 0)
	if agg != 30*time.Second {
		t.Errorf("SlingAggregateWindow = %v, want 30s", agg)
	}
	if cfg.MinAggregateCount != 3 {
		t.Errorf("MinAggregateCount = %d, want 3", cfg.MinAggregateCount)
	}
}

// --- JSON serialization round-trips ---

func TestWebTimeoutsConfig_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	original := &WebTimeoutsConfig{
		CmdTimeout:        "20s",
		GhCmdTimeout:      "15s",
		TmuxCmdTimeout:    "3s",
		FetchTimeout:      "12s",
		DefaultRunTimeout: "45s",
		MaxRunTimeout:     "90s",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var loaded WebTimeoutsConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded != *original {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", loaded, *original)
	}
}

func TestWorkerStatusConfig_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	original := &WorkerStatusConfig{
		StaleThreshold:          "10m",
		StuckThreshold:          "1h",
		HeartbeatFreshThreshold: "3m",
		MayorActiveThreshold:    "8m",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var loaded WorkerStatusConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded != *original {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", loaded, *original)
	}
}

func TestFeedCuratorConfig_JSONRoundTrip(t *testing.T) {
	t.Parallel()
	original := &FeedCuratorConfig{
		DoneDedupeWindow:     "20s",
		SlingAggregateWindow: "1m",
		MinAggregateCount:    5,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var loaded FeedCuratorConfig
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded != *original {
		t.Errorf("round-trip mismatch:\ngot  %+v\nwant %+v", loaded, *original)
	}
}

// --- TownSettings with/without new config fields ---

// --- Gemini provider defaults ---

func TestGeminiProviderDefaults(t *testing.T) {
	t.Parallel()

	t.Run("defaultRuntimeCommand", func(t *testing.T) {
		cmd := defaultRuntimeCommand("gemini")
		if cmd != "gemini" {
			t.Errorf("defaultRuntimeCommand(gemini) = %q, want %q", cmd, "gemini")
		}
	})

	t.Run("defaultSessionIDEnv", func(t *testing.T) {
		env := defaultSessionIDEnv("gemini")
		if env != "GEMINI_SESSION_ID" {
			t.Errorf("defaultSessionIDEnv(gemini) = %q, want %q", env, "GEMINI_SESSION_ID")
		}
	})

	t.Run("defaultHooksProvider", func(t *testing.T) {
		provider := defaultHooksProvider("gemini")
		if provider != "gemini" {
			t.Errorf("defaultHooksProvider(gemini) = %q, want %q", provider, "gemini")
		}
	})

	t.Run("defaultHooksDir", func(t *testing.T) {
		dir := defaultHooksDir("gemini")
		if dir != ".gemini" {
			t.Errorf("defaultHooksDir(gemini) = %q, want %q", dir, ".gemini")
		}
	})

	t.Run("defaultHooksFile", func(t *testing.T) {
		file := defaultHooksFile("gemini")
		if file != "settings.json" {
			t.Errorf("defaultHooksFile(gemini) = %q, want %q", file, "settings.json")
		}
	})

	t.Run("defaultProcessNames", func(t *testing.T) {
		names := defaultProcessNames("gemini", "gemini")
		if len(names) != 1 || names[0] != "gemini" {
			t.Errorf("defaultProcessNames(gemini) = %v, want [gemini]", names)
		}
	})

	t.Run("defaultReadyDelayMs", func(t *testing.T) {
		delay := defaultReadyDelayMs("gemini")
		if delay != 5000 {
			t.Errorf("defaultReadyDelayMs(gemini) = %d, want 5000", delay)
		}
	})

	t.Run("defaultInstructionsFile", func(t *testing.T) {
		file := defaultInstructionsFile("gemini")
		if file != "AGENTS.md" {
			t.Errorf("defaultInstructionsFile(gemini) = %q, want %q", file, "AGENTS.md")
		}
	})
}

func TestTownSettings_WithoutNewFields_LoadsDefaults(t *testing.T) {
	t.Parallel()
	// Simulate a pre-existing settings/config.json that has NO new config fields.
	// This verifies backward compatibility: existing deployments continue to work.
	settingsJSON := `{
		"type": "town-settings",
		"version": 1,
		"default_agent": "claude"
	}`

	tmpDir := t.TempDir()
	settingsDir := filepath.Join(tmpDir, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	settingsPath := filepath.Join(settingsDir, "config.json")
	if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}

	// All new fields should be nil (omitempty means absent in JSON → nil pointer)
	if ts.WebTimeouts != nil {
		t.Errorf("WebTimeouts should be nil for legacy config, got %+v", ts.WebTimeouts)
	}
	if ts.WorkerStatus != nil {
		t.Errorf("WorkerStatus should be nil for legacy config, got %+v", ts.WorkerStatus)
	}
	if ts.FeedCurator != nil {
		t.Errorf("FeedCurator should be nil for legacy config, got %+v", ts.FeedCurator)
	}

	// Existing fields should still load correctly
	if ts.DefaultAgent != "claude" {
		t.Errorf("DefaultAgent = %q, want %q", ts.DefaultAgent, "claude")
	}
}

func TestTownSettings_WithNewFields_RoundTrip(t *testing.T) {
	t.Parallel()
	// Save TownSettings WITH all new config fields, then reload and verify.
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "config.json")

	original := NewTownSettings()
	original.WebTimeouts = &WebTimeoutsConfig{
		CmdTimeout:        "20s",
		GhCmdTimeout:      "15s",
		TmuxCmdTimeout:    "3s",
		FetchTimeout:      "12s",
		DefaultRunTimeout: "45s",
		MaxRunTimeout:     "2m",
	}
	original.WorkerStatus = &WorkerStatusConfig{
		StaleThreshold:          "10m",
		StuckThreshold:          "1h",
		HeartbeatFreshThreshold: "3m",
		MayorActiveThreshold:    "8m",
	}
	original.FeedCurator = &FeedCuratorConfig{
		DoneDedupeWindow:     "20s",
		SlingAggregateWindow: "1m",
		MinAggregateCount:    5,
	}

	if err := SaveTownSettings(settingsPath, original); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	loaded, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}

	// Verify WebTimeouts
	if loaded.WebTimeouts == nil {
		t.Fatal("WebTimeouts is nil after round-trip")
	}
	if loaded.WebTimeouts.CmdTimeout != "20s" {
		t.Errorf("CmdTimeout = %q, want %q", loaded.WebTimeouts.CmdTimeout, "20s")
	}
	if loaded.WebTimeouts.GhCmdTimeout != "15s" {
		t.Errorf("GhCmdTimeout = %q, want %q", loaded.WebTimeouts.GhCmdTimeout, "15s")
	}
	if loaded.WebTimeouts.TmuxCmdTimeout != "3s" {
		t.Errorf("TmuxCmdTimeout = %q, want %q", loaded.WebTimeouts.TmuxCmdTimeout, "3s")
	}
	if loaded.WebTimeouts.FetchTimeout != "12s" {
		t.Errorf("FetchTimeout = %q, want %q", loaded.WebTimeouts.FetchTimeout, "12s")
	}
	if loaded.WebTimeouts.DefaultRunTimeout != "45s" {
		t.Errorf("DefaultRunTimeout = %q, want %q", loaded.WebTimeouts.DefaultRunTimeout, "45s")
	}
	if loaded.WebTimeouts.MaxRunTimeout != "2m" {
		t.Errorf("MaxRunTimeout = %q, want %q", loaded.WebTimeouts.MaxRunTimeout, "2m")
	}

	// Verify WorkerStatus
	if loaded.WorkerStatus == nil {
		t.Fatal("WorkerStatus is nil after round-trip")
	}
	if loaded.WorkerStatus.StaleThreshold != "10m" {
		t.Errorf("StaleThreshold = %q, want %q", loaded.WorkerStatus.StaleThreshold, "10m")
	}
	if loaded.WorkerStatus.StuckThreshold != "1h" {
		t.Errorf("StuckThreshold = %q, want %q", loaded.WorkerStatus.StuckThreshold, "1h")
	}
	if loaded.WorkerStatus.HeartbeatFreshThreshold != "3m" {
		t.Errorf("HeartbeatFreshThreshold = %q, want %q", loaded.WorkerStatus.HeartbeatFreshThreshold, "3m")
	}
	if loaded.WorkerStatus.MayorActiveThreshold != "8m" {
		t.Errorf("MayorActiveThreshold = %q, want %q", loaded.WorkerStatus.MayorActiveThreshold, "8m")
	}

	// Verify FeedCurator
	if loaded.FeedCurator == nil {
		t.Fatal("FeedCurator is nil after round-trip")
	}
	if loaded.FeedCurator.DoneDedupeWindow != "20s" {
		t.Errorf("DoneDedupeWindow = %q, want %q", loaded.FeedCurator.DoneDedupeWindow, "20s")
	}
	if loaded.FeedCurator.SlingAggregateWindow != "1m" {
		t.Errorf("SlingAggregateWindow = %q, want %q", loaded.FeedCurator.SlingAggregateWindow, "1m")
	}
	if loaded.FeedCurator.MinAggregateCount != 5 {
		t.Errorf("MinAggregateCount = %d, want %d", loaded.FeedCurator.MinAggregateCount, 5)
	}
}

func TestTownSettings_PartialNewFields(t *testing.T) {
	t.Parallel()
	// Only some new fields are set; others should remain nil.
	settingsJSON := `{
		"type": "town-settings",
		"version": 1,
		"web_timeouts": {
			"cmd_timeout": "25s"
		}
	}`

	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ts, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}

	// WebTimeouts present with partial fields
	if ts.WebTimeouts == nil {
		t.Fatal("WebTimeouts should not be nil")
	}
	if ts.WebTimeouts.CmdTimeout != "25s" {
		t.Errorf("CmdTimeout = %q, want %q", ts.WebTimeouts.CmdTimeout, "25s")
	}
	// Unset fields within the struct should be zero-value (empty string)
	if ts.WebTimeouts.GhCmdTimeout != "" {
		t.Errorf("GhCmdTimeout = %q, want empty", ts.WebTimeouts.GhCmdTimeout)
	}
	// ParseDurationOrDefault should apply fallback for empty fields
	ghTimeout := ParseDurationOrDefault(ts.WebTimeouts.GhCmdTimeout, 10*time.Second)
	if ghTimeout != 10*time.Second {
		t.Errorf("ParseDurationOrDefault for empty GhCmdTimeout = %v, want 10s", ghTimeout)
	}

	// Other config sections should remain nil
	if ts.WorkerStatus != nil {
		t.Errorf("WorkerStatus should be nil, got %+v", ts.WorkerStatus)
	}
	if ts.FeedCurator != nil {
		t.Errorf("FeedCurator should be nil, got %+v", ts.FeedCurator)
	}
}

func TestTownSettings_MissingFile_ReturnsDefaults(t *testing.T) {
	t.Parallel()
	// LoadOrCreateTownSettings on a missing file should return defaults.
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "does-not-exist.json")

	ts, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}

	if ts.Type != "town-settings" {
		t.Errorf("Type = %q, want %q", ts.Type, "town-settings")
	}
	if ts.Version != CurrentTownSettingsVersion {
		t.Errorf("Version = %d, want %d", ts.Version, CurrentTownSettingsVersion)
	}
	// New config sections should be nil (NewTownSettings doesn't set them)
	if ts.WebTimeouts != nil {
		t.Errorf("WebTimeouts should be nil for defaults")
	}
	if ts.WorkerStatus != nil {
		t.Errorf("WorkerStatus should be nil for defaults")
	}
	if ts.FeedCurator != nil {
		t.Errorf("FeedCurator should be nil for defaults")
	}
}

// --- omitempty behavior: nil config fields must not appear in JSON ---

func TestTownSettings_OmitemptyNilFields(t *testing.T) {
	t.Parallel()
	ts := NewTownSettings()

	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	jsonStr := string(data)
	for _, key := range []string{"web_timeouts", "worker_status", "feed_curator"} {
		if strings.Contains(jsonStr, key) {
			t.Errorf("JSON should not contain %q when field is nil, got:\n%s", key, jsonStr)
		}
	}
}

func TestTownSettings_OmitemptyEmptyDurations(t *testing.T) {
	t.Parallel()
	// When config struct is set but all duration fields are empty,
	// omitempty on the string fields means they should be absent from JSON.
	ts := NewTownSettings()
	ts.WebTimeouts = &WebTimeoutsConfig{} // all zero values

	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	jsonStr := string(data)
	// The "web_timeouts" key SHOULD appear (pointer is non-nil)
	if !strings.Contains(jsonStr, "web_timeouts") {
		t.Error("JSON should contain web_timeouts when struct is non-nil")
	}
	// But individual empty string fields should be omitted
	for _, key := range []string{"cmd_timeout", "gh_cmd_timeout", "tmux_cmd_timeout"} {
		if strings.Contains(jsonStr, key) {
			t.Errorf("JSON should not contain %q when field is empty string, got:\n%s", key, jsonStr)
		}
	}
}

func TestFeedCuratorConfig_OmitemptyZeroCount(t *testing.T) {
	t.Parallel()
	cfg := &FeedCuratorConfig{
		DoneDedupeWindow: "10s",
		// MinAggregateCount=0 should be omitted (omitempty on int)
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	jsonStr := string(data)
	if strings.Contains(jsonStr, "min_aggregate_count") {
		t.Errorf("JSON should not contain min_aggregate_count when 0, got: %s", jsonStr)
	}
}

func TestTownSettings_DisabledPatrols_RoundTrip(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()
	settingsPath := filepath.Join(tmpDir, "config.json")

	original := NewTownSettings()
	original.DisabledPatrols = []string{"doctor_dog", "compactor_dog", "witness"}

	if err := SaveTownSettings(settingsPath, original); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}

	loaded, err := LoadOrCreateTownSettings(settingsPath)
	if err != nil {
		t.Fatalf("LoadOrCreateTownSettings: %v", err)
	}

	if len(loaded.DisabledPatrols) != 3 {
		t.Fatalf("expected 3 disabled patrols, got %d", len(loaded.DisabledPatrols))
	}

	expected := map[string]bool{"doctor_dog": true, "compactor_dog": true, "witness": true}
	for _, p := range loaded.DisabledPatrols {
		if !expected[p] {
			t.Errorf("unexpected disabled patrol: %q", p)
		}
	}
}

func TestTownSettings_DisabledPatrols_OmitemptyWhenNil(t *testing.T) {
	t.Parallel()
	ts := NewTownSettings()
	data, err := json.MarshalIndent(ts, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(data), "disabled_patrols") {
		t.Error("JSON should not contain disabled_patrols when nil")
	}
}

// --- Edge cases for config values ---

func TestParseDurationOrDefault_AllWebTimeoutDefaults(t *testing.T) {
	t.Parallel()
	// Verify that an empty WebTimeoutsConfig (all fields "") falls back to
	// the same values as DefaultWebTimeoutsConfig when parsed.
	empty := &WebTimeoutsConfig{}
	defaults := DefaultWebTimeoutsConfig()

	pairs := []struct {
		name     string
		empty    string
		dflt     string
		fallback time.Duration
	}{
		{"CmdTimeout", empty.CmdTimeout, defaults.CmdTimeout, 15 * time.Second},
		{"GhCmdTimeout", empty.GhCmdTimeout, defaults.GhCmdTimeout, 10 * time.Second},
		{"TmuxCmdTimeout", empty.TmuxCmdTimeout, defaults.TmuxCmdTimeout, 2 * time.Second},
		{"FetchTimeout", empty.FetchTimeout, defaults.FetchTimeout, 8 * time.Second},
		{"DefaultRunTimeout", empty.DefaultRunTimeout, defaults.DefaultRunTimeout, 30 * time.Second},
		{"MaxRunTimeout", empty.MaxRunTimeout, defaults.MaxRunTimeout, 120 * time.Second},
	}

	for _, p := range pairs {
		t.Run(p.name, func(t *testing.T) {
			// Empty field should produce fallback
			got := ParseDurationOrDefault(p.empty, p.fallback)
			if got != p.fallback {
				t.Errorf("empty %s: got %v, want %v", p.name, got, p.fallback)
			}
			// Default field should produce same value as fallback
			got = ParseDurationOrDefault(p.dflt, 0)
			if got != p.fallback {
				t.Errorf("default %s: got %v, want %v", p.name, got, p.fallback)
			}
		})
	}
}
