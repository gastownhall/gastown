package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallForRole_RoleAware(t *testing.T) {
	// Claude has autonomous/interactive variants
	tests := []struct {
		name     string
		role     string
		wantFile string // expected template used
	}{
		{"autonomous polecat", "polecat", "settings-autonomous.json"},
		{"autonomous witness", "witness", "settings-autonomous.json"},
		{"interactive crew", "crew", "settings-interactive.json"},
		{"interactive mayor", "mayor", "settings-interactive.json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			err := InstallForRole("claude", dir, dir, tt.role, ".claude", "settings.json", true)
			if err != nil {
				t.Fatalf("InstallForRole: %v", err)
			}

			path := filepath.Join(dir, ".claude", "settings.json")
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Fatal("settings.json not created")
			}

			// Verify content matches resolved template (with {{GT_BIN}} substituted)
			got, _ := os.ReadFile(path)
			want, err := resolveAndSubstitute("claude", tt.wantFile, tt.role)
			if err != nil {
				t.Fatalf("resolveAndSubstitute: %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("content mismatch: got %d bytes, want %d bytes (from %s)", len(got), len(want), tt.wantFile)
			}
		})
	}
}

func TestInstallForRole_ClaudeSettingsSuppressStartupPrompts(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{"autonomous", "polecat"},
		{"interactive", "crew"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := InstallForRole("claude", dir, dir, tt.role, ".claude", "settings.json", true); err != nil {
				t.Fatalf("InstallForRole: %v", err)
			}

			data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			if strings.Contains(string(data), "export PATH=") {
				t.Fatal("claude settings contain stale export PATH marker")
			}
			if strings.Contains(string(data), "{{GT_BIN}}") {
				t.Fatal("claude settings contain unresolved {{GT_BIN}} placeholder")
			}

			var settings map[string]any
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatalf("settings are not valid JSON: %v", err)
			}
			if got, ok := settings["skipDangerousModePermissionPrompt"].(bool); !ok || !got {
				t.Fatalf("skipDangerousModePermissionPrompt = %v, want true", settings["skipDangerousModePermissionPrompt"])
			}
			if got, ok := settings["hasCompletedOnboarding"].(bool); !ok || !got {
				t.Fatalf("hasCompletedOnboarding = %v, want true", settings["hasCompletedOnboarding"])
			}
			if got := settings["theme"]; got != "dark" {
				t.Fatalf("theme = %v, want dark", got)
			}
			permissions, ok := settings["permissions"].(map[string]any)
			if !ok {
				t.Fatalf("permissions = %T, want object", settings["permissions"])
			}
			if got := permissions["defaultMode"]; got != "bypassPermissions" {
				t.Fatalf("permissions.defaultMode = %v, want bypassPermissions", got)
			}
		})
	}
}

func TestInstallForRole_ClaudeCurrentTemplatePreservesExistingSettings(t *testing.T) {
	tests := []struct {
		name string
		role string
	}{
		{"autonomous", "polecat"},
		{"interactive", "crew"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			settingsPath := filepath.Join(dir, ".claude", "settings.json")
			if err := InstallForRole("claude", dir, dir, tt.role, ".claude", "settings.json", true); err != nil {
				t.Fatalf("initial InstallForRole: %v", err)
			}

			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("read settings: %v", err)
			}
			var settings map[string]any
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatalf("unmarshal settings: %v", err)
			}
			settings["customSentinel"] = true
			updated, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				t.Fatalf("marshal settings: %v", err)
			}
			if err := os.WriteFile(settingsPath, append(updated, '\n'), 0600); err != nil {
				t.Fatalf("write settings: %v", err)
			}

			if err := InstallForRole("claude", dir, dir, tt.role, ".claude", "settings.json", true); err != nil {
				t.Fatalf("second InstallForRole: %v", err)
			}

			data, err = os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("read settings after second install: %v", err)
			}
			if err := json.Unmarshal(data, &settings); err != nil {
				t.Fatalf("unmarshal settings after second install: %v", err)
			}
			if got, ok := settings["customSentinel"].(bool); !ok || !got {
				t.Fatalf("customSentinel = %v, want true", settings["customSentinel"])
			}
		})
	}
}

