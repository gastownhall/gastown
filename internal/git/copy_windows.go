//go:build windows

package git

import (
	"os/exec"

	"github.com/jonbaldie/gastown/internal/util"
)

// copyDirPreserving copies a directory using robocopy, which preserves symlinks,
// permissions, timestamps, and all file attributes.
//
// NOTE: This Windows implementation has not been tested on Windows.
func copyDirPreserving(src, dest string) error {
	// robocopy flags:
	// /E - copy subdirectories including empty ones
	// /COPYALL - copy all file info (data, attributes, timestamps, security, owner, auditing)
	// /SL - copy symbolic links as links (not targets)
	// /R:0 - retry 0 times on failure
	// /W:0 - wait 0 seconds between retries
	//
	// Note: robocopy returns exit code 1 for successful copy with files copied,
	// so we only treat >= 8 as error (see robocopy documentation)
	cmd := exec.Command("robocopy", src, dest, "/E", "/COPYALL", "/SL", "/R:0", "/W:0")
	util.SetDetachedProcessGroup(cmd)
	err := cmd.Run()
	if err != nil {
		// robocopy exit codes: 0-7 are success/warnings, >= 8 are errors
		if exitErr, ok := err.(*exec.ExitError); ok {
			if exitErr.ExitCode() < 8 {
				return nil // Success or warning
			}
		}
		return err
	}
	return nil
}
