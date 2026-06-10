package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrIntegrityViolation classifies corrupted worktree metadata. Command paths
// that depend on hook/worktree state should fail closed when this is returned.
var ErrIntegrityViolation = errors.New("worktree integrity violation")

// IntegrityOptions controls worktree validation.
type IntegrityOptions struct {
	// TownRoot bounds the upward search for .git metadata. Empty means search to
	// the filesystem root.
	TownRoot string

	// Require reports a missing .git marker as an integrity violation. This is
	// appropriate only for cloned agent worktree roles (polecats, crew, refinery).
	// No-clone roles (witness, dog, boot) legitimately have no .git and must not
	// set Require, or gt prime aborts in those directories.
	Require bool
}

// Validate checks the nearest worktree metadata for path. It accepts regular
// clones (.git directory) and validates linked worktree .git files by ensuring
// they are well formed and point at usable git metadata.
func Validate(path string, opts IntegrityOptions) error {
	if path == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("%w: get cwd: %v", ErrIntegrityViolation, err)
		}
		path = cwd
	}

	marker, found, err := findGitMarker(path, opts.TownRoot)
	if err != nil {
		return err
	}
	if !found {
		if opts.Require {
			return fmt.Errorf("%w: missing .git metadata under %s", ErrIntegrityViolation, path)
		}
		return nil
	}

	info, err := os.Stat(marker)
	if err != nil {
		return fmt.Errorf("%w: cannot stat %s: %v", ErrIntegrityViolation, marker, err)
	}
	if info.IsDir() {
		return validateGitDir(marker, marker)
	}

	content, err := os.ReadFile(marker)
	if err != nil {
		return fmt.Errorf("%w: cannot read %s: %v", ErrIntegrityViolation, marker, err)
	}

	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir: ") {
		return fmt.Errorf("%w: malformed .git file at %s", ErrIntegrityViolation, marker)
	}

	target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
	if target == "" {
		return fmt.Errorf("%w: empty gitdir target in %s", ErrIntegrityViolation, marker)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Clean(filepath.Join(filepath.Dir(marker), target))
	}

	if info, err := os.Stat(target); err != nil {
		return fmt.Errorf("%w: gitdir target missing for %s: %s", ErrIntegrityViolation, marker, target)
	} else if !info.IsDir() {
		return fmt.Errorf("%w: gitdir target is not a directory for %s: %s", ErrIntegrityViolation, marker, target)
	}

	if err := validateGitDir(target, marker); err != nil {
		return err
	}

	return nil
}

func validateGitDir(gitdir, marker string) error {
	if _, err := os.Stat(filepath.Join(gitdir, "HEAD")); err != nil {
		return fmt.Errorf("%w: gitdir metadata incomplete for %s: missing HEAD in %s", ErrIntegrityViolation, marker, gitdir)
	}
	return nil
}

func findGitMarker(path, townRoot string) (string, bool, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", false, fmt.Errorf("%w: resolve path %s: %v", ErrIntegrityViolation, path, err)
	}

	var stop string
	if townRoot != "" {
		stop, err = filepath.Abs(townRoot)
		if err != nil {
			return "", false, fmt.Errorf("%w: resolve town root %s: %v", ErrIntegrityViolation, townRoot, err)
		}
	}

	for {
		marker := filepath.Join(path, ".git")
		if _, err := os.Lstat(marker); err == nil {
			return marker, true, nil
		} else if !os.IsNotExist(err) {
			return "", false, fmt.Errorf("%w: cannot inspect %s: %v", ErrIntegrityViolation, marker, err)
		}

		if stop != "" && path == stop {
			break
		}
		parent := filepath.Dir(path)
		if parent == path {
			break
		}
		path = parent
	}

	return "", false, nil
}
