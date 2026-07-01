// Package deps manages external dependencies for Gas Town.
package deps

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// MinBeadsVersion is the minimum compatible beads version for this Gas Town release.
// Update this when Gas Town requires new beads features.
const MinBeadsVersion = "0.57.0"

// BeadsInstallPath is the go install path for beads.
const BeadsInstallPath = "github.com/steveyegge/beads/cmd/bd@latest"

// IssueTrackerBackend identifies the issue-store implementation used by Gas Town.
type IssueTrackerBackend string

const (
	IssueTrackerBackendDefault   IssueTrackerBackend = ""
	IssueTrackerBackendUpstream  IssueTrackerBackend = "beads"
	IssueTrackerBackendMinibeads IssueTrackerBackend = "minibeads"

	// Backward-compatible aliases for callers still using Beads terminology.
	BeadsBackendDefault   = IssueTrackerBackendDefault
	BeadsBackendUpstream  = IssueTrackerBackendUpstream
	BeadsBackendMinibeads = IssueTrackerBackendMinibeads
)

// BeadsStatus represents the state of the beads installation.
type BeadsStatus int

const (
	BeadsOK       BeadsStatus = iota // bd found, version compatible
	BeadsNotFound                    // bd not in PATH
	BeadsTooOld                      // bd found but version too old
	BeadsUnknown                     // bd found but couldn't parse version
)

// RequestedIssueTrackerBackend returns the explicitly requested Beads-compatible
// issue tracker backend. Empty means the default upstream Beads/Dolt backend.
func RequestedIssueTrackerBackend() IssueTrackerBackend {
	backend, ok := requestedIssueTrackerBackendFromEnv()
	if !ok {
		return IssueTrackerBackendDefault
	}
	return backend
}

func requestedIssueTrackerBackendFromEnv() (IssueTrackerBackend, bool) {
	value := os.Getenv("GT_ISSUE_TRACKER_BACKEND")
	if value == "" {
		value = os.Getenv("GT_BEADS_BACKEND")
	}
	if strings.TrimSpace(value) == "" {
		return IssueTrackerBackendDefault, false
	}
	backend := normalizeIssueTrackerBackend(value)
	return backend, true
}

func normalizeIssueTrackerBackend(value string) IssueTrackerBackend {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "beads", "bd", "upstream":
		return IssueTrackerBackendDefault
	case "mb", "minibeads":
		return IssueTrackerBackendMinibeads
	default:
		return IssueTrackerBackendDefault
	}
}

// EffectiveIssueTrackerBackend returns the backend selected by explicit env vars,
// then by mayor/town.json when available, then by the running gt distribution
// or issue tracker CLIs on PATH, otherwise by the default upstream Beads/Dolt
// backend.
func EffectiveIssueTrackerBackend(townRoot string) IssueTrackerBackend {
	if backend, ok := requestedIssueTrackerBackendFromEnv(); ok {
		return backend
	}
	if backend, ok := issueTrackerBackendFromTown(townRoot); ok {
		return backend
	}
	if backend, ok := installedIssueTrackerBackend(); ok {
		return backend
	}
	return IssueTrackerBackendDefault
}

// IssueTrackerBackendForCommand returns the backend to use when dispatching an
// issue-tracker subprocess. It intentionally does not probe bd/mb versions,
// because command construction must not perform extra issue-tracker calls.
func IssueTrackerBackendForCommand(townRoot string) IssueTrackerBackend {
	if backend, ok := requestedIssueTrackerBackendFromEnv(); ok {
		return backend
	}
	if backend, ok := issueTrackerBackendFromTown(townRoot); ok {
		return backend
	}
	if runningFromMiniBeadsEdition() {
		return IssueTrackerBackendMinibeads
	}
	return IssueTrackerBackendDefault
}

// RequestedBeadsBackend is kept for compatibility with older internal callers.
func RequestedBeadsBackend() IssueTrackerBackend {
	return RequestedIssueTrackerBackend()
}

// UsingMiniBeads reports whether Gas Town should use minibeads for local
// compatibility paths.
func UsingMiniBeads() bool {
	return RequestedIssueTrackerBackend() == IssueTrackerBackendMinibeads
}

// UsingMiniBeadsForTown reports whether the effective backend for a town is
// minibeads, honoring env overrides first and persisted town config second.
func UsingMiniBeadsForTown(townRoot string) bool {
	return EffectiveIssueTrackerBackend(townRoot) == IssueTrackerBackendMinibeads
}

// CheckBeads checks if the env-requested issue tracker is installed and compatible.
// Returns status and the installed version (if found).
func CheckBeads() (BeadsStatus, string) {
	return CheckBeadsForBackend(RequestedIssueTrackerBackend())
}

