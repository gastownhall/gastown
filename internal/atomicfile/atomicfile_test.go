package atomicfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.json")

	data := map[string]string{"key": "value"}
	if err := WriteJSON(testFile, data); err != nil {
		t.Fatalf("WriteJSON error: %v", err)
	}

	if _, err := os.Stat(testFile); os.IsNotExist(err) {
		t.Fatal("File was not created")
	}

	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if e.Name() != "test.json" {
			t.Fatalf("Temp file was not cleaned up: %s", e.Name())
		}
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(content) != "{\n  \"key\": \"value\"\n}" {
		t.Fatalf("Unexpected content: %s", content)
	}
}

func TestWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	data := []byte("hello world")
	if err := WriteFile(testFile, data, 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(content) != "hello world" {
		t.Fatalf("Unexpected content: %s", content)
	}

	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		if e.Name() != "test.txt" {
			t.Fatalf("Temp file was not cleaned up: %s", e.Name())
		}
	}
}

func TestWriteOverwrite(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.json")

	if err := WriteJSON(testFile, "first"); err != nil {
		t.Fatalf("First write error: %v", err)
	}

	if err := WriteJSON(testFile, "second"); err != nil {
		t.Fatalf("Second write error: %v", err)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(content) != "\"second\"" {
		t.Fatalf("Unexpected content: %s", content)
	}
}

func TestWriteFilePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")

	data := []byte("test data")
	if err := WriteFile(testFile, data, 0600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	perm := info.Mode().Perm()
	if perm&0600 != 0600 {
		t.Errorf("Expected owner read/write permissions, got %o", perm)
	}
}

func TestWriteFileEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "empty.txt")

	if err := WriteFile(testFile, []byte{}, 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if len(content) != 0 {
		t.Fatalf("Expected empty file, got %d bytes", len(content))
	}
}

func TestWriteJSONTypes(t *testing.T) {
	tmpDir := t.TempDir()

	tests := []struct {
		name     string
		data     interface{}
		expected string
	}{
		{"string", "hello", `"hello"`},
		{"int", 42, "42"},
		{"float", 3.14, "3.14"},
		{"bool", true, "true"},
		{"null", nil, "null"},
		{"array", []int{1, 2, 3}, "[\n  1,\n  2,\n  3\n]"},
		{"nested", map[string]interface{}{"a": map[string]int{"b": 1}}, "{\n  \"a\": {\n    \"b\": 1\n  }\n}"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			testFile := filepath.Join(tmpDir, tc.name+".json")
			if err := WriteJSON(testFile, tc.data); err != nil {
				t.Fatalf("WriteJSON error: %v", err)
			}

			content, err := os.ReadFile(testFile)
			if err != nil {
				t.Fatalf("ReadFile error: %v", err)
			}
			if string(content) != tc.expected {
				t.Errorf("Expected %q, got %q", tc.expected, string(content))
			}
		})
	}
}

func TestWriteJSONUnmarshallable(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "unmarshallable.json")

	ch := make(chan int)
	err := WriteJSON(testFile, ch)
	if err == nil {
		t.Fatal("Expected error for unmarshallable type")
	}

	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Fatal("File should not exist after marshal error")
	}

	entries, _ := os.ReadDir(tmpDir)
	for _, e := range entries {
		t.Fatalf("Unexpected file after marshal error: %s", e.Name())
	}
}

func TestWriteFileReadOnlyDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only directories are not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permission bits; chmod read-only is not enforceable")
	}

	tmpDir := t.TempDir()
	roDir := filepath.Join(tmpDir, "readonly")

	if err := os.Mkdir(roDir, 0555); err != nil {
		t.Fatalf("Failed to create readonly dir: %v", err)
	}
	defer os.Chmod(roDir, 0755)

	testFile := filepath.Join(roDir, "test.txt")
	err := WriteFile(testFile, []byte("test"), 0644)
	if err == nil {
		t.Fatal("Expected permission error")
	}

	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Fatal("File should not exist after permission error")
	}
}

func TestWriteFileConcurrent(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "concurrent.txt")

	if err := WriteFile(testFile, []byte("initial"), 0644); err != nil {
		t.Fatalf("Initial write error: %v", err)
	}

	const numWriters = 10
	var wg sync.WaitGroup
	errs := make([]error, numWriters)
	wg.Add(numWriters)

	for i := 0; i < numWriters; i++ {
		go func(n int) {
			defer wg.Done()
			data := []byte(string(rune('A' + n)))
			errs[n] = WriteFile(testFile, data, 0644)
		}(i)
	}

	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if runtime.GOOS == "windows" {
		if len(content) == 0 {
			t.Error("Expected non-empty content on Windows")
		}
	} else if len(content) != 1 {
		t.Errorf("Expected single character, got %q", content)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir error: %v", err)
	}
	for _, e := range entries {
		if e.Name() != "concurrent.txt" {
			t.Errorf("Temp file left behind: %s", e.Name())
		}
	}
}

func TestWritePreservesOnFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based read-only directories are not reliable on Windows")
	}
	if os.Getuid() == 0 {
		t.Skip("root bypasses directory permission bits; chmod read-only is not enforceable")
	}

	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "preserve.txt")

	initialContent := []byte("original content")
	if err := WriteFile(testFile, initialContent, 0644); err != nil {
		t.Fatalf("Initial write error: %v", err)
	}

	if err := os.Chmod(tmpDir, 0555); err != nil {
		t.Fatalf("Failed to make dir read-only: %v", err)
	}
	defer os.Chmod(tmpDir, 0755)

	err := WriteFile(testFile, []byte("new content"), 0644)
	if err == nil {
		t.Fatal("Expected error when directory is read-only")
	}

	os.Chmod(tmpDir, 0755)

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(content) != string(initialContent) {
		t.Errorf("Original content not preserved: got %q", content)
	}
}

