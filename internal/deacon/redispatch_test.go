package deacon

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseRecoveredBeadSubject(t *testing.T) {
	tests := []struct {
		subject string
		wantID  string
		wantOK  bool
	}{
		{"RECOVERED_BEAD gt-abc123", "gt-abc123", true},
		{"RECOVERED_BEAD bd-xyz", "bd-xyz", true},
		{"RECOVERED_BEAD   gt-abc123  ", "gt-abc123", true},
		{"RECOVERED_BEAD", "", false},
		{"RECOVERED_BEAD ", "", false},
		{"MERGE_READY foo", "", false},
		{"", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.subject, func(t *testing.T) {
			gotID, gotOK := ParseRecoveredBeadSubject(tt.subject)
			if gotID != tt.wantID || gotOK != tt.wantOK {
				t.Errorf("ParseRecoveredBeadSubject(%q) = (%q, %v), want (%q, %v)",
					tt.subject, gotID, gotOK, tt.wantID, tt.wantOK)
			}
		})
	}
}

func TestParseRecoveredBeadBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantRig string
	}{
		{
			name: "standard format",
			body: `Recovered abandoned bead from dead polecat.

Bead: gt-abc123
Polecat: gastown/max
Previous Status: hooked

The bead has been reset to open with no assignee.`,
			wantRig: "gastown",
		},
		{
			name:    "no polecat line",
			body:    "Some other body content",
			wantRig: "",
		},
		{
			name: "different rig",
			body: `Bead: bd-xyz
Polecat: beads/alpha
Previous Status: in_progress`,
			wantRig: "beads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRecoveredBeadBody(tt.body)
			if got != tt.wantRig {
				t.Errorf("ParseRecoveredBeadBody() = %q, want %q", got, tt.wantRig)
			}
		})
	}
}

