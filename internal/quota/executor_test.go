package quota

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
)

// mockExecutor implements TmuxExecutor for testing.
// All mutable fields are protected by mu for concurrent executeOne calls.
type mockExecutor struct {
	mu           sync.Mutex
	envSets      map[string]map[string]string // session -> key -> value
	paneIDs      map[string]string            // session -> pane ID (read-only after setup)
	remainOnExit map[string]bool              // pane -> value
	killed       []string                     // panes that had processes killed
	cleared      []string                     // panes that had history cleared
	respawned    map[string]string            // pane -> command

	// Error injection (read-only after setup)
	setEnvErr    map[string]error // session -> error
	getPaneIDErr map[string]error // session -> error
	respawnErr   map[string]error // pane -> error
}

func newMockExecutor() *mockExecutor {
	return &mockExecutor{
		envSets:      make(map[string]map[string]string),
		paneIDs:      make(map[string]string),
		remainOnExit: make(map[string]bool),
		respawned:    make(map[string]string),
		setEnvErr:    make(map[string]error),
		getPaneIDErr: make(map[string]error),
		respawnErr:   make(map[string]error),
	}
}

func (m *mockExecutor) SetEnvironment(session, key, value string) error {
	if err, ok := m.setEnvErr[session]; ok {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.envSets[session] == nil {
		m.envSets[session] = make(map[string]string)
	}
	m.envSets[session][key] = value
	return nil
}

func (m *mockExecutor) GetPaneID(session string) (string, error) {
	if err, ok := m.getPaneIDErr[session]; ok {
		return "", err
	}
	id, ok := m.paneIDs[session]
	if !ok {
		return "", fmt.Errorf("session %s not found", session)
	}
	return id, nil
}

func (m *mockExecutor) SetRemainOnExit(pane string, on bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remainOnExit[pane] = on
	return nil
}

func (m *mockExecutor) KillPaneProcesses(pane string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.killed = append(m.killed, pane)
	return nil
}

func (m *mockExecutor) ClearHistory(pane string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleared = append(m.cleared, pane)
	return nil
}

func (m *mockExecutor) RespawnPane(pane, command string) error {
	if err, ok := m.respawnErr[pane]; ok {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.respawned[pane] = command
	return nil
}

func (m *mockExecutor) AcceptStartupDialogs(_ string) error       { return nil }
func (m *mockExecutor) AcceptWorkspaceTrustDialog(_ string) error { return nil }
func (m *mockExecutor) AcceptBypassPermissionsWarning(_ string) error {
	return nil
}

// mockLogger captures warnings for assertion.
type mockLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (m *mockLogger) Warn(format string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.warnings = append(m.warnings, fmt.Sprintf(format, args...))
}

func TestExecute_Success(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Pre-seed state
	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work":     {Status: config.QuotaStatusLimited, LastUsed: "2025-01-01T01:00:00Z"},
			"personal": {Status: config.QuotaStatusAvailable, LastUsed: "2025-01-01T00:00:00Z"},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{
		envVars: map[string]map[string]string{
			"gt-crew-bear": {"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work"},
		},
	}

	exec := newMockExecutor()
	exec.paneIDs["gt-crew-bear"] = "%0"

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work":     {ConfigDir: "/home/user/.claude-accounts/work"},
			"personal": {ConfigDir: "/home/user/.claude-accounts/personal"},
		},
	}

	log := &mockLogger{}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude --resume", nil },
		log, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{
			"gt-crew-bear": "personal",
		},
	}

	results := rotator.Execute(plan, []string{"gt-crew-bear"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Rotated {
		t.Errorf("expected Rotated=true, got false; error=%s", r.Error)
	}
	if r.OldAccount != "work" {
		t.Errorf("expected OldAccount=work, got %q", r.OldAccount)
	}
	if r.NewAccount != "personal" {
		t.Errorf("expected NewAccount=personal, got %q", r.NewAccount)
	}
	if r.Error != "" {
		t.Errorf("unexpected error: %s", r.Error)
	}

	// Verify tmux operations occurred
	if env, ok := exec.envSets["gt-crew-bear"]; !ok || env["CLAUDE_CONFIG_DIR"] != "/home/user/.claude-accounts/personal" {
		t.Errorf("expected CLAUDE_CONFIG_DIR set to personal config dir")
	}
	if _, ok := exec.respawned["%0"]; !ok {
		t.Error("expected pane %0 to be respawned")
	}

	// Verify state was persisted with updated LastUsed
	loaded, err := mgr.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Accounts["personal"].LastUsed == "2025-01-01T00:00:00Z" {
		t.Error("expected personal LastUsed to be updated")
	}
}

func TestExecute_MultiSession(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"alpha": {Status: config.QuotaStatusAvailable},
			"beta":  {Status: config.QuotaStatusAvailable},
			"gamma": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{
		envVars: map[string]map[string]string{
			"hq-mayor":     {"CLAUDE_CONFIG_DIR": "/home/.claude/alpha"},
			"gt-crew-bear": {"CLAUDE_CONFIG_DIR": "/home/.claude/alpha"},
		},
	}

	exec := newMockExecutor()
	exec.paneIDs["hq-mayor"] = "%0"
	exec.paneIDs["gt-crew-bear"] = "%1"

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"alpha": {ConfigDir: "/home/.claude/alpha"},
			"beta":  {ConfigDir: "/home/.claude/beta"},
			"gamma": {ConfigDir: "/home/.claude/gamma"},
		},
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{
			"gt-crew-bear": "beta",
			"hq-mayor":     "gamma",
		},
	}

	results := rotator.Execute(plan, []string{"gt-crew-bear", "hq-mayor"})

	rotated := 0
	for _, r := range results {
		if r.Rotated {
			rotated++
		}
	}
	if rotated != 2 {
		t.Errorf("expected 2 rotated, got %d", rotated)
	}
}

