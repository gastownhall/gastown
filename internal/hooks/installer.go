// Package hooks provides a generic hook/settings installer for all agent runtimes.
//
// Instead of per-agent packages (claude/, gemini/, cursor/, etc.) each containing
// near-identical boilerplate, this package embeds all agent templates and provides
// a single generic installer that reads template metadata from AgentPresetInfo.
package hooks

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/hookutil"
)

//go:embed templates/*
var templateFS embed.FS

// InstallForRole provisions hook/settings files for an agent based on its preset config.
// It creates the file if it does not exist, or overwrites if the existing file contains
// known stale patterns (e.g., legacy "export PATH=" format). Otherwise it does not
// overwrite — this is the safe path for session startup, where Claude's settings.json
// may have been customized by syncTarget (base + role overrides merge) and must not
// be clobbered.
//
// For explicit sync operations that should update stale files, use SyncForRole.
//
// Parameters:
//   - provider: the preset's HooksProvider (e.g., "claude", "gemini").
//   - settingsDir: the gastown-managed parent (used by agents with --settings flag).
//   - workDir: the agent's working directory.
//   - role: the Gas Town role (e.g., "polecat", "crew", "witness").
//   - hooksDir/hooksFile: from the preset's HooksDir and HooksSettingsFile.
//
// Template resolution:
//   - Role-aware agents (have both autonomous and interactive templates):
//     templates/<provider>/settings-autonomous.json + settings-interactive.json
//     or templates/<provider>/hooks-autonomous.json + hooks-interactive.json
//   - Role-agnostic agents (single template): templates/<provider>/<hooksFile>
//
// The install directory is settingsDir for agents that support --settings (useSettingsDir=true),
// or workDir for all others.
func InstallForRole(provider, settingsDir, workDir, role, hooksDir, hooksFile string, useSettingsDir bool) error {
	if provider == "" || hooksDir == "" || hooksFile == "" {
		return nil
	}

	targetPath := installTargetPath(settingsDir, workDir, hooksDir, hooksFile, useSettingsDir)

	if existing, err := os.ReadFile(targetPath); err == nil {
		if !needsUpgrade(existing) {
			return nil // File exists and is current — don't overwrite
		}
		// Stale file detected — fall through to overwrite with current template
	}

	return writeTemplate(provider, role, hooksFile, targetPath)
}

// needsUpgrade returns true if an existing hooks file contains stale patterns
// that should be replaced by the current template. This allows the installer
// to auto-upgrade hooks from earlier versions without requiring manual intervention.
func needsUpgrade(content []byte) bool {
	// Stale pattern: export PATH=... && gt — replaced by {{GT_BIN}} in current templates.
	// The PATH export breaks Gemini CLI's hook runner which expands $PATH into
	// an enormous string. Also catches files missing GT_HOOK_SOURCE env vars.
	if bytes.Contains(content, []byte(`export PATH=`)) {
		return true
	}
	if bytes.Contains(content, []byte(`Gas Town OpenCode plugin`)) {
		return bytes.Contains(content, []byte(`captureRun("gt prime")`)) ||
			bytes.Contains(content, []byte("$`gt prime`")) ||
			!bytes.Contains(content, []byte(`prime --hook`))
	}
	// Stale binary path: hooks reference a different gt binary than the
	// currently running one. This happens when Homebrew gt is ahead of
	// ~/.local/bin/gt in PATH, or when the user switches between installs.
	// Detect by checking if the embedded gt path differs from os.Executable.
	if currentGT := resolveGTBinary(); currentGT != "" && currentGT != "gt" {
		// Build the old-path pattern that would be stale
		// The template uses {{GT_BIN}} which resolves to the current binary.
		// If any other absolute gt path is present, the file is stale.
		re := regexp.MustCompile(`"command"\s*:\s*"(/[^"]*\bgt\b[^"]*)"`)
		for _, match := range re.FindAllSubmatch(content, -1) {
			if len(match) >= 2 {
				embeddedPath := string(match[1])
				if embeddedPath != currentGT && strings.Contains(embeddedPath, "/gt") {
					return true
				}
			}
		}
	}
	return false
}

// SyncResult describes what SyncForRole did.
type SyncResult int

const (
	SyncUnchanged SyncResult = iota // File already matches template
	SyncCreated                     // File did not exist, created
	SyncUpdated                     // File existed but content differed, updated
)

// SyncForRole compares the deployed hook/settings file against the current template
// and overwrites if content differs. Returns what action was taken.
//
// This is the explicit sync path used by "gt hooks sync" for template-based agents
// (OpenCode, Copilot, Pi, OMP, etc.). It should NOT be used for agents whose settings
// are managed by the JSON merge path (Claude), as that would clobber merged overrides.
func SyncForRole(provider, settingsDir, workDir, role, hooksDir, hooksFile string, useSettingsDir bool) (SyncResult, error) {
	if provider == "" || hooksDir == "" || hooksFile == "" {
		return SyncUnchanged, nil
	}

	targetPath := installTargetPath(settingsDir, workDir, hooksDir, hooksFile, useSettingsDir)

	content, err := resolveAndSubstitute(provider, hooksFile, role)
	if err != nil {
		return 0, err
	}

	fileExisted := false
	if existing, err := os.ReadFile(targetPath); err == nil {
		fileExisted = true
		if isSettingsFile(hooksFile) {
			// JSON files: use structural comparison to tolerate whitespace differences.
			if TemplateContentEqual(existing, content) {
				return SyncUnchanged, nil
			}
		} else {
			if bytes.Equal(existing, content) {
				return SyncUnchanged, nil
			}
		}
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return 0, fmt.Errorf("creating hooks directory: %w", err)
	}

	perm := os.FileMode(0644)
	if isSettingsFile(hooksFile) {
		perm = 0600
	}

	// Atomic write (temp + rename) prevents concurrent polecat spawns from
	// interleaving truncates+writes into a partial JSON file that Claude
	// rejects at startup. See gh#3500.
	if err := atomicfile.WriteFile(targetPath, content, perm); err != nil {
		return 0, fmt.Errorf("writing hooks file: %w", err)
	}

	if fileExisted {
		return SyncUpdated, nil
	}
	return SyncCreated, nil
}

