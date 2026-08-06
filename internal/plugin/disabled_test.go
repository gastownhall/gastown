package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markDisabled creates a marker directory under <townRoot>/.disabled-plugins/.
// dirName is the full archive name, suffixes and all — markers in a real town
// are never bare plugin names.
func markDisabled(t *testing.T, townRoot, dirName string) string {
	t.Helper()
	path := filepath.Join(townRoot, DisabledPluginsDir, dirName)
	if err := os.MkdirAll(path, 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDisabledPluginName(t *testing.T) {
	// Names taken from a live town's .disabled-plugins/ — matching the whole
	// directory name instead of the prefix is the failure this guards against.
	tests := []struct {
		dirName string
		want    string
	}{
		{"dolt-archive.DEN-SOURCE.removed-20260806", "dolt-archive"},
		{"rebuild-gt.town.redisabled-20260806", "rebuild-gt"},
		{"tool-updater.rig.redisabled-20260714", "tool-updater"},
		{"compactor-dog.plugins.re-disabled-20260604-103030", "compactor-dog"},
		{"rate-limit-watchdog.town", "rate-limit-watchdog"},
		{"rebuild-gt", "rebuild-gt"}, // bare name, no suffix
	}
	for _, tt := range tests {
		if got := disabledPluginName(tt.dirName); got != tt.want {
			t.Errorf("disabledPluginName(%q) = %q, want %q", tt.dirName, got, tt.want)
		}
	}
}

func TestLoadDisabledPlugins(t *testing.T) {
	townRoot := t.TempDir()
	markDisabled(t, townRoot, "rebuild-gt.town.redisabled-20260806")
	markDisabled(t, townRoot, "dolt-archive.DEN-SOURCE.removed-20260806")
	markDisabled(t, townRoot, "dolt-archive.plugins.redisabled-20260714") // second marker, same plugin

	disabled := LoadDisabledPlugins(townRoot)
	if len(disabled) != 2 {
		t.Fatalf("expected 2 disabled plugin names, got %d: %v", len(disabled), disabled)
	}
	if d, ok := disabled["rebuild-gt"]; !ok || d.Marker != "rebuild-gt.town.redisabled-20260806" {
		t.Errorf("rebuild-gt not blocked correctly: %+v (ok=%v)", d, ok)
	}
	// ReadDir is sorted, so the marker reported is deterministic.
	if d, ok := disabled["dolt-archive"]; !ok || d.Marker != "dolt-archive.DEN-SOURCE.removed-20260806" {
		t.Errorf("dolt-archive not blocked correctly: %+v (ok=%v)", d, ok)
	}
}

func TestLoadDisabledPlugins_NoDirectory(t *testing.T) {
	if disabled := LoadDisabledPlugins(t.TempDir()); len(disabled) != 0 {
		t.Errorf("expected empty set with no .disabled-plugins dir, got %v", disabled)
	}
	if disabled := LoadDisabledPlugins(""); len(disabled) != 0 {
		t.Errorf("expected empty set for empty townRoot, got %v", disabled)
	}
}

func TestLoadDisabledPlugins_IgnoresFilesAndDotEntries(t *testing.T) {
	townRoot := t.TempDir()
	disabledDir := filepath.Join(townRoot, DisabledPluginsDir)
	if err := os.MkdirAll(disabledDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(disabledDir, "notes.md"), []byte("why we disabled these"), 0644); err != nil {
		t.Fatal(err)
	}
	markDisabled(t, townRoot, ".hidden-archive")

	if disabled := LoadDisabledPlugins(townRoot); len(disabled) != 0 {
		t.Errorf("expected files and dot entries to be ignored, got %v", disabled)
	}
}

// TestSyncPlugins_DisabledBothDirections is the load-bearing test: the same
// source and town, synced twice, differing only in whether the marker exists.
// A guard verified in one direction only is not known to work.
func TestSyncPlugins_DisabledBothDirections(t *testing.T) {
	townRoot := t.TempDir()
	srcDir := t.TempDir()
	dstDir := filepath.Join(townRoot, "plugins")

	createTestPlugin(t, srcDir, "rebuild-gt", "+++\nname = \"rebuild-gt\"\n+++\nrebuild the binary", nil)
	createTestPlugin(t, srcDir, "github-sheriff", "+++\nname = \"github-sheriff\"\n+++\nsheriff", nil)

	// Direction 1: marker present -> rebuild-gt must not be installed.
	marker := markDisabled(t, townRoot, "rebuild-gt.town.redisabled-20260806")

	result, err := SyncPlugins(townRoot, srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 1 || result.Copied[0] != "github-sheriff" {
		t.Errorf("expected only github-sheriff copied, got %v", result.Copied)
	}
	if len(result.Disabled) != 1 || result.Disabled[0].Name != "rebuild-gt" {
		t.Fatalf("expected rebuild-gt reported as disabled, got %+v", result.Disabled)
	}
	// Not installed, so the marker is doing exactly its job — no stale warning.
	if result.Disabled[0].Installed {
		t.Errorf("rebuild-gt is not installed; it must not be flagged as a stale marker")
	}
	if result.Disabled[0].Marker != "rebuild-gt.town.redisabled-20260806" {
		t.Errorf("expected the marker dirname in the result, got %q", result.Disabled[0].Marker)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "rebuild-gt")); !os.IsNotExist(err) {
		t.Fatalf("rebuild-gt was resurrected into the runtime: %v", err)
	}

	// Direction 2: same source, marker removed -> rebuild-gt must be installed.
	// Without this the "skip" could be an artifact of anything at all.
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}

	result, err = SyncPlugins(townRoot, srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 1 || result.Copied[0] != "rebuild-gt" {
		t.Errorf("expected rebuild-gt copied after marker removal, got %v", result.Copied)
	}
	if len(result.Disabled) != 0 {
		t.Errorf("expected nothing disabled after marker removal, got %+v", result.Disabled)
	}
	if _, err := os.Stat(filepath.Join(dstDir, "rebuild-gt", "plugin.md")); err != nil {
		t.Errorf("rebuild-gt not installed after marker removal: %v", err)
	}
}

// TestSyncPlugins_DisabledMatchesPrefixNotWholeName pins the name-matching rule:
// markers carry provenance suffixes, so a whole-name comparison would match
// nothing and the guard would silently pass while copying the plugin.
func TestSyncPlugins_DisabledMatchesPrefixNotWholeName(t *testing.T) {
	townRoot := t.TempDir()
	srcDir := t.TempDir()
	dstDir := filepath.Join(townRoot, "plugins")

	createTestPlugin(t, srcDir, "dolt-archive", "+++\nname = \"dolt-archive\"\n+++\narchive", nil)
	markDisabled(t, townRoot, "dolt-archive.DEN-SOURCE.removed-20260806")

	result, err := SyncPlugins(townRoot, srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 0 {
		t.Errorf("suffixed marker failed to block the plugin, copied %v", result.Copied)
	}
	if len(result.Disabled) != 1 {
		t.Errorf("expected dolt-archive reported as disabled, got %+v", result.Disabled)
	}
}

// TestSyncPlugins_DisabledDoesNotOverMatch guards the other side of prefix
// matching: a marker must block exactly its own plugin, not neighbours that
// merely share a leading substring.
func TestSyncPlugins_DisabledDoesNotOverMatch(t *testing.T) {
	townRoot := t.TempDir()
	srcDir := t.TempDir()
	dstDir := filepath.Join(townRoot, "plugins")

	createTestPlugin(t, srcDir, "rebuild-gt-docs", "+++\nname = \"rebuild-gt-docs\"\n+++\ndocs", nil)
	markDisabled(t, townRoot, "rebuild-gt.town.redisabled-20260806")

	result, err := SyncPlugins(townRoot, srcDir, dstDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Copied) != 1 || result.Copied[0] != "rebuild-gt-docs" {
		t.Errorf("expected rebuild-gt-docs to sync normally, copied %v disabled %+v", result.Copied, result.Disabled)
	}
}

// TestSyncPlugins_DisabledNotRemovedByClean pins the blast radius of a stale
// marker. Markers accumulate over months and a re-enabled plugin can still have
// old ones (compactor-dog did, in the town this bead came from), so a marker
// must only withhold updates — never escalate into deleting a live plugin.
func TestSyncPlugins_DisabledNotRemovedByClean(t *testing.T) {
	townRoot := t.TempDir()
	srcDir := t.TempDir()
	dstDir := filepath.Join(townRoot, "plugins")

	createTestPlugin(t, srcDir, "compactor-dog", "+++\nname = \"compactor-dog\"\n+++\nnew version", nil)
	createTestPlugin(t, dstDir, "compactor-dog", "+++\nname = \"compactor-dog\"\n+++\ninstalled version", nil)
	markDisabled(t, townRoot, "compactor-dog.disabled-20260604-072900")

	result, err := SyncPlugins(townRoot, srcDir, dstDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 0 {
		t.Errorf("clean mode removed a disabled plugin, got %v", result.Removed)
	}
	if len(result.Copied) != 0 {
		t.Errorf("disabled plugin was updated from source, got %v", result.Copied)
	}
	// The contradiction — marked disabled, yet running — must be flagged, not
	// absorbed silently.
	if len(result.Disabled) != 1 || !result.Disabled[0].Installed {
		t.Errorf("expected the skipped plugin flagged as installed, got %+v", result.Disabled)
	}
	installed, err := os.ReadFile(filepath.Join(dstDir, "compactor-dog", "plugin.md"))
	if err != nil {
		t.Fatalf("disabled plugin did not survive clean sync: %v", err)
	}
	if !strings.Contains(string(installed), "installed version") {
		t.Errorf("installed copy was overwritten from source: %q", installed)
	}
}

func TestDetectDrift_DisabledIsNotDrift(t *testing.T) {
	townRoot := t.TempDir()
	srcDir := t.TempDir()
	dstDir := filepath.Join(townRoot, "plugins")
	if err := os.MkdirAll(dstDir, 0755); err != nil {
		t.Fatal(err)
	}

	createTestPlugin(t, srcDir, "rebuild-gt", "+++\nname = \"rebuild-gt\"\n+++\nrebuild", nil)
	marker := markDisabled(t, townRoot, "rebuild-gt.town.redisabled-20260806")

	report, err := DetectDrift(townRoot, srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if report.HasDrift() {
		t.Errorf("disabled plugin reported as drift: missing=%v drifted=%v", report.Missing, report.Drifted)
	}
	if len(report.Disabled) != 1 || report.Disabled[0].Name != "rebuild-gt" {
		t.Errorf("expected rebuild-gt reported as disabled, got %+v", report.Disabled)
	}

	// Other direction: without the marker it is ordinary drift again.
	if err := os.RemoveAll(marker); err != nil {
		t.Fatal(err)
	}
	report, err = DetectDrift(townRoot, srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if !report.HasDrift() || len(report.Missing) != 1 || report.Missing[0] != "rebuild-gt" {
		t.Errorf("expected rebuild-gt as drift after marker removal, got missing=%v", report.Missing)
	}
}

func TestDetectDrift_InstalledDisabledPluginIsNotExtra(t *testing.T) {
	townRoot := t.TempDir()
	srcDir := t.TempDir()
	dstDir := filepath.Join(townRoot, "plugins")

	createTestPlugin(t, srcDir, "dolt-archive", "+++\nname = \"dolt-archive\"\n+++\narchive", nil)
	createTestPlugin(t, dstDir, "dolt-archive", "+++\nname = \"dolt-archive\"\n+++\narchive", nil)
	markDisabled(t, townRoot, "dolt-archive.town.redisabled-20260806")

	report, err := DetectDrift(townRoot, srcDir, dstDir)
	if err != nil {
		t.Fatal(err)
	}
	if report.HasDrift() {
		t.Errorf("disabled plugin reported as drift: missing=%v drifted=%v", report.Missing, report.Drifted)
	}
	// Not Extra either: --clean must not read this report as license to
	// delete a plugin that is merely frozen.
	if len(report.Extra) != 0 {
		t.Errorf("expected the installed disabled plugin not to be listed as extra, got %v", report.Extra)
	}
	if len(report.Disabled) != 1 || report.Disabled[0].Name != "dolt-archive" {
		t.Fatalf("expected dolt-archive reported as disabled, got %+v", report.Disabled)
	}
	if !report.Disabled[0].Installed {
		t.Errorf("expected the installed disabled plugin flagged so the marker can be questioned")
	}
}
