//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// execBdShow replaces the current process with 'bd show'.
// Resolves the correct rig directory from the bead's prefix via routes.jsonl
// so that rig-prefixed beads (e.g., myproject-abc) are found in their rig
// database rather than only the town-level hq database. (GH#2126)
func execBdShow(args []string) error {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: %w", err)
	}

	invocation := currentBdShowInvocation(args)
	bdPath, err = prepareBdShowExec(bdPath, invocation)
	if err != nil {
		return err
	}

	return syscall.Exec(bdPath, invocation.ExecArgs, invocation.Env)
}

func prepareBdShowExec(bdPath string, invocation bdShowInvocation) (string, error) {
	if !filepath.IsAbs(bdPath) {
		abs, err := filepath.Abs(bdPath)
		if err != nil {
			return "", fmt.Errorf("resolve bd path %q: %w", bdPath, err)
		}
		bdPath = abs
	}
	if invocation.Dir != "" {
		if err := os.Chdir(invocation.Dir); err != nil {
			return "", fmt.Errorf("chdir %s: %w", invocation.Dir, err)
		}
	}
	return bdPath, nil
}
