package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/config"
	"github.com/jonbaldie/gastown/internal/constants"
	"github.com/jonbaldie/gastown/internal/dog"
)

// =============================================================================
// Test Fixtures
// =============================================================================

// testDogManager creates a dog.Manager with a temporary town root for testing.
func testDogManager(t *testing.T) (*dog.Manager, string) {
	t.Helper()
	tmpDir := t.TempDir()

	rigsConfig := &config.RigsConfig{
		Version: 1,
		Rigs: map[string]config.RigEntry{
			"gastown": {GitURL: "git@github.com:test/gastown.git"},
			"beads":   {GitURL: "git@github.com:test/beads.git"},
		},
	}

	m := dog.NewManager(tmpDir, rigsConfig)
	return m, tmpDir
}

// setupTestDog creates a dog directory with a state file for testing.
func setupTestDog(t *testing.T, m *dog.Manager, townRoot, name string, state *dog.DogState) {
	t.Helper()

	dogPath := filepath.Join(townRoot, "deacon", "dogs", name)
	if err := os.MkdirAll(dogPath, 0755); err != nil {
		t.Fatalf("Failed to create dog dir: %v", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal state: %v", err)
	}

	statePath := filepath.Join(dogPath, ".dog.json")
	if err := os.WriteFile(statePath, data, 0644); err != nil {
		t.Fatalf("Failed to write state file: %v", err)
	}
}

// =============================================================================
// Dog Name Detection from Path Tests
// =============================================================================

// TestDetectDogNameFromPath tests the path parsing logic used by runDogDone
// to auto-detect the dog name from the current working directory.
func TestDetectDogNameFromPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		wantName string
		wantOK   bool
	}{
		{
			name:     "dog worktree root",
			path:     "/Users/user/gt/deacon/dogs/alpha",
			wantName: "alpha",
			wantOK:   true,
		},
		{
			name:     "dog rig worktree",
			path:     "/Users/user/gt/deacon/dogs/alpha/gastown",
			wantName: "alpha",
			wantOK:   true,
		},
		{
			name:     "deep path in dog worktree",
			path:     "/Users/user/gt/deacon/dogs/bravo/beads/internal/cmd",
			wantName: "bravo",
			wantOK:   true,
		},
		{
			name:     "hyphenated dog name",
			path:     "/Users/user/gt/deacon/dogs/my-dog/gastown",
			wantName: "my-dog",
			wantOK:   true,
		},
		{
			name:     "numeric dog name",
			path:     "/Users/user/gt/deacon/dogs/dog123/beads",
			wantName: "dog123",
			wantOK:   true,
		},
		{
			name:     "not a dog path - polecat",
			path:     "/Users/user/gt/gastown/polecats/fixer/internal",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "not a dog path - crew",
			path:     "/Users/user/gt/gastown/crew/george/internal",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "deacon but not dogs directory",
			path:     "/Users/user/gt/deacon/boot",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "dogs without deacon parent",
			path:     "/Users/user/gt/some/dogs/alpha",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "empty path",
			path:     "",
			wantName: "",
			wantOK:   false,
		},
		{
			name:     "root path",
			path:     "/",
			wantName: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotOK := detectDogNameFromPath(tt.path)
			if gotName != tt.wantName {
				t.Errorf("detectDogNameFromPath(%q) name = %q, want %q", tt.path, gotName, tt.wantName)
			}
			if gotOK != tt.wantOK {
				t.Errorf("detectDogNameFromPath(%q) ok = %v, want %v", tt.path, gotOK, tt.wantOK)
			}
		})
	}
}

// detectDogNameFromPath extracts the dog name from a filesystem path.
// This mirrors the logic in runDogDone for testability.
// Returns the dog name and true if found, empty string and false otherwise.
func detectDogNameFromPath(path string) (string, bool) {
	if path == "" {
		return "", false
	}

	// Use the same split logic as runDogDone
	parts := splitPathComponents(path)

	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "dogs" && i > 0 && parts[i-1] == "deacon" {
			return parts[i+1], true
		}
	}

	return "", false
}