func TestInstallForRole_RoleAgnostic(t *testing.T) {
	// OpenCode, Pi, OMP have single templates
	tests := []struct {
		provider  string
		hooksDir  string
		hooksFile string
	}{
		{"opencode", ".opencode/plugins", "gastown.js"},
		{"pi", ".pi/extensions", "gastown-hooks.js"},
		{"omp", ".omp/hooks", "gastown-hook.ts"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			dir := t.TempDir()
			err := InstallForRole(tt.provider, dir, dir, "polecat", tt.hooksDir, tt.hooksFile, false)
			if err != nil {
				t.Fatalf("InstallForRole(%s): %v", tt.provider, err)
			}

			path := filepath.Join(dir, tt.hooksDir, tt.hooksFile)
			if _, err := os.Stat(path); os.IsNotExist(err) {
				t.Fatalf("%s not created", tt.hooksFile)
			}
		})
	}
}

func TestOpenCodeTemplateFailureDiagnostics(t *testing.T) {
	template, err := templateFS.ReadFile("templates/opencode/gastown.js")
	if err != nil {
		t.Fatalf("read opencode template: %v", err)
	}
	content := string(template)
	for _, want := range []string{
		"command: ${cmd}",
		"exit_code:",
		"exit code 124",
		"timeout:",
		"stdout_tail:",
		"stderr_tail:",
		"timeout 10s ${gtCommand()} dolt status 2>&1",
		"dolt_status_tail:",
		"suggested_recovery:",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("opencode template missing diagnostic field %q", want)
		}
	}
}

func TestOpenCodeTemplateUsesHookPrime(t *testing.T) {
	template, err := templateFS.ReadFile("templates/opencode/gastown.js")
	if err != nil {
		t.Fatalf("read opencode template: %v", err)
	}
	content := string(template)
	for _, want := range []string{
		"GT_HOOK_SOURCE=",
		"GT_SESSION_ID=",
		"prime --hook",
		"gt prime --hook",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("opencode template missing hook prime field %q", want)
		}
	}
	if strings.Contains(content, "gt mail check --inject") {
		t.Fatal("opencode template should let gt prime --hook handle mail injection")
	}
}

func TestInstallForRole_SkipsExisting(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(hooksPath), 0755)
	os.WriteFile(hooksPath, []byte("custom"), 0644)

	err := InstallForRole("claude", dir, dir, "crew", ".claude", "settings.json", true)
	if err != nil {
		t.Fatalf("InstallForRole: %v", err)
	}

	got, _ := os.ReadFile(hooksPath)
	if string(got) != "custom" {
		t.Error("existing file was overwritten")
	}
}

func TestInstallForRole_UpgradesStaleExportPath(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".opencode/plugins", "gastown.js")
	os.MkdirAll(filepath.Dir(hooksPath), 0755)

	// Write a stale file with the legacy "export PATH=" pattern
	os.WriteFile(hooksPath, []byte(`export PATH=/usr/local/bin:$PATH && gt hook`), 0644)

	err := InstallForRole("opencode", dir, dir, "crew", ".opencode/plugins", "gastown.js", false)
	if err != nil {
		t.Fatalf("InstallForRole: %v", err)
	}

	got, _ := os.ReadFile(hooksPath)
	if strings.Contains(string(got), "export PATH=") {
		t.Error("stale export PATH pattern was not upgraded")
	}
	// Should now match the current template after placeholder substitution.
	template, _ := resolveAndSubstitute("opencode", "gastown.js", "crew")
	if string(got) != string(template) {
		t.Error("upgraded file does not match current template")
	}
}

