package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBdCmd_Build(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() *bdCmd
		wantArgs []string
		wantDir  string
		wantEnv  map[string]string
	}{
		{
			name: "basic command with defaults",
			setup: func() *bdCmd {
				return BdCmd("show", "test-id", "--json")
			},
			wantArgs: []string{"bd", "show", "test-id", "--json"},
			wantDir:  "",
			wantEnv:  map[string]string{},
		},
		{
			name: "with directory",
			setup: func() *bdCmd {
				return BdCmd("list").Dir("/some/path")
			},
			wantArgs: []string{"bd", "list"},
			wantDir:  "/some/path",
			wantEnv: map[string]string{
				"BEADS_DIR": "/some/path/.beads",
			},
		},
		{
			name: "with auto commit",
			setup: func() *bdCmd {
				return BdCmd("update", "id").WithAutoCommit()
			},
			wantArgs: []string{"bd", "update", "id"},
			wantEnv: map[string]string{
				"BD_DOLT_AUTO_COMMIT": "on",
			},
		},
		{
			name: "with GT_ROOT",
			setup: func() *bdCmd {
				return BdCmd("cook", "formula").WithGTRoot("/town/root")
			},
			wantArgs: []string{"bd", "cook", "formula"},
			wantEnv: map[string]string{
				"GT_ROOT": "/town/root",
			},
		},
		{
			name: "chained configuration",
			setup: func() *bdCmd {
				return BdCmd("mol", "wisp", "formula").
					Dir("/work/dir").
					WithAutoCommit().
					WithGTRoot("/town/root")
			},
			wantArgs: []string{"bd", "mol", "wisp", "formula"},
			wantDir:  "/work/dir",
			wantEnv: map[string]string{
				"BD_DOLT_AUTO_COMMIT": "on",
				"GT_ROOT":             "/town/root",
				"BEADS_DIR":           "/work/dir/.beads",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bdc := tt.setup()
			cmd := bdc.Build()

			// Verify command arguments
			if len(cmd.Args) != len(tt.wantArgs) {
				t.Errorf("Args length = %d, want %d", len(cmd.Args), len(tt.wantArgs))
			}
			for i, arg := range tt.wantArgs {
				if i >= len(cmd.Args) || cmd.Args[i] != arg {
					t.Errorf("Args[%d] = %q, want %q", i, cmd.Args[i], arg)
				}
			}

			// Verify working directory
			if cmd.Dir != tt.wantDir {
				t.Errorf("Dir = %q, want %q", cmd.Dir, tt.wantDir)
			}

			// Verify environment variables
			envMap := parseEnv(cmd.Env)
			for key, wantVal := range tt.wantEnv {
				if gotVal, ok := envMap[key]; !ok {
					t.Errorf("Env %q not found, want %q", key, wantVal)
				} else if gotVal != wantVal {
					t.Errorf("Env %q = %q, want %q", key, gotVal, wantVal)
				}
			}
		})
	}
}

func TestBdCmd_Stderr(t *testing.T) {
	var stderrBuf bytes.Buffer

	bdc := BdCmd("show", "nonexistent-id").
		Stderr(&stderrBuf)

	cmd := bdc.Build()

	// Verify stderr writer is set
	if cmd.Stderr != &stderrBuf {
		t.Error("Stderr should be set to custom writer")
	}
}

func TestBdCmd_DefaultStderr(t *testing.T) {
	bdc := BdCmd("list")
	cmd := bdc.Build()

	// Verify default stderr is os.Stderr
	if cmd.Stderr != os.Stderr {
		t.Error("Default Stderr should be os.Stderr")
	}
}

func TestBdCmd_Output(t *testing.T) {
	// Use "bd version" or similar that should work
	// Note: This requires bd to be installed. If not available, skip.
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test: " + err.Error())
	}

	bdc := BdCmd("--version")
	out, err := bdc.Output()

	// Should not error and should produce output
	if err != nil {
		t.Errorf("Output() error = %v", err)
	}
	if len(out) == 0 {
		t.Error("Output() produced no output")
	}
}

