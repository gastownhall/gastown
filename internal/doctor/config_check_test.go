package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/constants"
)

func installFakeBdForConfigChecks(t *testing.T, townRoot string) {
	t.Helper()

	binDir := filepath.Join(townRoot, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}

	script := `#!/bin/sh
set -eu

target="${BEADS_DIR:-$PWD}"
if [ -d "$target/.beads" ]; then
  target="$target/.beads"
fi

case "$1:$2:$3" in
  config:get:types.custom)
    if [ -f "$target/types.custom" ]; then
      cat "$target/types.custom"
    else
      exit 1
    fi
    ;;
	  config:set:types.custom)
	    printf '%s\n' "$4" > "$target/types.custom"
	    ;;
	  config:get:types.infra)
	    if [ -f "$target/types.infra" ]; then
	      cat "$target/types.infra"
	    else
	      exit 1
	    fi
	    ;;
	  config:set:types.infra)
	    printf '%s\n' "$4" > "$target/types.infra"
	    ;;
  config:get:status.custom)
    if [ -f "$target/status.custom" ]; then
      cat "$target/status.custom"
    else
      exit 1
    fi
    ;;
  config:set:status.custom)
    printf '%s\n' "$4" > "$target/status.custom"
    ;;
  *)
    echo "unexpected bd invocation: $*" >&2
    exit 1
    ;;
esac
`

	bdPath := filepath.Join(binDir, "bd")
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", fmt.Sprintf("%s:%s", binDir, oldPath)); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Setenv("PATH", oldPath)
	})
	for _, key := range []string{"BEADS_DIR", "BEADS_DB", "BEADS_DOLT_SERVER_DATABASE"} {
		oldVal, hadVal := os.LookupEnv(key)
		_ = os.Unsetenv(key)
		t.Cleanup(func() {
			if hadVal {
				_ = os.Setenv(key, oldVal)
			} else {
				_ = os.Unsetenv(key)
			}
		})
	}
}

