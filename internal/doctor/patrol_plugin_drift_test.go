package doctor

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestPlugin(t *testing.T, dir, name string) {
	t.Helper()
	pluginDir := filepath.Join(dir, name)
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		t.Fatal(err)
	}
	content := "+++\nname = \"" + name + "\"\n+++\ninstructions"
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.md"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestPatrolPluginDriftCheck_DisabledPluginNotResurrected covers the path that
// actually re-armed disabled plugins in production: a patrol formula reaching
// 'gt doctor --fix', which runs this check's Fix without a human present.
func TestPatrolPluginDriftCheck_DisabledPluginNotResurrected(t *testing.T) {
	townRoot := t.TempDir()
	sourceDir := filepath.Join(townRoot, "gastown", "plugins")
	targetDir := filepath.Join(townRoot, "plugins")

	writeTestPlugin(t, sourceDir, "rebuild-gt")
	writeTestPlugin(t, sourceDir, "github-sheriff")
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Chdir away from the gastown checkout so source resolution falls through
	// to the town candidates instead of this repo's own plugins/ directory.
	t.Chdir(t.TempDir())

	marker := filepath.Join(townRoot, ".disabled-plugins", "rebuild-gt.town.redisabled-20260806")
	if err := os.MkdirAll(marker, 0755); err != nil {
		t.Fatal(err)
	}

	ctx := &CheckContext{TownRoot: townRoot}
	check := NewPatrolPluginDriftCheck()

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected drift warning for github-sheriff, got %s: %s", result.Status, result.Message)
	}
	if len(check.disabled) != 1 || check.disabled[0].Name != "rebuild-gt" {
		t.Errorf("expected rebuild-gt recorded as disabled, got %+v", check.disabled)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "github-sheriff", "plugin.md")); err != nil {
		t.Errorf("github-sheriff should have been synced: %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "rebuild-gt")); !os.IsNotExist(err) {
		t.Fatalf("doctor --fix resurrected a disabled plugin: %v", err)
	}

	// Marker gone: the same check must install it again, or "skipped" was
	// proving nothing.
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}
	if result := check.Run(ctx); result.Status != StatusWarning {
		t.Fatalf("expected rebuild-gt to register as drift again, got %s: %s", result.Status, result.Message)
	}
	if err := check.Fix(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "rebuild-gt", "plugin.md")); err != nil {
		t.Errorf("rebuild-gt should sync once the marker is removed: %v", err)
	}
}
