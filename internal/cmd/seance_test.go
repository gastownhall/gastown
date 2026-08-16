package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/events"
)

// setupSeanceTestEnv creates a test environment with multiple accounts and sessions.
func setupSeanceTestEnv(t *testing.T) (townRoot, fakeHome string, cleanup func()) {
	t.Helper()

	// Create fake home directory
	fakeHome = t.TempDir()

	// Create town root
	townRoot = t.TempDir()

	// Create mayor directory structure
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("mkdir mayor: %v", err)
	}

	// Create two account config directories
	account1Dir := filepath.Join(fakeHome, "claude-config-account1")
	account2Dir := filepath.Join(fakeHome, "claude-config-account2")
	if err := os.MkdirAll(account1Dir, 0755); err != nil {
		t.Fatalf("mkdir account1: %v", err)
	}
	if err := os.MkdirAll(account2Dir, 0755); err != nil {
		t.Fatalf("mkdir account2: %v", err)
	}

	// Create accounts.json pointing to both accounts
	accountsCfg := &config.AccountsConfig{
		Version: 1,
		Default: "account1",
		Accounts: map[string]config.Account{
			"account1": {Email: "test1@example.com", ConfigDir: account1Dir},
			"account2": {Email: "test2@example.com", ConfigDir: account2Dir},
		},
	}
	accountsPath := filepath.Join(mayorDir, "accounts.json")
	if err := config.SaveAccountsConfig(accountsPath, accountsCfg); err != nil {
		t.Fatalf("save accounts.json: %v", err)
	}

	// Create ~/.claude symlink pointing to account1 (current account)
	claudeDir := filepath.Join(fakeHome, ".claude")
	if err := os.Symlink(account1Dir, claudeDir); err != nil {
		t.Fatalf("symlink .claude: %v", err)
	}

	// Set up HOME env var
	oldHome := os.Getenv("HOME")
	os.Setenv("HOME", fakeHome)

	cleanup = func() {
		os.Setenv("HOME", oldHome)
	}

	return townRoot, fakeHome, cleanup
}

// createTestSession creates a mock session file and index entry.
func createTestSession(t *testing.T, configDir, projectName, sessionID string) {
	t.Helper()

	projectDir := filepath.Join(configDir, "projects", projectName)
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	// Create session file
	sessionFile := filepath.Join(projectDir, sessionID+".jsonl")
	if err := os.WriteFile(sessionFile, []byte(`{"type":"test"}`), 0600); err != nil {
		t.Fatalf("write session file: %v", err)
	}

	// Create or update sessions-index.json
	indexPath := filepath.Join(projectDir, "sessions-index.json")
	var index sessionsIndex
	if data, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(data, &index)
	} else {
		index.Version = 1
	}

	// Add session entry
	entry := map[string]interface{}{
		"sessionId":    sessionID,
		"name":         "Test Session",
		"lastAccessed": "2026-01-22T00:00:00Z",
	}
	entryJSON, _ := json.Marshal(entry)
	index.Entries = append(index.Entries, entryJSON)

	indexData, _ := json.MarshalIndent(index, "", "  ")
	if err := os.WriteFile(indexPath, indexData, 0600); err != nil {
		t.Fatalf("write sessions-index.json: %v", err)
	}
}

func TestFindSessionLocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require elevated privileges on Windows")
	}

	t.Run("finds session in account1", func(t *testing.T) {
		townRoot, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		account1Dir := filepath.Join(fakeHome, "claude-config-account1")
		createTestSession(t, account1Dir, "test-project", "session-abc123")

		loc := findSessionLocation(townRoot, "session-abc123")
		if loc == nil {
			t.Fatal("expected to find session, got nil")
		}
		if loc.configDir != account1Dir {
			t.Errorf("expected configDir %s, got %s", account1Dir, loc.configDir)
		}
		if loc.projectDir != "test-project" {
			t.Errorf("expected projectDir test-project, got %s", loc.projectDir)
		}
	})

	t.Run("finds session in account2", func(t *testing.T) {
		townRoot, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		account2Dir := filepath.Join(fakeHome, "claude-config-account2")
		createTestSession(t, account2Dir, "other-project", "session-xyz789")

		loc := findSessionLocation(townRoot, "session-xyz789")
		if loc == nil {
			t.Fatal("expected to find session, got nil")
		}
		if loc.configDir != account2Dir {
			t.Errorf("expected configDir %s, got %s", account2Dir, loc.configDir)
		}
		if loc.projectDir != "other-project" {
			t.Errorf("expected projectDir other-project, got %s", loc.projectDir)
		}
	})

	t.Run("returns nil for nonexistent session", func(t *testing.T) {
		townRoot, _, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		loc := findSessionLocation(townRoot, "session-notfound")
		if loc != nil {
			t.Errorf("expected nil for nonexistent session, got %+v", loc)
		}
	})

	t.Run("returns nil for empty townRoot", func(t *testing.T) {
		loc := findSessionLocation("", "session-abc")
		if loc != nil {
			t.Errorf("expected nil for empty townRoot, got %+v", loc)
		}
	})
}