func TestInstallForRole_UpgradesStaleOpenCodePrimeHook(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".opencode/plugins", "gastown.js")
	os.MkdirAll(filepath.Dir(hooksPath), 0755)

	os.WriteFile(hooksPath, []byte(`// Gas Town OpenCode plugin: hooks SessionStart/Compaction via events.
export const GasTown = async ({ $ }) => {
  await $`+"`"+`gt prime`+"`"+`
}`), 0644)

	if err := InstallForRole("opencode", dir, dir, "crew", ".opencode/plugins", "gastown.js", false); err != nil {
		t.Fatalf("InstallForRole: %v", err)
	}

	got, err := os.ReadFile(hooksPath)
	if err != nil {
		t.Fatalf("read upgraded hook: %v", err)
	}
	if strings.Contains(string(got), "captureRun(\"gt prime\")") || strings.Contains(string(got), "$`gt prime`") {
		t.Fatal("stale bare gt prime was not upgraded")
	}
	if !strings.Contains(string(got), "prime --hook") {
		t.Fatal("upgraded OpenCode hook does not run prime --hook")
	}
}

func TestOpenCodeTemplateUsesHookModeAndCompoundRoles(t *testing.T) {
	content, err := resolveAndSubstitute("opencode", "gastown.js", "polecat")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	s := string(content)
	for _, want := range []string{
		"prime --hook",
		"GT_HOOK_SOURCE=",
		"GT_SESSION_ID=",
		`parts[1] === "polecats"`,
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("OpenCode template missing %q", want)
		}
	}
	if strings.Contains(s, "{{GT_BIN}}") {
		t.Fatal("OpenCode template contains unresolved {{GT_BIN}}")
	}
	if strings.Contains(s, "mail check --inject") {
		t.Fatal("OpenCode template should not duplicate startup mail injection")
	}
}

func TestSyncForRole_UpdatesStaleContent(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".opencode/plugins", "gastown.js")
	os.MkdirAll(filepath.Dir(hooksPath), 0755)
	os.WriteFile(hooksPath, []byte("stale-content"), 0644)

	result, err := SyncForRole("opencode", dir, dir, "crew", ".opencode/plugins", "gastown.js", false)
	if err != nil {
		t.Fatalf("SyncForRole: %v", err)
	}
	if result != SyncUpdated {
		t.Errorf("expected SyncUpdated, got %d", result)
	}

	got, _ := os.ReadFile(hooksPath)
	if string(got) == "stale-content" {
		t.Error("stale file was not updated")
	}

	// Should match the template after placeholder substitution.
	template, _ := resolveAndSubstitute("opencode", "gastown.js", "crew")
	if string(got) != string(template) {
		t.Error("updated file does not match current template")
	}
}

func TestSyncForRole_SkipsMatchingContent(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".opencode/plugins", "gastown.js")
	os.MkdirAll(filepath.Dir(hooksPath), 0755)

	// Write the actual installed template content — should report unchanged.
	template, _ := resolveAndSubstitute("opencode", "gastown.js", "crew")
	os.WriteFile(hooksPath, template, 0644)

	result, err := SyncForRole("opencode", dir, dir, "crew", ".opencode/plugins", "gastown.js", false)
	if err != nil {
		t.Fatalf("SyncForRole: %v", err)
	}
	if result != SyncUnchanged {
		t.Errorf("expected SyncUnchanged, got %d", result)
	}
}

func TestSyncForRole_CreatesNewFile(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, ".opencode/plugins", "gastown.js")

	result, err := SyncForRole("opencode", dir, dir, "polecat", ".opencode/plugins", "gastown.js", false)
	if err != nil {
		t.Fatalf("SyncForRole: %v", err)
	}
	if result != SyncCreated {
		t.Errorf("expected SyncCreated, got %d", result)
	}

	if _, err := os.Stat(hooksPath); os.IsNotExist(err) {
		t.Error("file was not created")
	}
}

func TestSyncForRole_EmptyProvider(t *testing.T) {
	dir := t.TempDir()
	result, err := SyncForRole("", dir, dir, "crew", ".opencode/plugins", "gastown.js", false)
	if err != nil {
		t.Fatalf("expected nil error for empty provider, got: %v", err)
	}
	if result != SyncUnchanged {
		t.Errorf("expected SyncUnchanged for empty provider, got %d", result)
	}
}

func TestSyncForRole_InvalidProvider(t *testing.T) {
	dir := t.TempDir()
	_, err := SyncForRole("nonexistent-provider", dir, dir, "crew", ".test", "settings.json", false)
	if err == nil {
		t.Error("expected error for invalid provider")
	}
}

