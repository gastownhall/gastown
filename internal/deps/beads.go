// Package deps manages external dependencies for Gas Town.
package deps

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/util"
)

// MinBeadsVersion is the minimum compatible beads version for this Gas Town release.
// Update this when Gas Town requires new beads features.
const MinBeadsVersion = "0.57.0"

// BeadsInstallPath is the go install path for beads.
const BeadsInstallPath = "github.com/steveyegge/beads/cmd/bd@latest"

// BeadsStatus represents the state of the beads installation.
type BeadsStatus int

const (
	BeadsOK       BeadsStatus = iota // bd found, version compatible
	BeadsNotFound                    // bd not in PATH
	BeadsTooOld                      // bd found but version too old
	BeadsUnknown                     // bd found but couldn't parse version
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
	if CompareVersions(version, MinBeadsVersion) < 0 {
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
		if !autoInstall {
			return fmt.Errorf("beads (bd) not found in PATH\n\nInstall with: go install %s", BeadsInstallPath)
		}
		return installBeads()

	case BeadsTooOld:
		return fmt.Errorf("beads version %s is too old (minimum: %s)\n\nUpgrade with: go install %s",
			version, MinBeadsVersion, BeadsInstallPath)

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
	fmt.Printf("   beads (bd) not found. Installing...\n")

	cmd := exec.Command("go", "install", BeadsInstallPath)
	util.SetDetachedProcessGroup(cmd)
	cmd.Env = appendGOBIN(cmd.Environ())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to install beads: %s\n%s", err, string(output))
	}

	// Verify installation
	status, version := CheckBeads()
	if status == BeadsNotFound {
		return fmt.Errorf("beads installed but not in PATH - ensure $GOPATH/bin is in your PATH")
	}
	if status == BeadsTooOld {
		return fmt.Errorf("installed beads %s but minimum required is %s", version, MinBeadsVersion)
	}

	fmt.Printf("   ✓ Installed beads %s\n", version)
	return nil
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
	re := regexp.MustCompile(`bd version (\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
