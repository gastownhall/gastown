package daemon

import (
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/constants"
	"github.com/steveyegge/gastown/internal/session"
)

// testDaemon creates a minimal Daemon for testing.
func testDaemon() *Daemon {
	return &Daemon{
		config: &Config{TownRoot: "/tmp/test"},
		logger: log.New(io.Discard, "", 0), // silent logger for tests
	}
}

// testDaemonWithTown creates a Daemon with a proper town setup for testing.
// Returns the daemon and a cleanup function.
func testDaemonWithTown(t *testing.T, townName string) (*Daemon, func()) {
	t.Helper()
	townRoot := t.TempDir()

	// Create mayor directory and town.json
	mayorDir := filepath.Join(townRoot, "mayor")
	if err := os.MkdirAll(mayorDir, 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}
	townJSON := filepath.Join(mayorDir, "town.json")
	content := `{"name": "` + townName + `"}`
	if err := os.WriteFile(townJSON, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write town.json: %v", err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}

	return d, func() {
		// Cleanup handled by t.TempDir()
	}
}

func TestParseLifecycleRequest_Cycle(t *testing.T) {
	d := testDaemon()

	tests := []struct {
		subject  string
		body     string
		expected LifecycleAction
	}{
		// JSON body format
		{"LIFECYCLE: requesting action", `{"action": "cycle"}`, ActionCycle},
		// Simple text body format
		{"LIFECYCLE: requesting action", "cycle", ActionCycle},
		{"lifecycle: action request", "action: cycle", ActionCycle},
	}

	for _, tc := range tests {
		msg := &BeadsMessage{
			Subject: tc.subject,
			Body:    tc.body,
			From:    "test-sender",
		}
		result := d.parseLifecycleRequest(msg)
		if result == nil {
			t.Errorf("parseLifecycleRequest(subject=%q, body=%q) returned nil, expected action %s", tc.subject, tc.body, tc.expected)
			continue
		}
		if result.Action != tc.expected {
			t.Errorf("parseLifecycleRequest(subject=%q, body=%q) action = %s, expected %s", tc.subject, tc.body, result.Action, tc.expected)
		}
	}
}

func TestParseLifecycleRequest_RestartAndShutdown(t *testing.T) {
	// Verify that restart and shutdown are correctly parsed using structured body.
	d := testDaemon()

	tests := []struct {
		subject  string
		body     string
		expected LifecycleAction
	}{
		{"LIFECYCLE: action", `{"action": "restart"}`, ActionRestart},
		{"LIFECYCLE: action", `{"action": "shutdown"}`, ActionShutdown},
		{"lifecycle: action", "stop", ActionShutdown},
		{"LIFECYCLE: action", "restart", ActionRestart},
	}

	for _, tc := range tests {
		msg := &BeadsMessage{
			Subject: tc.subject,
			Body:    tc.body,
			From:    "test-sender",
		}
		result := d.parseLifecycleRequest(msg)
		if result == nil {
			t.Errorf("parseLifecycleRequest(subject=%q, body=%q) returned nil", tc.subject, tc.body)
			continue
		}
		if result.Action != tc.expected {
			t.Errorf("parseLifecycleRequest(subject=%q, body=%q) action = %s, expected %s", tc.subject, tc.body, result.Action, tc.expected)
		}
	}
}

func TestParseLifecycleRequest_NotLifecycle(t *testing.T) {
	d := testDaemon()

	tests := []string{
		"Regular message",
		"HEARTBEAT: check rigs",
		"lifecycle without colon",
		"Something else: requesting cycle",
		"",
	}

	for _, title := range tests {
		msg := &BeadsMessage{
			Subject: title,
			From:    "test-sender",
		}
		result := d.parseLifecycleRequest(msg)
		if result != nil {
			t.Errorf("parseLifecycleRequest(%q) = %+v, expected nil", title, result)
		}
	}
}

func TestParseLifecycleRequest_UsesFromField(t *testing.T) {
	d := testDaemon()

	// Now that we use structured body, the From field comes directly from the message
	tests := []struct {
		subject      string
		body         string
		sender       string
		expectedFrom string
	}{
		{"LIFECYCLE: action", `{"action": "cycle"}`, "mayor", "mayor"},
		{"LIFECYCLE: action", "restart", "gastown-witness", "gastown-witness"},
		{"lifecycle: action", "shutdown", "my-rig-refinery", "my-rig-refinery"},
	}

	for _, tc := range tests {
		msg := &BeadsMessage{
			Subject: tc.subject,
			Body:    tc.body,
			From:    tc.sender,
		}
		result := d.parseLifecycleRequest(msg)
		if result == nil {
			t.Errorf("parseLifecycleRequest(body=%q) returned nil", tc.body)
			continue
		}
		if result.From != tc.expectedFrom {
			t.Errorf("parseLifecycleRequest() from = %q, expected %q", result.From, tc.expectedFrom)
		}
	}
}

func TestParseLifecycleRequest_AlwaysUsesFromField(t *testing.T) {
	d := testDaemon()

	// With structured body parsing, From always comes from message From field
	msg := &BeadsMessage{
		Subject: "LIFECYCLE: action",
		Body:    "cycle",
		From:    "the-sender",
	}
	result := d.parseLifecycleRequest(msg)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.From != "the-sender" {
		t.Errorf("parseLifecycleRequest() from = %q, expected 'the-sender'", result.From)
	}
}

func TestIdentityToSession_Mayor(t *testing.T) {
	d, cleanup := testDaemonWithTown(t, "ai")
	defer cleanup()

	// Mayor session name is now fixed (one per machine, uses hq- prefix)
	result := d.identityToSession("mayor")
	if result != "hq-mayor" {
		t.Errorf("identityToSession('mayor') = %q, expected 'hq-mayor'", result)
	}
}

func TestIdentityToSession_Witness(t *testing.T) {
	d := testDaemon()

	// Default prefix registry: all unknown rigs map to DefaultPrefix ("gt")
	tests := []struct {
		identity string
		expected string
	}{
		{"gastown-witness", "gt-witness"},
		{"myrig-witness", "gt-witness"},
		{"my-rig-name-witness", "gt-witness"},
	}

	for _, tc := range tests {
		result := d.identityToSession(tc.identity)
		if result != tc.expected {
			t.Errorf("identityToSession(%q) = %q, expected %q", tc.identity, result, tc.expected)
		}
	}
}

func TestIdentityToSession_WitnessWithPrefix(t *testing.T) {
	d := testDaemon()

	// Register a rig with a distinct prefix to verify prefix differentiation
	oldRegistry := session.DefaultRegistry()
	r := session.NewPrefixRegistry()
	r.Register("gt", "gastown")
	r.Register("bd", "beads")
	session.SetDefaultRegistry(r)
	defer session.SetDefaultRegistry(oldRegistry)

	tests := []struct {
		identity string
		expected string
	}{
		{"gastown-witness", "gt-witness"},
		{"beads-witness", "bd-witness"},
		{"unknown-witness", "gt-witness"}, // unknown rig falls back to DefaultPrefix
	}

	for _, tc := range tests {
		result := d.identityToSession(tc.identity)
		if result != tc.expected {
			t.Errorf("identityToSession(%q) = %q, expected %q", tc.identity, result, tc.expected)
		}
	}
}

func TestIdentityToSession_Unknown(t *testing.T) {
	d := testDaemon()

	tests := []string{
		"unknown",
		"polecat",
		"refinery",
		"gastown", // rig name without -witness
		"",
	}

	for _, identity := range tests {
		result := d.identityToSession(identity)
		if result != "" {
			t.Errorf("identityToSession(%q) = %q, expected empty string", identity, result)
		}
	}
}

func TestBeadsMessage_Serialization(t *testing.T) {
	msg := BeadsMessage{
		ID:       "msg-123",
		Subject:  "Test Message",
		Body:     "A test message body",
		From:     "test-sender",
		To:       "test-recipient",
		Priority: "high",
		Type:     "message",
	}

	// Verify all fields are accessible
	if msg.ID != "msg-123" {
		t.Errorf("ID mismatch")
	}
	if msg.Subject != "Test Message" {
		t.Errorf("Subject mismatch")
	}
	if msg.From != "test-sender" {
		t.Errorf("From mismatch")
	}
}

func TestSyncFailureTracking(t *testing.T) {
	d := testDaemon()

	workDir := "/tmp/test-workdir"

	// Initially zero failures
	if got := d.getSyncFailures(workDir); got != 0 {
		t.Errorf("initial getSyncFailures() = %d, want 0", got)
	}

	// Record failures and check counting
	d.recordSyncFailure(workDir)
	if got := d.getSyncFailures(workDir); got != 1 {
		t.Errorf("getSyncFailures() after 1 failure = %d, want 1", got)
	}

	d.recordSyncFailure(workDir)
	d.recordSyncFailure(workDir)
	if got := d.getSyncFailures(workDir); got != 3 {
		t.Errorf("getSyncFailures() after 3 failures = %d, want 3", got)
	}

	// Reset clears the counter
	d.resetSyncFailures(workDir)
	if got := d.getSyncFailures(workDir); got != 0 {
		t.Errorf("getSyncFailures() after reset = %d, want 0", got)
	}

	// Different workdirs are tracked independently
	d.recordSyncFailure("/tmp/dir-a")
	d.recordSyncFailure("/tmp/dir-a")
	d.recordSyncFailure("/tmp/dir-b")
	if got := d.getSyncFailures("/tmp/dir-a"); got != 2 {
		t.Errorf("getSyncFailures(dir-a) = %d, want 2", got)
	}
	if got := d.getSyncFailures("/tmp/dir-b"); got != 1 {
		t.Errorf("getSyncFailures(dir-b) = %d, want 1", got)
	}
}

func TestSyncFailureEscalationThreshold(t *testing.T) {
	// Verify the threshold constant is sensible
	if syncFailureEscalationThreshold < 2 {
		t.Errorf("syncFailureEscalationThreshold = %d, should be >= 2 to avoid premature escalation", syncFailureEscalationThreshold)
	}
	if syncFailureEscalationThreshold > 10 {
		t.Errorf("syncFailureEscalationThreshold = %d, should be <= 10 to ensure timely escalation", syncFailureEscalationThreshold)
	}
}

func TestIsWorkingTreeDirty(t *testing.T) {
	d := testDaemon()

	// Create a git repo in a temp dir
	tmpDir := t.TempDir()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = tmpDir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v\n%s", args, err, out)
		}
	}

	runGit("init")
	runGit("commit", "--allow-empty", "-m", "initial")

	// Clean tree should not be dirty
	if d.isWorkingTreeDirty(tmpDir) {
		t.Error("clean repo reported as dirty")
	}

	// Create an untracked file
	if err := os.WriteFile(filepath.Join(tmpDir, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	if !d.isWorkingTreeDirty(tmpDir) {
		t.Error("repo with untracked file reported as clean")
	}

	// Stage the file
	runGit("add", "dirty.txt")
	if !d.isWorkingTreeDirty(tmpDir) {
		t.Error("repo with staged file reported as clean")
	}

	// Commit it - should be clean again
	runGit("commit", "-m", "add dirty.txt")
	if d.isWorkingTreeDirty(tmpDir) {
		t.Error("repo after commit reported as dirty")
	}

	// Modify the committed file (unstaged change)
	if err := os.WriteFile(filepath.Join(tmpDir, "dirty.txt"), []byte("modified"), 0644); err != nil {
		t.Fatal(err)
	}
	if !d.isWorkingTreeDirty(tmpDir) {
		t.Error("repo with unstaged modification reported as clean")
	}
}

func TestSyncWorkspace_DirtyTreeAutoStash(t *testing.T) {
	d := testDaemon()

	// Create a git repo with a remote to simulate real workspace
	tmpDir := t.TempDir()
	bareDir := filepath.Join(tmpDir, "bare.git")
	workDir := filepath.Join(tmpDir, "work")

	runGitIn := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@test.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@test.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
		}
	}

	// Create bare repo with main as default branch
	if err := os.MkdirAll(bareDir, 0755); err != nil {
		t.Fatal(err)
	}
	runGitIn(bareDir, "init", "--bare", "--initial-branch=main")

	// Clone it as work dir
	cmd := exec.Command("git", "clone", bareDir, workDir)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=test",
		"GIT_COMMITTER_EMAIL=test@test.com",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone failed: %v\n%s", err, out)
	}

	// Create initial commit and push to set up main branch
	if err := os.WriteFile(filepath.Join(workDir, "README.md"), []byte("hello"), 0644); err != nil {
		t.Fatal(err)
	}
	runGitIn(workDir, "add", "README.md")
	runGitIn(workDir, "commit", "-m", "initial")
	runGitIn(workDir, "push", "-u", "origin", "main")

	// Create a dirty file (simulating .beads/ changes)
	if err := os.WriteFile(filepath.Join(workDir, "local-changes.txt"), []byte("dirty data"), 0644); err != nil {
		t.Fatal(err)
	}

	// Verify tree is dirty
	if !d.isWorkingTreeDirty(workDir) {
		t.Fatal("expected dirty tree before sync")
	}

	// syncWorkspace should auto-stash, pull, and restore
	d.syncWorkspace(workDir)

	// Dirty file should still exist after sync (restored from stash)
	if _, err := os.Stat(filepath.Join(workDir, "local-changes.txt")); os.IsNotExist(err) {
		t.Error("dirty file was lost after syncWorkspace - stash pop failed")
	}

	// No sync failures should have been recorded (pull succeeded)
	if got := d.getSyncFailures(workDir); got != 0 {
		t.Errorf("expected 0 sync failures after successful sync, got %d", got)
	}
}