func TestExecute_AccountNotFound(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version:  config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{
		envVars: map[string]map[string]string{},
	}

	exec := newMockExecutor()
	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{}, // empty — account won't be found
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{
			"gt-test": "nonexistent",
		},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Rotated {
		t.Error("expected Rotated=false for missing account")
	}
	if !strings.Contains(results[0].Error, "not found in config") {
		t.Errorf("expected 'not found' error, got %q", results[0].Error)
	}
}

func TestExecute_SetEnvironmentFailure(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{envVars: map[string]map[string]string{}}

	exec := newMockExecutor()
	exec.paneIDs["gt-test"] = "%0"
	exec.setEnvErr["gt-test"] = fmt.Errorf("tmux set-env: session not found")

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/.claude/work"},
		},
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Rotated {
		t.Error("expected Rotated=false on SetEnvironment failure")
	}
	if !strings.Contains(results[0].Error, "setting CLAUDE_CONFIG_DIR") {
		t.Errorf("expected SetEnvironment error, got %q", results[0].Error)
	}
}

func TestExecute_RespawnFailure(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{envVars: map[string]map[string]string{}}

	exec := newMockExecutor()
	exec.paneIDs["gt-test"] = "%0"
	exec.respawnErr["%0"] = fmt.Errorf("respawn-pane failed")

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/.claude/work"},
		},
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Rotated {
		t.Error("expected Rotated=false on RespawnPane failure")
	}
	if !strings.Contains(results[0].Error, "respawning pane") {
		t.Errorf("expected respawn error, got %q", results[0].Error)
	}
}

func TestExecute_RestartCommandFailure(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{envVars: map[string]map[string]string{}}

	exec := newMockExecutor()
	exec.paneIDs["gt-test"] = "%0"

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/.claude/work"},
		},
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "", fmt.Errorf("cannot detect town root") },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Rotated {
		t.Error("expected Rotated=false on restart command failure")
	}
	if !strings.Contains(results[0].Error, "building restart command") {
		t.Errorf("expected restart command error, got %q", results[0].Error)
	}
}

func TestExecute_NonCriticalWarnings(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{envVars: map[string]map[string]string{}}

	// Create an executor where non-critical ops fail
	exec := &failingNonCriticalExecutor{
		paneIDs: map[string]string{"gt-test": "%0"},
	}

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/.claude/work"},
		},
	}

	log := &mockLogger{}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		log, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	// Should still succeed despite non-critical failures
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Rotated {
		t.Errorf("expected Rotated=true despite non-critical failures; error=%s", results[0].Error)
	}

	// Should have logged warnings for the non-critical failures
	// (SetRemainOnExit, KillPaneProcesses, ClearHistory, AcceptStartupDialogs)
	if len(log.warnings) != 4 {
		t.Errorf("expected 4 warnings, got %d: %v", len(log.warnings), log.warnings)
	}
}

// failingNonCriticalExecutor fails on SetRemainOnExit, KillPaneProcesses,
// and ClearHistory but succeeds on critical operations.
type failingNonCriticalExecutor struct {
	paneIDs   map[string]string
	respawned map[string]string
	envSets   map[string]map[string]string
}

func (f *failingNonCriticalExecutor) SetEnvironment(session, key, value string) error {
	if f.envSets == nil {
		f.envSets = make(map[string]map[string]string)
	}
	if f.envSets[session] == nil {
		f.envSets[session] = make(map[string]string)
	}
	f.envSets[session][key] = value
	return nil
}

func (f *failingNonCriticalExecutor) GetPaneID(session string) (string, error) {
	id, ok := f.paneIDs[session]
	if !ok {
		return "", fmt.Errorf("session %s not found", session)
	}
	return id, nil
}

