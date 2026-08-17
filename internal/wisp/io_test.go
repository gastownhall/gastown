package wisp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestEnsureDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Test creating directory in existing root
	dir, err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}

	expectedDir := filepath.Join(tmpDir, WispDir)
	if dir != expectedDir {
		t.Errorf("EnsureDir() = %q, want %q", dir, expectedDir)
	}

	// Verify directory exists
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Failed to stat directory: %v", err)
	}
	if !info.IsDir() {
		t.Error("EnsureDir() should create a directory")
	}

	// Test calling again (should be idempotent)
	dir2, err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir() second call error = %v", err)
	}
	if dir2 != dir {
		t.Errorf("EnsureDir() second call = %q, want %q", dir2, dir)
	}
}

func TestEnsureDir_Permissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not reliable on Windows")
	}

	tmpDir := t.TempDir()

	dir, err := EnsureDir(tmpDir)
	if err != nil {
		t.Fatalf("EnsureDir() error = %v", err)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Failed to stat directory: %v", err)
	}

	// Check directory permissions are Beads-recommended 0700
	expectedPerm := os.FileMode(0700)
	if info.Mode().Perm() != expectedPerm {
		t.Errorf("Directory permissions = %v, want %v", info.Mode().Perm(), expectedPerm)
	}
}

func TestWispPath(t *testing.T) {
	tests := []struct {
		name     string
		root     string
		filename string
		want     string
	}{
		{
			name:     "basic path",
			root:     "/path/to/root",
			filename: "bead.json",
			want:     "/path/to/root/.beads/bead.json",
		},
		{
			name:     "nested filename",
			root:     "/path/to/root",
			filename: "subdir/bead.json",
			want:     "/path/to/root/.beads/subdir/bead.json",
		},
		{
			name:     "empty filename",
			root:     "/path/to/root",
			filename: "",
			want:     "/path/to/root/.beads",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WispPath(tt.root, tt.filename)
			if filepath.ToSlash(got) != tt.want {
				t.Errorf("WispPath() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWispPath_WithWispDir(t *testing.T) {
	// Verify WispPath uses WispDir constant
	root := "/test/root"
	filename := "test.json"

	expected := filepath.Join(root, WispDir, filename)
	got := WispPath(root, filename)

	if got != expected {
		t.Errorf("WispPath() = %q, want %q (using WispDir=%q)", got, expected, WispDir)
	}
}

func TestWriteJSON_Helper(t *testing.T) {
	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "test.json")

	type TestData struct {
		Name  string `json:"name"`
		Value int    `json:"value"`
	}

	data := TestData{
		Name:  "test",
		Value: 42,
	}

	// writeJSON is unexported, so we test it indirectly through other functions
	// or we can test the behavior through EnsureDir which uses similar patterns

	// For now, test that we can write JSON using AtomicWriteJSON from util package
	// which is what wisp would typically use
	err := writeJSON(testPath, data)
	if err != nil {
		t.Fatalf("writeJSON() error = %v", err)
	}

	// Verify file exists
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	// Verify JSON is valid
	var decoded TestData
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if decoded.Name != data.Name {
		t.Errorf("Name = %q, want %q", decoded.Name, data.Name)
	}
	if decoded.Value != data.Value {
		t.Errorf("Value = %d, want %d", decoded.Value, data.Value)
	}

	// Verify temp file was cleaned up
	tmpPath := testPath + ".tmp"
	if _, err := os.Stat(tmpPath); err == nil {
		t.Error("Temp file should be removed after successful write")
	}
}

func TestWriteJSON_Overwrite(t *testing.T) {
	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "test.json")

	// Write initial data
	initialData := map[string]string{"key": "initial"}
	if err := writeJSON(testPath, initialData); err != nil {
		t.Fatalf("writeJSON() initial error = %v", err)
	}

	// Overwrite with new data
	newData := map[string]string{"key": "updated", "new": "value"}
	if err := writeJSON(testPath, newData); err != nil {
		t.Fatalf("writeJSON() overwrite error = %v", err)
	}

	// Verify data was updated
	content, err := os.ReadFile(testPath)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	var decoded map[string]string
	if err := json.Unmarshal(content, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	if decoded["key"] != "updated" {
		t.Errorf("key = %q, want %q", decoded["key"], "updated")
	}
	if decoded["new"] != "value" {
		t.Errorf("new = %q, want %q", decoded["new"], "value")
	}
}