// TestGetStartCommand_NonClaudeAgentBypassesToml verifies GH #2417:
// when a non-Claude agent is configured as default_agent or role_agents[witness],
// the daemon's getStartCommand must NOT use the hardcoded "exec claude" start_command
// from the built-in role TOMLs.
func TestGetStartCommand_NonClaudeAgentBypassesToml(t *testing.T) {
	townRoot := t.TempDir()

	// Create settings dir and town config.json with gemini as default_agent.
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "gemini"
	settingsJSON, err := json.Marshal(townSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), settingsJSON, 0644); err != nil {
		t.Fatal(err)
	}

	// Create mayor dir (required for TownRoot to be valid).
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}

	// roleConfig with the builtin TOML start_command — this is what the daemon
	// sees at runtime when the built-in role TOML is loaded.
	roleConfig := &beads.RoleConfig{
		StartCommand: "exec claude --dangerously-skip-permissions",
	}
	parsed := &ParsedIdentity{
		RoleType: "witness",
		RigName:  "",
	}

	startCmd := d.getStartCommand(roleConfig, parsed)

	// The result must NOT start with "exec claude" — that would mean the TOML
	// start_command was used, bypassing the configured non-Claude agent.
	if strings.Contains(startCmd, "claude --dangerously-skip-permissions") {
		t.Errorf("getStartCommand returned hardcoded Claude TOML command for non-Claude default_agent: %q", startCmd)
	}

	// The result should reference the gemini binary (the resolved agent).
	if !strings.Contains(startCmd, "gemini") {
		t.Errorf("getStartCommand should use gemini (the configured default_agent), got: %q", startCmd)
	}
}