func TestBdCmd_Run(t *testing.T) {
	// Use "bd --version" or similar that should work
	// Note: This requires bd to be installed. If not available, skip.
	if _, err := exec.LookPath("bd"); err != nil {
		t.Skip("bd not installed, skipping integration test: " + err.Error())
	}

	bdc := BdCmd("--version")
	err := bdc.Run()

	// Should not error
	if err != nil {
		t.Errorf("Run() error = %v", err)
	}
}

func TestBdCmd_RunTimesOut(t *testing.T) {
	binDir := t.TempDir()
	writeBDStub(t, binDir, `#!/usr/bin/env sh
sleep 5
`, `@echo off
timeout /t 5 /nobreak >NUL
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GT_BD_TIMEOUT_SEC", "1")

	start := time.Now()
	err := BdCmd("list").Run()
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want timeout", err)
	}
	if !strings.Contains(err.Error(), "bd list") {
		t.Fatalf("error = %v, want command description bd list in timeout message", err)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("timeout took %v, want under 4s", elapsed)
	}
}

func TestBdCmd_CombinedOutputDoesNotPreSetStderr(t *testing.T) {
	binDir := t.TempDir()
	writeBDStub(t, binDir, `#!/usr/bin/env sh
printf 'stdout:%s\n' "$*"
printf 'stderr:%s\n' "$*" >&2
`, `@echo off
echo stdout:%*
echo stderr:%* 1>&2
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := BdCmd("show", "id").CombinedOutput()
	if err != nil {
		t.Fatalf("CombinedOutput: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "stdout:show id") {
		t.Fatalf("missing stdout in combined output: %q", text)
	}
	if !strings.Contains(text, "stderr:show id") {
		t.Fatalf("missing stderr in combined output: %q", text)
	}
}

func TestBdCmd_Chaining(t *testing.T) {
	// Test that all builder methods return the receiver for chaining
	bdc := BdCmd("test")

	// Each method should return the same pointer for fluent chaining
	if bdc.WithAutoCommit() != bdc {
		t.Error("WithAutoCommit() should return receiver for chaining")
	}
	if bdc.AllowStale() != bdc {
		t.Error("AllowStale() should return receiver for chaining")
	}
	if !bdc.allowStale {
		t.Error("AllowStale() should mark stale-read compatibility as requested")
	}
	if bdc.WithGTRoot("/test") != bdc {
		t.Error("WithGTRoot() should return receiver for chaining")
	}
	if bdc.Dir("/test") != bdc {
		t.Error("Dir() should return receiver for chaining")
	}
	if bdc.Stderr(os.Stdout) != bdc {
		t.Error("Stderr() should return receiver for chaining")
	}
}

// parseEnv converts an environment slice to a map for easier testing
func parseEnv(env []string) map[string]string {
	m := make(map[string]string)
	for _, e := range env {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			m[parts[0]] = parts[1]
		} else if len(parts) == 1 {
			m[parts[0]] = ""
		}
	}
	return m
}

// ===================================================================
// Corner case tests for bdCmd environment handling
// ===================================================================

func TestBdCmd_WithAutoCommit_OverridesParentOff(t *testing.T) {
	// Test that WithAutoCommit() removes the existing BD_DOLT_AUTO_COMMIT=off
	// before appending BD_DOLT_AUTO_COMMIT=on. This is critical because
	// glibc getenv() returns the first match in the env array, so a duplicate
	// "off" entry would shadow the appended "on".
	baseEnv := []string{"PATH=/usr/bin", "BD_DOLT_AUTO_COMMIT=off", "BD_READONLY=true", "HOME=/home/user"}

	bdc := &bdCmd{
		args:   []string{"show", "id"},
		env:    baseEnv,
		stderr: os.Stderr,
	}
	bdc.WithAutoCommit()
	cmd := bdc.Build()
	envMap := parseEnv(cmd.Env)

	// The value should be "on" (old "off" entry removed)
	if envMap["BD_DOLT_AUTO_COMMIT"] != "on" {
		t.Errorf("BD_DOLT_AUTO_COMMIT = %q, want 'on' (should override parent's 'off')", envMap["BD_DOLT_AUTO_COMMIT"])
	}

	// Verify there is exactly one BD_DOLT_AUTO_COMMIT entry (no duplicates)
	count := 0
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "BD_DOLT_AUTO_COMMIT=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d BD_DOLT_AUTO_COMMIT entries, want exactly 1 (dedup must remove old entry)", count)
	}
	if _, ok := envMap["BD_READONLY"]; ok {
		t.Errorf("BD_READONLY should be stripped for WithAutoCommit mutation env, got %q", envMap["BD_READONLY"])
	}
}