func TestSyncForRole_WriteError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not support read-only directories reliably")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permission bits; chmod read-only is not enforceable")
	}

	dir := t.TempDir()
	// Create a read-only parent to prevent MkdirAll from creating the hooks dir
	readOnlyDir := filepath.Join(dir, "readonly")
	os.MkdirAll(readOnlyDir, 0755)
	os.Chmod(readOnlyDir, 0444)
	defer os.Chmod(readOnlyDir, 0755) // cleanup

	_, err := SyncForRole("opencode", readOnlyDir, readOnlyDir, "crew", ".opencode/plugins", "gastown.js", false)
	if err == nil {
		t.Error("expected error when directory is read-only")
	}
}

func TestSyncForRole_JSONWhitespaceInsensitive(t *testing.T) {
	dir := t.TempDir()

	// First, create the file via SyncForRole
	result, err := SyncForRole("gemini", dir, dir, "crew", ".gemini", "settings.json", false)
	if err != nil {
		t.Fatalf("initial SyncForRole: %v", err)
	}
	if result != SyncCreated {
		t.Fatalf("expected SyncCreated, got %d", result)
	}

	// Read the canonical file, reformat with different whitespace
	targetPath := filepath.Join(dir, ".gemini", "settings.json")
	original, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("reading created file: %v", err)
	}

	// Reformat with different whitespace by round-tripping through json.MarshalIndent.
	// This changes indentation structure without corrupting string values (safe on Windows
	// where strings.ReplaceAll(":", " : ") would corrupt drive letters like C: → C :).
	var parsed interface{}
	if err := json.Unmarshal(original, &parsed); err != nil {
		t.Fatalf("parsing original JSON: %v", err)
	}
	reformatted, err := json.MarshalIndent(parsed, "", "    ")
	if err != nil {
		t.Fatalf("reformatting JSON: %v", err)
	}
	if string(original) == string(reformatted) {
		t.Fatal("reformatted content should differ from original bytes")
	}
	if err := os.WriteFile(targetPath, reformatted, 0600); err != nil {
		t.Fatalf("writing reformatted file: %v", err)
	}

	// SyncForRole should treat this as unchanged (structurally equal JSON)
	result, err = SyncForRole("gemini", dir, dir, "crew", ".gemini", "settings.json", false)
	if err != nil {
		t.Fatalf("SyncForRole after reformat: %v", err)
	}
	if result != SyncUnchanged {
		t.Errorf("expected SyncUnchanged for whitespace-only JSON difference, got %d", result)
	}
}

