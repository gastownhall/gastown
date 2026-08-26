// Package deps manages external dependencies for Gas Town.
package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// MinBeadsVersion is the minimum compatible downstream beads release.
const MinBeadsVersion = "1.2.2-dc3"

// BeadsInstallPath identifies the maintained downstream distribution. The fork
// intentionally retains the upstream Go module path, so `go install` cannot
// select its release tags safely; installation must use the governed beads rig.
const BeadsInstallPath = "github.com/marlon-costa-dc/beads"

const BeadsInstallHint = "build the reviewed marlon-costa-dc/beads v1.2.2-dc3 release with `make install-force` from the beads rig"

// BeadsStatus represents the state of the beads installation.
type BeadsStatus int

const (
	BeadsOK                BeadsStatus = iota // bd found, version compatible
	BeadsNotFound                             // bd not in PATH
	BeadsTooOld                               // bd found but version too old
	BeadsWrongDistribution                    // canonical/non-dc binary found
	BeadsUnknown                              // bd found but couldn't parse version
)

// CheckBeads checks if bd is installed and compatible.
// Returns status and the installed version (if found).
func CheckBeads() (BeadsStatus, string) {
	// Check if bd exists in PATH
	path, err := exec.LookPath("bd")
	if err != nil {
		return BeadsNotFound, ""
	}
	_ = path // bd found

	// Get version (with timeout to prevent hanging on broken bd installs).
	// 10s is generous but necessary: under heavy CI load (parallel test
	// packages), even a trivial shell script can take >3s to start.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bd", "version")
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		return BeadsUnknown, ""
	}

	version := parseBeadsVersion(string(output))
	if version == "" {
		return BeadsUnknown, ""
	}

	// Compare versions
	if !strings.Contains(version, "-dc") {
		return BeadsWrongDistribution, version
	}
	if !compatibleBeadsVersion(version) {
		return BeadsTooOld, version
	}

	return BeadsOK, version
}

// EnsureBeads checks for bd and installs it if missing or outdated.
// Returns nil if bd is available and compatible.
// If autoInstall is true, will attempt to install bd when missing.
func EnsureBeads(autoInstall bool) error {
	status, version := CheckBeads()

	switch status {
	case BeadsOK:
		return nil

	case BeadsNotFound:
		return fmt.Errorf("beads (bd) not found in PATH\n\nInstall: %s", BeadsInstallHint)

	case BeadsTooOld:
		return fmt.Errorf("beads version %s is too old (minimum: %s)\n\nUpgrade: %s",
			version, MinBeadsVersion, BeadsInstallHint)

	case BeadsWrongDistribution:
		return fmt.Errorf("beads version %s is not the governed downstream distribution\n\nInstall: %s", version, BeadsInstallHint)

	case BeadsUnknown:
		// Found bd but couldn't determine version - proceed with warning
		return nil
	}

	return nil
}

// installBeads runs go install to install the latest beads.
// GOBIN is set to ~/.local/bin so the binary lands in the canonical
// location rather than the default $GOPATH/bin (~/go/bin/).
func installBeads() error {
	return fmt.Errorf("automatic go install is disabled for the downstream beads fork; %s", BeadsInstallHint)
}

// appendGOBIN returns env with GOBIN set to ~/.local/bin so that
// `go install` places binaries in the canonical location instead of
// the default $GOPATH/bin (which creates a stale shadow copy).
func appendGOBIN(env []string) []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return env // fall back to default
	}
	gobin := filepath.Join(home, ".local", "bin")
	// Replace existing GOBIN if present, otherwise append.
	for i, e := range env {
		if strings.HasPrefix(e, "GOBIN=") {
			env[i] = "GOBIN=" + gobin
			return env
		}
	}
	return append(env, "GOBIN="+gobin)
}

// parseBeadsVersion extracts version from "bd version X.Y.Z ..." output.
func parseBeadsVersion(output string) string {
	// Match patterns like "bd version 0.52.0" or "bd version 0.52.0 (dev: ...)"
	re := regexp.MustCompile(`bd version (\d+\.\d+\.\d+(?:-dc\d+)?)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

var downstreamBeadsVersion = regexp.MustCompile(`^(\d+\.\d+\.\d+)-dc(\d+)$`)

func compatibleBeadsVersion(version string) bool {
	matches := downstreamBeadsVersion.FindStringSubmatch(version)
	if len(matches) != 3 {
		return false
	}
	if CompareVersions(matches[1], "1.2.2") < 0 {
		return false
	}
	if matches[1] != "1.2.2" {
		return true
	}
	patch, err := strconv.Atoi(matches[2])
	return err == nil && patch >= 3
}