func TestSymlinkSessionToCurrentAccount(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require elevated privileges on Windows")
	}

	t.Run("creates symlink for session in other account", func(t *testing.T) {
		townRoot, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		// Create session in account2 (not the current account)
		account2Dir := filepath.Join(fakeHome, "claude-config-account2")
		createTestSession(t, account2Dir, "cross-project", "session-cross123")

		// Call symlinkSessionToCurrentAccount
		cleanupFn, err := symlinkSessionToCurrentAccount(townRoot, "session-cross123")
		if err != nil {
			t.Fatalf("symlinkSessionToCurrentAccount failed: %v", err)
		}
		if cleanupFn == nil {
			t.Fatal("expected cleanup function, got nil")
		}

		// Verify symlink was created in current account (account1)
		account1Dir := filepath.Join(fakeHome, "claude-config-account1")
		symlinkPath := filepath.Join(account1Dir, "projects", "cross-project", "session-cross123.jsonl")

		info, err := os.Lstat(symlinkPath)
		if err != nil {
			t.Fatalf("symlink not found: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Error("expected symlink, got regular file")
		}

		// Verify sessions-index.json was updated
		indexPath := filepath.Join(account1Dir, "projects", "cross-project", "sessions-index.json")
		data, err := os.ReadFile(indexPath)
		if err != nil {
			t.Fatalf("reading index: %v", err)
		}

		var index sessionsIndex
		if err := json.Unmarshal(data, &index); err != nil {
			t.Fatalf("parsing index: %v", err)
		}

		found := false
		for _, entry := range index.Entries {
			var e sessionsIndexEntry
			if json.Unmarshal(entry, &e) == nil && e.SessionID == "session-cross123" {
				found = true
				break
			}
		}
		if !found {
			t.Error("session not found in target index")
		}

		// Test cleanup
		cleanupFn()

		// Verify symlink was removed
		if _, err := os.Lstat(symlinkPath); !os.IsNotExist(err) {
			t.Error("symlink should have been removed after cleanup")
		}

		// Verify session was removed from index
		data, _ = os.ReadFile(indexPath)
		_ = json.Unmarshal(data, &index)
		for _, entry := range index.Entries {
			var e sessionsIndexEntry
			if json.Unmarshal(entry, &e) == nil && e.SessionID == "session-cross123" {
				t.Error("session should have been removed from index after cleanup")
			}
		}
	})

	t.Run("returns nil cleanup for session in current account same project", func(t *testing.T) {
		townRoot, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		// Create session in account1 (the current account) using cwd-based project dir
		cwd, _ := os.Getwd()
		cwdProjectDir := strings.ReplaceAll(cwd, "/", "-")
		account1Dir := filepath.Join(fakeHome, "claude-config-account1")
		createTestSession(t, account1Dir, cwdProjectDir, "session-local456")

		cleanupFn, err := symlinkSessionToCurrentAccount(townRoot, "session-local456")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cleanupFn != nil {
			t.Error("expected nil cleanup for session in current account and project dir")
		}
	})

	t.Run("symlinks session from different project dir in same account", func(t *testing.T) {
		townRoot, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		// Create session in account1 but with a different project dir
		account1Dir := filepath.Join(fakeHome, "claude-config-account1")
		createTestSession(t, account1Dir, "other-project", "session-crossproj789")

		cleanupFn, err := symlinkSessionToCurrentAccount(townRoot, "session-crossproj789")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cleanupFn == nil {
			t.Fatal("expected non-nil cleanup for cross-project-dir session")
		}
		defer cleanupFn()

		// Verify symlink was created in cwd-based project dir
		cwd, _ := os.Getwd()
		cwdProjectDir := strings.ReplaceAll(cwd, "/", "-")
		symlinkPath := filepath.Join(account1Dir, "projects", cwdProjectDir, "session-crossproj789.jsonl")
		if _, err := os.Lstat(symlinkPath); err != nil {
			t.Errorf("expected symlink at %s: %v", symlinkPath, err)
		}
	})

	t.Run("returns error for nonexistent session", func(t *testing.T) {
		townRoot, _, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		_, err := symlinkSessionToCurrentAccount(townRoot, "session-notfound")
		if err == nil {
			t.Error("expected error for nonexistent session")
		}
	})
}

func TestCleanupOrphanedSessionSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink tests require elevated privileges on Windows")
	}

	t.Run("removes orphaned symlinks", func(t *testing.T) {
		_, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		account1Dir := filepath.Join(fakeHome, "claude-config-account1")
		projectDir := filepath.Join(account1Dir, "projects", "orphan-project")
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("mkdir project: %v", err)
		}

		// Create an orphaned symlink (target doesn't exist)
		orphanSymlink := filepath.Join(projectDir, "orphan-session.jsonl")
		nonexistentTarget := filepath.Join(fakeHome, "nonexistent", "session.jsonl")
		if err := os.Symlink(nonexistentTarget, orphanSymlink); err != nil {
			t.Fatalf("create orphan symlink: %v", err)
		}

		// Create a sessions-index.json with the orphaned entry
		index := sessionsIndex{
			Version: 1,
			Entries: []json.RawMessage{
				json.RawMessage(`{"sessionId":"orphan-session","name":"Orphan"}`),
			},
		}
		indexPath := filepath.Join(projectDir, "sessions-index.json")
		data, _ := json.MarshalIndent(index, "", "  ")
		if err := os.WriteFile(indexPath, data, 0600); err != nil {
			t.Fatalf("write index: %v", err)
		}

		// Run cleanup
		cleanupOrphanedSessionSymlinks()

		// Verify orphan symlink was removed
		if _, err := os.Lstat(orphanSymlink); !os.IsNotExist(err) {
			t.Error("orphaned symlink should have been removed")
		}

		// Verify entry was removed from index
		data, _ = os.ReadFile(indexPath)
		var updatedIndex sessionsIndex
		_ = json.Unmarshal(data, &updatedIndex)
		if len(updatedIndex.Entries) != 0 {
			t.Errorf("expected 0 entries after cleanup, got %d", len(updatedIndex.Entries))
		}
	})

	t.Run("preserves valid symlinks", func(t *testing.T) {
		_, fakeHome, cleanup := setupSeanceTestEnv(t)
		defer cleanup()

		account1Dir := filepath.Join(fakeHome, "claude-config-account1")
		account2Dir := filepath.Join(fakeHome, "claude-config-account2")

		// Create a real session in account2
		createTestSession(t, account2Dir, "valid-project", "valid-session")

		// Create project dir in account1
		projectDir := filepath.Join(account1Dir, "projects", "valid-project")
		if err := os.MkdirAll(projectDir, 0755); err != nil {
			t.Fatalf("mkdir project: %v", err)
		}

		// Create a valid symlink pointing to the real session
		validSymlink := filepath.Join(projectDir, "valid-session.jsonl")
		realTarget := filepath.Join(account2Dir, "projects", "valid-project", "valid-session.jsonl")
		if err := os.Symlink(realTarget, validSymlink); err != nil {
			t.Fatalf("create valid symlink: %v", err)
		}

		// Create index with valid entry
		index := sessionsIndex{
			Version: 1,
			Entries: []json.RawMessage{
				json.RawMessage(`{"sessionId":"valid-session","name":"Valid"}`),
			},
		}
		indexPath := filepath.Join(projectDir, "sessions-index.json")
		data, _ := json.MarshalIndent(index, "", "  ")
		if err := os.WriteFile(indexPath, data, 0600); err != nil {
			t.Fatalf("write index: %v", err)
		}

		// Run cleanup
		cleanupOrphanedSessionSymlinks()

		// Verify valid symlink was preserved
		if _, err := os.Lstat(validSymlink); err != nil {
			t.Error("valid symlink should have been preserved")
		}

		// Verify entry was preserved in index
		data, _ = os.ReadFile(indexPath)
		var updatedIndex sessionsIndex
		_ = json.Unmarshal(data, &updatedIndex)
		if len(updatedIndex.Entries) != 1 {
			t.Errorf("expected 1 entry preserved, got %d", len(updatedIndex.Entries))
		}
	})
}