func TestSyncForRole_GeminiWithGTBinSubstitution(t *testing.T) {
	dir := t.TempDir()

	result, err := SyncForRole("gemini", dir, dir, "witness", ".gemini", "settings.json", false)
	if err != nil {
		t.Fatalf("SyncForRole: %v", err)
	}
	if result != SyncCreated {
		t.Errorf("expected SyncCreated, got %d", result)
	}

	got, _ := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	// Verify {{GT_BIN}} was substituted (should not appear in output)
	if strings.Contains(string(got), "{{GT_BIN}}") {
		t.Error("{{GT_BIN}} placeholder was not substituted")
	}
	// Verify the resolved binary path is present (JSON-escaped for Windows compatibility).
	gtBin := resolveGTBinary()
	gtBinJSON := strings.ReplaceAll(gtBin, `\`, `\\`)
	if !strings.Contains(string(got), gtBinJSON) {
		t.Errorf("expected resolved gt binary %q in output", gtBin)
	}
}

func TestInstallForRole_SettingsDirVsWorkDir(t *testing.T) {
	settingsDir := t.TempDir()
	workDir := t.TempDir()

	// Claude uses settingsDir (useSettingsDir=true)
	err := InstallForRole("claude", settingsDir, workDir, "crew", ".claude", "settings.json", true)
	if err != nil {
		t.Fatalf("InstallForRole (claude): %v", err)
	}
	if _, err := os.Stat(filepath.Join(settingsDir, ".claude", "settings.json")); os.IsNotExist(err) {
		t.Error("claude: file not in settingsDir")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Error("claude: file should not be in workDir")
	}

	// OpenCode uses workDir (useSettingsDir=false)
	err = InstallForRole("opencode", settingsDir, workDir, "polecat", ".opencode/plugins", "gastown.js", false)
	if err != nil {
		t.Fatalf("InstallForRole (opencode): %v", err)
	}
	if _, err := os.Stat(filepath.Join(workDir, ".opencode/plugins", "gastown.js")); os.IsNotExist(err) {
		t.Error("opencode: file not in workDir")
	}
}

func TestInstallForRole_EmptyProvider(t *testing.T) {
	dir := t.TempDir()
	err := InstallForRole("", dir, dir, "crew", ".claude", "settings.json", false)
	if err != nil {
		t.Fatalf("expected nil error for empty provider, got: %v", err)
	}
}

func TestInstallForRole_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not preserve POSIX file mode bits from os.WriteFile")
	}

	dir := t.TempDir()

	// JSON files should get 0600
	err := InstallForRole("claude", dir, dir, "crew", ".claude", "settings.json", true)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(filepath.Join(dir, ".claude", "settings.json"))
	if info.Mode().Perm() != 0600 {
		t.Errorf("JSON file perm = %o, want 0600", info.Mode().Perm())
	}

	// Non-JSON files should get 0644
	dir2 := t.TempDir()
	err = InstallForRole("pi", dir2, dir2, "polecat", ".pi/extensions", "gastown-hooks.js", false)
	if err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(filepath.Join(dir2, ".pi/extensions", "gastown-hooks.js"))
	if info.Mode().Perm() != 0644 {
		t.Errorf("JS file perm = %o, want 0644", info.Mode().Perm())
	}
}

func TestInstallForRole_CursorRoleAware(t *testing.T) {
	// Cursor uses hooks-autonomous.json / hooks-interactive.json naming
	dir := t.TempDir()
	err := InstallForRole("cursor", dir, dir, "polecat", ".cursor", "hooks.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(cursor, polecat): %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, ".cursor", "hooks.json"))
	want, err := resolveAndSubstitute("cursor", "hooks-autonomous.json", "polecat")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	if string(got) != string(want) {
		t.Error("cursor autonomous: content mismatch")
	}

	dir2 := t.TempDir()
	err = InstallForRole("cursor", dir2, dir2, "crew", ".cursor", "hooks.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(cursor, crew): %v", err)
	}

	got, _ = os.ReadFile(filepath.Join(dir2, ".cursor", "hooks.json"))
	want, err = resolveAndSubstitute("cursor", "hooks-interactive.json", "crew")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	if string(got) != string(want) {
		t.Error("cursor interactive: content mismatch")
	}
}

func TestInstallForRole_GeminiRoleAware(t *testing.T) {
	dir := t.TempDir()
	err := InstallForRole("gemini", dir, dir, "witness", ".gemini", "settings.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(gemini, witness): %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, ".gemini", "settings.json"))
	want, _ := templateFS.ReadFile("templates/gemini/settings-autonomous.json")
	// Gemini templates contain {{GT_BIN}} which gets resolved at install time.
	// Apply the same substitution (with JSON escaping) to the expected content for comparison.
	gtBin := resolveGTBinary()
	gtBinJSON := strings.ReplaceAll(gtBin, `\`, `\\`)
	wantResolved := strings.ReplaceAll(string(want), "{{GT_BIN}}", gtBinJSON)
	if string(got) != wantResolved {
		t.Error("gemini autonomous: content mismatch")
	}
}

func TestInstallForRole_CodexRoleAware(t *testing.T) {
	dir := t.TempDir()
	err := InstallForRole("codex", dir, dir, "crew", ".codex", "hooks.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(codex, crew): %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, ".codex", "hooks.json"))
	want, err := resolveAndSubstitute("codex", "hooks-interactive.json", "crew")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	if string(got) != string(want) {
		t.Error("codex interactive: content mismatch")
	}
	if !strings.Contains(string(got), "costs record >/dev/null 2>&1 &") {
		t.Error("codex interactive: stop hook should silence gt costs record output")
	}

	dir2 := t.TempDir()
	err = InstallForRole("codex", dir2, dir2, "polecat", ".codex", "hooks.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(codex, polecat): %v", err)
	}

	got, _ = os.ReadFile(filepath.Join(dir2, ".codex", "hooks.json"))
	want, err = resolveAndSubstitute("codex", "hooks-autonomous.json", "polecat")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	if string(got) != string(want) {
		t.Error("codex autonomous: content mismatch")
	}
	if !strings.Contains(string(got), "costs record >/dev/null 2>&1 &") {
		t.Error("codex autonomous: stop hook should silence gt costs record output")
	}
}

func TestInstallForRole_CopilotRoleAware(t *testing.T) {
	// Copilot uses gastown-autonomous.json / gastown-interactive.json naming
	dir := t.TempDir()
	err := InstallForRole("copilot", dir, dir, "polecat", ".github/hooks", "gastown.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(copilot, polecat): %v", err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, ".github/hooks", "gastown.json"))
	want, err := resolveAndSubstitute("copilot", "gastown-autonomous.json", "polecat")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	if string(got) != string(want) {
		t.Error("copilot autonomous: content mismatch")
	}

	dir2 := t.TempDir()
	err = InstallForRole("copilot", dir2, dir2, "crew", ".github/hooks", "gastown.json", false)
	if err != nil {
		t.Fatalf("InstallForRole(copilot, crew): %v", err)
	}

	got, _ = os.ReadFile(filepath.Join(dir2, ".github/hooks", "gastown.json"))
	want, err = resolveAndSubstitute("copilot", "gastown-interactive.json", "crew")
	if err != nil {
		t.Fatalf("resolveAndSubstitute: %v", err)
	}
	if string(got) != string(want) {
		t.Error("copilot interactive: content mismatch")
	}
}

func TestComputeExpectedTemplate_Gemini(t *testing.T) {
	// Autonomous role should get settings-autonomous.json template
	content, err := ComputeExpectedTemplate("gemini", "settings.json", "witness")
	if err != nil {
		t.Fatalf("ComputeExpectedTemplate: %v", err)
	}

	// Should contain resolved gt binary path, not {{GT_BIN}}
	if strings.Contains(string(content), "{{GT_BIN}}") {
		t.Error("expected {{GT_BIN}} to be resolved")
	}

	// Should contain GT_HOOK_SOURCE=compact (from autonomous template)
	if !strings.Contains(string(content), "GT_HOOK_SOURCE=compact") {
		t.Error("expected GT_HOOK_SOURCE=compact in autonomous template")
	}

	if strings.Contains(string(content), `"context"`) || strings.Contains(string(content), `"fileName"`) {
		t.Error("Gemini template should not force context.fileName; GEMINI.md overlays must remain loadable")
	}

	// Interactive role should get settings-interactive.json template
	interactiveContent, err := ComputeExpectedTemplate("gemini", "settings.json", "crew")
	if err != nil {
		t.Fatalf("ComputeExpectedTemplate(crew): %v", err)
	}

	// Interactive template should NOT contain GT_HOOK_SOURCE=compact
	if strings.Contains(string(interactiveContent), "GT_HOOK_SOURCE=compact") {
		t.Error("interactive template should not contain GT_HOOK_SOURCE=compact")
	}
}

func TestTemplateContentEqual(t *testing.T) {
	// Same JSON, different formatting
	a := []byte(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"test"}]}]}}`)
	b := []byte(`{
  "hooks": {
    "SessionStart": [
      {
        "matcher": "",
        "hooks": [
          {
            "type": "command",
            "command": "test"
          }
        ]
      }
    ]
  }
}`)

	if !TemplateContentEqual(a, b) {
		t.Error("expected structurally equal JSON to match")
	}

	// Different content
	c := []byte(`{"hooks":{"SessionStart":[{"matcher":"","hooks":[{"type":"command","command":"different"}]}]}}`)
	if TemplateContentEqual(a, c) {
		t.Error("expected different JSON to not match")
	}

	// Invalid JSON
	invalid := []byte(`not json`)
	if TemplateContentEqual(a, invalid) {
		t.Error("expected invalid JSON to not match")
	}
}
