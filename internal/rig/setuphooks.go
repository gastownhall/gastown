// Package rig provides rig-level configuration and utilities.
package rig

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"

	"github.com/jonbaldie/gastown/internal/style"
)

// RunSetupHooks executes setup hooks found in <rigPath>/.runtime/setup-hooks/.
// These hooks run in the context of the newly created worktree and can inject
// local configurations, run custom scripts, or perform other setup tasks.
//
// Hook Execution Order:
// Hooks are executed in alphabetical order by filename. Each hook is run
// with the worktree path as its working directory.
//
// Hook Requirements:
// - Hooks must be executable (chmod +x)
// - Hooks can be shell scripts, binaries, or any executable file
// - Non-executable files are skipped with a warning
// - Hook failures are logged as warnings but don't stop worktree creation
//
// Directory Structure:
//
//	rig/
//	  .runtime/
//	    setup-hooks/
//	      01-git-config.sh    <- Run first
//	      02-copy-secrets.sh  <- Run second
//	      99-finalize.sh      <- Run last
//
// Returns nil if the setup-hooks directory doesn't exist (nothing to run).
// Individual hook failures are logged as warnings but don't fail the overall operation.
func RunSetupHooks(rigPath, worktreePath string) error {
	hooksDir := filepath.Join(rigPath, ".runtime", "setup-hooks")

	// Check if setup-hooks directory exists
	entries, err := os.ReadDir(hooksDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No setup-hooks directory - not an error, just nothing to run
			return nil
		}
		return fmt.Errorf("reading setup-hooks dir: %w", err)
	}

	if len(entries) == 0 {
		// Directory exists but is empty - nothing to run
		return nil
	}

	// Sort hooks alphabetically for consistent execution order
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	// Execute each hook
	for _, entry := range entries {
		if entry.IsDir() {
			// Skip subdirectories
			continue
		}

		hookPath := filepath.Join(hooksDir, entry.Name())

		// Check if file is executable
		info, err := entry.Info()
		if err != nil {
			style.PrintWarning("could not stat hook %s: %v", entry.Name(), err)
			continue
		}

		// Skip non-executable files (warn user)
		if info.Mode().Perm()&0111 == 0 {
			style.PrintWarning("skipping non-executable hook %s (use chmod +x to make it executable)", entry.Name())
			continue
		}

		// Execute the hook
		if err := runHook(hookPath, worktreePath); err != nil {
			// Log warning but continue - don't fail spawn for hook failures
			style.PrintWarning("setup hook %s failed: %v", entry.Name(), err)
			continue
		}

		fmt.Printf("Ran setup hook: %s\n", entry.Name())
	}

	return nil
}

// runHook executes a single hook script in the context of the worktree.
// The hook is run with:
// - Working directory set to worktreePath
// - Environment variable GT_WORKTREE_PATH pointing to the worktree
// - Environment variable GT_RIG_PATH pointing to the rig
// hookTimeout is the maximum time a setup hook is allowed to run.
const hookTimeout = 60 * time.Second

func runHook(hookPath, worktreePath string) error {
	// Get the rig path from the hook path (strip .runtime/setup-hooks/)
	rigPath := filepath.Dir(filepath.Dir(filepath.Dir(hookPath)))

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, hookPath)
	cmd.Dir = worktreePath
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GT_WORKTREE_PATH=%s", worktreePath),
		fmt.Sprintf("GT_RIG_PATH=%s", rigPath),
	)

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("timed out after %s", hookTimeout)
		}
		return err
	}
	return nil
}
