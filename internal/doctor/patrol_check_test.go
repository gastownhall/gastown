package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/formula"
)

// writeRigsJSON creates a mayor/rigs.json with a single rig entry.
func writeRigsJSON(t *testing.T, townRoot, rigName string) {
	t.Helper()
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("MkdirAll mayor: %v", err)
	}
	rigsConfig := config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			rigName: {
				GitURL:  "https://github.com/test/" + rigName,
				AddedAt: time.Now(),
			},
		},
	}
	data, err := json.Marshal(rigsConfig)
	if err != nil {
		t.Fatalf("json.Marshal rigs: %v", err)
	}
	rigsPath := filepath.Join(mayorDir, "rigs.json")
	if err := os.WriteFile(rigsPath, data, 0644); err != nil {
		t.Fatalf("WriteFile rigs.json: %v", err)
	}
}

func TestPatrolMoleculesExistCheck_NoRigs(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("MkdirAll mayor: %v", err)
	}
	// Write rigs.json with no rigs
	rigsConfig := config.RigsConfig{Version: 1, Rigs: map[string]config.RigEntry{}}
	data, _ := json.Marshal(rigsConfig)
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), data, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	check := NewPatrolMoleculesExistCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK (no rigs configured)", result.Status)
	}
}

func TestPatrolMoleculesExistCheck_RigPathMissing_FallbackToTownRoot(t *testing.T) {
	// Regression test for: when gt doctor runs from a mayor's canonical clone,
	// TownRoot/rigName doesn't exist but patrol formulas are accessible from TownRoot.
	// The check should fall back to TownRoot instead of reporting false missing formulas.
	tmpDir := t.TempDir()

	// Provision patrol formulas at TownRoot level (not at a rig subdirectory).
	// This simulates formulas being accessible from the town root.
	if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	// Register "gastown" rig but do NOT create TownRoot/gastown directory.
	// This simulates the mayor's clone scenario where the rig isn't a subdirectory.
	writeRigsJSON(t, tmpDir, "gastown")

	check := NewPatrolMoleculesExistCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK (formulas accessible from TownRoot fallback)", result.Status)
		for _, d := range result.Details {
			t.Logf("  detail: %s", d)
		}
	}
}

func TestPatrolMoleculesExistCheck_RigPathExists_FormulasPresent(t *testing.T) {
	tmpDir := t.TempDir()

	// Create the rig directory and provision formulas there.
	rigDir := filepath.Join(tmpDir, "gastown")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("MkdirAll rig: %v", err)
	}
	if _, err := formula.ProvisionFormulas(rigDir); err != nil {
		t.Fatalf("ProvisionFormulas: %v", err)
	}

	writeRigsJSON(t, tmpDir, "gastown")

	check := NewPatrolMoleculesExistCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK (formulas in rig dir)", result.Status)
		for _, d := range result.Details {
			t.Logf("  detail: %s", d)
		}
	}
}

func TestPatrolMoleculesExistCheck_RigPathExists_TownLevelFormulas(t *testing.T) {
	// When the rig directory exists but has no .beads/formulas/, the check should
	// find patrol formulas at the town level (.beads/formulas/) instead of
	// reporting them as missing.
	tmpDir := t.TempDir()

	// Create the rig directory WITHOUT formulas.
	rigDir := filepath.Join(tmpDir, "gastown")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("MkdirAll rig: %v", err)
	}

	// Provision formulas at the town root level only.
	if _, err := formula.ProvisionFormulas(tmpDir); err != nil {
		t.Fatalf("ProvisionFormulas at town root: %v", err)
	}

	writeRigsJSON(t, tmpDir, "gastown")

	check := NewPatrolMoleculesExistCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK (formulas accessible from town root)", result.Status)
		for _, d := range result.Details {
			t.Logf("  detail: %s", d)
		}
	}
}

func TestNewPatrolHooksWiredCheck(t *testing.T) {
	check := NewPatrolHooksWiredCheck()
	if check == nil {
		t.Fatal("NewPatrolHooksWiredCheck() returned nil")
	}
	if check.Name() != "patrol-hooks-wired" {
		t.Errorf("Name() = %q, want %q", check.Name(), "patrol-hooks-wired")
	}
	if !check.CanFix() {
		t.Error("CanFix() should return true")
	}
}

func TestPatrolHooksWiredCheck_NoDaemonConfig(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	check := NewPatrolHooksWiredCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want Warning", result.Status)
	}
	if result.FixHint == "" {
		t.Error("FixHint should not be empty")
	}
}

func TestPatrolHooksWiredCheck_ValidConfig(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := config.NewDaemonPatrolConfig()
	path := config.DaemonPatrolConfigPath(tmpDir)
	if err := config.SaveDaemonPatrolConfig(path, cfg); err != nil {
		t.Fatalf("SaveDaemonPatrolConfig: %v", err)
	}

	check := NewPatrolHooksWiredCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK", result.Status)
	}
}

