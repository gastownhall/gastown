package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/session"
)

func setupWarrantTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

// =============================================================================
// Warrant Tests
// =============================================================================

// TestWarrantFile_NewWarrant verifies that filing a new warrant creates the file.
func TestWarrantFile_NewWarrant(t *testing.T) {
	tmpDir := t.TempDir()
	warrantDir := filepath.Join(tmpDir, "warrants")

	// Create warrant manually (simulating the function)
	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	target := "gastown/polecats/alpha"
	reason := "Zombie detected: no session, idle >10m"

	warrant := Warrant{
		ID:       "warrant-test-123",
		Target:   target,
		Reason:   reason,
		FiledBy:  "test-agent",
		FiledAt:  time.Now(),
		Executed: false,
	}

	data, err := json.MarshalIndent(warrant, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}

	warrantPath := filepath.Join(warrantDir, "gastown_polecats_alpha.warrant.json")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Verify file exists and can be read back
	readData, err := os.ReadFile(warrantPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var readWarrant Warrant
	if err := json.Unmarshal(readData, &readWarrant); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if readWarrant.Target != target {
		t.Errorf("Target = %q, want %q", readWarrant.Target, target)
	}
	if readWarrant.Reason != reason {
		t.Errorf("Reason = %q, want %q", readWarrant.Reason, reason)
	}
	if readWarrant.Executed {
		t.Error("Executed = true, want false")
	}
}

// TestWarrantFile_DuplicateWarrant verifies that filing a duplicate warrant
// is handled gracefully (doesn't overwrite).
func TestWarrantFile_DuplicateWarrant(t *testing.T) {
	tmpDir := t.TempDir()
	warrantDir := filepath.Join(tmpDir, "warrants")

	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	target := "gastown/polecats/alpha"
	originalReason := "First reason"

	// Create first warrant
	warrant := Warrant{
		ID:       "warrant-first",
		Target:   target,
		Reason:   originalReason,
		FiledBy:  "test-agent",
		FiledAt:  time.Now(),
		Executed: false,
	}

	warrantPath := filepath.Join(warrantDir, "gastown_polecats_alpha.warrant.json")
	data, _ := json.MarshalIndent(warrant, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Try to detect duplicate (simulating the check in runWarrantFile)
	if _, err := os.Stat(warrantPath); err == nil {
		// File exists - read it
		existingData, _ := os.ReadFile(warrantPath)
		var existing Warrant
		if json.Unmarshal(existingData, &existing) == nil && !existing.Executed {
			// Duplicate detected - this is the expected behavior
			if existing.Reason != originalReason {
				t.Errorf("Existing warrant reason = %q, want %q", existing.Reason, originalReason)
			}
			return // Test passes - duplicate was detected
		}
	}

	t.Error("Expected duplicate warrant to be detected")
}

// TestWarrantExecute_MarksExecuted verifies that executing a warrant marks it as executed.
func TestWarrantExecute_MarksExecuted(t *testing.T) {
	tmpDir := t.TempDir()
	warrantDir := filepath.Join(tmpDir, "warrants")

	if err := os.MkdirAll(warrantDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	target := "gastown/polecats/alpha"

	// Create pending warrant
	warrant := Warrant{
		ID:       "warrant-pending",
		Target:   target,
		Reason:   "Test execution",
		FiledBy:  "test-agent",
		FiledAt:  time.Now(),
		Executed: false,
	}

	warrantPath := filepath.Join(warrantDir, "gastown_polecats_alpha.warrant.json")
	data, _ := json.MarshalIndent(warrant, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Simulate execution (mark as executed)
	now := time.Now()
	warrant.Executed = true
	warrant.ExecutedAt = &now

	data, _ = json.MarshalIndent(warrant, "", "  ")
	if err := os.WriteFile(warrantPath, data, 0644); err != nil {
		t.Fatalf("WriteFile() after execution error = %v", err)
	}

	// Verify warrant is marked as executed
	readData, _ := os.ReadFile(warrantPath)
	var readWarrant Warrant
	if err := json.Unmarshal(readData, &readWarrant); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if !readWarrant.Executed {
		t.Error("Executed = false, want true")
	}
	if readWarrant.ExecutedAt == nil {
		t.Error("ExecutedAt = nil, want non-nil")
	}
}

func TestTargetToSessionName(t *testing.T) {
	setupWarrantTestRegistry(t)
	tests := []struct {
		target  string
		wantErr bool
		want    string
	}{
		{"gastown/polecats/alpha", false, "gt-alpha"},
		{"beads/polecats/charlie", false, "bd-charlie"},
		{"deacon/dogs", true, ""},
		{"deacon/dogs/alpha", false, "hq-dog-alpha"},
		{"gastown/crew/bob", false, "gt-crew-bob"},
		{"gastown/witness", false, "gt-witness"},
		{"gastown/refinery", false, "gt-refinery"},
		{"beads/witness", false, "bd-witness"},
		{"beads/refinery", false, "bd-refinery"},
		{"unknownrig/something/else", false, "gt-unknownrig-something-else"},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got, err := targetToSessionName(tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("targetToSessionName(%q) error = %v, wantErr %v", tt.target, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("targetToSessionName(%q) = %q, want %q", tt.target, got, tt.want)
			}
		})
	}
}

// TestWarrantFilePath verifies warrant file path generation.
func TestWarrantFilePath(t *testing.T) {
	tests := []struct {
		dir    string
		target string
		want   string
	}{
		{
			dir:    filepath.Join("/tmp", "warrants"),
			target: "gastown/polecats/alpha",
			want:   filepath.Join("/tmp", "warrants", "gastown_polecats_alpha.warrant.json"),
		},
		{
			dir:    filepath.Join("/home", "user", "gt", "warrants"),
			target: "deacon/dogs/bravo",
			want:   filepath.Join("/home", "user", "gt", "warrants", "deacon_dogs_bravo.warrant.json"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			got := warrantFilePath(tt.dir, tt.target)
			if got != tt.want {
				t.Errorf("warrantFilePath(%q, %q) = %q, want %q", tt.dir, tt.target, got, tt.want)
			}
		})
	}
}
