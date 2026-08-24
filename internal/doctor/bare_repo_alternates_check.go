package doctor

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BareRepoAlternatesCheck detects a shared bare repo whose objects/info/alternates
// file references an external checkout that no longer exists. Gas Town clones
// sometimes borrow objects from another local repo via `git clone --reference-if-able`
// to save disk; if that external repo is later deleted (a sibling rig removed, an
// unrelated local checkout cleaned up), the bare repo silently loses access to every
// object it never copied for itself. The break stays invisible until a `git log`,
// `git fetch`, or worktree checkout needs one of those objects — this check catches
// it during `gt doctor` instead.
type BareRepoAlternatesCheck struct {
	FixableCheck
	stalePaths []string // alternates entries whose target directory is gone
}

// NewBareRepoAlternatesCheck creates a new stale-alternates check.
func NewBareRepoAlternatesCheck() *BareRepoAlternatesCheck {
	return &BareRepoAlternatesCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "bare-repo-alternates",
				CheckDescription: "Verify shared bare repo does not depend on a removed external alternates path",
				CheckCategory:    CategoryRig,
			},
		},
	}
}

// alternatesFilePath returns the path to a bare repo's alternates file.
func alternatesFilePath(bareRepoPath string) string {
	return filepath.Join(bareRepoPath, "objects", "info", "alternates")
}

// resolveAlternateTarget resolves an alternates file entry to an absolute path.
// Per gitrepository-layout(5), relative entries are relative to $GIT_DIR/objects.
func resolveAlternateTarget(bareRepoPath, line string) string {
	if filepath.IsAbs(line) {
		return line
	}
	return filepath.Join(bareRepoPath, "objects", line)
}

// readAlternates returns the non-empty, non-comment lines of a bare repo's
// alternates file. A missing file is not an error — it means nothing is configured.
func readAlternates(bareRepoPath string) ([]string, error) {
	data, err := os.ReadFile(alternatesFilePath(bareRepoPath))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines, scanner.Err()
}

// splitLiveDead partitions alternates entries into ones whose target directory
// still exists and ones whose target directory is gone.
func splitLiveDead(bareRepoPath string, lines []string) (live, dead []string) {
	for _, line := range lines {
		if _, err := os.Stat(resolveAlternateTarget(bareRepoPath, line)); os.IsNotExist(err) {
			dead = append(dead, line)
		} else {
			live = append(live, line)
		}
	}
	return live, dead
}

// unreachableOIDs lists objects that registered refs (branches and tags) need
// but cannot find in the bare repo's current object store (local + alternates).
// Uses `rev-list --missing=print` so a broken chain is reported, not fatal.
func unreachableOIDs(bareRepoPath string) ([]string, error) {
	cmd := exec.Command("git", "-C", bareRepoPath, "rev-list", "--objects",
		"--branches", "--tags", "--missing=print")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git rev-list failed: %s", msg)
	}
	var missing []string
	scanner := bufio.NewScanner(bytes.NewReader(stdout.Bytes()))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "?") {
			missing = append(missing, strings.Fields(strings.TrimPrefix(line, "?"))[0])
		}
	}
	return missing, scanner.Err()
}

