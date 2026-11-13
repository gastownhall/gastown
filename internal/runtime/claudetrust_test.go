package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readConfig parses a .claude.json test fixture into a generic map.
func readConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	return doc
}

func projectEntry(t *testing.T, doc map[string]any, dir string) map[string]any {
	t.Helper()
	projects, ok := doc["projects"].(map[string]any)
	if !ok {
		t.Fatalf("config has no projects map: %v", doc)
	}
	entry, ok := projects[dir].(map[string]any)
	if !ok {
		t.Fatalf("projects has no entry for %s: %v", dir, projects)
	}
	return entry
}

func assertTrustFlags(t *testing.T, entry map[string]any) {
	t.Helper()
	for _, flag := range []string{
		"hasTrustDialogAccepted",
		"hasClaudeMdExternalIncludesApproved",
		"hasClaudeMdExternalIncludesWarningShown",
	} {
		if v, ok := entry[flag].(bool); !ok || !v {
			t.Errorf("%s = %v, want true", flag, entry[flag])
		}
	}
}

func TestPreapproveProjectTrust_CreatesFileWhenMissing(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ".claude.json")
	projectDir := "/town/rig/polecats/chrome/source"

	if err := preapproveProjectTrust(configPath, projectDir); err != nil {
		t.Fatalf("preapproveProjectTrust: %v", err)
	}

	doc := readConfig(t, configPath)
	assertTrustFlags(t, projectEntry(t, doc, projectDir))
}

func TestPreapproveProjectTrust_PreservesExistingConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ".claude.json")
	existing := `{
		"numStartups": 42,
		"oauthAccount": {"accountUuid": "abc-123"},
		"projects": {
			"/other/project": {"hasTrustDialogAccepted": false, "allowedTools": ["Bash"]}
		}
	}`
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	projectDir := "/town/rig/polecats/rust/source"

	if err := preapproveProjectTrust(configPath, projectDir); err != nil {
		t.Fatalf("preapproveProjectTrust: %v", err)
	}

	doc := readConfig(t, configPath)
	assertTrustFlags(t, projectEntry(t, doc, projectDir))

	// Unrelated top-level fields survive untouched.
	if v, ok := doc["numStartups"].(float64); !ok || v != 42 {
		t.Errorf("numStartups = %v, want 42", doc["numStartups"])
	}
	oauth, _ := doc["oauthAccount"].(map[string]any)
	if oauth["accountUuid"] != "abc-123" {
		t.Errorf("oauthAccount.accountUuid = %v, want abc-123", oauth["accountUuid"])
	}

	// Other projects survive untouched (including their false flags).
	other := projectEntry(t, doc, "/other/project")
	if v, ok := other["hasTrustDialogAccepted"].(bool); !ok || v {
		t.Errorf("/other/project hasTrustDialogAccepted = %v, want false", other["hasTrustDialogAccepted"])
	}
}

func TestPreapproveProjectTrust_MergesExistingProjectEntry(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), ".claude.json")
	projectDir := "/town/rig/polecats/nitro/source"
	existing := `{
		"projects": {
			"` + projectDir + `": {
				"hasTrustDialogAccepted": false,
				"allowedTools": ["Bash", "Edit"],
				"history": [{"display": "hello"}]
			}
		}
	}`
	if err := os.WriteFile(configPath, []byte(existing), 0600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}

	if err := preapproveProjectTrust(configPath, projectDir); err != nil {
		t.Fatalf("preapproveProjectTrust: %v", err)
	}

	doc := readConfig(t, configPath)
	entry := projectEntry(t, doc, projectDir)
	assertTrustFlags(t, entry)

	// Pre-existing per-project fields survive the merge.
	tools, _ := entry["allowedTools"].([]any)
	if len(tools) != 2 || tools[0] != "Bash" || tools[1] != "Edit" {
		t.Errorf("allowedTools = %v, want [Bash Edit]", entry["allowedTools"])
	}
	history, _ := entry["history"].([]any)
	if len(history) != 1 {
		t.Errorf("history = %v, want 1 entry", entry["history"])
	}
}

func TestClaudeConfigJSONPath_RespectsConfigDirEnv(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/config")
	if got := claudeConfigJSONPath(); got != "/custom/config/.claude.json" {
		t.Errorf("claudeConfigJSONPath() = %q, want /custom/config/.claude.json", got)
	}
}

func TestClaudeConfigJSONPath_DefaultsToHome(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got := claudeConfigJSONPath(); got != filepath.Join(home, ".claude.json") {
		t.Errorf("claudeConfigJSONPath() = %q, want %s/.claude.json", got, home)
	}
}