// TestGetStartCommand_ClaudeAgentFallsThrough verifies that when Claude is the
// resolved agent, getStartCommand falls through to BuildStartupCommandFromConfig
// (not the literal TOML string) for proper model flag and beacon injection.
func TestGetStartCommand_ClaudeAgentFallsThrough(t *testing.T) {
	townRoot := t.TempDir()

	// Default town settings — claude is the default agent.
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "claude"
	settingsJSON, err := json.Marshal(townSettings)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "config.json"), settingsJSON, 0644); err != nil {
		t.Fatal(err)
	}

	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}

	roleConfig := &beads.RoleConfig{
		StartCommand: "exec claude --dangerously-skip-permissions",
	}
	parsed := &ParsedIdentity{
		RoleType: "witness",
		RigName:  "",
	}

	startCmd := d.getStartCommand(roleConfig, parsed)

	// Result should still reference claude.
	if !strings.Contains(startCmd, "claude") {
		t.Errorf("getStartCommand should produce a claude command for claude default_agent, got: %q", startCmd)
	}
	// The literal TOML string should NOT be returned verbatim — the fall-through
	// path adds beacon injection and model flags.
	if startCmd == "exec claude --dangerously-skip-permissions" {
		t.Errorf("getStartCommand returned literal TOML start_command verbatim — beacon injection was skipped: %q", startCmd)
	}
}

