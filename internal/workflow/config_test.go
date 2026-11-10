package workflow

import (
	"os"
	"path/filepath"
	"testing"
)

func writeRigSettings(t *testing.T, rigPath, body string) {
	t.Helper()
	dir := filepath.Join(rigPath, "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadConfig_DefaultBudgets(t *testing.T) {
	rig := t.TempDir()
	writeRigSettings(t, rig, `{"type":"rig-settings","version":1,"workflow":{"gated":{"enabled":true}}}`)
	c, err := LoadConfig(rig)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if !c.Enabled {
		t.Fatal("Enabled = false, want true")
	}
	if c.MaxFixFiles != 5 || c.MaxFixRounds != 3 || c.MaxPlanLoops != 3 {
		t.Fatalf("budgets = %d/%d/%d, want 5/3/3", c.MaxFixFiles, c.MaxFixRounds, c.MaxPlanLoops)
	}
}

func TestLoadConfig_KeepsExplicitBudgetsAndKeys(t *testing.T) {
	rig := t.TempDir()
	writeRigSettings(t, rig, `{"type":"rig-settings","version":1,"workflow":{"gated":{
		"enabled":true,"check_commands":["go test ./..."],"check_commands_approved_sha":"abc",
		"version_files":["VERSION"],"max_fix_files":9,"max_fix_rounds":1,"max_plan_loops":2}}}`)
	c, err := LoadConfig(rig)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(c.CheckCommands) != 1 || c.CheckCommands[0] != "go test ./..." ||
		c.CheckCommandsApprovedSHA != "abc" || len(c.VersionFiles) != 1 || c.VersionFiles[0] != "VERSION" {
		t.Fatalf("unexpected config: %+v", c)
	}
	if c.MaxFixFiles != 9 || c.MaxFixRounds != 1 || c.MaxPlanLoops != 2 {
		t.Fatalf("budgets = %d/%d/%d, want 9/1/2", c.MaxFixFiles, c.MaxFixRounds, c.MaxPlanLoops)
	}
}

func TestLoadConfig_MissingFileOrBlockIsDisabledDefaults(t *testing.T) {
	for name, body := range map[string]string{
		"no file":  "",
		"no block": `{"type":"rig-settings","version":1,"workflow":{"default_formula":"x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			rig := t.TempDir()
			if body != "" {
				writeRigSettings(t, rig, body)
			}
			c, err := LoadConfig(rig)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if c == nil || c.Enabled {
				t.Fatalf("got %+v, want non-nil disabled config", c)
			}
			if c.MaxFixFiles != 5 || c.MaxFixRounds != 3 || c.MaxPlanLoops != 3 {
				t.Fatalf("budgets = %d/%d/%d, want 5/3/3", c.MaxFixFiles, c.MaxFixRounds, c.MaxPlanLoops)
			}
		})
	}
}

func TestLoadConfig_MalformedFileErrors(t *testing.T) {
	rig := t.TempDir()
	writeRigSettings(t, rig, `{not json`)
	if _, err := LoadConfig(rig); err == nil {
		t.Fatal("LoadConfig on malformed settings: want error, got nil")
	}
}

func TestCheckCommandsSHA(t *testing.T) {
	// printf 'a\nb' | shasum -a 256
	const want = "7e18f737311b2dc3b2f269dd78396b0351f14fb66efa879f768cb23181883c78"
	got := CheckCommandsSHA([]string{"a", "b"})
	if got != want {
		t.Fatalf("CheckCommandsSHA = %s, want %s", got, want)
	}
	if got == CheckCommandsSHA([]string{"a b"}) || got == CheckCommandsSHA([]string{"b", "a"}) {
		t.Fatal("sha does not distinguish command lists")
	}
}

func TestChecksApproved_EditVoids(t *testing.T) {
	c, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c.CheckCommands = []string{"go vet ./...", "go test ./..."}
	c.CheckCommandsApprovedSHA = CheckCommandsSHA(c.CheckCommands)
	if !ChecksApproved(c) {
		t.Fatal("ChecksApproved = false right after approval")
	}
	c.CheckCommands = append(c.CheckCommands, "rm -rf /tmp/x")
	if ChecksApproved(c) {
		t.Fatal("ChecksApproved = true after appending a command")
	}
}
