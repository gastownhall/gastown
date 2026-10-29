package checkpoint

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPath(t *testing.T) {
	dir := "/some/polecat/dir"
	got := Path(dir)
	want := filepath.Join(dir, Filename)
	if got != want {
		t.Errorf("Path(%q) = %q, want %q", dir, got, want)
	}
}

func TestReadWrite(t *testing.T) {
	// Create temp directory
	tmpDir := t.TempDir()

	// Test reading non-existent checkpoint returns nil, nil
	cp, err := Read(tmpDir)
	if err != nil {
		t.Fatalf("Read non-existent: unexpected error: %v", err)
	}
	if cp != nil {
		t.Fatal("Read non-existent: expected nil checkpoint")
	}

	// Create and write a checkpoint
	original := &Checkpoint{
		MoleculeID:    "mol-123",
		CurrentStep:   "step-1",
		StepTitle:     "Build the thing",
		ModifiedFiles: []string{"file1.go", "file2.go"},
		LastCommit:    "abc123",
		Branch:        "feature/test",
		HookedBead:    "gt-xyz",
		Notes:         "Some notes",
	}

	if err := Write(tmpDir, original); err != nil {
		t.Fatalf("Write: unexpected error: %v", err)
	}

	// Verify file exists
	path := Path(tmpDir)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("Write: checkpoint file not created")
	}

	// Read it back
	loaded, err := Read(tmpDir)
	if err != nil {
		t.Fatalf("Read: unexpected error: %v", err)
	}
	if loaded == nil {
		t.Fatal("Read: expected non-nil checkpoint")
	}

	// Verify fields
	if loaded.MoleculeID != original.MoleculeID {
		t.Errorf("MoleculeID = %q, want %q", loaded.MoleculeID, original.MoleculeID)
	}
	if loaded.CurrentStep != original.CurrentStep {
		t.Errorf("CurrentStep = %q, want %q", loaded.CurrentStep, original.CurrentStep)
	}
	if loaded.StepTitle != original.StepTitle {
		t.Errorf("StepTitle = %q, want %q", loaded.StepTitle, original.StepTitle)
	}
	if loaded.Branch != original.Branch {
		t.Errorf("Branch = %q, want %q", loaded.Branch, original.Branch)
	}
	if loaded.HookedBead != original.HookedBead {
		t.Errorf("HookedBead = %q, want %q", loaded.HookedBead, original.HookedBead)
	}
	if loaded.Notes != original.Notes {
		t.Errorf("Notes = %q, want %q", loaded.Notes, original.Notes)
	}
	if len(loaded.ModifiedFiles) != len(original.ModifiedFiles) {
		t.Errorf("ModifiedFiles len = %d, want %d", len(loaded.ModifiedFiles), len(original.ModifiedFiles))
	}

	// Verify timestamp was set
	if loaded.Timestamp.IsZero() {
		t.Error("Timestamp should be set by Write")
	}

	// Verify SessionID was set
	if loaded.SessionID == "" {
		t.Error("SessionID should be set by Write")
	}
}