// writeTestEvents writes session_start events to a .events.jsonl file for testing.
func writeTestEvents(t *testing.T, townRoot string, sessionIDs []string) {
	t.Helper()
	eventsPath := filepath.Join(townRoot, events.EventsFile)
	var lines []string
	for i, id := range sessionIDs {
		event := fmt.Sprintf(
			`{"ts":"2026-01-22T%02d:00:00Z","type":"session_start","actor":"test/agent","payload":{"session_id":"%s","topic":"test"}}`,
			i, id)
		lines = append(lines, event)
	}
	if err := os.WriteFile(eventsPath, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatalf("write events file: %v", err)
	}
}

func TestResolveSessionPrefix(t *testing.T) {
	t.Run("resolves unique prefix", func(t *testing.T) {
		townRoot := t.TempDir()
		fullID := "46621448-3caa-4bbb-8ccc-123456789abc"
		writeTestEvents(t, townRoot, []string{fullID})

		resolved, err := resolveSessionPrefix(townRoot, "46621448-3c")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != fullID {
			t.Errorf("expected %s, got %s", fullID, resolved)
		}
	})

	t.Run("resolves full ID as prefix of itself", func(t *testing.T) {
		townRoot := t.TempDir()
		fullID := "46621448-3caa-4bbb-8ccc-123456789abc"
		writeTestEvents(t, townRoot, []string{fullID})

		resolved, err := resolveSessionPrefix(townRoot, fullID)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != fullID {
			t.Errorf("expected %s, got %s", fullID, resolved)
		}
	})

	t.Run("returns error for ambiguous prefix", func(t *testing.T) {
		townRoot := t.TempDir()
		id1 := "abcdef00-1111-2222-3333-444444444444"
		id2 := "abcdef00-5555-6666-7777-888888888888"
		writeTestEvents(t, townRoot, []string{id1, id2})

		_, err := resolveSessionPrefix(townRoot, "abcdef00")
		if err == nil {
			t.Fatal("expected error for ambiguous prefix")
		}
		if !strings.Contains(err.Error(), "ambiguous") {
			t.Errorf("expected ambiguous error, got: %v", err)
		}
	})

	t.Run("returns error for no match", func(t *testing.T) {
		townRoot := t.TempDir()
		writeTestEvents(t, townRoot, []string{"46621448-3caa-4bbb-8ccc-123456789abc"})

		_, err := resolveSessionPrefix(townRoot, "ffffffff")
		if err == nil {
			t.Fatal("expected error for no match")
		}
		if !strings.Contains(err.Error(), "no session found") {
			t.Errorf("expected 'no session found' error, got: %v", err)
		}
	})

	t.Run("returns error for empty events", func(t *testing.T) {
		townRoot := t.TempDir()

		_, err := resolveSessionPrefix(townRoot, "anything")
		if err == nil {
			t.Fatal("expected error for no events file")
		}
	})

	t.Run("deduplicates repeated session IDs", func(t *testing.T) {
		townRoot := t.TempDir()
		fullID := "46621448-3caa-4bbb-8ccc-123456789abc"
		// Same session ID appears multiple times (e.g., agent restarted)
		writeTestEvents(t, townRoot, []string{fullID, fullID, fullID})

		resolved, err := resolveSessionPrefix(townRoot, "46621448")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved != fullID {
			t.Errorf("expected %s, got %s", fullID, resolved)
		}
	})
}
