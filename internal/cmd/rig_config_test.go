package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/wisp"
)

// setupTestRigForConfig creates a minimal Gas Town workspace for rig config testing.
// Returns townRoot and rigName.
func setupTestRigForConfig(t *testing.T) (string, string) {
	t.Helper()

	townRoot := t.TempDir()
	rigName := "testrig"

	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	townConfig := &config.TownConfig{
		Type:      "town",
		Version:   config.CurrentTownVersion,
		Name:      "test-town",
		CreatedAt: time.Now().Truncate(time.Second),
	}
	if err := config.SaveTownConfig(filepath.Join(mayorDir, "town.json"), townConfig); err != nil {
		t.Fatalf("save town.json: %v", err)
	}

	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			rigName: {
				GitURL:  "git@github.com:test/testrig.git",
				AddedAt: time.Now().Truncate(time.Second),
			},
		},
	}
	if err := config.SaveRigsConfig(filepath.Join(mayorDir, "rigs.json"), rigsConfig); err != nil {
		t.Fatalf("save rigs.json: %v", err)
	}

	rigPath := filepath.Join(townRoot, rigName)
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	rigConfig := config.NewRigConfig(rigName, "git@github.com:test/testrig.git")
	if err := config.SaveRigConfig(filepath.Join(rigPath, "config.json"), rigConfig); err != nil {
		t.Fatalf("save rig config: %v", err)
	}

	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatalf("chdir to town root: %v", err)
	}
	t.Cleanup(func() { os.Chdir(oldCwd) })

	return townRoot, rigName
}

func TestRigConfigSet_WispLayerWarning(t *testing.T) {
	t.Run("warns about ephemeral when writing to wisp layer", func(t *testing.T) {
		townRoot, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = false

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "max_polecats", "5"})
			if err != nil {
				t.Fatalf("runRigConfigSet: %v", err)
			}
		})

		if !strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("expected ephemeral warning on stderr, got: %q", stderrOut)
		}
		if !strings.Contains(stderrOut, "--global") {
			t.Errorf("expected --global hint on stderr, got: %q", stderrOut)
		}

		// Verify value was actually stored in wisp layer
		wispCfg := wisp.NewConfig(townRoot, rigName)
		val := wispCfg.Get("max_polecats")
		if val == nil {
			t.Error("expected max_polecats to be set in wisp layer")
		}
	})

	t.Run("warns for string values in wisp layer", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = false

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "default_formula", "mol-custom"})
			if err != nil {
				t.Fatalf("runRigConfigSet: %v", err)
			}
		})

		if !strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("expected ephemeral warning on stderr for string value, got: %q", stderrOut)
		}
	})

	t.Run("warns for boolean values in wisp layer", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = false

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "auto_restart", "false"})
			if err != nil {
				t.Fatalf("runRigConfigSet: %v", err)
			}
		})

		if !strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("expected ephemeral warning on stderr for boolean value, got: %q", stderrOut)
		}
	})

	t.Run("no ephemeral warning when using --block flag", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigSetGlobal = false
		rigConfigSetBlock = true
		t.Cleanup(func() { rigConfigSetBlock = false })

		stderrOut := captureStderr(t, func() {
			err := runRigConfigSet(rigConfigSetCmd, []string{rigName, "auto_restart"})
			if err != nil {
				t.Fatalf("runRigConfigSet with --block: %v", err)
			}
		})

		// --block also writes to wisp but has different UX semantics; no ephemeral warning expected
		if strings.Contains(stderrOut, "ephemeral") {
			t.Errorf("unexpected ephemeral warning for --block operation, got: %q", stderrOut)
		}
	})
}

func TestConfigAnnotation(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value interface{}
		want  string
	}{
		{"max_polecats ON (int)", "max_polecats", 10, "[deferred dispatch: ON — set to -1 to disable]"},
		{"max_polecats ON (int 1)", "max_polecats", 1, "[deferred dispatch: ON — set to -1 to disable]"},
		{"max_polecats OFF (-1)", "max_polecats", -1, "[deferred dispatch: OFF]"},
		{"max_polecats OFF (0)", "max_polecats", 0, "[deferred dispatch: OFF]"},
		{"max_polecats ON (string)", "max_polecats", "5", "[deferred dispatch: ON — set to -1 to disable]"},
		{"max_polecats OFF (string -1)", "max_polecats", "-1", "[deferred dispatch: OFF]"},
		{"other key no annotation", "auto_restart", true, ""},
		{"other key no annotation (string)", "status", "operational", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := configAnnotation(tt.key, tt.value)
			if got != tt.want {
				t.Errorf("configAnnotation(%q, %v) = %q, want %q", tt.key, tt.value, got, tt.want)
			}
		})
	}
}

func TestRigConfigShow_MaxPolecatsAnnotation(t *testing.T) {
	t.Run("system default (10) shows deferred dispatch ON annotation", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigShowLayers = false
		t.Cleanup(func() { rigConfigShowLayers = false })

		out := captureStdout(t, func() {
			if err := runRigConfigShow(rigConfigShowCmd, []string{rigName}); err != nil {
				t.Fatalf("runRigConfigShow: %v", err)
			}
		})

		if !strings.Contains(out, "deferred dispatch: ON") {
			t.Errorf("expected 'deferred dispatch: ON' in output for max_polecats=10, got:\n%s", out)
		}
		if !strings.Contains(out, "set to -1 to disable") {
			t.Errorf("expected 'set to -1 to disable' hint in output, got:\n%s", out)
		}
	})

	t.Run("--layers flag shows annotation with source", func(t *testing.T) {
		_, rigName := setupTestRigForConfig(t)

		rigConfigShowLayers = true
		t.Cleanup(func() { rigConfigShowLayers = false })

		out := captureStdout(t, func() {
			if err := runRigConfigShow(rigConfigShowCmd, []string{rigName}); err != nil {
				t.Fatalf("runRigConfigShow --layers: %v", err)
			}
		})

		if !strings.Contains(out, "deferred dispatch: ON") {
			t.Errorf("expected annotation in --layers output, got:\n%s", out)
		}
		if !strings.Contains(out, "system") {
			t.Errorf("expected 'system' source in --layers output, got:\n%s", out)
		}
	})

	t.Run("max_polecats -1 shows deferred dispatch OFF annotation", func(t *testing.T) {
		townRoot, rigName := setupTestRigForConfig(t)

		// Override max_polecats to -1 via wisp layer
		wispCfg := wisp.NewConfig(townRoot, rigName)
		if err := wispCfg.Set("max_polecats", -1); err != nil {
			t.Fatalf("set wisp max_polecats: %v", err)
		}

		rigConfigShowLayers = false
		t.Cleanup(func() { rigConfigShowLayers = false })

		out := captureStdout(t, func() {
			if err := runRigConfigShow(rigConfigShowCmd, []string{rigName}); err != nil {
				t.Fatalf("runRigConfigShow: %v", err)
			}
		})

		if !strings.Contains(out, "deferred dispatch: OFF") {
			t.Errorf("expected 'deferred dispatch: OFF' for max_polecats=-1, got:\n%s", out)
		}
	})
}