func TestWritePreservesTimestamp(t *testing.T) {
	tmpDir := t.TempDir()

	// Create checkpoint with explicit timestamp
	ts := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cp := &Checkpoint{
		Timestamp: ts,
		Notes:     "test",
	}

	if err := Write(tmpDir, cp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	loaded, err := Read(tmpDir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if !loaded.Timestamp.Equal(ts) {
		t.Errorf("Timestamp = %v, want %v", loaded.Timestamp, ts)
	}
}

func TestReadCorruptedJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := Path(tmpDir)

	// Write invalid JSON
	if err := os.WriteFile(path, []byte("not valid json{"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := Read(tmpDir)
	if err == nil {
		t.Fatal("Read corrupted JSON: expected error")
	}
}

func TestRemove(t *testing.T) {
	tmpDir := t.TempDir()

	// Write a checkpoint
	cp := &Checkpoint{Notes: "to be removed"}
	if err := Write(tmpDir, cp); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Verify it exists
	path := Path(tmpDir)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("checkpoint should exist before Remove")
	}

	// Remove it
	if err := Remove(tmpDir); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("checkpoint should not exist after Remove")
	}

	// Remove again should not error
	if err := Remove(tmpDir); err != nil {
		t.Fatalf("Remove non-existent: %v", err)
	}
}

func TestCapture(t *testing.T) {
	// Use current directory (should be a git repo)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	// Find git root
	gitRoot := cwd
	for {
		if _, err := os.Stat(filepath.Join(gitRoot, ".git")); err == nil {
			break
		}
		parent := filepath.Dir(gitRoot)
		if parent == gitRoot {
			t.Skip("not in a git repository")
		}
		gitRoot = parent
	}

	cp, err := Capture(gitRoot)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}

	// Should have timestamp
	if cp.Timestamp.IsZero() {
		t.Error("Timestamp should be set")
	}

	// Should have branch (we're in a git repo)
	if cp.Branch == "" {
		t.Error("Branch should be set in git repo")
	}

	// Should have last commit
	if cp.LastCommit == "" {
		t.Error("LastCommit should be set in git repo")
	}
}

func TestWithMolecule(t *testing.T) {
	cp := &Checkpoint{}
	result := cp.WithMolecule("mol-abc", "step-1", "Do the thing")

	if result != cp {
		t.Error("WithMolecule should return same checkpoint")
	}
	if cp.MoleculeID != "mol-abc" {
		t.Errorf("MoleculeID = %q, want %q", cp.MoleculeID, "mol-abc")
	}
	if cp.CurrentStep != "step-1" {
		t.Errorf("CurrentStep = %q, want %q", cp.CurrentStep, "step-1")
	}
	if cp.StepTitle != "Do the thing" {
		t.Errorf("StepTitle = %q, want %q", cp.StepTitle, "Do the thing")
	}
}

func TestWithHookedBead(t *testing.T) {
	cp := &Checkpoint{}
	result := cp.WithHookedBead("gt-123")

	if result != cp {
		t.Error("WithHookedBead should return same checkpoint")
	}
	if cp.HookedBead != "gt-123" {
		t.Errorf("HookedBead = %q, want %q", cp.HookedBead, "gt-123")
	}
}

func TestWithNotes(t *testing.T) {
	cp := &Checkpoint{}
	result := cp.WithNotes("important context")

	if result != cp {
		t.Error("WithNotes should return same checkpoint")
	}
	if cp.Notes != "important context" {
		t.Errorf("Notes = %q, want %q", cp.Notes, "important context")
	}
}

func TestAge(t *testing.T) {
	cp := &Checkpoint{
		Timestamp: time.Now().Add(-5 * time.Minute),
	}

	age := cp.Age()
	if age < 4*time.Minute || age > 6*time.Minute {
		t.Errorf("Age = %v, expected ~5 minutes", age)
	}
}

