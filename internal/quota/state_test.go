package quota

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
)

// setupTestTown creates a temporary town root with mayor directory.
func setupTestTown(t *testing.T) string {
	t.Helper()
	townRoot := t.TempDir()
	mayorDir := filepath.Join(townRoot, constants.DirMayor)
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatal(err)
	}
	return townRoot
}

func TestNewManager(t *testing.T) {
	mgr := NewManager("/tmp/test-town")
	if mgr.townRoot != "/tmp/test-town" {
		t.Errorf("expected townRoot /tmp/test-town, got %s", mgr.townRoot)
	}
}

func TestLoadEmpty(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if state.Version != config.CurrentQuotaVersion {
		t.Errorf("expected version %d, got %d", config.CurrentQuotaVersion, state.Version)
	}
	if len(state.Accounts) != 0 {
		t.Errorf("expected empty accounts, got %d", len(state.Accounts))
	}
}

func TestSaveAndLoad(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {
				Status:   config.QuotaStatusAvailable,
				LastUsed: "2025-01-01T00:00:00Z",
			},
			"personal": {
				Status:    config.QuotaStatusLimited,
				LimitedAt: "2025-01-01T12:00:00Z",
				ResetsAt:  "2025-01-01T13:00:00Z",
			},
		},
	}

	if err := mgr.Save(state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() after Save() error: %v", err)
	}

	if loaded.Version != config.CurrentQuotaVersion {
		t.Errorf("expected version %d, got %d", config.CurrentQuotaVersion, loaded.Version)
	}
	if len(loaded.Accounts) != 2 {
		t.Errorf("expected 2 accounts, got %d", len(loaded.Accounts))
	}
	if loaded.Accounts["work"].Status != config.QuotaStatusAvailable {
		t.Errorf("expected work status available, got %s", loaded.Accounts["work"].Status)
	}
	if loaded.Accounts["personal"].Status != config.QuotaStatusLimited {
		t.Errorf("expected personal status limited, got %s", loaded.Accounts["personal"].Status)
	}
}

func TestMarkLimited(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Save initial state
	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable, LastUsed: "2025-01-01T00:00:00Z"},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Mark as limited
	if err := mgr.MarkLimited("work", "7:00 PM PST"); err != nil {
		t.Fatalf("MarkLimited() error: %v", err)
	}

	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	acct := loaded.Accounts["work"]
	if acct.Status != config.QuotaStatusLimited {
		t.Errorf("expected status limited, got %s", acct.Status)
	}
	if acct.LimitedAt == "" {
		t.Error("expected LimitedAt to be set")
	}
	if acct.ResetsAt != "7:00 PM PST" {
		t.Errorf("expected ResetsAt '7:00 PM PST', got %q", acct.ResetsAt)
	}
	// LastUsed should be preserved
	if acct.LastUsed != "2025-01-01T00:00:00Z" {
		t.Errorf("expected LastUsed preserved, got %q", acct.LastUsed)
	}
}

func TestMarkAvailable(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Save initial state with limited account
	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {
				Status:    config.QuotaStatusLimited,
				LimitedAt: "2025-01-01T12:00:00Z",
				LastUsed:  "2025-01-01T11:00:00Z",
			},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if err := mgr.MarkAvailable("work"); err != nil {
		t.Fatalf("MarkAvailable() error: %v", err)
	}

	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	acct := loaded.Accounts["work"]
	if acct.Status != config.QuotaStatusAvailable {
		t.Errorf("expected status available, got %s", acct.Status)
	}
	if acct.LimitedAt != "" {
		t.Errorf("expected LimitedAt cleared, got %q", acct.LimitedAt)
	}
	// LastUsed should be preserved
	if acct.LastUsed != "2025-01-01T11:00:00Z" {
		t.Errorf("expected LastUsed preserved, got %q", acct.LastUsed)
	}
}

func TestAvailableAccounts(t *testing.T) {
	mgr := NewManager("/tmp/unused")
	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"a": {Status: config.QuotaStatusAvailable, LastUsed: "2025-01-01T03:00:00Z"},
			"b": {Status: config.QuotaStatusLimited},
			"c": {Status: config.QuotaStatusAvailable, LastUsed: "2025-01-01T01:00:00Z"},
			"d": {Status: "", LastUsed: "2025-01-01T02:00:00Z"}, // empty status = available
		},
	}

	available := mgr.AvailableAccounts(state)
	if len(available) != 3 {
		t.Fatalf("expected 3 available, got %d: %v", len(available), available)
	}
	// Should be sorted by LastUsed ascending
	if available[0] != "c" {
		t.Errorf("expected first available 'c' (oldest), got %q", available[0])
	}
	if available[1] != "d" {
		t.Errorf("expected second available 'd', got %q", available[1])
	}
	if available[2] != "a" {
		t.Errorf("expected third available 'a' (newest), got %q", available[2])
	}
}