// splitPath splits a path into its components.
func splitPath(path string) []string {
	// Clean and split the path
	path = filepath.Clean(path)
	var parts []string
	for {
		dir, file := filepath.Split(path)
		if file != "" {
			parts = append([]string{file}, parts...)
		}
		if dir == "" || dir == "/" || dir == path {
			break
		}
		path = filepath.Clean(dir)
	}
	return parts
}

// =============================================================================
// Dog Done Command Tests
// =============================================================================

// TestDogDone_AlreadyIdle verifies that dogDone handles the case where
// a dog is already idle gracefully.
func TestDogDone_AlreadyIdle(t *testing.T) {
	m, tmpDir := testDogManager(t)

	now := time.Now()
	state := &dog.DogState{
		Name:       "alpha",
		State:      dog.StateIdle,
		Work:       "",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupTestDog(t, m, tmpDir, "alpha", state)

	// Get the dog and verify it's idle
	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if d.State != dog.StateIdle {
		t.Errorf("State = %q, want %q", d.State, dog.StateIdle)
	}
	if d.Work != "" {
		t.Errorf("Work = %q, want empty", d.Work)
	}

	// ClearWork on already-idle dog should succeed without error
	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}

	// Verify still idle
	d, _ = m.Get("alpha")
	if d.State != dog.StateIdle {
		t.Errorf("After ClearWork: State = %q, want %q", d.State, dog.StateIdle)
	}
}

// TestDogDone_WorkingToIdle verifies that dogDone transitions a working
// dog back to idle state.
func TestDogDone_WorkingToIdle(t *testing.T) {
	m, tmpDir := testDogManager(t)

	now := time.Now()
	state := &dog.DogState{
		Name:       "alpha",
		State:      dog.StateWorking,
		Work:       "hq-convoy-xyz",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupTestDog(t, m, tmpDir, "alpha", state)

	// Verify dog is working
	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if d.State != dog.StateWorking {
		t.Errorf("Initial State = %q, want %q", d.State, dog.StateWorking)
	}
	if d.Work != "hq-convoy-xyz" {
		t.Errorf("Initial Work = %q, want 'hq-convoy-xyz'", d.Work)
	}

	// Clear work
	if err := m.ClearWork("alpha"); err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}

	// Verify now idle with no work
	d, _ = m.Get("alpha")
	if d.State != dog.StateIdle {
		t.Errorf("After ClearWork: State = %q, want %q", d.State, dog.StateIdle)
	}
	if d.Work != "" {
		t.Errorf("After ClearWork: Work = %q, want empty", d.Work)
	}
}

// TestDogDone_NotFound verifies error handling for non-existent dog.
func TestDogDone_NotFound(t *testing.T) {
	m, _ := testDogManager(t)

	err := m.ClearWork("nonexistent")
	if err != dog.ErrDogNotFound {
		t.Errorf("ClearWork() error = %v, want ErrDogNotFound", err)
	}
}

// =============================================================================
// Dog Clear Tests
// =============================================================================

// TestDogClear_WorkingToIdle verifies that dogClear transitions a working
// dog back to idle state.
func TestDogClear_WorkingToIdle(t *testing.T) {
	m, tmpDir := testDogManager(t)

	now := time.Now()
	state := &dog.DogState{
		Name:       "alpha",
		State:      dog.StateWorking,
		Work:       constants.MolConvoyFeed,
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupTestDog(t, m, tmpDir, "alpha", state)

	// Verify dog is working
	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if d.State != dog.StateWorking {
		t.Errorf("Initial State = %q, want %q", d.State, dog.StateWorking)
	}

	// Clear the dog (simulates gt dog clear alpha)
	err = m.ClearWork("alpha")
	if err != nil {
		t.Fatalf("ClearWork() error = %v", err)
	}

	// Verify dog is now idle
	d, err = m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() after clear error = %v", err)
	}
	if d.State != dog.StateIdle {
		t.Errorf("After ClearWork: State = %q, want %q", d.State, dog.StateIdle)
	}
	if d.Work != "" {
		t.Errorf("After ClearWork: Work = %q, want empty", d.Work)
	}
}

