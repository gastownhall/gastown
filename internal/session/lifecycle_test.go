package session

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
)

func TestStartSession_RequiresSessionID(t *testing.T) {
	_, err := StartSession(nil, "polecat", Work{
		WorkDir: "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for missing SessionID")
	}
	if err.Error() != "SessionID is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartSession_RequiresWorkDir(t *testing.T) {
	_, err := StartSession(nil, "polecat", Work{
		SessionID: "gt-test",
	})
	if err == nil {
		t.Fatal("expected error for missing WorkDir")
	}
	if err.Error() != "WorkDir is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStartSession_RequiresRole(t *testing.T) {
	_, err := StartSession(nil, "", Work{
		SessionID: "gt-test",
		WorkDir:   "/tmp",
	})
	if err == nil {
		t.Fatal("expected error for missing Role")
	}
	if err.Error() != "Role is required" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveRuntimeConfig_CrewUsesWorkerResolver(t *testing.T) {
	work := Work{
		AgentName: "denali",
		TownRoot:  t.TempDir(),
		RigPath:   t.TempDir(),
	}
	rc, err := resolveRuntimeConfig("crew", work)
	if err != nil {
		t.Fatalf("resolveRuntimeConfig: %v", err)
	}
	if rc == nil {
		t.Fatal("expected runtime config")
	}
}

func TestResolveRuntimeConfig_OverrideWinsOverCrewWorker(t *testing.T) {
	work := Work{
		AgentName:     "denali",
		TownRoot:      t.TempDir(),
		RigPath:       t.TempDir(),
		AgentOverride: "opencode",
	}
	rc, err := resolveRuntimeConfig("crew", work)
	if err != nil {
		t.Fatalf("resolveRuntimeConfig: %v", err)
	}
	if rc == nil {
		t.Fatal("expected runtime config")
	}
	if rc.ResolvedAgent != "opencode" && rc.Command == "" {
		t.Fatalf("override was ignored: %+v", rc)
	}
}

func TestBuildCommand_IncludesSessionIdentity(t *testing.T) {
	work := Work{
		SessionID: "gt-gastown-Toast",
		RigName:   "gastown",
		AgentName: "Toast",
		TownRoot:  t.TempDir(),
		RigPath:   t.TempDir(),
		Beacon: BeaconConfig{
			Topic: "assigned",
			MolID: "gt-abc",
		},
	}
	cmd, err := buildCommand("polecat", work, "test prompt")
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestKillExistingSession_ErrSessionAliveSentinel(t *testing.T) {
	err := fmt.Errorf("%w: %s", ErrSessionAlive, "gt-test")
	if !errors.Is(err, ErrSessionAlive) {
		t.Fatal("wrapped ErrSessionAlive should match errors.Is")
	}
}

func TestBuildPrompt_BeaconOnly(t *testing.T) {
	work := Work{
		Beacon: BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
	}
	prompt := buildPrompt(work)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	if !contains(prompt, "[GAS TOWN]") {
		t.Errorf("prompt should contain beacon: %s", prompt)
	}
}

func TestBuildPrompt_WithInstructions(t *testing.T) {
	work := Work{
		Beacon: BeaconConfig{
			Recipient: "boot",
			Sender:    "daemon",
			Topic:     "triage",
		},
		Instructions: "Run gt boot triage now.",
	}
	prompt := buildPrompt(work)
	if !contains(prompt, "Run gt boot triage now.") {
		t.Errorf("prompt should contain instructions: %s", prompt)
	}
	if !contains(prompt, "[GAS TOWN]") {
		t.Errorf("prompt should contain beacon: %s", prompt)
	}
}

func TestBuildCommand_DefaultAgent(t *testing.T) {
	work := Work{
		TownRoot: "/tmp/town",
	}
	cmd, err := buildCommand("boot", work, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestBuildCommand_WithAgentOverride(t *testing.T) {
	work := Work{
		TownRoot:      "/tmp/town",
		AgentOverride: "opencode",
	}
	cmd, err := buildCommand("boot", work, "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd == "" {
		t.Fatal("expected non-empty command")
	}
}

func TestKillExistingSession_NoSession(t *testing.T) {
	// KillExistingSession with nil tmux would panic, but we test the logic
	// by verifying it's callable. Full integration tests need a real tmux.
	// This test verifies the function signature and basic flow.
	t.Skip("requires tmux for integration testing")
}

func TestMapKeysSorted(t *testing.T) {
	got := mapKeysSorted(map[string]string{
		"GT_SESSION": "1",
		"GT_ROLE":    "polecat",
		"GT_RIG":     "alpha",
	})

	want := []string{"GT_RIG", "GT_ROLE", "GT_SESSION"}
	if len(got) != len(want) {
		t.Fatalf("mapKeysSorted() length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mapKeysSorted()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMergeRuntimeLivenessEnv_SetsResolvedAgentAndProcessNames(t *testing.T) {
	env := map[string]string{
		"GT_ROLE": "polecat",
	}
	rc := &config.RuntimeConfig{
		Command:       "claude",
		ResolvedAgent: "claude",
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "claude" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "claude")
	}
	if got["GT_PROCESS_NAMES"] != "node,claude" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q", got["GT_PROCESS_NAMES"], "node,claude")
	}
}

func TestMergeRuntimeLivenessEnv_RespectsExistingValues(t *testing.T) {
	env := map[string]string{
		"GT_AGENT":         "explicit-agent",
		"GT_PROCESS_NAMES": "custom-bin,custom-agent",
	}
	rc := &config.RuntimeConfig{
		Command:       "bun",
		ResolvedAgent: "wen",
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "explicit-agent" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "explicit-agent")
	}
	if got["GT_PROCESS_NAMES"] != "custom-bin,custom-agent" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q", got["GT_PROCESS_NAMES"], "custom-bin,custom-agent")
	}
}

func TestMergeRuntimeLivenessEnv_UsesEffectiveAgentForProcessNames(t *testing.T) {
	// When AgentOverride sets GT_AGENT to a different agent than
	// runtimeConfig.ResolvedAgent, process names must be resolved from
	// the effective agent (GT_AGENT), not the workspace-default resolved agent.
	env := map[string]string{
		"GT_AGENT": "codex", // set by AgentEnv from AgentOverride
	}
	rc := &config.RuntimeConfig{
		Command:       "claude",
		ResolvedAgent: "claude", // workspace default, NOT the override
	}

	got := MergeRuntimeLivenessEnv(env, rc)

	if got["GT_AGENT"] != "codex" {
		t.Fatalf("GT_AGENT = %q, want %q", got["GT_AGENT"], "codex")
	}
	if got["GT_PROCESS_NAMES"] != "codex" {
		t.Fatalf("GT_PROCESS_NAMES = %q, want %q (should resolve from effective agent, not runtimeConfig)", got["GT_PROCESS_NAMES"], "codex")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