func TestRedispatchState_LoadSave(t *testing.T) {
	tmpDir := t.TempDir()
	deaconDir := filepath.Join(tmpDir, "deacon")
	if err := os.MkdirAll(deaconDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Test empty state
	state, err := LoadRedispatchState(tmpDir)
	if err != nil {
		t.Fatalf("LoadRedispatchState: %v", err)
	}
	if len(state.Beads) != 0 {
		t.Errorf("expected empty beads, got %d", len(state.Beads))
	}

	// Add some state
	beadState := state.GetBeadState("gt-abc")
	beadState.RecordAttempt("gastown")
	beadState.RecordAttempt("gastown")

	if err := SaveRedispatchState(tmpDir, state); err != nil {
		t.Fatalf("SaveRedispatchState: %v", err)
	}

	// Reload
	loaded, err := LoadRedispatchState(tmpDir)
	if err != nil {
		t.Fatalf("LoadRedispatchState after save: %v", err)
	}

	loadedBead := loaded.GetBeadState("gt-abc")
	if loadedBead.AttemptCount != 2 {
		t.Errorf("expected 2 attempts, got %d", loadedBead.AttemptCount)
	}
	if loadedBead.LastRig != "gastown" {
		t.Errorf("expected LastRig=gastown, got %q", loadedBead.LastRig)
	}
}

func TestBeadRedispatchState_Cooldown(t *testing.T) {
	state := &BeadRedispatchState{BeadID: "gt-test"}

	// Not in cooldown initially
	if state.IsInCooldown(5 * time.Minute) {
		t.Error("expected not in cooldown initially")
	}

	// Record attempt puts in cooldown
	state.RecordAttempt("gastown")
	if !state.IsInCooldown(5 * time.Minute) {
		t.Error("expected in cooldown after attempt")
	}

	remaining := state.CooldownRemaining(5 * time.Minute)
	if remaining <= 0 || remaining > 5*time.Minute {
		t.Errorf("expected cooldown remaining in (0, 5m], got %v", remaining)
	}

	// Not in cooldown with 0 duration
	if state.IsInCooldown(0) {
		t.Error("expected not in cooldown with 0 duration")
	}
}

func TestBeadRedispatchState_ShouldEscalate(t *testing.T) {
	state := &BeadRedispatchState{BeadID: "gt-test"}

	if state.ShouldEscalate(3) {
		t.Error("should not escalate with 0 attempts")
	}

	state.AttemptCount = 2
	if state.ShouldEscalate(3) {
		t.Error("should not escalate with 2/3 attempts")
	}

	state.AttemptCount = 3
	if !state.ShouldEscalate(3) {
		t.Error("should escalate with 3/3 attempts")
	}

	state.AttemptCount = 5
	if !state.ShouldEscalate(3) {
		t.Error("should escalate with 5/3 attempts")
	}
}

func TestBeadRedispatchState_Escalation(t *testing.T) {
	state := &BeadRedispatchState{BeadID: "gt-test"}

	if state.Escalated {
		t.Error("should not be escalated initially")
	}

	state.RecordEscalation()

	if !state.Escalated {
		t.Error("should be escalated after RecordEscalation")
	}
	if state.EscalatedAt.IsZero() {
		t.Error("EscalatedAt should be set")
	}
}

func TestRedispatchState_GetBeadState(t *testing.T) {
	state := &RedispatchState{}

	// GetBeadState creates map if nil
	bead := state.GetBeadState("gt-new")
	if bead == nil {
		t.Fatal("expected non-nil bead state")
	}
	if bead.BeadID != "gt-new" {
		t.Errorf("expected BeadID=gt-new, got %q", bead.BeadID)
	}

	// Second call returns same object
	bead2 := state.GetBeadState("gt-new")
	if bead != bead2 {
		t.Error("expected same bead state object on second call")
	}

	// Different bead returns different object
	bead3 := state.GetBeadState("gt-other")
	if bead == bead3 {
		t.Error("expected different bead state for different ID")
	}
}

func TestLoadModelEscalationConfig(t *testing.T) {
	t.Run("missing file returns nil", func(t *testing.T) {
		cfg, err := LoadModelEscalationConfig(t.TempDir())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg != nil {
			t.Error("expected nil config for missing file")
		}
	})

	t.Run("loads valid config", func(t *testing.T) {
		dir := t.TempDir()
		gastown := filepath.Join(dir, ".gastown")
		if err := os.MkdirAll(gastown, 0755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"type": "model-escalation",
			"version": 1,
			"enabled": true,
			"rules": [
				{
					"from_agent": "claude-sonnet",
					"to_agent": "claude",
					"promote_after_failures": 1
				}
			]
		}`
		if err := os.WriteFile(filepath.Join(gastown, "model-escalation.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadModelEscalationConfig(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("expected non-nil config")
		}
		if !cfg.Enabled {
			t.Error("expected enabled=true")
		}
		if len(cfg.Rules) != 1 {
			t.Fatalf("expected 1 rule, got %d", len(cfg.Rules))
		}
		if cfg.Rules[0].ToAgent != "claude" {
			t.Errorf("expected ToAgent=claude, got %q", cfg.Rules[0].ToAgent)
		}
	})

	t.Run("loads bounded local first policy", func(t *testing.T) {
		dir := t.TempDir()
		gastown := filepath.Join(dir, ".gastown")
		if err := os.MkdirAll(gastown, 0755); err != nil {
			t.Fatal(err)
		}
		content := `{
			"type":"model-escalation","version":2,"enabled":true,"rules":[],
			"recovery":{
				"nudge_after":"15m","local_restart_after":"30m","go_escalate_after":"60m",
				"max_local_restarts":1,"max_go_attempts":1,
				"break_glass":{"enabled":true,"scope":"infrastructure-only",
					"agents":["claude","codex"],"max_attempts_per_agent":1,
					"timeout":"10m","automatic":true,
					"tier_policy":{
						"claude":{"max_attempts":1,"timeout":"10m","mode":"automatic"},
						"codex":{"max_attempts":1,"timeout":"10m","mode":"automatic"}
					}}
			}
		}`
		if err := os.WriteFile(filepath.Join(gastown, "model-escalation.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadModelEscalationConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Recovery == nil || cfg.Recovery.GoEscalateAfter != "60m" {
			t.Fatalf("recovery policy = %#v", cfg.Recovery)
		}
	})

	t.Run("rejects product scoped break glass", func(t *testing.T) {
		dir := t.TempDir()
		gastown := filepath.Join(dir, ".gastown")
		if err := os.MkdirAll(gastown, 0755); err != nil {
			t.Fatal(err)
		}
		content := `{"enabled":true,"rules":[],"recovery":{"break_glass":{
			"enabled":true,"scope":"all-work","agents":["claude","codex"],
			"max_attempts_per_agent":1,"timeout":"10m",
			"tier_policy":{
				"claude":{"max_attempts":1,"timeout":"10m","mode":"automatic"},
				"codex":{"max_attempts":1,"timeout":"10m","mode":"automatic"}
			}}}}`
		if err := os.WriteFile(filepath.Join(gastown, "model-escalation.json"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadModelEscalationConfig(dir); err == nil {
			t.Fatal("expected unsafe break-glass policy to be rejected")
		}
	})
}

func TestResolveAgentForRedispatch(t *testing.T) {
	// Build a temp town with a fake rig project directory containing escalation config.
	townDir := t.TempDir()
	rigName := "myrig"
	rigProjectDir := filepath.Join(townDir, rigName, "refinery", "rig", ".gastown")
	if err := os.MkdirAll(rigProjectDir, 0755); err != nil {
		t.Fatal(err)
	}
	configContent := `{
		"type": "model-escalation",
		"version": 1,
		"enabled": true,
		"rules": [
			{
				"from_agent": "claude-sonnet",
				"to_agent": "claude",
				"promote_after_failures": 1
			}
		]
	}`
	if err := os.WriteFile(filepath.Join(rigProjectDir, "model-escalation.json"), []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name         string
		attemptCount int
		wantAgent    string
	}{
		{
			name:         "first re-dispatch (1 total failure) promotes at threshold 1",
			attemptCount: 0,
			wantAgent:    "claude",
		},
		{
			name:         "second re-dispatch also promotes",
			attemptCount: 1,
			wantAgent:    "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := &BeadRedispatchState{
				BeadID:       "gt-test",
				AttemptCount: tt.attemptCount,
			}
			got := resolveAgentForRedispatch(townDir, rigName, state)
			if got != tt.wantAgent {
				t.Errorf("resolveAgentForRedispatch() = %q, want %q", got, tt.wantAgent)
			}
		})
	}
}

func TestResolveAgentForRedispatch_NoConfig(t *testing.T) {
	state := &BeadRedispatchState{BeadID: "gt-test", AttemptCount: 0}
	got := resolveAgentForRedispatch(t.TempDir(), "myrig", state)
	if got != "" {
		t.Errorf("expected empty agent when no config, got %q", got)
	}
}

func TestResolveAgentForRedispatch_EnforcesFromAgent(t *testing.T) {
	townDir := t.TempDir()
	rigName := "myrig"
	rigProjectDir := filepath.Join(townDir, rigName, "refinery", "rig", ".gastown")
	if err := os.MkdirAll(rigProjectDir, 0755); err != nil {
		t.Fatal(err)
	}
	configContent := `{
		"type": "model-escalation",
		"version": 1,
		"enabled": true,
		"rules": [{
			"from_agent": "opencode-local",
			"to_agent": "opencode-go",
			"promote_after_failures": 1
		}]
	}`
	if err := os.WriteFile(filepath.Join(rigProjectDir, "model-escalation.json"), []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}
	state := &BeadRedispatchState{BeadID: "gt-test"}
	if got := resolveAgentForRedispatch(townDir, rigName, state, "opencode-local"); got != "opencode-go" {
		t.Fatalf("local failure target = %q, want opencode-go", got)
	}
	if got := resolveAgentForRedispatch(townDir, rigName, state, "opencode-go"); got != "" {
		t.Fatalf("hosted failure must not match local rule, got %q", got)
	}
}

func TestResolveAgentForRedispatch_HighThreshold(t *testing.T) {
	townDir := t.TempDir()
	rigName := "myrig"
	rigProjectDir := filepath.Join(townDir, rigName, "refinery", "rig", ".gastown")
	if err := os.MkdirAll(rigProjectDir, 0755); err != nil {
		t.Fatal(err)
	}
	// threshold=2: need 2 total failures before promoting
	configContent := `{
		"type": "model-escalation",
		"version": 1,
		"enabled": true,
		"rules": [
			{
				"from_agent": "claude-sonnet",
				"to_agent": "claude",
				"promote_after_failures": 2
			}
		]
	}`
	if err := os.WriteFile(filepath.Join(rigProjectDir, "model-escalation.json"), []byte(configContent), 0600); err != nil {
		t.Fatal(err)
	}

	// First re-dispatch: totalFailures=1, threshold=2 → no promotion yet
	state := &BeadRedispatchState{BeadID: "gt-test", AttemptCount: 0}
	got := resolveAgentForRedispatch(townDir, rigName, state)
	if got != "" {
		t.Errorf("expected no promotion at 1 failure with threshold 2, got %q", got)
	}

	// Second re-dispatch: totalFailures=2, threshold=2 → promote
	state.AttemptCount = 1
	got = resolveAgentForRedispatch(townDir, rigName, state)
	if got != "claude" {
		t.Errorf("expected promotion at 2 failures with threshold 2, got %q", got)
	}
}
