//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
)

// execBdShow runs 'bd show' with stdio passthrough on Windows.
// Resolves the correct rig directory from the bead's prefix via routes.jsonl
// so that rig-prefixed beads (e.g., myproject-abc) are found in their rig
// database rather than only the town-level hq database. (GH#2126)
func execBdShow(args []string) error {
	invocation := currentBdShowInvocation(args)
	cliName := invocation.ExecArgs[0]
	bdPath, err := exec.LookPath(cliName)
	if err != nil {
		return fmt.Errorf("%s not found in PATH: %w", cliName, err)
	}

	cmd := exec.Command(bdPath, invocation.CommandArgs...)
	cmd.Dir = invocation.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = invocation.Env

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		return err
	}
	os.Exit(0)
	return nil // unreachable
}