func TestIsStale(t *testing.T) {
	tests := []struct {
		name      string
		age       time.Duration
		threshold time.Duration
		want      bool
	}{
		{"fresh", 5 * time.Minute, 1 * time.Hour, false},
		{"stale", 2 * time.Hour, 1 * time.Hour, true},
		{"exactly threshold", 1 * time.Hour, 1 * time.Hour, true}, // timing race: by the time IsStale runs, age > threshold
		{"just over threshold", 1*time.Hour + time.Second, 1 * time.Hour, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cp := &Checkpoint{
				Timestamp: time.Now().Add(-tt.age),
			}
			got := cp.IsStale(tt.threshold)
			if got != tt.want {
				t.Errorf("IsStale(%v) = %v, want %v", tt.threshold, got, tt.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	tests := []struct {
		name string
		cp   *Checkpoint
		want string
	}{
		{
			name: "empty",
			cp:   &Checkpoint{},
			want: "no significant state",
		},
		{
			name: "molecule only",
			cp:   &Checkpoint{MoleculeID: "mol-123"},
			want: "molecule mol-123",
		},
		{
			name: "molecule with step",
			cp:   &Checkpoint{MoleculeID: "mol-123", CurrentStep: "step-1"},
			want: "molecule mol-123, step step-1",
		},
		{
			name: "hooked bead",
			cp:   &Checkpoint{HookedBead: "gt-abc"},
			want: "hooked: gt-abc",
		},
		{
			name: "modified files",
			cp:   &Checkpoint{ModifiedFiles: []string{"a.go", "b.go"}},
			want: "2 modified files",
		},
		{
			name: "branch",
			cp:   &Checkpoint{Branch: "feature/test"},
			want: "branch: feature/test",
		},
		{
			name: "full",
			cp: &Checkpoint{
				MoleculeID:    "mol-123",
				CurrentStep:   "step-1",
				HookedBead:    "gt-abc",
				ModifiedFiles: []string{"a.go"},
				Branch:        "main",
			},
			want: "molecule mol-123, step step-1, hooked: gt-abc, 1 modified files, branch: main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cp.Summary()
			if got != tt.want {
				t.Errorf("Summary() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckpointJSONRoundtrip(t *testing.T) {
	original := &Checkpoint{
		MoleculeID:    "mol-test",
		CurrentStep:   "step-2",
		StepTitle:     "Testing JSON",
		ModifiedFiles: []string{"x.go", "y.go", "z.go"},
		LastCommit:    "deadbeef",
		Branch:        "develop",
		HookedBead:    "gt-roundtrip",
		Timestamp:     time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC),
		SessionID:     "session-123",
		Notes:         "Testing round trip",
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var loaded Checkpoint
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if loaded.MoleculeID != original.MoleculeID {
		t.Errorf("MoleculeID mismatch")
	}
	if loaded.CurrentStep != original.CurrentStep {
		t.Errorf("CurrentStep mismatch")
	}
	if loaded.StepTitle != original.StepTitle {
		t.Errorf("StepTitle mismatch")
	}
	if loaded.Branch != original.Branch {
		t.Errorf("Branch mismatch")
	}
	if loaded.HookedBead != original.HookedBead {
		t.Errorf("HookedBead mismatch")
	}
	if loaded.SessionID != original.SessionID {
		t.Errorf("SessionID mismatch")
	}
	if loaded.Notes != original.Notes {
		t.Errorf("Notes mismatch")
	}
	if !loaded.Timestamp.Equal(original.Timestamp) {
		t.Errorf("Timestamp mismatch")
	}
	if len(loaded.ModifiedFiles) != len(original.ModifiedFiles) {
		t.Errorf("ModifiedFiles length mismatch")
	}
}

func TestCaptureModifiedFiles(t *testing.T) {
	for _, tc := range []struct {
		name   string
		file   string
		staged bool
	}{
		{name: "unstaged", file: "alpha.txt"},
		{name: "single-character", file: "a"},
		{name: "staged", file: "alpha.txt", staged: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = dir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v: %s", args, err, out)
				}
			}
			git("init", "-q")
			git("config", "user.name", "Checkpoint Test")
			git("config", "user.email", "checkpoint@example.invalid")
			path := filepath.Join(dir, tc.file)
			if err := os.WriteFile(path, []byte("before\n"), 0600); err != nil {
				t.Fatal(err)
			}
			git("add", "--", tc.file)
			git("-c", "core.hooksPath="+t.TempDir(), "commit", "-qm", "initial", "--no-gpg-sign")
			if err := os.WriteFile(path, []byte("after\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.staged {
				git("add", "--", tc.file)
			}
			cp, err := Capture(dir)
			if err != nil {
				t.Fatal(err)
			}
			if want := []string{tc.file}; !reflect.DeepEqual(cp.ModifiedFiles, want) {
				t.Errorf("ModifiedFiles = %q, want %q", cp.ModifiedFiles, want)
			}
		})
	}
}
