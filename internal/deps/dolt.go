package deps

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/util"
)

// MinDoltVersion is the minimum compatible dolt version for this Gas Town release.
// Update this when Gas Town requires new dolt features.
const MinDoltVersion = "2.0.7"

// DoltInstallURL is the installation page for dolt.
const DoltInstallURL = "https://github.com/dolthub/dolt#installation"

// DoltStatus represents the state of the dolt installation.
type DoltStatus int

const (
	DoltOK         DoltStatus = iota // dolt found, version compatible
	DoltNotFound                     // dolt not in PATH
	DoltTooOld                       // dolt found but version too old
	DoltExecFailed                   // dolt found but 'dolt version' failed to execute
	DoltUnknown                      // dolt version ran but output couldn't be parsed
)

// CheckDolt checks if dolt is installed and compatible.
// Returns status, the installed version (if found), and diagnostic detail
// for failure cases (stderr/error output).
func CheckDolt() (DoltStatus, string, string) {
	path, err := exec.LookPath("dolt")
	if err != nil {
		return DoltNotFound, "", ""
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	util.SetDetachedProcessGroup(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail == "" {
			detail = err.Error()
		}
		return DoltExecFailed, "", fmt.Sprintf("at %s: %s", path, detail)
	}

	version := parseDoltVersion(string(output))
	if version == "" {
		return DoltUnknown, "", strings.TrimSpace(string(output))
	}

	if CompareVersions(version, MinDoltVersion) < 0 {
		return DoltTooOld, version, ""
	}

	return DoltOK, version, ""
}

// parseDoltVersion extracts version from "dolt version X.Y.Z" output.
func parseDoltVersion(output string) string {
	re := regexp.MustCompile(`dolt version (\d+\.\d+\.\d+)`)
	matches := re.FindStringSubmatch(output)
	if len(matches) >= 2 {
		return matches[1]
	}
	return ""
}