func TestBdCmd_MultipleAutoCommit_DedupRemovesOld(t *testing.T) {
	// Test that WithAutoCommit() deduplicates: removes existing "off" and adds "on".
	// This ensures glibc getenv() (first-match-wins) returns the correct value.
	baseEnv := []string{"BD_DOLT_AUTO_COMMIT=off"}

	bdc := &bdCmd{
		args:   []string{"show", "id"},
		env:    baseEnv,
		stderr: os.Stderr,
	}
	bdc.WithAutoCommit()
	cmd := bdc.Build()

	// Count occurrences — should have exactly one "on" and zero "off"
	offCount := 0
	onCount := 0
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "BD_DOLT_AUTO_COMMIT=") {
			if e == "BD_DOLT_AUTO_COMMIT=off" {
				offCount++
			} else if e == "BD_DOLT_AUTO_COMMIT=on" {
				onCount++
			}
		}
	}

	envMap := parseEnv(cmd.Env)
	if envMap["BD_DOLT_AUTO_COMMIT"] != "on" {
		t.Errorf("Expected 'on', got %q", envMap["BD_DOLT_AUTO_COMMIT"])
	}

	// Old "off" entry must be removed (deduplication)
	if offCount != 0 {
		t.Errorf("Expected 0 'off' entries (dedup should remove old), got %d", offCount)
	}
	if onCount != 1 {
		t.Errorf("Expected exactly 1 'on' entry, got %d", onCount)
	}
}

func TestBdCmd_EmptyGTRoot_Skipped(t *testing.T) {
	// Test that empty GT_ROOT is not added to env.
	// Use a clean env to avoid inheriting GT_ROOT from the test runner.
	bdc := BdCmd("show", "id").
		WithGTRoot("")
	bdc.env = filterEnv(bdc.env, "GT_ROOT")

	cmd := bdc.Build()

	// Check that GT_ROOT is not in env
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "GT_ROOT=") {
			t.Errorf("GT_ROOT should not be added when empty, found: %s", e)
		}
	}
}

func TestBdCmd_AllCombinations(t *testing.T) {
	// Test all possible option combinations
	baseEnv := []string{"BD_DOLT_AUTO_COMMIT=off", "PATH=/usr/bin"}

	tests := []struct {
		name             string
		autoCommit       bool
		gtRoot           string
		wantAutoCommitOn bool
		wantGTRoot       bool
	}{
		{"none", false, "", false, false},
		{"autocommit only", true, "", true, false},
		{"gtroot only", false, "/town", false, true},
		{"autocommit+gtroot", true, "/town", true, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bdc := &bdCmd{
				args:   []string{"show", "id"},
				env:    append([]string{}, baseEnv...), // Copy to avoid mutation
				stderr: os.Stderr,
			}

			if tt.autoCommit {
				bdc.autoCommit = true
			}
			bdc.gtRoot = tt.gtRoot

			cmd := bdc.Build()
			envMap := parseEnv(cmd.Env)

			// Check BD_DOLT_AUTO_COMMIT
			if tt.wantAutoCommitOn {
				if envMap["BD_DOLT_AUTO_COMMIT"] != "on" {
					t.Errorf("BD_DOLT_AUTO_COMMIT = %q, want 'on'", envMap["BD_DOLT_AUTO_COMMIT"])
				}
			} else {
				// When not explicitly set via WithAutoCommit, should keep original
				if envMap["BD_DOLT_AUTO_COMMIT"] != "off" {
					t.Errorf("BD_DOLT_AUTO_COMMIT = %q, want 'off' (original value)", envMap["BD_DOLT_AUTO_COMMIT"])
				}
			}

			// Check GT_ROOT
			_, hasGTRoot := envMap["GT_ROOT"]
			if tt.wantGTRoot && !hasGTRoot {
				t.Error("GT_ROOT should be present")
			}
			if !tt.wantGTRoot && hasGTRoot {
				t.Error("GT_ROOT should not be present")
			}
		})
	}
}