// IssueTrackerCommandName returns the executable name for an issue tracker
// backend. Upstream Beads is exposed as bd; minibeads is exposed as mb.
func IssueTrackerCommandName(backend IssueTrackerBackend) string {
	if backend == IssueTrackerBackendMinibeads {
		return "mb"
	}
	return "bd"
}

// CheckBeadsForBackend checks if the configured issue tracker CLI is installed
// and compatible with a backend.
func CheckBeadsForBackend(backend IssueTrackerBackend) (BeadsStatus, string) {
	cliName := IssueTrackerCommandName(backend)
	path, err := exec.LookPath(cliName)
	if err != nil {
		return BeadsNotFound, ""
	}

	outputStr, ok := issueTrackerVersionOutput(path)
	if !ok {
		return BeadsUnknown, ""
	}

	if backend == IssueTrackerBackendMinibeads {
		version := parseMiniBeadsVersion(outputStr)
		if version == "" {
			return BeadsUnknown, ""
		}
		return BeadsOK, "minibeads " + version
	}

	version := parseBeadsVersion(outputStr)
	if version == "" {
		return BeadsUnknown, ""
	}

	// Compare versions
	if CompareVersions(version, MinBeadsVersion) < 0 {
		return BeadsTooOld, version
	}

	return BeadsOK, version
}

// EnsureBeadsForBackend checks for the selected issue tracker CLI and installs
// upstream beads only for the default upstream backend. Minibeads must already
// be available as mb.
func EnsureBeadsForBackend(backend IssueTrackerBackend, autoInstall bool) error {
	status, version := CheckBeadsForBackend(backend)

	switch status {
	case BeadsOK:
		return nil

	case BeadsNotFound:
		if backend == IssueTrackerBackendMinibeads {
			return fmt.Errorf("minibeads (mb) not found in PATH\n\nBuild minibeads and ensure the mb binary is on PATH")
		}
		if !autoInstall {
			return fmt.Errorf("beads (bd) not found in PATH\n\nInstall with: go install %s", BeadsInstallPath)
		}
		return installBeads()

	case BeadsTooOld:
		return fmt.Errorf("beads version %s is too old (minimum: %s)\n\nUpgrade with: go install %s",
			version, MinBeadsVersion, BeadsInstallPath)

	case BeadsUnknown:
		if backend == IssueTrackerBackendMinibeads {
			return fmt.Errorf("mb was found but its version response was not recognized; expected `mb version X.Y.Z`")
		}
		// Found bd but couldn't determine version - proceed with warning
		return nil
	}

	return nil
}

// EnsureBeads checks for bd and installs it if missing or outdated.
// Returns nil if bd is available and compatible.
// If autoInstall is true, will attempt to install bd when missing.
func EnsureBeads(autoInstall bool) error {
	return EnsureBeadsForBackend(RequestedIssueTrackerBackend(), autoInstall)
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

func parseMiniBeadsVersion(output string) string {
	re := regexp.MustCompile(`mb version (\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}

func installedIssueTrackerBackend() (IssueTrackerBackend, bool) {
	if runningFromMiniBeadsEdition() {
		return IssueTrackerBackendMinibeads, true
	}

	if path, ok := lookPathIssueTracker("bd"); ok {
		if versionOutput, ok := issueTrackerVersionOutput(path); ok && parseBeadsVersion(versionOutput) != "" {
			return IssueTrackerBackendDefault, true
		}
	}

	return IssueTrackerBackendDefault, false
}

func runningFromMiniBeadsEdition() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	siblingMB := filepath.Join(filepath.Dir(exe), "mb")
	if runtimePathExecutable(siblingMB) {
		if versionOutput, ok := issueTrackerVersionOutput(siblingMB); ok && parseMiniBeadsVersion(versionOutput) != "" {
			return true
		}
	}
	return false
}

func lookPathIssueTracker(name string) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	return path, true
}

func issueTrackerVersionOutput(path string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		return "", false
	}
	return string(output), true
}

func runtimePathExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0
}

func issueTrackerBackendFromTown(townRoot string) (IssueTrackerBackend, bool) {
	if townRoot == "" {
		return IssueTrackerBackendDefault, false
	}
	data, err := os.ReadFile(filepath.Join(townRoot, "mayor", "town.json"))
	if err != nil {
		return IssueTrackerBackendDefault, false
	}
	var town struct {
		IssueTrackerBackend string `json:"issue_tracker_backend"`
	}
	if err := json.Unmarshal(data, &town); err != nil {
		return IssueTrackerBackendDefault, false
	}
	if strings.TrimSpace(town.IssueTrackerBackend) == "" {
		return IssueTrackerBackendDefault, false
	}
	return normalizeIssueTrackerBackend(town.IssueTrackerBackend), true
}
