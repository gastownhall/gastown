// Package cmd provides CLI commands for the gt tool.
package cmd

import (
	"fmt"
	"sync"

	"github.com/steveyegge/gastown/internal/deps"
)

var (
	cachedVersionCheckResult error
	versionCheckOnce         sync.Once
)

// CheckBeadsVersion verifies that the installed beads version meets the minimum requirement.
// Returns nil if the version is sufficient, or an error with details if not.
// The check is performed only once per process execution.
func CheckBeadsVersion() error {
	versionCheckOnce.Do(func() {
		status, version := deps.CheckBeads()
		switch status {
		case deps.BeadsOK:
			cachedVersionCheckResult = nil
		case deps.BeadsUnknown:
			cachedVersionCheckResult = fmt.Errorf("beads (bd) version could not be determined\n\nTry reinstalling: %s", deps.BeadsInstallHint)
		case deps.BeadsNotFound:
			cachedVersionCheckResult = fmt.Errorf("beads (bd) not found in PATH\n\nInstall: %s", deps.BeadsInstallHint)
		case deps.BeadsTooOld:
			cachedVersionCheckResult = fmt.Errorf("beads %s is required, but %s is installed\n\nUpgrade: %s",
				deps.MinBeadsVersion, version, deps.BeadsInstallHint)
		case deps.BeadsWrongDistribution:
			cachedVersionCheckResult = fmt.Errorf("beads %s is not the governed downstream distribution\n\nReplace it: %s",
				version, deps.BeadsInstallHint)
		}
	})
	return cachedVersionCheckResult
}
