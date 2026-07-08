package polecat

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyWorktreeExistsStructuralFailures(t *testing.T) {
	tmp := t.TempDir()

	t.Run("missing directory", func(t *testing.T) {
		err := VerifyWorktreeExists(filepath.Join(tmp, "missing"))
		if !IsStructuralWorktreeError(err) {
			t.Fatalf("VerifyWorktreeExists missing dir structural = false, err=%v", err)
		}
		if !IsStructuralWorktreeError(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("wrapped structural error was not classified as structural: %v", err)
		}
	})

	t.Run("path is file", func(t *testing.T) {
		path := filepath.Join(tmp, "file")
		if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
		err := VerifyWorktreeExists(path)
		if !IsStructuralWorktreeError(err) {
			t.Fatalf("VerifyWorktreeExists file path structural = false, err=%v", err)
		}
	})

	t.Run("missing git file", func(t *testing.T) {
		path := filepath.Join(tmp, "missing-git")
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		err := VerifyWorktreeExists(path)
		if !IsStructuralWorktreeError(err) {
			t.Fatalf("VerifyWorktreeExists missing .git structural = false, err=%v", err)
		}
	})

	t.Run("broken gitdir reference", func(t *testing.T) {
		path := filepath.Join(tmp, "broken-gitdir")
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("gitdir: missing-gitdir\n"), 0644); err != nil {
			t.Fatal(err)
		}
		err := VerifyWorktreeExists(path)
		if !IsStructuralWorktreeError(err) {
			t.Fatalf("VerifyWorktreeExists broken gitdir structural = false, err=%v", err)
		}
	})

	t.Run("invalid git content is non structural", func(t *testing.T) {
		path := filepath.Join(tmp, "invalid-git")
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, ".git"), []byte("not a gitdir"), 0644); err != nil {
			t.Fatal(err)
		}
		err := VerifyWorktreeExists(path)
		if err == nil {
			t.Fatal("VerifyWorktreeExists invalid git content returned nil")
		}
		if IsStructuralWorktreeError(err) {
			t.Fatalf("VerifyWorktreeExists invalid git content structural = true, err=%v", err)
		}
	})
}
