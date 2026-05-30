package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/workspace"
)

func setupHandoffTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestHandoffStdinFlag(t *testing.T) {
	t.Run("errors when both stdin and message provided", func(t *testing.T) {
		// Save and restore flag state
		origMessage := handoffMessage
		origStdin := handoffStdin
		defer func() {
			handoffMessage = origMessage
			handoffStdin = origStdin
		}()

		handoffMessage = "some message"
		handoffStdin = true

		err := runHandoff(handoffCmd, nil)
		if err == nil {
			t.Fatal("expected error when both --stdin and --message are set")
		}
		if !strings.Contains(err.Error(), "cannot use --stdin with --message/-m") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestSessionWorkDir(t *testing.T) {
	setupHandoffTestRegistry(t)
	townRoot := "/home/test/gt"

	tests := []struct {
		name        string
		sessionName string
		wantDir     string
		wantErr     bool
	}{
		{
			name:        "mayor runs from mayor subdirectory",
			sessionName: "hq-mayor",
			wantDir:     townRoot + "/mayor",
			wantErr:     false,
		},
		{
			name:        "deacon runs from deacon subdirectory",
			sessionName: "hq-deacon",
			wantDir:     townRoot + "/deacon",
			wantErr:     false,
		},
		{
			name:        "crew runs from crew subdirectory",
			sessionName: "gt-crew-holden",
			wantDir:     townRoot + "/gastown/crew/holden",
			wantErr:     false,
		},
		{
			name:        "witness runs from witness directory",
			sessionName: "gt-witness",
			wantDir:     townRoot + "/gastown/witness",
			wantErr:     false,
		},
		{
			name:        "refinery runs from refinery/rig directory",
			sessionName: "gt-refinery",
			wantDir:     townRoot + "/gastown/refinery/rig",
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDir, err := sessionWorkDir(tt.sessionName, townRoot)
			if (err != nil) != tt.wantErr {
				t.Errorf("sessionWorkDir() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if gotDir != tt.wantDir {
				t.Errorf("sessionWorkDir() = %q, want %q", gotDir, tt.wantDir)
			}
		})
	}
}

func TestBuildRestartCommand_UsesRoleAgentsWhenNoAgentOverride(t *testing.T) {
	setupHandoffTestRegistry(t)

	origCwd, _ := os.Getwd()
	origGTAgent := os.Getenv("GT_AGENT")
	origTownRoot := os.Getenv("GT_TOWN_ROOT")
	origRoot := os.Getenv("GT_ROOT")

	// TempDir must be called BEFORE registering the Chdir cleanup so that
	// LIFO ordering restores the working directory before TempDir removal.
	// On Windows the directory cannot be deleted while the process CWD is
	// inside it.
	townRoot := t.TempDir()

	t.Cleanup(func() {
		_ = os.Chdir(origCwd)
		_ = os.Setenv("GT_AGENT", origGTAgent)
		_ = os.Setenv("GT_TOWN_ROOT", origTownRoot)
		_ = os.Setenv("GT_ROOT", origRoot)
	})
	rigPath := filepath.Join(townRoot, "gastown")
	witnessDir := filepath.Join(rigPath, "witness")

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"gastown"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		t.Fatalf("mkdir witness dir: %v", err)
	}

	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "claude"
	townSettings.Agents = map[string]*config.RuntimeConfig{
		"claude-sonnet": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions", "--model", "sonnet"},
		},
	}
	townSettings.RoleAgents = map[string]string{
		"witness": "claude-sonnet",
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), config.NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	if err := os.Setenv("GT_AGENT", ""); err != nil {
		t.Fatalf("Setenv GT_AGENT: %v", err)
	}
	if err := os.Setenv("GT_TOWN_ROOT", ""); err != nil {
		t.Fatalf("Setenv GT_TOWN_ROOT: %v", err)
	}
	if err := os.Setenv("GT_ROOT", ""); err != nil {
		t.Fatalf("Setenv GT_ROOT: %v", err)
	}
	if err := os.Chdir(witnessDir); err != nil {
		t.Fatalf("chdir witness dir: %v", err)
	}

	cmd, err := buildRestartCommand("gt-witness")
	if err != nil {
		t.Fatalf("buildRestartCommand: %v", err)
	}

	if !strings.Contains(cmd, "--model sonnet") {
		t.Errorf("expected role_agents witness model flag in restart command, got: %q", cmd)
	}
}

func TestBuildRestartCommand_MergesAgentPresetEnv(t *testing.T) {
	// Regression test: ensure agent preset Env block (config.json [agents.X.env])
	// is fully merged into the respawn command, not just NODE_OPTIONS.
	// Without this, custom env vars like ANTHROPIC_BASE_URL configured for
	// proxied Claude were silently dropped on handoff/respawn.
	setupHandoffTestRegistry(t)

	origCwd, _ := os.Getwd()
	origGTAgent := os.Getenv("GT_AGENT")
	origTownRoot := os.Getenv("GT_TOWN_ROOT")
	origRoot := os.Getenv("GT_ROOT")

	townRoot := t.TempDir()

	t.Cleanup(func() {
		_ = os.Chdir(origCwd)
		_ = os.Setenv("GT_AGENT", origGTAgent)
		_ = os.Setenv("GT_TOWN_ROOT", origTownRoot)
		_ = os.Setenv("GT_ROOT", origRoot)
	})
	rigPath := filepath.Join(townRoot, "gastown")
	witnessDir := filepath.Join(rigPath, "witness")

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"gastown"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.MkdirAll(witnessDir, 0755); err != nil {
		t.Fatalf("mkdir witness dir: %v", err)
	}

	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "claude-proxy"
	townSettings.Agents = map[string]*config.RuntimeConfig{
		"claude-proxy": {
			Command: "claude",
			Args:    []string{"--dangerously-skip-permissions"},
			Env: map[string]string{
				"ANTHROPIC_BASE_URL":       "http://localhost:8080",
				"CLAUDE_CODE_OAUTH_TOKEN":  "placeholder",
				"ANTHROPIC_CUSTOM_HEADERS": "Authorization: Bearer prx_test",
			},
		},
	}
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), config.NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	_ = os.Setenv("GT_AGENT", "claude-proxy")
	_ = os.Setenv("GT_TOWN_ROOT", "")
	_ = os.Setenv("GT_ROOT", "")
	if err := os.Chdir(witnessDir); err != nil {
		t.Fatalf("chdir witness dir: %v", err)
	}

	cmd, err := buildRestartCommand("gt-witness")
	if err != nil {
		t.Fatalf("buildRestartCommand: %v", err)
	}

	wantEnv := map[string]string{
		"ANTHROPIC_BASE_URL":       "http://localhost:8080",
		"CLAUDE_CODE_OAUTH_TOKEN":  "placeholder",
		"ANTHROPIC_CUSTOM_HEADERS": "Authorization: Bearer prx_test",
	}
	for k, v := range wantEnv {
		if !strings.Contains(cmd, k+"=") {
			t.Errorf("agent preset env %q not exported in restart command\ncmd: %s", k, cmd)
		}
		if !strings.Contains(cmd, v) {
			t.Errorf("agent preset env value for %q (%q) missing in restart command\ncmd: %s", k, v, cmd)
		}
	}
}