// TestDogClear_AlreadyIdle verifies that dogClear handles the case where
// a dog is already idle gracefully.
func TestDogClear_AlreadyIdle(t *testing.T) {
	m, tmpDir := testDogManager(t)

	now := time.Now()
	state := &dog.DogState{
		Name:       "alpha",
		State:      dog.StateIdle,
		Work:       "",
		LastActive: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	setupTestDog(t, m, tmpDir, "alpha", state)

	// Get the dog and verify it's idle
	d, err := m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if d.State != dog.StateIdle {
		t.Errorf("Initial State = %q, want %q", d.State, dog.StateIdle)
	}

	// ClearWork on an already idle dog should succeed (idempotent)
	err = m.ClearWork("alpha")
	if err != nil {
		t.Errorf("ClearWork() on idle dog error = %v, want nil", err)
	}

	// Verify dog is still idle
	d, err = m.Get("alpha")
	if err != nil {
		t.Fatalf("Get() after clear error = %v", err)
	}
	if d.State != dog.StateIdle {
		t.Errorf("After ClearWork: State = %q, want %q", d.State, dog.StateIdle)
	}
}

// TestDogClear_NotFound verifies error handling for non-existent dog.
func TestDogClear_NotFound(t *testing.T) {
	m, _ := testDogManager(t)

	err := m.ClearWork("nonexistent")
	if err != dog.ErrDogNotFound {
		t.Errorf("ClearWork() error = %v, want ErrDogNotFound", err)
	}
}

// =============================================================================
// Path Splitting Tests
// =============================================================================

func TestSplitPath(t *testing.T) {
	tests := []struct {
		path string
		want []string
	}{
		{
			path: "/Users/user/gt/deacon/dogs/alpha",
			want: []string{"Users", "user", "gt", "deacon", "dogs", "alpha"},
		},
		{
			path: "/a/b/c",
			want: []string{"a", "b", "c"},
		},
		{
			path: "relative/path",
			want: []string{"relative", "path"},
		},
		{
			path: "/",
			want: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := splitPath(tt.path)
			if len(got) != len(tt.want) {
				t.Errorf("splitPath(%q) = %v (len %d), want %v (len %d)",
					tt.path, got, len(got), tt.want, len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitPath(%q)[%d] = %q, want %q",
						tt.path, i, got[i], tt.want[i])
				}
			}
		})
	}
}

// =============================================================================
// Dog Format Time Ago Tests
// =============================================================================

func TestDogFormatTimeAgo(t *testing.T) {
	tests := []struct {
		name   string
		offset time.Duration
		want   string
	}{
		{"just now", 30 * time.Second, "just now"},
		{"1 minute ago", 1 * time.Minute, "1 minute ago"},
		{"5 minutes ago", 5 * time.Minute, "5 minutes ago"},
		{"1 hour ago", 1 * time.Hour, "1 hour ago"},
		{"3 hours ago", 3 * time.Hour, "3 hours ago"},
		{"1 day ago", 24 * time.Hour, "1 day ago"},
		{"5 days ago", 5 * 24 * time.Hour, "5 days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testTime := time.Now().Add(-tt.offset)
			got := dogFormatTimeAgo(testTime)
			if got != tt.want {
				t.Errorf("dogFormatTimeAgo(%v ago) = %q, want %q", tt.offset, got, tt.want)
			}
		})
	}
}

func TestDogFormatTimeAgo_ZeroTime(t *testing.T) {
	got := dogFormatTimeAgo(time.Time{})
	if got != "(unknown)" {
		t.Errorf("dogFormatTimeAgo(zero) = %q, want '(unknown)'", got)
	}
}