func TestSortByLastUsed_EmptyStrings(t *testing.T) {
	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"a": {LastUsed: "2025-01-01T03:00:00Z"},
			"b": {LastUsed: ""},
			"c": {LastUsed: "2025-01-01T01:00:00Z"},
		},
	}
	handles := []string{"a", "b", "c"}
	sortByLastUsed(handles, state)

	// Empty LastUsed sorts first (least recently used = highest priority).
	if handles[0] != "b" {
		t.Errorf("expected 'b' (empty LastUsed) first, got %q", handles[0])
	}
	if handles[1] != "c" {
		t.Errorf("expected 'c' second, got %q", handles[1])
	}
	if handles[2] != "a" {
		t.Errorf("expected 'a' (most recent) last, got %q", handles[2])
	}
}

func TestLimitedAccounts(t *testing.T) {
	mgr := NewManager("/tmp/unused")
	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"a": {Status: config.QuotaStatusAvailable},
			"b": {Status: config.QuotaStatusLimited},
			"c": {Status: config.QuotaStatusLimited},
			"d": {Status: config.QuotaStatusCooldown},
		},
	}

	limited := mgr.LimitedAccounts(state)
	if len(limited) != 2 {
		t.Fatalf("expected 2 limited, got %d: %v", len(limited), limited)
	}
}

func TestEnsureAccountsTracked(t *testing.T) {
	mgr := NewManager("/tmp/unused")
	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"existing": {Status: config.QuotaStatusLimited, LimitedAt: "2025-01-01T00:00:00Z"},
		},
	}

	accounts := map[string]config.Account{
		"existing": {Email: "a@test.com"},
		"new":      {Email: "b@test.com"},
	}

	mgr.EnsureAccountsTracked(state, accounts)

	if len(state.Accounts) != 2 {
		t.Errorf("expected 2 accounts, got %d", len(state.Accounts))
	}
	// Existing should be unchanged
	if state.Accounts["existing"].Status != config.QuotaStatusLimited {
		t.Errorf("existing account status changed: %s", state.Accounts["existing"].Status)
	}
	// New should default to available
	if state.Accounts["new"].Status != config.QuotaStatusAvailable {
		t.Errorf("new account status: %s, expected available", state.Accounts["new"].Status)
	}
}

func TestLoadCorruptFile(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Write corrupt JSON
	path := constants.MayorQuotaPath(townRoot)
	if err := os.WriteFile(path, []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := mgr.Load()
	if err == nil {
		t.Error("expected error loading corrupt file")
	}
}

func TestWithLock(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Save initial state
	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	// Use WithLock to do load + modify + save atomically
	err := mgr.WithLock(func() error {
		s, err := mgr.Load()
		if err != nil {
			return err
		}
		s.Accounts["work"] = config.AccountQuotaState{
			Status:   config.QuotaStatusLimited,
			LastUsed: "2025-01-01T00:00:00Z",
		}
		return mgr.SaveUnlocked(s)
	})
	if err != nil {
		t.Fatalf("WithLock() error: %v", err)
	}

	// Verify the change persisted
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Accounts["work"].Status != config.QuotaStatusLimited {
		t.Errorf("expected status limited, got %s", loaded.Accounts["work"].Status)
	}
}

func TestWithLock_PropagatesError(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	sentinel := fmt.Errorf("test error")
	err := mgr.WithLock(func() error {
		return sentinel
	})
	if err != sentinel {
		t.Errorf("expected sentinel error, got %v", err)
	}
}

func TestSaveUnlocked(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Use WithLock + SaveUnlocked
	err := mgr.WithLock(func() error {
		state := &config.QuotaState{
			Accounts: map[string]config.AccountQuotaState{
				"test": {Status: config.QuotaStatusAvailable},
			},
		}
		return mgr.SaveUnlocked(state)
	})
	if err != nil {
		t.Fatalf("WithLock/SaveUnlocked error: %v", err)
	}

	// Verify
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if loaded.Version != config.CurrentQuotaVersion {
		t.Errorf("expected version %d, got %d", config.CurrentQuotaVersion, loaded.Version)
	}
	if loaded.Accounts["test"].Status != config.QuotaStatusAvailable {
		t.Errorf("expected status available, got %s", loaded.Accounts["test"].Status)
	}
}

func TestSaveCreatesDirectory(t *testing.T) {
	townRoot := t.TempDir()
	// Don't create mayor dir — Save should handle it via EnsureDirAndWriteJSON
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version:  config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{},
	}

	if err := mgr.Save(state); err != nil {
		t.Fatalf("Save() should create directories: %v", err)
	}

	// Verify file exists
	data, err := os.ReadFile(constants.MayorQuotaPath(townRoot))
	if err != nil {
		t.Fatalf("reading saved file: %v", err)
	}
	var loaded config.QuotaState
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("parsing saved file: %v", err)
	}
}