// installTargetPath computes the full path for a hook/settings file.
func installTargetPath(settingsDir, workDir, hooksDir, hooksFile string, useSettingsDir bool) string {
	installDir := workDir
	if useSettingsDir {
		installDir = settingsDir
	}
	return filepath.Join(installDir, hooksDir, hooksFile)
}

// resolveAndSubstitute resolves the template and performs {{GT_BIN}} substitution.
func resolveAndSubstitute(provider, hooksFile, role string) ([]byte, error) {
	content, err := resolveTemplate(provider, hooksFile, role)
	if err != nil {
		return nil, fmt.Errorf("resolving template for %s: %w", provider, err)
	}

	if bytes.Contains(content, []byte("{{GT_BIN}}")) {
		gtBin := resolveGTBinary()
		gtBinBytes := []byte(gtBin)
		if isSettingsFile(hooksFile) {
			// JSON-encode the path so Windows backslashes are properly escaped.
			// json.Marshal produces `"C:\\path\\gt.exe"` (with quotes); strip the quotes.
			if encoded, err := json.Marshal(gtBin); err == nil {
				gtBinBytes = encoded[1 : len(encoded)-1]
			}
		}
		content = bytes.ReplaceAll(content, []byte("{{GT_BIN}}"), gtBinBytes)
	}

	return content, nil
}

// writeTemplate resolves a template, substitutes placeholders, and writes it to targetPath.
func writeTemplate(provider, role, hooksFile, targetPath string) error {
	content, err := resolveAndSubstitute(provider, hooksFile, role)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("creating hooks directory: %w", err)
	}

	perm := os.FileMode(0644)
	if isSettingsFile(hooksFile) {
		perm = 0600
	}

	// Atomic write (temp + rename) — see gh#3500.
	if err := atomicfile.WriteFile(targetPath, content, perm); err != nil {
		return fmt.Errorf("writing hooks file: %w", err)
	}

	return nil
}

// resolveTemplate finds the right template for a provider+role combination.
func resolveTemplate(provider, hooksFile, role string) ([]byte, error) {
	// Determine role type
	autonomous := hookutil.IsAutonomousRole(role)

	// Try role-aware naming conventions
	if autonomous {
		for _, pattern := range roleAwarePatterns("autonomous", hooksFile) {
			path := fmt.Sprintf("templates/%s/%s", provider, pattern)
			if content, err := templateFS.ReadFile(path); err == nil {
				return content, nil
			}
		}
	} else {
		for _, pattern := range roleAwarePatterns("interactive", hooksFile) {
			path := fmt.Sprintf("templates/%s/%s", provider, pattern)
			if content, err := templateFS.ReadFile(path); err == nil {
				return content, nil
			}
		}
	}

	// Fall back to single template (role-agnostic agents)
	path := fmt.Sprintf("templates/%s/%s", provider, hooksFile)
	content, err := templateFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("no template found for provider %q file %q: %w", provider, hooksFile, err)
	}
	return content, nil
}

// roleAwarePatterns generates candidate template filenames for role-aware agents.
// Given roleType "autonomous" and hooksFile "settings.json", it tries:
//   - settings-autonomous.json
//   - hooks-autonomous.json
func roleAwarePatterns(roleType, hooksFile string) []string {
	ext := filepath.Ext(hooksFile)
	base := hooksFile[:len(hooksFile)-len(ext)]

	return []string{
		base + "-" + roleType + ext,  // settings-autonomous.json
		"hooks-" + roleType + ext,    // hooks-autonomous.json
		"settings-" + roleType + ext, // settings-autonomous.json (fallback)
	}
}

// isSettingsFile returns true for files that may contain sensitive role config.
func isSettingsFile(name string) bool {
	return filepath.Ext(name) == ".json"
}

// resolveGTBinary returns the absolute path to the gt binary.
// Tries os.Executable() first (most reliable when running as gt), then
// falls back to exec.LookPath for PATH-based discovery. If both fail,
// returns "gt" and hopes the runtime PATH has it.
func resolveGTBinary() string {
	if exe, err := os.Executable(); err == nil {
		return exe
	}
	if path, err := exec.LookPath("gt"); err == nil {
		return path
	}
	return "gt"
}

// ComputeExpectedTemplate returns the expected file content for a template-based
// provider (e.g., gemini) with {{GT_BIN}} resolved to the actual gt binary path.
// This is used by the doctor hooks-sync check to compare installed files against
// current templates.
func ComputeExpectedTemplate(provider, hooksFile, role string) ([]byte, error) {
	return resolveAndSubstitute(provider, hooksFile, role)
}

// TemplateContentEqual compares two JSON byte slices for structural equality
// by normalizing whitespace. Returns true if they represent the same JSON.
func TemplateContentEqual(expected, actual []byte) bool {
	var e, a interface{}
	if err := json.Unmarshal(expected, &e); err != nil {
		return false
	}
	if err := json.Unmarshal(actual, &a); err != nil {
		return false
	}
	ej, _ := json.Marshal(e)
	aj, _ := json.Marshal(a)
	return string(ej) == string(aj)
}
