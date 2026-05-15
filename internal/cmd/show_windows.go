//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/steveyegge/gastown/internal/beads"
)

// execBdShow runs 'bd show' with stdio passthrough on Windows.
// Resolves the correct rig directory from the bead's prefix via routes.jsonl
// so that rig-prefixed beads (e.g., myproject-abc) are found in their rig
// database rather than only the town-level hq database. (GH#2126)
func execBdShow(args []string) error {
	bdPath, err := exec.LookPath("bd")
	if err != nil {
		return fmt.Errorf("bd not found in PATH: %w", err)
	}

	beadsDir := ""
	if beadID := extractBeadIDFromArgs(args); beadID != "" {
		if dir := resolveBeadDir(beadID); dir != "" && dir != "." {
			_ = os.Chdir(dir)
			beadsDir = beads.ResolveBeadsDir(dir)
		}
	}

	env := pinBeadsDirEnv(os.Environ(), beadsDir)

	cmdArgs := append([]string{"show"}, args...)
	cmd := exec.Command(bdPath, cmdArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