func writeConfigCheckFile(t *testing.T, beadsDir, name, value string) {
	t.Helper()
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, name), []byte(value+"\n"), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readConfigCheckFile(t *testing.T, beadsDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(beadsDir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.TrimSpace(string(data))
}

func TestSessionHookCheck_UsesSessionStartScript(t *testing.T) {
	check := NewSessionHookCheck()

	tests := []struct {
		name     string
		content  string
		hookType string
		want     bool
	}{
		{
			name:     "bare gt prime fails",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`,
			hookType: "SessionStart",
			want:     false,
		},
		{
			name:     "gt prime --hook passes",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]}}`,
			hookType: "SessionStart",
			want:     true,
		},
		{
			name:     "session-start.sh passes",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "bash ~/.claude/hooks/session-start.sh"}]}]}}`,
			hookType: "SessionStart",
			want:     true,
		},
		{
			name:     "no SessionStart hook passes",
			content:  `{"hooks": {"Stop": [{"hooks": [{"type": "command", "command": "gt handoff"}]}]}}`,
			hookType: "SessionStart",
			want:     true,
		},
		{
			name:     "PreCompact with --hook passes",
			content:  `{"hooks": {"PreCompact": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]}}`,
			hookType: "PreCompact",
			want:     true,
		},
		{
			name:     "PreCompact bare gt prime fails",
			content:  `{"hooks": {"PreCompact": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`,
			hookType: "PreCompact",
			want:     false,
		},
		{
			name:     "gt prime --hook with extra flags passes",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook --verbose"}]}]}}`,
			hookType: "SessionStart",
			want:     true,
		},
		{
			name:     "gt prime with --hook not first still passes",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --verbose --hook"}]}]}}`,
			hookType: "SessionStart",
			want:     true,
		},
		{
			name:     "gt prime with other flags but no --hook fails",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --verbose"}]}]}}`,
			hookType: "SessionStart",
			want:     false,
		},
		{
			name:     "both session-start.sh and gt prime passes (session-start.sh wins)",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "bash session-start.sh && gt prime"}]}]}}`,
			hookType: "SessionStart",
			want:     true,
		},
		{
			name:     "gt prime --hookup is NOT valid (false positive check)",
			content:  `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hookup"}]}]}}`,
			hookType: "SessionStart",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := check.usesSessionStartScript(tt.content, tt.hookType)
			if got != tt.want {
				t.Errorf("usesSessionStartScript() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSessionHookCheck_Run(t *testing.T) {
	t.Run("mayor bare gt prime warns", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Settings must be at mayor/.claude/settings.json
		claudeDir := filepath.Join(tmpDir, "mayor", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusWarning {
			t.Errorf("expected StatusWarning, got %v", result.Status)
		}
	})

	t.Run("mayor gt prime --hook passes", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Settings must be at mayor/.claude/settings.json
		claudeDir := filepath.Join(tmpDir, "mayor", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusOK {
			t.Errorf("expected StatusOK, got %v: %v", result.Status, result.Details)
		}
	})

	t.Run("witness settings with --hook passes", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create rig structure with witness
		rigDir := filepath.Join(tmpDir, "myrig")
		if err := os.MkdirAll(filepath.Join(rigDir, "witness"), 0755); err != nil {
			t.Fatal(err)
		}
		// Settings at witness/.claude/settings.json (parent dir, loaded via --settings)
		claudeDir := filepath.Join(rigDir, "witness", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusOK {
			t.Errorf("expected StatusOK for witness settings, got %v: %v", result.Status, result.Details)
		}
	})

	t.Run("witness bare gt prime warns", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create rig structure with witness
		rigDir := filepath.Join(tmpDir, "myrig")
		if err := os.MkdirAll(filepath.Join(rigDir, "witness"), 0755); err != nil {
			t.Fatal(err)
		}
		// Settings at witness/.claude/settings.json (parent dir, loaded via --settings)
		claudeDir := filepath.Join(rigDir, "witness", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusWarning {
			t.Errorf("expected StatusWarning for witness bare gt prime, got %v", result.Status)
		}
	})

	t.Run("mixed valid and invalid hooks warns", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Settings must be at mayor/.claude/settings.json
		claudeDir := filepath.Join(tmpDir, "mayor", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}], "PreCompact": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`
		if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusWarning {
			t.Errorf("expected StatusWarning when PreCompact is invalid, got %v", result.Status)
		}
		if len(result.Details) != 1 {
			t.Errorf("expected 1 issue (PreCompact), got %d: %v", len(result.Details), result.Details)
		}
	})

	t.Run("no settings files returns OK", func(t *testing.T) {
		tmpDir := t.TempDir()

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}
		result := check.Run(ctx)

		if result.Status != StatusOK {
			t.Errorf("expected StatusOK when no settings files, got %v", result.Status)
		}
	})
}

func TestSessionHookCheck_Fix(t *testing.T) {
	t.Run("fixes bare gt prime to gt prime --hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create mayor/.claude/ directory (SessionHookCheck looks for settings.json in agent dirs)
		claudeDir := filepath.Join(tmpDir, "mayor", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		// Create settings.json with bare gt prime (should be gt prime --hook)
		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}

		// Run to detect issue and cache file
		result := check.Run(ctx)
		if result.Status != StatusWarning {
			t.Errorf("expected StatusWarning before fix, got %v", result.Status)
		}

		// Apply fix
		if err := check.Fix(ctx); err != nil {
			t.Fatalf("Fix failed: %v", err)
		}

		// Re-run to verify fix
		result = check.Run(ctx)
		if result.Status != StatusOK {
			t.Errorf("expected StatusOK after fix, got %v: %v", result.Status, result.Details)
		}

		// Verify file content
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, "gt prime --hook") {
			t.Errorf("expected 'gt prime --hook' in fixed file, got: %s", content)
		}
	})

	t.Run("fixes multiple hooks in same file", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create mayor/.claude/ directory (SessionHookCheck looks for settings.json in agent dirs)
		claudeDir := filepath.Join(tmpDir, "mayor", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime && echo done"}]}], "PreCompact": [{"hooks": [{"type": "command", "command": "gt prime"}]}]}}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}

		result := check.Run(ctx)
		if result.Status != StatusWarning {
			t.Errorf("expected StatusWarning before fix, got %v", result.Status)
		}

		if err := check.Fix(ctx); err != nil {
			t.Fatalf("Fix failed: %v", err)
		}

		result = check.Run(ctx)
		if result.Status != StatusOK {
			t.Errorf("expected StatusOK after fix, got %v: %v", result.Status, result.Details)
		}

		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		// Both hooks should now have --hook
		if strings.Count(content, "gt prime --hook") != 2 {
			t.Errorf("expected 2 occurrences of 'gt prime --hook', got content: %s", content)
		}
	})

	t.Run("does not double-add --hook", func(t *testing.T) {
		tmpDir := t.TempDir()
		// Create mayor/.claude/ directory (SessionHookCheck looks for settings.json in agent dirs)
		claudeDir := filepath.Join(tmpDir, "mayor", ".claude")
		if err := os.MkdirAll(claudeDir, 0755); err != nil {
			t.Fatal(err)
		}

		settings := `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "gt prime --hook"}]}]}}`
		settingsPath := filepath.Join(claudeDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settings), 0644); err != nil {
			t.Fatal(err)
		}

		check := NewSessionHookCheck()
		ctx := &CheckContext{TownRoot: tmpDir}

		// Should already be OK (already has --hook)
		result := check.Run(ctx)
		if result.Status != StatusOK {
			t.Errorf("expected StatusOK for already-fixed file, got %v", result.Status)
		}

		// Fix should be no-op (no files cached)
		if err := check.Fix(ctx); err != nil {
			t.Fatalf("Fix failed: %v", err)
		}

		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		// Should not have --hook --hook
		if strings.Contains(content, "--hook --hook") {
			t.Errorf("fix doubled --hook flag: %s", content)
		}
	})
}

func TestParseConfigOutput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple value",
			input: "agent,role,rig,convoy,slot\n",
			want:  "agent,role,rig,convoy,slot",
		},
		{
			name:  "value with trailing newlines",
			input: "agent,role,rig,convoy,slot\n\n",
			want:  "agent,role,rig,convoy,slot",
		},
		{
			name:  "Note prefix filtered",
			input: "Note: No git repository initialized - running without background sync\nagent,role,rig,convoy,slot\n",
			want:  "agent,role,rig,convoy,slot",
		},
		{
			name:  "multiple Note prefixes filtered",
			input: "Note: First note\nNote: Second note\nagent,role,rig,convoy,slot\n",
			want:  "agent,role,rig,convoy,slot",
		},
		{
			name:  "empty output",
			input: "",
			want:  "",
		},
		{
			name:  "only whitespace",
			input: "  \n  \n",
			want:  "",
		},
		{
			name:  "Note with different casing is not filtered",
			input: "note: lowercase should not match\n",
			want:  "note: lowercase should not match",
		},
		{
			name:  "not set message filtered",
			input: "status.custom (not set)\n",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseConfigOutput([]byte(tt.input))
			if got != tt.want {
				t.Errorf("parseConfigOutput() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCustomTypesCheck_ParsesOutputWithNotePrefix(t *testing.T) {
	// This test verifies that CustomTypesCheck correctly parses bd output
	// that contains "Note:" informational messages before the actual config value.
	// Without proper filtering, the check would see "Note: ..." as the config value
	// and incorrectly report all custom types as missing.

	// Test the parsing logic directly - this simulates bd outputting:
	// "Note: No git repository initialized - running without background sync"
	// followed by the actual config value
	output := "Note: No git repository initialized - running without background sync\n" + constants.BeadsCustomTypes + "\n"
	parsed := parseConfigOutput([]byte(output))

	if parsed != constants.BeadsCustomTypes {
		t.Errorf("parseConfigOutput failed to filter Note: prefix\ngot: %q\nwant: %q", parsed, constants.BeadsCustomTypes)
	}

	// Verify that all required types are found in the parsed output
	configuredSet := make(map[string]bool)
	for _, typ := range strings.Split(parsed, ",") {
		configuredSet[strings.TrimSpace(typ)] = true
	}

	var missing []string
	for _, required := range constants.BeadsCustomTypesList() {
		if !configuredSet[required] {
			missing = append(missing, required)
		}
	}

	if len(missing) > 0 {
		t.Errorf("After parsing, missing types: %v", missing)
	}
}

func TestCustomStatusesCheck_ParsesOutputWithNotePrefix(t *testing.T) {
	// Verify that CustomStatusesCheck correctly handles bd output with "Note:" prefix
	output := "Note: No git repository initialized - running without background sync\n" + constants.BeadsCustomStatuses + "\n"
	parsed := parseConfigOutput([]byte(output))

	if parsed != constants.BeadsCustomStatuses {
		t.Errorf("parseConfigOutput failed to filter Note: prefix\ngot: %q\nwant: %q", parsed, constants.BeadsCustomStatuses)
	}

	// Verify all required statuses are found
	configuredSet := make(map[string]bool)
	for _, s := range strings.Split(parsed, ",") {
		configuredSet[strings.TrimSpace(s)] = true
	}

	var missing []string
	for _, required := range constants.BeadsCustomStatusesList() {
		if !configuredSet[required] {
			missing = append(missing, required)
		}
	}

	if len(missing) > 0 {
		t.Errorf("After parsing, missing statuses: %v", missing)
	}
}

func TestCustomTypesCheck_UsesRigScopedBeadsDir(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(rigDir, ".beads")

	writeConfigCheckFile(t, townBeadsDir, "types.custom", constants.BeadsCustomTypes)
	writeConfigCheckFile(t, rigBeadsDir, "types.custom", "agent,role")
	installFakeBdForConfigChecks(t, townRoot)

	check := NewCustomTypesCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: "gastown"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v (%v)", result.Status, result.Details)
	}
	if check.targetBeadsDir != rigBeadsDir {
		t.Fatalf("Run cached %q, want %q", check.targetBeadsDir, rigBeadsDir)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	gotTypes := strings.Split(readConfigCheckFile(t, rigBeadsDir, "types.custom"), ",")
	wantTypes := make(map[string]struct{})
	for _, item := range constants.BeadsCustomTypesList() {
		wantTypes[item] = struct{}{}
	}
	for _, item := range gotTypes {
		delete(wantTypes, item)
	}
	if len(wantTypes) != 0 {
		t.Fatalf("rig types.custom missing expected entries after fix: %v (got %q)", wantTypes, strings.Join(gotTypes, ","))
	}
	if got := readConfigCheckFile(t, townBeadsDir, "types.custom"); got != constants.BeadsCustomTypes {
		t.Fatalf("town types.custom changed unexpectedly: %q", got)
	}
	if got := readConfigCheckFile(t, rigBeadsDir, "types.infra"); got != constants.BeadsInfraTypes {
		t.Fatalf("rig types.infra = %q, want %q", got, constants.BeadsInfraTypes)
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK after fix, got %v (%v)", result.Status, result.Details)
	}
}

func TestCustomTypesCheck_FixPreservesExistingRigTypes(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(rigDir, ".beads")

	writeConfigCheckFile(t, townBeadsDir, "types.custom", constants.BeadsCustomTypes)
	writeConfigCheckFile(t, rigBeadsDir, "types.custom", "agent,role,external")
	installFakeBdForConfigChecks(t, townRoot)

	check := NewCustomTypesCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: "gastown"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v (%v)", result.Status, result.Details)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	got := strings.Split(readConfigCheckFile(t, rigBeadsDir, "types.custom"), ",")
	wantSet := make(map[string]struct{})
	for _, item := range append(constants.BeadsCustomTypesList(), "external") {
		wantSet[item] = struct{}{}
	}
	for _, item := range got {
		delete(wantSet, item)
	}
	if len(wantSet) != 0 {
		t.Fatalf("rig types.custom missing expected entries after fix: %v (got %q)", wantSet, strings.Join(got, ","))
	}
	if got := readConfigCheckFile(t, rigBeadsDir, "types.infra"); got != constants.BeadsInfraTypes {
		t.Fatalf("rig types.infra = %q, want %q", got, constants.BeadsInfraTypes)
	}
}

func TestCustomTypesCheck_FixesStaleInfraTypes(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown")
	rigBeadsDir := filepath.Join(rigDir, ".beads")

	writeConfigCheckFile(t, rigBeadsDir, "types.custom", constants.BeadsCustomTypes)
	writeConfigCheckFile(t, rigBeadsDir, "types.infra", "agent,role,rig,message")
	installFakeBdForConfigChecks(t, townRoot)

	check := NewCustomTypesCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: "gastown"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v (%v)", result.Status, result.Details)
	}
	if !strings.Contains(strings.Join(result.Details, "\n"), "rig must not be an infra type") {
		t.Fatalf("expected rig infra warning, got %v", result.Details)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	if got := readConfigCheckFile(t, rigBeadsDir, "types.infra"); got != constants.BeadsInfraTypes {
		t.Fatalf("rig types.infra = %q, want %q", got, constants.BeadsInfraTypes)
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK after fix, got %v (%v)", result.Status, result.Details)
	}
}

func TestCustomTypesCheck_FixesMissingInfraTypes(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown")
	rigBeadsDir := filepath.Join(rigDir, ".beads")

	writeConfigCheckFile(t, rigBeadsDir, "types.custom", constants.BeadsCustomTypes)
	installFakeBdForConfigChecks(t, townRoot)

	check := NewCustomTypesCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: "gastown"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v (%v)", result.Status, result.Details)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}
	if got := readConfigCheckFile(t, rigBeadsDir, "types.infra"); got != constants.BeadsInfraTypes {
		t.Fatalf("rig types.infra = %q, want %q", got, constants.BeadsInfraTypes)
	}
}

func TestCustomStatusesCheck_UsesRigScopedBeadsDir(t *testing.T) {
	townRoot := t.TempDir()
	rigDir := filepath.Join(townRoot, "gastown")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(rigDir, ".beads")

	writeConfigCheckFile(t, townBeadsDir, "status.custom", constants.BeadsCustomStatuses)
	writeConfigCheckFile(t, rigBeadsDir, "status.custom", "queued")
	installFakeBdForConfigChecks(t, townRoot)

	check := NewCustomStatusesCheck()
	ctx := &CheckContext{TownRoot: townRoot, RigName: "gastown"}

	result := check.Run(ctx)
	if result.Status != StatusWarning {
		t.Fatalf("expected StatusWarning, got %v (%v)", result.Status, result.Details)
	}
	if check.targetBeadsDir != rigBeadsDir {
		t.Fatalf("Run cached %q, want %q", check.targetBeadsDir, rigBeadsDir)
	}

	if err := check.Fix(ctx); err != nil {
		t.Fatalf("Fix failed: %v", err)
	}

	if got := readConfigCheckFile(t, rigBeadsDir, "status.custom"); got != "queued,"+constants.BeadsCustomStatuses {
		t.Fatalf("rig status.custom = %q", got)
	}
	if got := readConfigCheckFile(t, townBeadsDir, "status.custom"); got != constants.BeadsCustomStatuses {
		t.Fatalf("town status.custom changed unexpectedly: %q", got)
	}

	result = check.Run(ctx)
	if result.Status != StatusOK {
		t.Fatalf("expected StatusOK after fix, got %v (%v)", result.Status, result.Details)
	}
}
