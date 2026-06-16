// Package deps manages external dependencies for Gas Town.
package deps

import (
	"context"
	"debug/buildinfo"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	rdebug "runtime/debug"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
	"golang.org/x/mod/semver"
)

const beadsModulePath = "github.com/steveyegge/beads"

// MinBeadsVersion is the minimum CLI version string accepted from `bd version`.
// Schema compatibility is enforced separately with the binary's Go module info.
const MinBeadsVersion = "0.57.0"

// RequiredBeadsModuleVersion is the Beads module version Gas Town embeds and
// expects shell `bd` calls to use. Keep this aligned with go.mod.
const RequiredBeadsModuleVersion = "v1.0.6-0.20260615070122-8e18581dc0dd"

// BeadsInstallPath is the go install path for beads.
const BeadsInstallPath = beadsModulePath + "/cmd/bd@" + RequiredBeadsModuleVersion

// BeadsStatus represents the state of the beads installation.
type BeadsStatus int

const (
	BeadsOK             BeadsStatus = iota // bd found, version compatible
	BeadsNotFound                          // bd not in PATH
	BeadsTooOld                            // bd found but CLI version too old
	BeadsModuleMismatch                    // bd found but Go module version differs from Gas Town
	BeadsUnknown                           // bd found but couldn't parse version
)

// CheckBeads checks if bd is installed and compatible. It returns the CLI
// version for CLI-version statuses and the Beads module version for module
// mismatch statuses.
func CheckBeads() (BeadsStatus, string) {
	status, version, _ := checkBeadsCandidates()
	return status, version
}

// ResolveBeadsPath returns a bd binary from PATH whose embedded Beads module
// version exactly matches Gas Town's pinned dependency. It scans the whole PATH
// so an older shadow binary earlier in PATH does not hide a compatible one.
func ResolveBeadsPath() (string, error) {
	status, version, path := checkBeadsCandidates()
	if status == BeadsOK {
		return path, nil
	}
	return "", beadsStatusError(status, version)
}

func checkBeadsCandidates() (BeadsStatus, string, string) {
	candidates := beadsPathCandidates()
	if len(candidates) == 0 {
		return BeadsNotFound, "", ""
	}

	firstStatus := BeadsUnknown
	firstVersion := ""
	firstPath := candidates[0]
	for i, path := range candidates {
		status, version := checkBeadsAtPath(path)
		if i == 0 {
			firstStatus = status
			firstVersion = version
			firstPath = path
		}
		if status == BeadsOK {
			return status, version, path
		}
	}
	return firstStatus, firstVersion, firstPath
}

func checkBeadsAtPath(path string) (BeadsStatus, string) {
	// Get version (with timeout to prevent hanging on broken bd installs).
	// 10s is generous but necessary: under heavy CI load (parallel test
	// packages), even a trivial shell script can take >3s to start.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.Output()
	if err != nil {
		return BeadsUnknown, ""
	}

	version := parseBeadsVersion(string(output))
	moduleVersion, moduleOK := beadsModuleVersionFromBinary(path)
	if !moduleOK {
		return BeadsUnknown, ""
	}
	if beadsModuleVersionMismatch(moduleVersion) {
		return BeadsModuleMismatch, moduleVersion
	}
	if version == "" {
		return BeadsUnknown, ""
	}

	// Compare versions
	if CompareVersions(version, MinBeadsVersion) < 0 {
		return BeadsTooOld, version
	}
	return BeadsOK, version
}

func beadsPathCandidates() []string {
	seen := make(map[string]bool)
	var candidates []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		for _, name := range []string{"bd", "bd.exe", "bd.cmd", "bd.bat"} {
			path := filepath.Join(dir, name)
			if seen[path] || !isExecutableFile(path) {
				continue
			}
			seen[path] = true
			candidates = append(candidates, path)
		}
	}
	return candidates
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0111 != 0 || strings.HasSuffix(strings.ToLower(path), ".bat") || strings.HasSuffix(strings.ToLower(path), ".cmd")
}

func beadsStatusError(status BeadsStatus, version string) error {
	switch status {
	case BeadsNotFound:
		return fmt.Errorf("beads (bd) not found in PATH\n\nInstall with: go install %s", BeadsInstallPath)
	case BeadsTooOld:
		return fmt.Errorf("beads CLI version %s is too old (minimum: %s)\n\nUpgrade with: go install %s", version, MinBeadsVersion, BeadsInstallPath)
	case BeadsModuleMismatch:
		return fmt.Errorf("beads module version %s does not match Gas Town's required version %s\n\nUpgrade with: go install %s", version, RequiredBeadsModuleVersion, BeadsInstallPath)
	case BeadsUnknown:
		return fmt.Errorf("beads (bd) version or module provenance could not be verified\n\nTry reinstalling: go install %s", BeadsInstallPath)
	default:
		return nil
	}
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
			return beadsStatusError(status, version)
		}
		return installBeads()

	case BeadsTooOld:
		return beadsStatusError(status, version)

	case BeadsModuleMismatch:
		return beadsStatusError(status, version)

	case BeadsUnknown:
		// Found bd but couldn't verify version/module provenance - proceed with warning.
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
	if status == BeadsModuleMismatch {
		return fmt.Errorf("installed beads module %s but required is %s", version, RequiredBeadsModuleVersion)
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

func beadsModuleVersionFromBinary(path string) (string, bool) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", false
	}
	return beadsModuleVersionFromBuildInfo(info)
}

func beadsModuleVersionFromBuildInfo(info *rdebug.BuildInfo) (string, bool) {
	if info == nil {
		return "", false
	}
	if info.Main.Path == beadsModulePath {
		return comparableModuleVersion(info.Main)
	}
	for _, dep := range info.Deps {
		if dep.Path == beadsModulePath {
			return comparableModuleVersion(*dep)
		}
	}
	return "", false
}

func comparableModuleVersion(module rdebug.Module) (string, bool) {
	if module.Replace != nil {
		if semver.IsValid(module.Replace.Version) {
			return module.Replace.Version, true
		}
		return "", false
	}
	if semver.IsValid(module.Version) {
		return module.Version, true
	}
	return "", false
}

func beadsModuleVersionMismatch(version string) bool {
	return semver.IsValid(version) && version != RequiredBeadsModuleVersion
}
