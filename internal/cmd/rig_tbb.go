package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

// rejectTbbRepo refuses a repository checkout that is already managed by tbb
// (it contains a .tbb/ directory). gtb and tbb keep separate workflow state,
// so gtb must not take over a tbb repo.
func rejectTbbRepo(checkoutDir string) error {
	if _, err := os.Stat(filepath.Join(checkoutDir, ".tbb")); err == nil {
		return fmt.Errorf("%s uses tbb (.tbb/ found); gtb does not manage tbb repos", checkoutDir)
	}
	return nil
}

// rejectTbbRig applies rejectTbbRepo to every place an adopted rig's working
// checkout may live: the rig directory itself (a plain repo), the mayor clone
// and the refinery worktree.
func rejectTbbRig(rigPath string) error {
	for _, dir := range []string{
		rigPath,
		filepath.Join(rigPath, "mayor", "rig"),
		filepath.Join(rigPath, "refinery", "rig"),
	} {
		if err := rejectTbbRepo(dir); err != nil {
			return err
		}
	}
	return nil
}