// Run checks whether the rig's bare repo has an alternates entry pointing at a
// directory that no longer exists.
func (c *BareRepoAlternatesCheck) Run(ctx *CheckContext) *CheckResult {
	if ctx.RigName == "" {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No rig specified (skipping alternates check)",
			Category: c.Category(),
		}
	}

	bareRepoPath := filepath.Join(ctx.RigPath(), ".repo.git")
	if _, err := os.Stat(bareRepoPath); err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No shared bare repo found",
			Category: c.Category(),
		}
	}

	c.stalePaths = nil
	lines, err := readAlternates(bareRepoPath)
	if err != nil {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusWarning,
			Message:  "Cannot read objects/info/alternates",
			Details:  []string{err.Error()},
			Category: c.Category(),
		}
	}
	if len(lines) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  "No external alternates configured",
			Category: c.Category(),
		}
	}

	_, dead := splitLiveDead(bareRepoPath, lines)
	c.stalePaths = dead
	if len(dead) == 0 {
		return &CheckResult{
			Name:     c.Name(),
			Status:   StatusOK,
			Message:  fmt.Sprintf("%d external alternate(s) configured and reachable", len(lines)),
			Category: c.Category(),
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: fmt.Sprintf("%d stale external alternate(s) — objects may become unreadable", len(dead)),
		Details: append([]string{
			"Removed external checkout(s) still referenced by objects/info/alternates:",
		}, dead...),
		FixHint:  "Run 'gt doctor --fix --rig " + ctx.RigName + "' to internalize reachable objects and drop the stale alternate(s)",
		Category: c.Category(),
	}
}

// Fix internalizes every object that registered refs (branches, tags) can still
// reach, then drops alternates entries whose target is gone. It never trades
// data for a clean report: if any object required by a ref cannot be proven
// present afterward (locally, or by refetching from the configured remote), the
// alternates file is restored exactly as found and Fix returns an error listing
// the unreachable objects.
func (c *BareRepoAlternatesCheck) Fix(ctx *CheckContext) error {
	if ctx.RigName == "" {
		return nil
	}

	bareRepoPath := filepath.Join(ctx.RigPath(), ".repo.git")
	altPath := alternatesFilePath(bareRepoPath)

	// Re-read and re-verify before mutating — Run may have flagged this minutes
	// ago and the state could have changed since (TOCTOU guard).
	origContent, err := os.ReadFile(altPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing to fix
		}
		return fmt.Errorf("read alternates: %w", err)
	}
	lines, err := readAlternates(bareRepoPath)
	if err != nil {
		return fmt.Errorf("read alternates: %w", err)
	}
	live, dead := splitLiveDead(bareRepoPath, lines)
	if len(dead) == 0 {
		return nil // nothing stale to fix
	}

	// Stage the pruned alternates (dead entries removed, live entries kept) so
	// we can prove ref reachability using only what would remain after the fix.
	newContent := ""
	if len(live) > 0 {
		newContent = strings.Join(live, "\n") + "\n"
	}
	restore := func() error { return os.WriteFile(altPath, origContent, 0644) }

	if len(live) == 0 {
		if err := os.Remove(altPath); err != nil {
			return fmt.Errorf("stage alternates for verification: %w", err)
		}
	} else if err := os.WriteFile(altPath, []byte(newContent), 0644); err != nil {
		return fmt.Errorf("stage alternates for verification: %w", err)
	}

	missing, proofErr := unreachableOIDs(bareRepoPath)
	if proofErr != nil {
		_ = restore()
		return fmt.Errorf("prove ref reachability: %w", proofErr)
	}

	if len(missing) > 0 {
		// Best-effort recovery: the objects are gone from the dead alternate,
		// but a ref that mirrors something on the remote can often be repaired
		// by refetching it. Refs with no upstream (typical worker branches)
		// won't be helped by this — that's expected and handled below.
		refetchErr := exec.Command("git", "-C", bareRepoPath, "fetch", "origin",
			"+refs/heads/*:refs/remotes/origin/*", "--prune").Run()
		if refetchErr == nil {
			missing, proofErr = unreachableOIDs(bareRepoPath)
			if proofErr != nil {
				_ = restore()
				return fmt.Errorf("prove ref reachability after refetch: %w", proofErr)
			}
		}
	}

	if len(missing) > 0 {
		// Fail closed: restoring the dead pointer doesn't recover anything
		// either (the target is gone), but silently declaring the repo
		// self-contained while refs still need it would hide real data loss.
		_ = restore()
		return fmt.Errorf("refusing to drop stale alternate(s) %v: %d object(s) still unreachable from registered refs: %s",
			dead, len(missing), strings.Join(missing, ", "))
	}

	return nil
}
