package witness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestManagerStartForegroundDeprecated(t *testing.T) {
	mgr := NewManager(&rig.Rig{Name: "testrig", Path: t.TempDir()})
	err := mgr.Start(true, "", nil)
	if err == nil {
		t.Fatal("expected foreground mode deprecation error")
	}
	if !strings.Contains(err.Error(), "foreground mode is deprecated") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareWitnessDirCreatesMissingAddedRigWitnessDir(t *testing.T) {
	t.Parallel()

	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "gastown")
	rigBeadsDir := filepath.Join(rigPath, ".beads")
	mayorBeadsDir := filepath.Join(rigPath, "mayor", "rig", ".beads")

	if err := os.MkdirAll(rigBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir rig beads: %v", err)
	}
	if err := os.MkdirAll(mayorBeadsDir, 0755); err != nil {
		t.Fatalf("mkdir mayor beads: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeadsDir, "redirect"), []byte("mayor/rig/.beads\n"), 0644); err != nil {
		t.Fatalf("write rig beads redirect: %v", err)
	}

	mgr := NewManager(&rig.Rig{Name: "gastown", Path: rigPath})
	witnessDir, err := mgr.prepareWitnessDir(townRoot)
	if err != nil {
		t.Fatalf("prepareWitnessDir: %v", err)
	}

	wantWitnessDir := filepath.Join(rigPath, "witness")
	if witnessDir != wantWitnessDir {
		t.Fatalf("witnessDir = %q, want %q", witnessDir, wantWitnessDir)
	}
	if info, err := os.Stat(witnessDir); err != nil || !info.IsDir() {
		t.Fatalf("witness dir was not created: info=%v err=%v", info, err)
	}

	redirectData, err := os.ReadFile(filepath.Join(witnessDir, ".beads", "redirect"))
	if err != nil {
		t.Fatalf("read witness redirect: %v", err)
	}
	if got, want := string(redirectData), "../mayor/rig/.beads\n"; got != want {
		t.Fatalf("redirect = %q, want %q", got, want)
	}
}

func TestBuildWitnessStartCommand_UsesRoleConfig(t *testing.T) {
	t.Parallel()
	roleCfg := &beads.RoleConfig{
		StartCommand: "exec run --town {town} --rig {rig} --role {role}",
	}

	got, err := buildWitnessStartCommand("/town/rig", "gastown", "/town", "", "", roleCfg, "")
	if err != nil {
		t.Fatalf("buildWitnessStartCommand: %v", err)
	}

	want := "exec env -u CLAUDECODE NODE_OPTIONS='' run --town /town --rig gastown --role witness"
	if got != want {
		t.Errorf("buildWitnessStartCommand = %q, want %q", got, want)
	}
}

func TestBuildWitnessStartCommand_DefaultsToRuntime(t *testing.T) {
	t.Parallel()
	got, err := buildWitnessStartCommand("/town/rig", "gastown", "/town", "", "", nil, "")
	if err != nil {
		t.Fatalf("buildWitnessStartCommand: %v", err)
	}

	if !strings.Contains(got, "GT_ROLE=gastown/witness") {
		t.Errorf("expected GT_ROLE=gastown/witness in command, got %q", got)
	}
	if !strings.Contains(got, "BD_ACTOR=gastown/witness") {
		t.Errorf("expected BD_ACTOR=gastown/witness in command, got %q", got)
	}
}

// TestRoleConfigEnvVars_ExpandsQualifiedGTRole verifies that the TOML env vars
// expand GT_ROLE to a qualified value (e.g., "gastown/witness" not "witness").
func TestRoleConfigEnvVars_ExpandsQualifiedGTRole(t *testing.T) {
	t.Parallel()
	roleCfg := &beads.RoleConfig{
		EnvVars: map[string]string{
			"GT_ROLE":  "{rig}/witness",
			"GT_SCOPE": "rig",
		},
	}

	got := roleConfigEnvVars(roleCfg, "/town", "gastown")
	if got["GT_ROLE"] != "gastown/witness" {
		t.Errorf("GT_ROLE = %q, want %q", got["GT_ROLE"], "gastown/witness")
	}
	if got["GT_SCOPE"] != "rig" {
		t.Errorf("GT_SCOPE = %q, want %q", got["GT_SCOPE"], "rig")
	}
}

// TestRoleConfigEnvVars_NilConfig verifies nil roleConfig returns nil.
func TestRoleConfigEnvVars_NilConfig(t *testing.T) {
	t.Parallel()
	got := roleConfigEnvVars(nil, "/town", "gastown")
	if got != nil {
		t.Errorf("expected nil for nil roleConfig, got %v", got)
	}
}

func TestBuildWitnessStartCommand_IncludesConfigDir(t *testing.T) {
	t.Parallel()
	got, err := buildWitnessStartCommand("/town/rig", "gastown", "/town", "", "", nil, "/home/user/.claude-accounts/work")
	if err != nil {
		t.Fatalf("buildWitnessStartCommand: %v", err)
	}

	if !strings.Contains(got, "CLAUDE_CONFIG_DIR=/home/user/.claude-accounts/work") {
		t.Errorf("expected CLAUDE_CONFIG_DIR in command, got %q", got)
	}
}

func TestBuildWitnessStartCommand_AgentOverrideWins(t *testing.T) {
	t.Parallel()
	roleCfg := &beads.RoleConfig{
		StartCommand: "exec run --role {role}",
	}

	got, err := buildWitnessStartCommand("/town/rig", "gastown", "/town", "", "codex", roleCfg, "")
	if err != nil {
		t.Fatalf("buildWitnessStartCommand: %v", err)
	}
	if strings.Contains(got, "exec run") {
		t.Fatalf("expected agent override to bypass role start_command, got %q", got)
	}
	if !strings.Contains(got, "GT_ROLE=gastown/witness") {
		t.Errorf("expected GT_ROLE=gastown/witness in command, got %q", got)
	}
}
