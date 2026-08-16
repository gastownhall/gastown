//go:build !windows

package git

import (
	"os/exec"

	"github.com/jonbaldie/gastown/internal/util"
)

// copyDirPreserving copies a directory using cp -a, which preserves symlinks,
// permissions, timestamps, and all file attributes.
func copyDirPreserving(src, dest string) error {
	cmd := exec.Command("cp", "-a", src, dest)
	util.SetDetachedProcessGroup(cmd)
	return cmd.Run()
}