func TestWriteJSONStruct(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "struct.json")

	type TestStruct struct {
		Name    string   `json:"name"`
		Count   int      `json:"count"`
		Enabled bool     `json:"enabled"`
		Tags    []string `json:"tags"`
	}

	data := TestStruct{
		Name:    "test",
		Count:   42,
		Enabled: true,
		Tags:    []string{"a", "b"},
	}

	if err := WriteJSON(testFile, data); err != nil {
		t.Fatalf("WriteJSON error: %v", err)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	var result TestStruct
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}

	if result.Name != data.Name || result.Count != data.Count ||
		result.Enabled != data.Enabled || len(result.Tags) != len(data.Tags) {
		t.Errorf("Data mismatch: got %+v, want %+v", result, data)
	}
}

func TestWriteFileLargeData(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "large.bin")

	size := 1024 * 1024
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i % 256)
	}

	if err := WriteFile(testFile, data, 0644); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if len(content) != size {
		t.Errorf("Size mismatch: got %d, want %d", len(content), size)
	}
	for i := 0; i < size; i++ {
		if content[i] != byte(i%256) {
			t.Errorf("Content mismatch at byte %d", i)
			break
		}
	}
}

func TestWriteJSONWithPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions not meaningful on Windows")
	}
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "perm.json")

	if err := WriteJSONWithPerm(testFile, map[string]int{"n": 1}, 0600); err != nil {
		t.Fatalf("WriteJSONWithPerm error: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Expected 0600, got %o", info.Mode().Perm())
	}

	var got map[string]int
	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if err := json.Unmarshal(content, &got); err != nil {
		t.Fatalf("Unmarshal error: %v", err)
	}
	if got["n"] != 1 {
		t.Errorf("Expected {n:1}, got %v", got)
	}
}

func TestEnsureDirAndWriteJSON(t *testing.T) {
	tmpDir := t.TempDir()
	// Target sits two directory levels below tmpDir — neither exists yet.
	testFile := filepath.Join(tmpDir, "a", "b", "cfg.json")

	if err := EnsureDirAndWriteJSON(testFile, map[string]string{"k": "v"}); err != nil {
		t.Fatalf("EnsureDirAndWriteJSON error: %v", err)
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if string(content) != "{\n  \"k\": \"v\"\n}" {
		t.Errorf("Unexpected content: %s", content)
	}
}

func TestEnsureDirAndWriteJSONWithPerm(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions not meaningful on Windows")
	}
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "x", "y", "cfg.json")

	if err := EnsureDirAndWriteJSONWithPerm(testFile, map[string]string{"k": "v"}, 0600); err != nil {
		t.Fatalf("EnsureDirAndWriteJSONWithPerm error: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("Expected 0600, got %o", info.Mode().Perm())
	}
}

func TestWriteJSONWithPermUnmarshallable(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "unmarshallable.json")

	if err := WriteJSONWithPerm(testFile, make(chan int), 0600); err == nil {
		t.Fatal("Expected error for unmarshallable type")
	}

	if _, statErr := os.Stat(testFile); !os.IsNotExist(statErr) {
		t.Fatal("File should not exist after marshal error")
	}
}

func TestEnsureDirAndWriteJSONMkdirFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("blocking-file trick for MkdirAll isn't reliable on Windows")
	}
	tmpDir := t.TempDir()
	// A regular file where a directory is expected in the target's ancestry:
	// MkdirAll fails because "blocker" exists as a file.
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	testFile := filepath.Join(blocker, "sub", "cfg.json")

	if err := EnsureDirAndWriteJSON(testFile, map[string]string{"k": "v"}); err == nil {
		t.Fatal("Expected MkdirAll error, got nil")
	}
}

func TestEnsureDirAndWriteJSONWithPermMkdirFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("blocking-file trick for MkdirAll isn't reliable on Windows")
	}
	tmpDir := t.TempDir()
	blocker := filepath.Join(tmpDir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	testFile := filepath.Join(blocker, "sub", "cfg.json")

	if err := EnsureDirAndWriteJSONWithPerm(testFile, map[string]string{"k": "v"}, 0600); err == nil {
		t.Fatal("Expected MkdirAll error, got nil")
	}
}

func TestWriteFileConcurrentIntegrity(t *testing.T) {
	// Concurrent writers to the same path must each produce self-consistent
	// content (no cross-writer byte mixing).
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "integrity.txt")

	const numWriters = 20
	const dataSize = 1024

	var wg sync.WaitGroup
	errs := make([]error, numWriters)
	wg.Add(numWriters)

	for i := 0; i < numWriters; i++ {
		go func(n int) {
			defer wg.Done()
			data := make([]byte, dataSize)
			for j := range data {
				data[j] = byte(n)
			}
			errs[n] = WriteFile(testFile, data, 0644)
		}(i)
	}

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}

	content, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}
	if len(content) != dataSize {
		t.Fatalf("Expected %d bytes, got %d", dataSize, len(content))
	}
	expected := content[0]
	for i, b := range content {
		if b != expected {
			t.Fatalf("Data corruption at byte %d: expected %d, got %d (cross-writer contamination)", i, expected, b)
		}
	}
}