func TestBuildRestartCommandWithOpts_ContinuePrompt(t *testing.T) {
	setupHandoffTestRegistry(t)

	origCwd, _ := os.Getwd()
	origGTAgent := os.Getenv("GT_AGENT")
	origTownRoot := os.Getenv("GT_TOWN_ROOT")
	origRoot := os.Getenv("GT_ROOT")

	townRoot := t.TempDir()

	t.Cleanup(func() {
		_ = os.Chdir(origCwd)
		_ = os.Setenv("GT_AGENT", origGTAgent)
		_ = os.Setenv("GT_TOWN_ROOT", origTownRoot)
		_ = os.Setenv("GT_ROOT", origRoot)
	})
	rigPath := filepath.Join(townRoot, "gastown")
	crewDir := filepath.Join(rigPath, "crew", "bear")

	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"gastown"}`), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatalf("mkdir crew dir: %v", err)
	}

	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "claude"
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("SaveTownSettings: %v", err)
	}
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), config.NewRigSettings()); err != nil {
		t.Fatalf("SaveRigSettings: %v", err)
	}

	_ = os.Setenv("GT_AGENT", "")
	_ = os.Setenv("GT_TOWN_ROOT", "")
	_ = os.Setenv("GT_ROOT", "")
	_ = os.Chdir(crewDir)

	t.Run("custom ContinuePrompt overrides default", func(t *testing.T) {
		cmd, err := buildRestartCommandWithOpts("gt-crew-bear", buildRestartCommandOpts{
			ContinueSession: true,
			ContinuePrompt:  "Context compacted. Continue your previous task.",
		})
		if err != nil {
			t.Fatalf("buildRestartCommandWithOpts: %v", err)
		}
		if !strings.Contains(cmd, "--continue") {
			t.Errorf("expected --continue flag in restart command, got: %q", cmd)
		}
		if !strings.Contains(cmd, "Context compacted") {
			t.Errorf("expected custom prompt in restart command, got: %q", cmd)
		}
	})

	t.Run("empty ContinuePrompt falls back to default", func(t *testing.T) {
		cmd, err := buildRestartCommandWithOpts("gt-crew-bear", buildRestartCommandOpts{
			ContinueSession: true,
		})
		if err != nil {
			t.Fatalf("buildRestartCommandWithOpts: %v", err)
		}
		if !strings.Contains(cmd, "--continue") {
			t.Errorf("expected --continue flag in restart command, got: %q", cmd)
		}
		if !strings.Contains(cmd, "Continue your previous task") {
			t.Errorf("expected default continuation message when ContinuePrompt is empty, got: %q", cmd)
		}
	})

	t.Run("ContinueSession false uses beacon", func(t *testing.T) {
		cmd, err := buildRestartCommandWithOpts("gt-crew-bear", buildRestartCommandOpts{
			ContinueSession: false,
		})
		if err != nil {
			t.Fatalf("buildRestartCommandWithOpts: %v", err)
		}
		if strings.Contains(cmd, "--continue") {
			t.Errorf("expected no --continue flag when ContinueSession is false, got: %q", cmd)
		}
	})
}

func TestDetectTownRootFromCwd_EnvFallback(t *testing.T) {
	// Save original env vars and restore after test
	origTownRoot := os.Getenv("GT_TOWN_ROOT")
	origRoot := os.Getenv("GT_ROOT")
	defer func() {
		os.Setenv("GT_TOWN_ROOT", origTownRoot)
		os.Setenv("GT_ROOT", origRoot)
	}()

	// Create a temp directory that looks like a valid town
	tmpTown := t.TempDir()
	mayorDir := filepath.Join(tmpTown, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("creating mayor dir: %v", err)
	}
	townJSON := filepath.Join(mayorDir, "town.json")
	if err := os.WriteFile(townJSON, []byte(`{"name": "test-town"}`), 0644); err != nil {
		t.Fatalf("creating town.json: %v", err)
	}

	// Clear both env vars initially
	os.Setenv("GT_TOWN_ROOT", "")
	os.Setenv("GT_ROOT", "")

	t.Run("uses GT_TOWN_ROOT when cwd detection fails", func(t *testing.T) {
		// Set GT_TOWN_ROOT to our temp town
		os.Setenv("GT_TOWN_ROOT", tmpTown)
		os.Setenv("GT_ROOT", "")

		// Save cwd, cd to a non-town directory, and restore after
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		defer os.Chdir(origCwd)

		result := detectTownRootFromCwd()
		if result != tmpTown {
			t.Errorf("detectTownRootFromCwd() = %q, want %q (should use GT_TOWN_ROOT fallback)", result, tmpTown)
		}
	})

	t.Run("uses GT_ROOT when GT_TOWN_ROOT not set", func(t *testing.T) {
		// Set only GT_ROOT
		os.Setenv("GT_TOWN_ROOT", "")
		os.Setenv("GT_ROOT", tmpTown)

		// Save cwd, cd to a non-town directory, and restore after
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		defer os.Chdir(origCwd)

		result := detectTownRootFromCwd()
		if result != tmpTown {
			t.Errorf("detectTownRootFromCwd() = %q, want %q (should use GT_ROOT fallback)", result, tmpTown)
		}
	})

	t.Run("prefers GT_TOWN_ROOT over GT_ROOT", func(t *testing.T) {
		// Create another temp town for GT_ROOT
		anotherTown := t.TempDir()
		anotherMayor := filepath.Join(anotherTown, "mayor")
		os.MkdirAll(anotherMayor, 0755)
		os.WriteFile(filepath.Join(anotherMayor, "town.json"), []byte(`{"name": "other-town"}`), 0644)

		// Set both env vars
		os.Setenv("GT_TOWN_ROOT", tmpTown)
		os.Setenv("GT_ROOT", anotherTown)

		// Save cwd, cd to a non-town directory, and restore after
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		defer os.Chdir(origCwd)

		result := detectTownRootFromCwd()
		if result != tmpTown {
			t.Errorf("detectTownRootFromCwd() = %q, want %q (should prefer GT_TOWN_ROOT)", result, tmpTown)
		}
	})

	t.Run("ignores invalid GT_TOWN_ROOT", func(t *testing.T) {
		// Set GT_TOWN_ROOT to non-existent path, GT_ROOT to valid
		os.Setenv("GT_TOWN_ROOT", "/nonexistent/path/to/town")
		os.Setenv("GT_ROOT", tmpTown)

		// Save cwd, cd to a non-town directory, and restore after
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		defer os.Chdir(origCwd)

		result := detectTownRootFromCwd()
		if result != tmpTown {
			t.Errorf("detectTownRootFromCwd() = %q, want %q (should skip invalid GT_TOWN_ROOT and use GT_ROOT)", result, tmpTown)
		}
	})

	t.Run("uses secondary marker when primary missing", func(t *testing.T) {
		// Create a temp town with only mayor/ directory (no town.json)
		secondaryTown := t.TempDir()
		mayorOnlyDir := filepath.Join(secondaryTown, workspace.SecondaryMarker)
		os.MkdirAll(mayorOnlyDir, 0755)

		os.Setenv("GT_TOWN_ROOT", secondaryTown)
		os.Setenv("GT_ROOT", "")

		// Save cwd, cd to a non-town directory, and restore after
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		defer os.Chdir(origCwd)

		result := detectTownRootFromCwd()
		if result != secondaryTown {
			t.Errorf("detectTownRootFromCwd() = %q, want %q (should accept secondary marker)", result, secondaryTown)
		}
	})
}

// makeTestGitRepo creates a minimal git repo in a temp dir and returns its path.
// The caller is responsible for cleanup via t.Cleanup or defer os.RemoveAll.
func makeTestGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"git", "-C", dir, "init"},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
		// Disable background processes that hold file handles open after exit —
		// causes TempDir cleanup failures on Windows.
		{"git", "-C", dir, "config", "gc.auto", "0"},
		{"git", "-C", dir, "config", "core.fsmonitor", "false"},
		{"git", "-C", dir, "commit", "--allow-empty", "-m", "init"},
	} {
		if err := exec.Command(args[0], args[1:]...).Run(); err != nil {
			t.Fatalf("git setup %v: %v", args, err)
		}
	}
	return dir
}

// TestHandoffPolecatEnvCheck verifies that the polecat guard in runHandoff uses
// GT_ROLE as the authoritative check, so coordinators with a stale GT_POLECAT
// in their environment are not redirected to gt done (GH #1707).
func TestHandoffPolecatEnvCheck(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		polecat   string
		wantBlock bool
	}{
		{
			name:      "bare polecat role is redirected",
			role:      "polecat",
			polecat:   "alpha",
			wantBlock: true,
		},
		{
			name:      "compound polecat role is redirected",
			role:      "gastown/polecats/Toast",
			polecat:   "Toast",
			wantBlock: true,
		},
		{
			name:      "mayor with stale GT_POLECAT is NOT redirected",
			role:      "mayor",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "compound witness with stale GT_POLECAT is NOT redirected",
			role:      "gastown/witness",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "crew with stale GT_POLECAT is NOT redirected",
			role:      "crew",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "compound crew with stale GT_POLECAT is NOT redirected",
			role:      "gastown/crew/den",
			polecat:   "alpha",
			wantBlock: false,
		},
		{
			name:      "no GT_ROLE with GT_POLECAT set is redirected",
			role:      "",
			polecat:   "alpha",
			wantBlock: true,
		},
		{
			name:      "no GT_ROLE and no GT_POLECAT is not redirected",
			role:      "",
			polecat:   "",
			wantBlock: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			binDir := t.TempDir()
			gtLog := filepath.Join(t.TempDir(), "gt.log")
			_ = writeBDStub(t, binDir, "#!/bin/sh\nexit 0\n", "@echo off\r\nexit /b 0\r\n")
			gtStub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"" + gtLog + "\"\nprintf 'stub gt %s\\n' \"$*\"\nexit 0\n"
			if err := os.WriteFile(filepath.Join(binDir, "gt"), []byte(gtStub), 0755); err != nil {
				t.Fatalf("write gt stub: %v", err)
			}
			gtCmdStub := "@echo off\r\necho %* >> \"" + gtLog + "\"\r\necho stub gt %*\r\nexit /b 0\r\n"
			if err := os.WriteFile(filepath.Join(binDir, "gt.cmd"), []byte(gtCmdStub), 0644); err != nil {
				t.Fatalf("write gt.cmd stub: %v", err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			isolatedRoot := t.TempDir()
			t.Setenv("GT_TOWN_ROOT", isolatedRoot)
			t.Setenv("GT_ROOT", isolatedRoot)
			t.Chdir(isolatedRoot)
			t.Setenv("GT_ROLE", tt.role)
			t.Setenv("GT_POLECAT", tt.polecat)
			// Ensure deterministic non-tmux execution so the non-polecat
			// paths fail predictably instead of triggering real side effects.
			t.Setenv("TMUX", "")
			t.Setenv("TMUX_PANE", "")

			// Reset flags to avoid interference
			origMessage := handoffMessage
			origStdin := handoffStdin
			origAuto := handoffAuto
			defer func() {
				handoffMessage = origMessage
				handoffStdin = origStdin
				handoffAuto = origAuto
			}()
			handoffMessage = ""
			handoffStdin = false
			handoffAuto = false

			// The polecat path tries to exec "gt done" which will fail in tests.
			// We capture stdout to detect the "Polecat detected" message, which
			// confirms the polecat guard triggered. Non-polecat paths will fail
			// later (missing tmux, etc.) without printing the polecat message.
			var blocked bool
			output := captureStdout(t, func() {
				defer func() {
					if r := recover(); r != nil {
						// Panic means we got past the guard — not blocked
					}
				}()
				runHandoff(handoffCmd, nil)
			})
			blocked = strings.Contains(output, "Polecat detected")

			if blocked != tt.wantBlock {
				if tt.wantBlock {
					t.Errorf("expected polecat redirect but was not redirected (GT_ROLE=%q GT_POLECAT=%q)", tt.role, tt.polecat)
				} else {
					t.Errorf("unexpected polecat redirect with GT_ROLE=%q GT_POLECAT=%q; output: %s", tt.role, tt.polecat, output)
				}
			}
			gtLogBytes, _ := os.ReadFile(gtLog)
			stubRan := strings.Contains(string(gtLogBytes), "done --status DEFERRED")
			if stubRan != tt.wantBlock {
				t.Errorf("gt stub ran = %v, want %v; log: %s", stubRan, tt.wantBlock, gtLogBytes)
			}
		})
	}
}

func TestWarnHandoffGitStatus(t *testing.T) {
	origCwd, _ := os.Getwd()
	t.Cleanup(func() { os.Chdir(origCwd) })

	t.Run("no warning on clean repo", func(t *testing.T) {
		dir := makeTestGitRepo(t)
		os.Chdir(dir)
		t.Cleanup(func() { os.Chdir(origCwd) })
		output := captureStderr(t, func() {
			warnHandoffGitStatus()
		})
		if output != "" {
			t.Errorf("expected no output for clean repo, got: %q", output)
		}
	})

	t.Run("warns on untracked file", func(t *testing.T) {
		dir := makeTestGitRepo(t)
		os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0644)
		os.Chdir(dir)
		t.Cleanup(func() { os.Chdir(origCwd) })
		output := captureStderr(t, func() {
			warnHandoffGitStatus()
		})
		if !strings.Contains(output, "uncommitted work") {
			t.Errorf("expected warning about uncommitted work, got: %q", output)
		}
		if !strings.Contains(output, "untracked") {
			t.Errorf("expected 'untracked' in output, got: %q", output)
		}
	})

	t.Run("warns on modified tracked file", func(t *testing.T) {
		dir := makeTestGitRepo(t)
		// Create and commit a file
		fpath := filepath.Join(dir, "tracked.txt")
		os.WriteFile(fpath, []byte("original"), 0644)
		exec.Command("git", "-C", dir, "add", ".").Run()
		exec.Command("git", "-C", dir, "commit", "-m", "add file").Run()
		// Now modify it
		os.WriteFile(fpath, []byte("modified"), 0644)
		os.Chdir(dir)
		t.Cleanup(func() { os.Chdir(origCwd) })
		output := captureStderr(t, func() {
			warnHandoffGitStatus()
		})
		if !strings.Contains(output, "uncommitted work") {
			t.Errorf("expected warning about uncommitted work, got: %q", output)
		}
		if !strings.Contains(output, "modified") {
			t.Errorf("expected 'modified' in output, got: %q", output)
		}
	})

	t.Run("no warning for .beads-only changes", func(t *testing.T) {
		dir := makeTestGitRepo(t)
		// Only .beads/ untracked files — should be clean (excluded)
		os.MkdirAll(filepath.Join(dir, ".beads"), 0755)
		os.WriteFile(filepath.Join(dir, ".beads", "somefile.db"), []byte("db"), 0644)
		os.Chdir(dir)
		t.Cleanup(func() { os.Chdir(origCwd) })
		output := captureStderr(t, func() {
			warnHandoffGitStatus()
		})
		if output != "" {
			t.Errorf("expected no output for .beads-only changes, got: %q", output)
		}
	})

	t.Run("no warning outside git repo", func(t *testing.T) {
		os.Chdir(os.TempDir())
		output := captureStderr(t, func() {
			warnHandoffGitStatus()
		})
		if output != "" {
			t.Errorf("expected no output outside git repo, got: %q", output)
		}
	})

	t.Run("no-git-check flag suppresses warning", func(t *testing.T) {
		dir := makeTestGitRepo(t)
		os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0644)
		os.Chdir(dir)
		t.Cleanup(func() { os.Chdir(origCwd) })
		// Simulate --no-git-check by setting the flag
		origFlag := handoffNoGitCheck
		handoffNoGitCheck = true
		defer func() { handoffNoGitCheck = origFlag }()
		output := captureStderr(t, func() {
			if !handoffNoGitCheck {
				warnHandoffGitStatus()
			}
		})
		if output != "" {
			t.Errorf("expected no output with --no-git-check, got: %q", output)
		}
	})
}

func TestHandoffProcessNames(t *testing.T) {
	t.Run("same-agent restart preserves GT_PROCESS_NAMES from env", func(t *testing.T) {
		setupHandoffTestRegistry(t)

		tmpTown := t.TempDir()
		mayorDir := filepath.Join(tmpTown, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0644)

		t.Setenv("GT_ROOT", tmpTown)
		t.Setenv("GT_AGENT", "claude")
		t.Setenv("GT_PROCESS_NAMES", "node,claude")
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		t.Cleanup(func() { os.Chdir(origCwd) })

		// Same-agent restart should preserve existing process names from env
		cmd, err := buildRestartCommand("gt-crew-propane")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(cmd, "GT_PROCESS_NAMES") || !strings.Contains(cmd, "node,claude") {
			t.Errorf("expected GT_PROCESS_NAMES=node,claude preserved from env, got: %q", cmd)
		}
	})

	t.Run("first boot without GT_PROCESS_NAMES computes from config", func(t *testing.T) {
		setupHandoffTestRegistry(t)

		tmpTown := t.TempDir()
		mayorDir := filepath.Join(tmpTown, "mayor")
		os.MkdirAll(mayorDir, 0755)
		os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte(`{"name":"test"}`), 0644)

		t.Setenv("GT_ROOT", tmpTown)
		t.Setenv("GT_AGENT", "claude")
		// Explicitly clear GT_PROCESS_NAMES to simulate first boot
		t.Setenv("GT_PROCESS_NAMES", "")
		origCwd, _ := os.Getwd()
		os.Chdir(os.TempDir())
		t.Cleanup(func() { os.Chdir(origCwd) })

		// No GT_PROCESS_NAMES in env — should compute from agent config
		cmd, err := buildRestartCommand("gt-crew-propane")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Claude's default process names are "node,claude"
		if !strings.Contains(cmd, "GT_PROCESS_NAMES") || !strings.Contains(cmd, "node,claude") {
			t.Errorf("expected GT_PROCESS_NAMES=node,claude computed from config, got: %q", cmd)
		}
	})
}

// TestCollectGitState verifies that collectGitState returns deterministic
// workspace state from a git repo without shelling out to gt/bd. (GH#1996)
func TestCollectGitState(t *testing.T) {
	t.Run("returns_state_from_git_repo", func(t *testing.T) {
		// Create a temp git repo
		tmpDir := t.TempDir()
		cmds := [][]string{
			{"git", "init"},
			{"git", "config", "user.email", "test@test.com"},
			{"git", "config", "user.name", "Test"},
		}
		for _, args := range cmds {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %s", args, out)
			}
		}

		// Create a file and commit
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("hello"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}
		for _, args := range [][]string{
			{"git", "add", "file.txt"},
			{"git", "commit", "-m", "initial commit"},
		} {
			cmd := exec.Command(args[0], args[1:]...)
			cmd.Dir = tmpDir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v failed: %s", args, out)
			}
		}

		// Modify a file to create uncommitted changes
		if err := os.WriteFile(filepath.Join(tmpDir, "file.txt"), []byte("modified"), 0644); err != nil {
			t.Fatalf("write: %v", err)
		}

		// Run collectGitState from the temp repo
		t.Chdir(tmpDir)

		state := collectGitState()

		if state == "" {
			t.Fatal("collectGitState() returned empty string for a git repo with changes")
		}
		if !strings.Contains(state, "## Workspace State") {
			t.Errorf("expected '## Workspace State' header, got: %s", state)
		}
		if !strings.Contains(state, "Modified") {
			t.Errorf("expected 'Modified' in state, got: %s", state)
		}
		if !strings.Contains(state, "initial commit") {
			t.Errorf("expected recent commit in state, got: %s", state)
		}
	})

	t.Run("returns_empty_outside_git_repo", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		state := collectGitState()
		if state != "" {
			t.Errorf("expected empty string outside git repo, got: %s", state)
		}
	})
}

// TestRecordHandoffTime verifies that recordHandoffTime creates the
// timestamp file in .runtime/ with a recent modification time.
func TestRecordHandoffTime(t *testing.T) {
	tmpDir := t.TempDir()
	t.Chdir(tmpDir)

	recordHandoffTime()

	tsPath := filepath.Join(tmpDir, constants.DirRuntime, constants.FileLastHandoffTS)
	info, err := os.Stat(tsPath)
	if err != nil {
		t.Fatalf("expected last_handoff_ts file to exist: %v", err)
	}
	if time.Since(info.ModTime()) > 5*time.Second {
		t.Errorf("expected recent modification time, got %v ago", time.Since(info.ModTime()))
	}
}

// TestEnforceHandoffCooldown verifies the cooldown logic:
// - No cooldown when no previous handoff recorded
// - Cooldown triggers when last handoff was recent
// - No cooldown when enough time has passed
func TestEnforceHandoffCooldown(t *testing.T) {
	t.Run("no cooldown without previous handoff", func(t *testing.T) {
		t.Setenv("GT_ROLE", "")
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		start := time.Now()
		enforceHandoffCooldown()
		elapsed := time.Since(start)

		// Should return almost immediately (no file to check)
		if elapsed > 1*time.Second {
			t.Errorf("expected no cooldown, but waited %v", elapsed)
		}
	})

	t.Run("no cooldown when last handoff is old", func(t *testing.T) {
		t.Setenv("GT_ROLE", "")
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Create a last_handoff_ts file with old mtime
		runtimeDir := filepath.Join(tmpDir, constants.DirRuntime)
		os.MkdirAll(runtimeDir, 0755)
		tsPath := filepath.Join(runtimeDir, constants.FileLastHandoffTS)
		os.WriteFile(tsPath, []byte("1000000000"), 0644)
		// Set mtime to well in the past
		oldTime := time.Now().Add(-10 * time.Minute)
		os.Chtimes(tsPath, oldTime, oldTime)

		start := time.Now()
		enforceHandoffCooldown()
		elapsed := time.Since(start)

		if elapsed > 1*time.Second {
			t.Errorf("expected no cooldown for old handoff, but waited %v", elapsed)
		}
	})

	t.Run("cooldown triggers for recent handoff", func(t *testing.T) {
		// Use a non-exempt role so cooldown applies
		t.Setenv("GT_ROLE", "gastown/witness")
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Create a last_handoff_ts file with very recent mtime
		runtimeDir := filepath.Join(tmpDir, constants.DirRuntime)
		os.MkdirAll(runtimeDir, 0755)
		tsPath := filepath.Join(runtimeDir, constants.FileLastHandoffTS)
		os.WriteFile(tsPath, []byte("now"), 0644)
		// Set mtime to (MinHandoffCooldown - 1s) ago so remaining is ~1s
		recentTime := time.Now().Add(-(constants.MinHandoffCooldown - 1*time.Second))
		os.Chtimes(tsPath, recentTime, recentTime)

		start := time.Now()
		enforceHandoffCooldown()
		elapsed := time.Since(start)

		// Should have waited approximately 1 second (the remaining cooldown)
		if elapsed < 500*time.Millisecond {
			t.Errorf("expected cooldown sleep of ~1s, but only waited %v", elapsed)
		}
		if elapsed > 3*time.Second {
			t.Errorf("expected cooldown sleep of ~1s, but waited %v", elapsed)
		}
	})

	t.Run("no cooldown for crew role", func(t *testing.T) {
		t.Setenv("GT_ROLE", "gastown/crew/max")
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Create a recent handoff file that would normally trigger cooldown
		runtimeDir := filepath.Join(tmpDir, constants.DirRuntime)
		os.MkdirAll(runtimeDir, 0755)
		tsPath := filepath.Join(runtimeDir, constants.FileLastHandoffTS)
		os.WriteFile(tsPath, []byte("now"), 0644)

		start := time.Now()
		enforceHandoffCooldown()
		elapsed := time.Since(start)

		if elapsed > 1*time.Second {
			t.Errorf("crew should be exempt from cooldown, but waited %v", elapsed)
		}
	})

	t.Run("no cooldown for mayor role", func(t *testing.T) {
		t.Setenv("GT_ROLE", "mayor")
		tmpDir := t.TempDir()
		t.Chdir(tmpDir)

		// Create a recent handoff file that would normally trigger cooldown
		runtimeDir := filepath.Join(tmpDir, constants.DirRuntime)
		os.MkdirAll(runtimeDir, 0755)
		tsPath := filepath.Join(runtimeDir, constants.FileLastHandoffTS)
		os.WriteFile(tsPath, []byte("now"), 0644)

		start := time.Now()
		enforceHandoffCooldown()
		elapsed := time.Since(start)

		if elapsed > 1*time.Second {
			t.Errorf("mayor should be exempt from cooldown, but waited %v", elapsed)
		}
	})
}
