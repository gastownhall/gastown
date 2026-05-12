package wrappers

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// expectedWrappers is the canonical list of wrapper scripts.
// Keep in sync with Install() and Remove() in wrappers.go.
var expectedWrappers = []string{"gt-codex", "gt-gemini", "gt-opencode"}

func TestEmbeddedScripts_Exist(t *testing.T) {
	t.Parallel()
	for _, name := range expectedWrappers {
		t.Run(name, func(t *testing.T) {
			content, err := scriptsFS.ReadFile("scripts/" + name)
			if err != nil {
				t.Fatalf("Embedded script %s not found: %v", name, err)
			}
			if len(content) == 0 {
				t.Fatalf("Embedded script %s is empty", name)
			}
		})
	}
}

func TestEmbeddedScripts_HaveShebang(t *testing.T) {
	t.Parallel()
	for _, name := range expectedWrappers {
		t.Run(name, func(t *testing.T) {
			content, err := scriptsFS.ReadFile("scripts/" + name)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", name, err)
			}
			if !strings.HasPrefix(string(content), "#!/") {
				t.Errorf("Script %s missing shebang line", name)
			}
		})
	}
}

func TestEmbeddedScripts_HaveExecLine(t *testing.T) {
	t.Parallel()
	// Each wrapper should exec its target binary.
	// gt-codex → exec codex, gt-gemini → exec gemini, gt-opencode → exec opencode
	for _, name := range expectedWrappers {
		t.Run(name, func(t *testing.T) {
			content, err := scriptsFS.ReadFile("scripts/" + name)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", name, err)
			}

			// Extract expected binary name: gt-codex → codex
			binary := strings.TrimPrefix(name, "gt-")
			expectedExec := "exec " + binary

			if !strings.Contains(string(content), expectedExec) {
				t.Errorf("Script %s missing expected exec line %q", name, expectedExec)
			}
		})
	}
}

func TestEmbeddedScripts_HaveGtPrime(t *testing.T) {
	t.Parallel()
	for _, name := range expectedWrappers {
		t.Run(name, func(t *testing.T) {
			content, err := scriptsFS.ReadFile("scripts/" + name)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", name, err)
			}

			if !strings.Contains(string(content), "gt prime") {
				t.Errorf("Script %s should run 'gt prime' before launching agent", name)
			}
		})
	}
}

// TestEmbeddedScripts_UseHookFlag verifies that each wrapper uses the --hook flag
// when calling gt prime, ensuring session ID tracking and proper context injection.
// Without --hook, gt prime skips session event emission and agent-ready signaling.
// Regression test for hq-adl: Codex/API startup missed initial Gas Town context.
func TestEmbeddedScripts_UseHookFlag(t *testing.T) {
	t.Parallel()
	for _, name := range expectedWrappers {
		t.Run(name, func(t *testing.T) {
			content, err := scriptsFS.ReadFile("scripts/" + name)
			if err != nil {
				t.Fatalf("Failed to read %s: %v", name, err)
			}

			if !strings.Contains(string(content), "gt prime --hook") {
				t.Errorf("Script %s should use 'gt prime --hook' (not bare 'gt prime') for session tracking", name)
			}
		})
	}
}

func TestInstall_CreatesAllWrappers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper install not supported on Windows")
	}

	// Override HOME to use a temp directory
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	if err := Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	binDir := filepath.Join(tmpHome, "bin")
	for _, name := range expectedWrappers {
		path := filepath.Join(binDir, name)
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("Wrapper %s not created: %v", name, err)
			continue
		}
		// Check executable permissions
		if info.Mode()&0111 == 0 {
			t.Errorf("Wrapper %s is not executable: mode=%v", name, info.Mode())
		}
	}
}

func TestRemove_CleansUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper removal not supported on Windows")
	}

	// Override HOME to use a temp directory
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Install first
	if err := Install(); err != nil {
		t.Fatalf("Install() error = %v", err)
	}

	// Verify files exist
	binDir := filepath.Join(tmpHome, "bin")
	for _, name := range expectedWrappers {
		if _, err := os.Stat(filepath.Join(binDir, name)); err != nil {
			t.Fatalf("Precondition: wrapper %s should exist after Install", name)
		}
	}

	// Remove
	if err := Remove(); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}

	// Verify files are gone
	for _, name := range expectedWrappers {
		if _, err := os.Stat(filepath.Join(binDir, name)); err == nil {
			t.Errorf("Wrapper %s still exists after Remove()", name)
		}
	}
}

func TestRemove_NoErrorWhenMissing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper removal not supported on Windows")
	}

	// Override HOME to use a temp directory (no wrappers installed)
	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Remove should not error when files don't exist
	if err := Remove(); err != nil {
		t.Errorf("Remove() should not error when wrappers don't exist: %v", err)
	}
}

func TestInstall_Idempotent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("wrapper install not supported on Windows")
	}

	tmpHome := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpHome)
	t.Cleanup(func() { os.Setenv("HOME", origHome) })

	// Install twice
	if err := Install(); err != nil {
		t.Fatalf("First Install() error = %v", err)
	}
	if err := Install(); err != nil {
		t.Fatalf("Second Install() error = %v", err)
	}

	// All wrappers should still exist with correct content
	binDir := filepath.Join(tmpHome, "bin")
	for _, name := range expectedWrappers {
		content, err := os.ReadFile(filepath.Join(binDir, name))
		if err != nil {
			t.Errorf("Wrapper %s missing after double install: %v", name, err)
			continue
		}
		// Should match embedded content
		embedded, _ := scriptsFS.ReadFile("scripts/" + name)
		if string(content) != string(embedded) {
			t.Errorf("Wrapper %s content doesn't match embedded script after double install", name)
		}
	}
}