// --- Swap tracking tests ---

func TestRecordSwap(t *testing.T) {
	state := &config.QuotaState{
		Accounts: make(map[string]config.AccountQuotaState),
	}

	// Record a swap
	RecordSwap(state, "/home/user/.claude-accounts/clh", "dev1")

	if len(state.ActiveSwaps) != 1 {
		t.Fatalf("expected 1 active swap, got %d", len(state.ActiveSwaps))
	}
	if state.ActiveSwaps["/home/user/.claude-accounts/clh"] != "dev1" {
		t.Errorf("expected dev1, got %s", state.ActiveSwaps["/home/user/.claude-accounts/clh"])
	}

	// Record another swap (overwrites)
	RecordSwap(state, "/home/user/.claude-accounts/clh", "dev2")
	if state.ActiveSwaps["/home/user/.claude-accounts/clh"] != "dev2" {
		t.Errorf("expected dev2 after overwrite, got %s", state.ActiveSwaps["/home/user/.claude-accounts/clh"])
	}
}

func TestClearSwap(t *testing.T) {
	state := &config.QuotaState{
		Accounts: make(map[string]config.AccountQuotaState),
		ActiveSwaps: map[string]string{
			"/home/user/.claude-accounts/clh": "dev1",
			"/home/user/.claude-accounts/xyz": "dev2",
		},
	}

	ClearSwap(state, "/home/user/.claude-accounts/clh")

	if len(state.ActiveSwaps) != 1 {
		t.Fatalf("expected 1 active swap after clear, got %d", len(state.ActiveSwaps))
	}
	if _, ok := state.ActiveSwaps["/home/user/.claude-accounts/clh"]; ok {
		t.Error("expected clh swap to be cleared")
	}
}

func TestResolveSwapSourceDirs(t *testing.T) {
	activeSwaps := map[string]string{
		"/home/user/.claude-accounts/clh": "dev1",
		"/home/user/.claude-accounts/xyz": "dev2",
		"/home/user/.claude-accounts/abc": "unknown", // not in accounts
	}
	accounts := map[string]config.Account{
		"dev1": {ConfigDir: "/home/user/.claude-accounts/dev1"},
		"dev2": {ConfigDir: "/home/user/.claude-accounts/dev2"},
	}

	resolved := ResolveSwapSourceDirs(activeSwaps, accounts)

	if len(resolved) != 2 {
		t.Fatalf("expected 2 resolved, got %d", len(resolved))
	}
	if resolved["/home/user/.claude-accounts/clh"] != "/home/user/.claude-accounts/dev1" {
		t.Errorf("clh should resolve to dev1's dir, got %s", resolved["/home/user/.claude-accounts/clh"])
	}
	if resolved["/home/user/.claude-accounts/xyz"] != "/home/user/.claude-accounts/dev2" {
		t.Errorf("xyz should resolve to dev2's dir, got %s", resolved["/home/user/.claude-accounts/xyz"])
	}
}

func TestActiveSwaps_PersistsThroughSaveLoad(t *testing.T) {
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"dev1": {Status: config.QuotaStatusAvailable},
		},
		ActiveSwaps: map[string]string{
			"/home/user/.claude-accounts/clh": "dev1",
		},
	}

	if err := mgr.Save(state); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	loaded, err := mgr.Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(loaded.ActiveSwaps) != 1 {
		t.Fatalf("expected 1 active swap after load, got %d", len(loaded.ActiveSwaps))
	}
	if loaded.ActiveSwaps["/home/user/.claude-accounts/clh"] != "dev1" {
		t.Errorf("expected dev1, got %s", loaded.ActiveSwaps["/home/user/.claude-accounts/clh"])
	}
}