func (f *failingNonCriticalExecutor) SetRemainOnExit(_ string, _ bool) error {
	return fmt.Errorf("remain-on-exit failed")
}

func (f *failingNonCriticalExecutor) KillPaneProcesses(_ string) error {
	return fmt.Errorf("kill processes failed")
}

func (f *failingNonCriticalExecutor) ClearHistory(_ string) error {
	return fmt.Errorf("clear history failed")
}

func (f *failingNonCriticalExecutor) RespawnPane(pane, command string) error {
	if f.respawned == nil {
		f.respawned = make(map[string]string)
	}
	f.respawned[pane] = command
	return nil
}

func (f *failingNonCriticalExecutor) AcceptStartupDialogs(_ string) error {
	return fmt.Errorf("accept startup dialogs failed")
}
func (f *failingNonCriticalExecutor) AcceptWorkspaceTrustDialog(_ string) error { return nil }
func (f *failingNonCriticalExecutor) AcceptBypassPermissionsWarning(_ string) error {
	return fmt.Errorf("accept bypass permissions failed")
}

func TestExecute_TildeExpansion(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{
		envVars: map[string]map[string]string{
			"gt-test": {"CLAUDE_CONFIG_DIR": "/home/user/.claude/work"},
		},
	}

	exec := newMockExecutor()
	exec.paneIDs["gt-test"] = "%0"

	// Account uses tilde path
	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "~/.claude/work"},
		},
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].Rotated {
		t.Errorf("expected Rotated=true; error=%s", results[0].Error)
	}

	// Verify the CLAUDE_CONFIG_DIR was set with expanded path (not tilde)
	env := exec.envSets["gt-test"]
	if configDir, ok := env["CLAUDE_CONFIG_DIR"]; ok {
		if strings.HasPrefix(configDir, "~") {
			t.Errorf("expected expanded path, got tilde path: %s", configDir)
		}
	}
}

func TestExecute_EmptyPlan(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version:  config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	rotator := NewRotator(&mockTmux{}, newMockExecutor(), mgr,
		&config.AccountsConfig{Accounts: map[string]config.Account{}},
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{},
	}

	results := rotator.Execute(plan, []string{})

	if len(results) != 0 {
		t.Errorf("expected 0 results for empty plan, got %d", len(results))
	}
	for _, r := range results {
		if r.Error != "" {
			t.Errorf("unexpected error in empty plan: %s", r.Error)
		}
	}
}

func TestExecute_SaveUnlockedFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only directories are not reliable on Windows")
	}
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{envVars: map[string]map[string]string{}}
	exec := newMockExecutor()
	exec.paneIDs["gt-test"] = "%0"

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work": {ConfigDir: "/home/.claude/work"},
		},
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	// Pre-create the runtime directory (for lock file) then make the
	// mayor directory read-only so SaveUnlocked's temp file creation fails.
	runtimeDir := filepath.Join(townRoot, constants.DirMayor, constants.DirRuntime)
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatal(err)
	}
	mayorDir := filepath.Join(townRoot, constants.DirMayor)
	if err := os.Chmod(mayorDir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(mayorDir, 0755) })

	results := rotator.Execute(plan, []string{"gt-test"})

	// The pane was respawned (mutation succeeded) but SaveUnlocked failed.
	// Expect: the successful rotation result + a lifecycle error result.
	hasLifecycleErr := false
	hasRotated := false
	for _, r := range results {
		if r.Session == "" && r.Error != "" {
			hasLifecycleErr = true
		}
		if r.Rotated {
			hasRotated = true
		}
	}
	if !hasRotated {
		t.Error("expected successful rotation result before save failure")
	}
	if !hasLifecycleErr {
		t.Error("expected lifecycle error when SaveUnlocked fails")
	}
}

func TestExecute_CorruptStateFile(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	// Write corrupt JSON to state file
	statePath := constants.MayorQuotaPath(townRoot)
	if err := os.WriteFile(statePath, []byte("{invalid"), 0644); err != nil {
		t.Fatal(err)
	}

	rotator := NewRotator(&mockTmux{}, newMockExecutor(), mgr,
		&config.AccountsConfig{Accounts: map[string]config.Account{}},
		func(s string) (string, error) { return "claude", nil },
		&mockLogger{}, "", "", nil,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "work"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	// Should get a single lifecycle error (Load failed inside WithLock)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Session != "" {
		t.Errorf("expected empty session for lifecycle error, got %q", results[0].Session)
	}
	if !strings.Contains(results[0].Error, "rotation lifecycle") {
		t.Errorf("expected lifecycle error, got %q", results[0].Error)
	}
}

