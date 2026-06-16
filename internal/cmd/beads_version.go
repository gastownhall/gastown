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
			cachedVersionCheckResult = fmt.Errorf("beads (bd) version or module provenance could not be verified\n\nTry reinstalling: go install %s", deps.BeadsInstallPath)
		case deps.BeadsNotFound:
			cachedVersionCheckResult = fmt.Errorf("beads (bd) not found in PATH\n\nInstall with: go install %s", deps.BeadsInstallPath)
		case deps.BeadsTooOld:
			cachedVersionCheckResult = fmt.Errorf("beads %s is required, but %s is installed\n\nUpgrade: go install %s",
				deps.MinBeadsVersion, version, deps.BeadsInstallPath)
		case deps.BeadsModuleMismatch:
			cachedVersionCheckResult = fmt.Errorf("beads module %s is required, but bd was built with %s\n\nUpgrade: go install %s",
				deps.RequiredBeadsModuleVersion, version, deps.BeadsInstallPath)
		}
	})
	return cachedVersionCheckResult
}