// --- ParseResetTime tests ---

func TestParseResetTime_SimpleAMPM(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	ref := time.Date(2026, 2, 18, 10, 0, 0, 0, la)

	tests := []struct {
		input    string
		wantHour int
		wantMin  int
	}{
		{"7pm (America/Los_Angeles)", 19, 0},
		{"11am (America/Los_Angeles)", 11, 0},
		{"2pm (America/Los_Angeles)", 14, 0},
		{"12pm (America/Los_Angeles)", 12, 0},
		{"12am (America/Los_Angeles)", 0, 0},
		{"3:30pm (America/Los_Angeles)", 15, 30},
		{"7:00pm (America/Los_Angeles)", 19, 0},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseResetTime(tt.input, ref)
			if err != nil {
				t.Fatalf("ParseResetTime(%q) error: %v", tt.input, err)
			}
			gotInLA := got.In(la)
			if gotInLA.Hour() != tt.wantHour || gotInLA.Minute() != tt.wantMin {
				t.Errorf("ParseResetTime(%q) = %v, want %02d:%02d",
					tt.input, gotInLA, tt.wantHour, tt.wantMin)
			}
		})
	}
}

func TestParseResetTime_NoTimezone(t *testing.T) {
	// Without timezone, should use local time
	ref := time.Now()
	got, err := ParseResetTime("7pm", ref)
	if err != nil {
		t.Fatalf("ParseResetTime(\"7pm\") error: %v", err)
	}
	if got.Hour() != 19 {
		t.Errorf("expected hour 19, got %d", got.Hour())
	}
}

func TestParseResetTime_InvalidInput(t *testing.T) {
	ref := time.Now()
	_, err := ParseResetTime("garbage", ref)
	if err == nil {
		t.Error("expected error for invalid input")
	}
}

// --- ClearExpired tests ---

func TestClearExpired_ClearsPassedResetTime(t *testing.T) {
	la, _ := time.LoadLocation("America/Los_Angeles")
	// Set reference time to 3pm LA time
	now := time.Date(2026, 2, 18, 15, 0, 0, 0, la)

	mgr := NewManager("/tmp/unused")
	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"expired": {
				Status:   config.QuotaStatusLimited,
				ResetsAt: "11am (America/Los_Angeles)", // 11am < 3pm = expired
				LastUsed: "2026-02-18T10:00:00Z",
			},
			"still_limited": {
				Status:   config.QuotaStatusLimited,
				ResetsAt: "7pm (America/Los_Angeles)", // 7pm > 3pm = still limited
				LastUsed: "2026-02-18T10:00:00Z",
			},
			"available": {
				Status: config.QuotaStatusAvailable,
			},
		},
	}

	// Override now for testing by calling ClearExpiredAt
	cleared := clearExpiredAt(mgr, state, now)

	if cleared != 1 {
		t.Errorf("expected 1 cleared, got %d", cleared)
	}
	if state.Accounts["expired"].Status != config.QuotaStatusAvailable {
		t.Errorf("expected expired account to be available, got %s", state.Accounts["expired"].Status)
	}
	if state.Accounts["expired"].LastUsed != "2026-02-18T10:00:00Z" {
		t.Errorf("expected LastUsed preserved, got %q", state.Accounts["expired"].LastUsed)
	}
	if state.Accounts["still_limited"].Status != config.QuotaStatusLimited {
		t.Errorf("expected still_limited to remain limited, got %s", state.Accounts["still_limited"].Status)
	}
	if state.Accounts["available"].Status != config.QuotaStatusAvailable {
		t.Errorf("expected available to remain available, got %s", state.Accounts["available"].Status)
	}
}

func TestClearExpired_NoResetsAt(t *testing.T) {
	mgr := NewManager("/tmp/unused")
	state := &config.QuotaState{
		Accounts: map[string]config.AccountQuotaState{
			"no_reset": {
				Status: config.QuotaStatusLimited,
				// No ResetsAt — should not be cleared
			},
		},
	}

	cleared := mgr.ClearExpired(state)
	if cleared != 0 {
		t.Errorf("expected 0 cleared, got %d", cleared)
	}
	if state.Accounts["no_reset"].Status != config.QuotaStatusLimited {
		t.Errorf("expected no_reset to remain limited")
	}
}