func TestGetStartCommandPreservesOpenCodeWorkerSession(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	d := &Daemon{
		config: &Config{TownRoot: townRoot},
		logger: log.New(io.Discard, "", 0),
	}
	parsed := &ParsedIdentity{RoleType: constants.RoleCrew, RigName: "gastown", AgentName: "alice"}
	command := d.getStartCommand(nil, parsed, "gt-crew-alice", string(config.AgentOpenCodeServer))

	if !strings.Contains(command, "opencode-worker") {
		t.Fatalf("restart command = %q, want OpenCode worker", command)
	}
	if !strings.Contains(command, "GT_SESSION") || !strings.Contains(command, "gt-crew-alice") {
		t.Fatalf("restart command lost GT_SESSION: %q", command)
	}
	if !strings.Contains(command, "GT_AGENT") || !strings.Contains(command, string(config.AgentOpenCodeServer)) {
		t.Fatalf("restart command lost effective agent: %q", command)
	}
	if !strings.Contains(command, "OPENCODE_PERMISSION") {
		t.Fatalf("restart command lost OpenCode runtime environment: %q", command)
	}
}

func TestGetWorkDirUsesCanonicalStructuredWorkerDirectories(t *testing.T) {
	townRoot := t.TempDir()
	d := &Daemon{config: &Config{TownRoot: townRoot}, logger: log.New(io.Discard, "", 0)}

	nestedPolecat := filepath.Join(townRoot, "gastown", "polecats", "furiosa", "gastown")
	if err := os.MkdirAll(nestedPolecat, 0755); err != nil {
		t.Fatal(err)
	}
	polecatRole := &beads.RoleConfig{WorkDirPattern: "{town}/{rig}/polecats/{name}"}
	if got := d.getWorkDir(polecatRole, &ParsedIdentity{RoleType: constants.RolePolecat, RigName: "gastown", AgentName: "furiosa"}); got != nestedPolecat {
		t.Fatalf("polecat work dir = %q, want %q", got, nestedPolecat)
	}

	deaconDir := filepath.Join(townRoot, "deacon")
	deaconRole := &beads.RoleConfig{WorkDirPattern: "{town}"}
	if got := d.getWorkDir(deaconRole, &ParsedIdentity{RoleType: constants.RoleDeacon}); got != deaconDir {
		t.Fatalf("deacon work dir = %q, want %q", got, deaconDir)
	}
	if got := d.getWorkDir(nil, &ParsedIdentity{RoleType: constants.RoleDeacon}); got != deaconDir {
		t.Fatalf("fallback deacon work dir = %q, want %q", got, deaconDir)
	}
}

