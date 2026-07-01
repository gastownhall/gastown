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

// CheckBeadsVersion verifies that the env-requested issue tracker version meets
// the minimum requirement. Most command paths should call
// CheckBeadsVersionForTown so persisted town backend config is honored.
// Returns nil if the version is sufficient, or an error with details if not.
// The check is performed only once per process execution.
func CheckBeadsVersion() error {
	return CheckBeadsVersionForTown("")
}

// CheckBeadsVersionForTown verifies the effective issue tracker backend for a
// town. For minibeads-backed towns this checks mb, not bd.
func CheckBeadsVersionForTown(townRoot string) error {
	versionCheckOnce.Do(func() {
		backend := deps.EffectiveIssueTrackerBackend(townRoot)
		status, version := deps.CheckBeadsForBackend(backend)
		switch status {
		case deps.BeadsOK:
			cachedVersionCheckResult = nil
		case deps.BeadsUnknown:
			if backend == deps.IssueTrackerBackendMinibeads {
				cachedVersionCheckResult = fmt.Errorf("minibeads (mb) version could not be determined")
			} else {
				cachedVersionCheckResult = fmt.Errorf("beads (bd) version could not be determined\n\nTry reinstalling: go install %s", deps.BeadsInstallPath)
			}
		case deps.BeadsNotFound:
			if backend == deps.IssueTrackerBackendMinibeads {
				cachedVersionCheckResult = fmt.Errorf("minibeads (mb) not found in PATH")
			} else {
				cachedVersionCheckResult = fmt.Errorf("beads (bd) not found in PATH\n\nInstall with: go install %s", deps.BeadsInstallPath)
			}
		case deps.BeadsTooOld:
			cachedVersionCheckResult = fmt.Errorf("beads %s is required, but %s is installed\n\nUpgrade: go install %s",
				deps.MinBeadsVersion, version, deps.BeadsInstallPath)
		}
	})
	return cachedVersionCheckResult
}