func TestBdCmd_ConcurrentBuild(t *testing.T) {
	// Test that concurrent Build() calls are safe
	// Each Build() gets a snapshot via os.Environ(), so they should be independent
	bdc := BdCmd("show", "id")

	done := make(chan bool, 2)

	go func() {
		cmd1 := bdc.Build()
		_ = cmd1.Env
		done <- true
	}()

	go func() {
		cmd2 := bdc.Build()
		_ = cmd2.Env
		done <- true
	}()

	// Wait for both goroutines
	for i := 0; i < 2; i++ {
		select {
		case <-done:
			// Success
		case <-time.After(5 * time.Second):
			t.Fatal("Timeout waiting for concurrent builds")
		}
	}
}

func TestBdCmd_EnvImmutability(t *testing.T) {
	// Test that buildEnv doesn't mutate the original b.env
	baseEnv := []string{"PATH=/usr/bin", "HOME=/home/user"}
	originalLen := len(baseEnv)

	bdc := &bdCmd{
		args:   []string{"show", "id"},
		env:    baseEnv,
		stderr: os.Stderr,
	}
	bdc.WithAutoCommit().WithGTRoot("/town")

	// Call buildEnv multiple times
	_ = bdc.buildEnv()
	_ = bdc.buildEnv()

	// Original env should be unchanged
	if len(baseEnv) != originalLen {
		t.Errorf("Original env was mutated: length %d, expected %d", len(baseEnv), originalLen)
	}
}

func TestBdCmd_WithBeadsDir_SetsEnv(t *testing.T) {
	// WithBeadsDir should set BEADS_DIR in the environment
	bdc := BdCmd("show", "id").
		WithBeadsDir("/town/rig/mayor/rig/.beads")
	cmd := bdc.Build()
	envMap := parseEnv(cmd.Env)

	if envMap["BEADS_DIR"] != "/town/rig/mayor/rig/.beads" {
		t.Errorf("BEADS_DIR = %q, want %q", envMap["BEADS_DIR"], "/town/rig/mayor/rig/.beads")
	}
}

func TestBdCmd_DirPinsResolvedBeadsDir(t *testing.T) {
	// Dir should pin bd to that directory's resolved .beads database so ambient
	// discovery cannot select HQ or an inherited rig database.
	baseEnv := []string{"PATH=/usr/bin", "BEADS_DIR=/town/.beads", "HOME=/home/user"}

	bdc := &bdCmd{
		args:   []string{"mol", "wisp", "mol-polecat-work"},
		env:    baseEnv,
		stderr: os.Stderr,
	}
	bdc.Dir("/town/gastown/mayor/rig")
	cmd := bdc.Build()
	envMap := parseEnv(cmd.Env)

	if envMap["BEADS_DIR"] != "/town/gastown/mayor/rig/.beads" {
		t.Errorf("BEADS_DIR = %q, want %q", envMap["BEADS_DIR"], "/town/gastown/mayor/rig/.beads")
	}

	count := 0
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "BEADS_DIR=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d BEADS_DIR entries, want 1", count)
	}
}

func TestBdCmd_WithBeadsDir_OverridesInherited(t *testing.T) {
	// WithBeadsDir should override an inherited BEADS_DIR from the parent
	// process. This is the core fix for gt-ctir: without overriding,
	// bd could write to the wrong database (HQ instead of rig).
	baseEnv := []string{"PATH=/usr/bin", "BEADS_DIR=/town/.beads", "HOME=/home/user"}

	bdc := &bdCmd{
		args:   []string{"mol", "wisp", "create", "proto-id"},
		env:    baseEnv,
		stderr: os.Stderr,
	}
	bdc.WithBeadsDir("/town/rig/mayor/rig/.beads")
	cmd := bdc.Build()
	envMap := parseEnv(cmd.Env)

	if envMap["BEADS_DIR"] != "/town/rig/mayor/rig/.beads" {
		t.Errorf("BEADS_DIR = %q, want %q (should override inherited)", envMap["BEADS_DIR"], "/town/rig/mayor/rig/.beads")
	}

	// Verify exactly one BEADS_DIR entry (deduplication)
	count := 0
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "BEADS_DIR=") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("found %d BEADS_DIR entries, want 1 (dedup must remove old)", count)
	}
}

