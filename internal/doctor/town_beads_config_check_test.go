package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTownBeadsConfigCheck_NoTownBeadsDir(t *testing.T) {
	check := NewTownBeadsConfigCheck()

	result := check.Run(&CheckContext{TownRoot: t.TempDir()})
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want %v", result.Status, StatusOK)
	}
}

func TestTownBeadsConfigCheck_DetectsMissingConfig(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, ".beads"), 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}

	check := NewTownBeadsConfigCheck()
	result := check.Run(&CheckContext{TownRoot: townRoot})
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want %v", result.Status, StatusError)
	}
}

func TestTownBeadsConfigCheck_FixCreatesConfigFromMetadata(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	meta := `{"backend":"dolt","dolt_mode":"server","dolt_database":"hq","issue_prefix":"foo"}`
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), []byte(meta), 0644); err != nil {
		t.Fatalf("write metadata.json: %v", err)
	}

	check := NewTownBeadsConfigCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusError {
		t.Fatalf("Status = %v, want %v", result.Status, StatusError)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	got := string(data)
	if !strings.Contains(got, "prefix: foo\n") {
		t.Fatalf("config.yaml missing metadata-derived prefix: %q", got)
	}
	if !strings.Contains(got, "issue-prefix: foo\n") {
		t.Fatalf("config.yaml missing metadata-derived issue-prefix: %q", got)
	}
	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("Status after fix = %v, want %v", result.Status, StatusOK)
	}
}

func TestTownBeadsConfigCheck_FixDoesNotOverwriteExistingConfig(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	original := "prefix: custom\nissue-prefix: custom\nsync-branch: main\nexport.auto: \"false\"\n"
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	check := NewTownBeadsConfigCheck()
	ctx := &CheckContext{TownRoot: townRoot}

	result := check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("Status = %v, want %v", result.Status, StatusOK)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(after) != original {
		t.Fatalf("config.yaml was modified:\n got: %q\nwant: %q", string(after), original)
	}
}

func TestTownBeadsConfigCheck_FixAddsMissingExportAuto(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: hq\nissue-prefix: hq\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	check := NewTownBeadsConfigCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Status = %v, want %v", result.Status, StatusWarning)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(after), "export.auto: \"false\"\n") {
		t.Fatalf("config.yaml missing export.auto repair:\n%s", string(after))
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("Status after fix = %v, want %v", result.Status, StatusOK)
	}
}

func TestTownBeadsConfigCheck_FixDisablesEnabledExportAuto(t *testing.T) {
	townRoot := t.TempDir()
	beadsDir := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir .beads: %v", err)
	}
	configPath := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("prefix: hq\nissue-prefix: hq\nexport.auto: true\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	check := NewTownBeadsConfigCheck()
	ctx := &CheckContext{TownRoot: townRoot}
	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("Status = %v, want %v", result.Status, StatusWarning)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix() error: %v", err)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if !strings.Contains(string(after), "export.auto: \"false\"\n") {
		t.Fatalf("config.yaml did not disable export.auto:\n%s", string(after))
	}
}