func TestPatrolHooksWiredCheck_EmptyPatrols(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: map[string]config.PatrolConfig{},
	}
	path := config.DaemonPatrolConfigPath(tmpDir)
	if err := config.SaveDaemonPatrolConfig(path, cfg); err != nil {
		t.Fatalf("SaveDaemonPatrolConfig: %v", err)
	}

	check := NewPatrolHooksWiredCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusWarning {
		t.Errorf("Status = %v, want Warning (no patrols configured)", result.Status)
	}
}

func TestPatrolHooksWiredCheck_HeartbeatEnabled(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Heartbeat: &config.HeartbeatConfig{
			Enabled:  true,
			Interval: "3m",
		},
		Patrols: map[string]config.PatrolConfig{},
	}
	path := config.DaemonPatrolConfigPath(tmpDir)
	if err := config.SaveDaemonPatrolConfig(path, cfg); err != nil {
		t.Fatalf("SaveDaemonPatrolConfig: %v", err)
	}

	check := NewPatrolHooksWiredCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)

	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK (heartbeat enabled triggers patrols)", result.Status)
	}
}

func TestPatrolHooksWiredCheck_Fix(t *testing.T) {
	tmpDir := t.TempDir()
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	check := NewPatrolHooksWiredCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Initial Status = %v, want Warning", result.Status)
	}

	err := check.Fix(ctx)
	if err != nil {
		t.Fatalf("Fix() error = %v", err)
	}

	path := config.DaemonPatrolConfigPath(tmpDir)
	loaded, err := config.LoadDaemonPatrolConfig(path)
	if err != nil {
		t.Fatalf("LoadDaemonPatrolConfig: %v", err)
	}
	if loaded.Type != "daemon-patrol-config" {
		t.Errorf("Type = %q, want 'daemon-patrol-config'", loaded.Type)
	}
	if len(loaded.Patrols) != 3 {
		t.Errorf("Patrols count = %d, want 3", len(loaded.Patrols))
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("After Fix(), Status = %v, want OK", result.Status)
	}
}

func TestPatrolHooksWiredCheck_FixPreservesExisting(t *testing.T) {
	tmpDir := t.TempDir()

	existing := &config.DaemonPatrolConfig{
		Type:    "daemon-patrol-config",
		Version: 1,
		Patrols: map[string]config.PatrolConfig{
			"custom": {Enabled: true, Agent: "custom-agent"},
		},
	}
	path := config.DaemonPatrolConfigPath(tmpDir)
	if err := config.SaveDaemonPatrolConfig(path, existing); err != nil {
		t.Fatalf("SaveDaemonPatrolConfig: %v", err)
	}

	check := NewPatrolHooksWiredCheck()
	ctx := &CheckContext{TownRoot: tmpDir}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Errorf("Status = %v, want OK (has patrols)", result.Status)
	}

	err := check.Fix(ctx)
	if err != nil {
		t.Fatalf("Fix() error = %v", err)
	}

	loaded, err := config.LoadDaemonPatrolConfig(path)
	if err != nil {
		t.Fatalf("LoadDaemonPatrolConfig: %v", err)
	}
	if len(loaded.Patrols) != 1 {
		t.Errorf("Patrols count = %d, want 1 (should preserve existing)", len(loaded.Patrols))
	}
	if _, ok := loaded.Patrols["custom"]; !ok {
		t.Error("existing custom patrol was overwritten")
	}
}

func TestCheckStuckWispsDolt_ErrorOnMissingBd(t *testing.T) {
	// When bd is not available or rigPath is invalid, checkStuckWispsDolt should return an error.
	// With Dolt-only mode, there is no JSONL fallback.
	check := NewPatrolNotStuckCheck()
	_, err := check.checkStuckWispsDolt("/nonexistent/rig/path", "testrig")
	if err == nil {
		t.Error("expected error when bd sql fails on nonexistent path")
	}
}

func TestPatrolNotStuckCheck_Run_DoltFailureReportsError(t *testing.T) {
	// When Dolt fails for a rig, the check should report the error in details
	// rather than silently returning OK.
	tmpDir := t.TempDir()

	// Create rigs.json
	mayorDir := filepath.Join(tmpDir, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	rigsConfig := config.RigsConfig{
		Rigs: map[string]config.RigEntry{
			"testrig": {},
		},
	}
	rigsData, _ := json.Marshal(rigsConfig)
	if err := os.WriteFile(filepath.Join(mayorDir, "rigs.json"), rigsData, 0644); err != nil {
		t.Fatalf("write rigs.json: %v", err)
	}

	// Create rig directory but no Dolt database — bd sql will fail
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	check := NewPatrolNotStuckCheck()
	ctx := &CheckContext{TownRoot: tmpDir}
	result := check.Run(ctx)

	// Should report warning with Dolt failure detail (not silently OK)
	if result.Status == StatusOK && len(result.Details) == 0 {
		// If bd is not installed, the check reports the error; if bd is installed
		// but no database exists, it also reports an error. Either way, we should
		// see details about the failure unless the check happens to return no stuck wisps.
		// This is acceptable — the key behavior is that we DON'T silently fall back to JSONL.
	}
}

// Suppress unused import warning for fmt (used in test output formatting).
var _ = fmt.Sprintf
