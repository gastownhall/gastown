package polecat

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// WorktreeIntegrityError marks structural worktree damage that is safe to
// distinguish from transient git, permission, or tooling failures.
type WorktreeIntegrityError struct {
	Path string
	Err  error
}

func (e *WorktreeIntegrityError) Error() string {
	if e.Err == nil {
		return e.Path
	}
	return e.Err.Error()
}

func (e *WorktreeIntegrityError) Unwrap() error {
	return e.Err
}

// IsStructuralWorktreeError reports whether err proves the worktree shape is
// broken, rather than only proving git could not answer a runtime query.
func IsStructuralWorktreeError(err error) bool {
	var integrityErr *WorktreeIntegrityError
	return errors.As(err, &integrityErr)
}

func structuralWorktreeError(path string, format string, args ...any) error {
	return &WorktreeIntegrityError{Path: path, Err: fmt.Errorf(format, args...)}
}

// VerifyWorktreeExists checks that clonePath is a git worktree whose .git
// indirection points at an existing gitdir.
func VerifyWorktreeExists(clonePath string) error {
	info, err := os.Stat(clonePath)
	if err != nil {
		if os.IsNotExist(err) {
			return structuralWorktreeError(clonePath, "worktree directory does not exist: %s", clonePath)
		}
		return fmt.Errorf("checking worktree directory: %w", err)
	}
	if !info.IsDir() {
		return structuralWorktreeError(clonePath, "worktree path is not a directory: %s", clonePath)
	}

	gitPath := filepath.Join(clonePath, ".git")
	if _, err := os.Stat(gitPath); err != nil {
		if os.IsNotExist(err) {
			return structuralWorktreeError(clonePath, "worktree missing .git file (not a valid git worktree): %s", clonePath)
		}
		return fmt.Errorf("checking .git: %w", err)
	}

	gitContent, err := os.ReadFile(gitPath)
	if err == nil {
		content := strings.TrimSpace(string(gitContent))
		if strings.HasPrefix(content, "gitdir: ") {
			gitdirPath := strings.TrimPrefix(content, "gitdir: ")
			if !filepath.IsAbs(gitdirPath) {
				gitdirPath = filepath.Join(clonePath, gitdirPath)
			}
			if _, err := os.Stat(gitdirPath); err != nil {
				if os.IsNotExist(err) {
					return structuralWorktreeError(clonePath, "worktree .git references nonexistent gitdir %s: %w", gitdirPath, err)
				}
				return fmt.Errorf("checking worktree gitdir %s: %w", gitdirPath, err)
			}
		}
	}

	cmd := exec.Command("git", "-C", clonePath, "rev-parse", "--git-dir")
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("worktree at %s is not a valid git repository: %s", clonePath, strings.TrimSpace(string(output)))
	}

	return nil
}