func TestGetStartCommandUsesCapturedOpenCodeOverrides(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GT_OPENCODE_MODEL", "daemon/model")
	d := &Daemon{config: &Config{TownRoot: townRoot}, logger: log.New(io.Discard, "", 0)}
	parsed := &ParsedIdentity{RoleType: constants.RoleCrew, RigName: "gastown", AgentName: "alice"}
	command := d.getStartCommandWithOverrides(nil, parsed, "gt-crew-alice", string(config.AgentOpenCodeServer), map[string]string{
		"GT_OPENCODE_MODEL": "session/model",
	})
	if !strings.Contains(command, "session/model") || strings.Contains(command, "daemon/model") {
		t.Fatalf("restart command did not preserve captured override: %q", command)
	}
}

func TestApplyRuntimeOverridesWins(t *testing.T) {
	envVars := map[string]string{
		"GT_OPENCODE_MODEL": "daemon/model",
		"GT_AGENT":          "opencode-server",
	}
	applyRuntimeOverrides(envVars, map[string]string{
		"GT_OPENCODE_MODEL": "session/model",
	})
	if envVars["GT_OPENCODE_MODEL"] != "session/model" {
		t.Fatalf("GT_OPENCODE_MODEL = %q, want captured session override", envVars["GT_OPENCODE_MODEL"])
	}
}