func TestBdCmd_WithBeadsDir_OverridesInheritedDoltTarget(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatalf("mkdir beads dir: %v", err)
	}
	metadata := []byte(`{"backend":"dolt","dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatalf("write metadata: %v", err)
	}

	baseEnv := []string{
		"PATH=/usr/bin",
		"BEADS_DIR=/town/.beads",
		"BEADS_DB=stale",
		"BEADS_DOLT_SERVER_DATABASE=hq",
		"BEADS_DOLT_SERVER_HOST=100.107.173.83",
		"BEADS_DOLT_SERVER_PORT=3307",
		"BEADS_DOLT_PORT=3307",
		"BEADS_DOLT_DATA_DIR=/wrong/data",
		"BD_DB=/wrong.db",
	}

	bdc := &bdCmd{
		args:   []string{"show", "bds-abc", "--json"},
		env:    baseEnv,
		stderr: os.Stderr,
	}
	cmd := bdc.WithBeadsDir(beadsDir).Build()
	envMap := parseEnv(cmd.Env)

	if envMap["BEADS_DIR"] != beadsDir {
		t.Errorf("BEADS_DIR = %q, want %q", envMap["BEADS_DIR"], beadsDir)
	}
	if envMap["BEADS_DOLT_SERVER_DATABASE"] != "rigdb" {
		t.Errorf("BEADS_DOLT_SERVER_DATABASE = %q, want rigdb", envMap["BEADS_DOLT_SERVER_DATABASE"])
	}
	if envMap["BEADS_DOLT_SERVER_HOST"] != "127.0.0.1" {
		t.Errorf("BEADS_DOLT_SERVER_HOST = %q, want 127.0.0.1", envMap["BEADS_DOLT_SERVER_HOST"])
	}
	if envMap["BEADS_DOLT_SERVER_PORT"] != "3307" {
		t.Errorf("BEADS_DOLT_SERVER_PORT = %q, want 3307", envMap["BEADS_DOLT_SERVER_PORT"])
	}
	if envMap["BEADS_DOLT_PORT"] != "3307" {
		t.Errorf("BEADS_DOLT_PORT = %q, want 3307", envMap["BEADS_DOLT_PORT"])
	}
	for _, key := range []string{"BEADS_DB", "BD_DB", "BEADS_DOLT_DATA_DIR"} {
		if value, ok := envMap[key]; ok {
			t.Errorf("%s should be stripped when BEADS_DIR is pinned, got %q", key, value)
		}
	}
}

func TestBdCmd_EmptyBeadsDir_Skipped(t *testing.T) {
	// Empty WithBeadsDir should not add BEADS_DIR to env
	bdc := BdCmd("show", "id").
		WithBeadsDir("")
	bdc.env = filterEnv(bdc.env, "BEADS_DIR")
	cmd := bdc.Build()

	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "BEADS_DIR=") {
			t.Errorf("BEADS_DIR should not be added when empty, found: %s", e)
		}
	}
}

func TestBdCmd_DefaultStripsTargetEnvAndSuppressesSideEffects(t *testing.T) {
	bdc := &bdCmd{
		args: []string{"version"},
		env: []string{
			"PATH=/usr/bin",
			"BEADS_DIR=/wrong",
			"BEADS_DOLT_SERVER_DATABASE=hq",
			"BEADS_DOLT_SERVER_HOST=wrong-host",
			"BD_EXPORT_AUTO=true",
		},
		stderr: os.Stderr,
	}
	cmd := bdc.Build()
	envMap := parseEnv(cmd.Env)
	for _, key := range []string{"BEADS_DIR", "BEADS_DOLT_SERVER_DATABASE", "BEADS_DOLT_SERVER_HOST"} {
		if value, ok := envMap[key]; ok {
			t.Fatalf("%s should be stripped for unpinned BdCmd, got %q in %v", key, value, cmd.Env)
		}
	}
	if envMap["BD_EXPORT_AUTO"] != "false" {
		t.Fatalf("BD_EXPORT_AUTO = %q, want false in %v", envMap["BD_EXPORT_AUTO"], cmd.Env)
	}
}

func TestBdCmd_WithBeadsDir_Chaining(t *testing.T) {
	// WithBeadsDir should return receiver for chaining
	bdc := BdCmd("test")
	if bdc.WithBeadsDir("/test") != bdc {
		t.Error("WithBeadsDir() should return receiver for chaining")
	}
}

func TestBdCmd_StripBeadsDir_RemovesInherited(t *testing.T) {
	// StripBeadsDir should remove inherited BEADS_DIR from the environment.
	// Dir() still pins BEADS_DIR to the resolved target database.
	bdc := &bdCmd{
		args:   []string{"show", "myproject-abc", "--json"},
		env:    []string{"PATH=/usr/bin", "BEADS_DIR=/town/.beads", "HOME=/home/user"},
		stderr: os.Stderr,
	}
	bdc.Dir("/town/myproject/mayor/rig").StripBeadsDir()
	cmd := bdc.Build()

	envMap := parseEnv(cmd.Env)
	if envMap["BEADS_DIR"] != "/town/myproject/mayor/rig/.beads" {
		t.Errorf("BEADS_DIR = %q, want %q", envMap["BEADS_DIR"], "/town/myproject/mayor/rig/.beads")
	}

	if cmd.Dir != "/town/myproject/mayor/rig" {
		t.Errorf("Dir = %q, want %q", cmd.Dir, "/town/myproject/mayor/rig")
	}
}

func TestBdCmd_StripBeadsDir_NoOpWhenAbsent(t *testing.T) {
	// StripBeadsDir should be harmless when BEADS_DIR is not set; Dir() still
	// pins the target database.
	bdc := &bdCmd{
		args:   []string{"show", "hq-abc"},
		env:    []string{"PATH=/usr/bin", "HOME=/home/user"},
		stderr: os.Stderr,
	}
	bdc.Dir("/town").StripBeadsDir()
	cmd := bdc.Build()

	envMap := parseEnv(cmd.Env)
	if envMap["BEADS_DIR"] != "/town/.beads" {
		t.Errorf("BEADS_DIR = %q, want %q", envMap["BEADS_DIR"], "/town/.beads")
	}
}

func TestBdCmd_WithRoutingDoesNotPinBeadsDir(t *testing.T) {
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Dir(beadsDir)
	bdc := &bdCmd{
		args: []string{"show", "gt-abc", "--json"},
		env: []string{
			"PATH=/usr/bin",
			"BEADS_DIR=/wrong",
			"BEADS_DOLT_SERVER_DATABASE=hq",
			"BEADS_DOLT_SERVER_HOST=wrong-host",
		},
		stderr: os.Stderr,
	}
	cmd := bdc.Dir(workDir).WithRouting().Build()
	envMap := parseEnv(cmd.Env)
	if _, ok := envMap["BEADS_DIR"]; ok {
		t.Fatalf("BEADS_DIR should be absent for routing command, got %v", cmd.Env)
	}
	if _, ok := envMap["BEADS_DOLT_SERVER_DATABASE"]; ok {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE should be absent for routing command, got %v", cmd.Env)
	}
	if envMap["BEADS_DOLT_SERVER_HOST"] != "127.0.0.1" {
		t.Fatalf("BEADS_DOLT_SERVER_HOST = %q, want 127.0.0.1 in %v", envMap["BEADS_DOLT_SERVER_HOST"], cmd.Env)
	}
}

func TestBdCmd_StripBeadsDir_Chaining(t *testing.T) {
	bdc := BdCmd("test")
	if bdc.StripBeadsDir() != bdc {
		t.Error("StripBeadsDir() should return receiver for chaining")
	}
}

// filterEnv returns env with all entries matching the given key prefix removed.
func filterEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}
