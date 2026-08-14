//go:build windows

package atomicfile

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestReplaceFileRetriesSharingViolation(t *testing.T) {
	dir := t.TempDir()
	oldPath := filepath.Join(dir, "old")
	newPath := filepath.Join(dir, "new")
	if err := os.WriteFile(oldPath, []byte("new"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newPath, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	attempts := 0
	err := replaceFileWith(oldPath, newPath, func(oldPath, newPath string) error {
		attempts++
		if attempts == 1 {
			return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.Errno(32)}
		}
		return os.Rename(oldPath, newPath)
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 {
		t.Fatalf("rename attempts = %d, want 2", attempts)
	}
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("replacement content = %q", data)
	}
}

func TestWriteFileRetriesOpenDestination(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := WriteFile(path, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}

	openFile, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	writeResult := make(chan error, 1)
	go func() {
		close(started)
		writeResult <- WriteFile(path, []byte("new"), 0644)
	}()
	<-started

	// Wait until the writer has created its temp file. If replacement fails
	// immediately instead of retrying, writeResult reports that error here.
	attemptDeadline := time.Now().Add(time.Second)
	completed := false
	for {
		select {
		case err := <-writeResult:
			if err != nil {
				t.Fatalf("replacement did not retry sharing contention: %v", err)
			}
			completed = true
		default:
		}
		if completed {
			break
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		attempted := false
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "config.json.tmp.") {
				attempted = true
				break
			}
		}
		if attempted {
			select {
			case err := <-writeResult:
				if err != nil {
					t.Fatalf("replacement did not retry sharing contention: %v", err)
				}
				completed = true
			case <-time.After(50 * time.Millisecond):
				// The destination is still open, so a sharing failure should be
				// held inside the retry loop rather than returned to the caller.
			}
			break
		}
		if time.Now().After(attemptDeadline) {
			t.Fatal("writer did not attempt atomic replacement")
		}
		time.Sleep(time.Millisecond)
	}

	oldData, err := io.ReadAll(openFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(oldData) != "old" {
		t.Fatalf("open handle read %q, want old content", oldData)
	}
	if err := openFile.Close(); err != nil {
		t.Fatal(err)
	}
	if !completed {
		select {
		case err := <-writeResult:
			if err != nil {
				t.Fatalf("replace destination after reader closed: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("replacement did not complete after reader closed")
		}
	}
	newData, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(newData) != "new" {
		t.Fatalf("new handle read %q, want new content", newData)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "config.json" {
		t.Fatalf("unexpected files after replacement: %v", entries)
	}
}