func TestExecute_WithResume(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work":     {Status: config.QuotaStatusLimited, LastUsed: "2025-01-01T01:00:00Z"},
			"personal": {Status: config.QuotaStatusAvailable, LastUsed: "2025-01-01T00:00:00Z"},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{
		envVars: map[string]map[string]string{
			"gt-crew-bear": {
				"CLAUDE_CONFIG_DIR": "/home/user/.claude-accounts/work",
				"CLAUDE_SESSION_ID": "test-session-abc123",
			},
		},
	}

	exec := newMockExecutor()
	exec.paneIDs["gt-crew-bear"] = "%0"

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work":     {ConfigDir: "/home/user/.claude-accounts/work"},
			"personal": {ConfigDir: "/home/user/.claude-accounts/personal"},
		},
	}

	log := &mockLogger{}

	// Session linker that succeeds (no-op symlink for testing)
	linkerCalled := false
	linker := func(townRoot, sessionID, targetConfigDir string) (func(), error) {
		linkerCalled = true
		if sessionID != "test-session-abc123" {
			t.Errorf("linker got sessionID=%q, want test-session-abc123", sessionID)
		}
		if targetConfigDir != "/home/user/.claude-accounts/personal" {
			t.Errorf("linker got targetConfigDir=%q, want /home/user/.claude-accounts/personal", targetConfigDir)
		}
		return nil, nil
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude --fallback", nil },
		log, townRoot, "claude", linker,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{
			"gt-crew-bear": "personal",
		},
	}

	results := rotator.Execute(plan, []string{"gt-crew-bear"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Rotated {
		t.Errorf("expected Rotated=true, got false; error=%s", r.Error)
	}
	if r.ResumedSession != "test-session-abc123" {
		t.Errorf("expected ResumedSession=test-session-abc123, got %q", r.ResumedSession)
	}
	if !linkerCalled {
		t.Error("expected session linker to be called")
	}

	// Verify the respawn command uses --resume instead of fallback
	cmd := exec.respawned["%0"]
	if !strings.Contains(cmd, "--resume") {
		t.Errorf("expected resume command, got %q", cmd)
	}
	if !strings.Contains(cmd, "test-session-abc123") {
		t.Errorf("expected session ID in command, got %q", cmd)
	}
	if strings.Contains(cmd, "--fallback") {
		t.Errorf("expected resume to replace fallback command, got %q", cmd)
	}
}

func TestExecute_ResumeSymlinkFails_FallsBack(t *testing.T) {
	setupTestRegistry(t)
	townRoot := setupTestTown(t)
	mgr := NewManager(townRoot)

	state := &config.QuotaState{
		Version: config.CurrentQuotaVersion,
		Accounts: map[string]config.AccountQuotaState{
			"work":     {Status: config.QuotaStatusLimited},
			"personal": {Status: config.QuotaStatusAvailable},
		},
	}
	if err := mgr.Save(state); err != nil {
		t.Fatal(err)
	}

	tmuxClient := &mockTmux{
		envVars: map[string]map[string]string{
			"gt-test": {
				"CLAUDE_CONFIG_DIR": "/home/.claude/work",
				"CLAUDE_SESSION_ID": "session-xyz",
			},
		},
	}

	exec := newMockExecutor()
	exec.paneIDs["gt-test"] = "%0"

	accounts := &config.AccountsConfig{
		Accounts: map[string]config.Account{
			"work":     {ConfigDir: "/home/.claude/work"},
			"personal": {ConfigDir: "/home/.claude/personal"},
		},
	}

	log := &mockLogger{}

	// Session linker that fails
	linker := func(townRoot, sessionID, targetConfigDir string) (func(), error) {
		return nil, fmt.Errorf("session not found in any account")
	}

	rotator := NewRotator(tmuxClient, exec, mgr, accounts,
		func(s string) (string, error) { return "claude --fresh-start", nil },
		log, townRoot, "claude", linker,
	)

	plan := &RotatePlan{
		Assignments: map[string]string{"gt-test": "personal"},
	}

	results := rotator.Execute(plan, []string{"gt-test"})

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	if !r.Rotated {
		t.Errorf("expected Rotated=true (fallback should succeed); error=%s", r.Error)
	}
	if r.ResumedSession != "" {
		t.Errorf("expected empty ResumedSession on fallback, got %q", r.ResumedSession)
	}

	// Verify the respawn command uses the fallback (fresh start)
	cmd := exec.respawned["%0"]
	if !strings.Contains(cmd, "--fresh-start") {
		t.Errorf("expected fallback command, got %q", cmd)
	}

	// Should have logged a warning about the symlink failure
	found := false
	for _, w := range log.warnings {
		if strings.Contains(w, "symlink session for resume") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning about symlink failure, got %v", log.warnings)
	}
}
