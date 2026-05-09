package doctor

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/runtime"
	"github.com/steveyegge/gastown/internal/session"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/tmux"
)

// gitFileStatus represents the git status of a file.
type gitFileStatus string

const (
	gitStatusUntracked       gitFileStatus = "untracked"        // File not tracked by git
	gitStatusTrackedClean    gitFileStatus = "tracked-clean"    // Tracked, no local modifications
	gitStatusTrackedModified gitFileStatus = "tracked-modified" // Tracked with local modifications
	gitStatusIgnored         gitFileStatus = "ignored"          // File is gitignored
	gitStatusUnknown         gitFileStatus = "unknown"          // Not in a git repo or error
)

// ClaudeSettingsCheck verifies that Claude settings.json files match the expected templates.
// Detects stale settings files that are missing required hooks or configuration.
type ClaudeSettingsCheck struct {
	FixableCheck
	staleSettings []staleSettingsInfo
}

type staleSettingsInfo struct {
	path           string        // Full path to settings file
	agentType      string        // e.g., "witness", "refinery", "deacon", "mayor"
	rigName        string        // Rig name (empty for town-level agents)
	sessionName    string        // tmux session name for cycling
	missing        []string      // What's missing from the settings
	wrongLocation  bool          // True if file is in wrong location (should be deleted)
	missingFile    bool          // True if settings.local.json doesn't exist (needs agent restart)
	gitStatus      gitFileStatus // Git status for wrong-location files (for safe deletion)
}

// NewClaudeSettingsCheck creates a new Claude settings validation check.
func NewClaudeSettingsCheck() *ClaudeSettingsCheck {
	return &ClaudeSettingsCheck{
		FixableCheck: FixableCheck{
			BaseCheck: BaseCheck{
				CheckName:        "claude-settings",
				CheckDescription: "Verify Claude settings.json files match expected templates",
				CheckCategory:    CategoryConfig,
			},
		},
	}
}

// Run checks all Claude settings files for staleness or missing settings.json.
func (c *ClaudeSettingsCheck) Run(ctx *CheckContext) *CheckResult {
	c.staleSettings = nil

	var details []string
	var hasModifiedFiles bool
	var hasMissingFiles bool
	var hasStaleFiles bool

	// Find all settings files (stale and missing)
	settingsFiles := c.findSettingsFiles(ctx.TownRoot)

	for _, sf := range settingsFiles {
		// Missing settings.local.json files need agent restart to create
		if sf.missingFile {
			c.staleSettings = append(c.staleSettings, sf)
			details = append(details, fmt.Sprintf("%s: missing (restart %s to create)", sf.path, sf.agentType))
			hasMissingFiles = true
			continue
		}

		// Files in wrong locations are always stale (should be deleted)
		if sf.wrongLocation {
			// Check git status to determine safe deletion strategy
			sf.gitStatus = c.getGitFileStatus(sf.path)

			// Skip gitignored files that aren't gastown-generated settings.
			// Gastown settings (settings.json, settings.local.json) must always
			// be detected even when gitignored, since gastown itself previously
			// added the gitignore patterns and stale files must be cleaned up.
			baseName := filepath.Base(sf.path)
			isGastownSettings := baseName == "settings.json" || baseName == "settings.local.json"
			if sf.gitStatus == gitStatusIgnored && !isGastownSettings {
				continue
			}

			c.staleSettings = append(c.staleSettings, sf)
			hasStaleFiles = true

			// Provide detailed message based on git status
			var statusMsg string
			switch sf.gitStatus {
			case gitStatusIgnored:
				statusMsg = "wrong location, gitignored (safe to delete)"
			case gitStatusUntracked:
				statusMsg = "wrong location, untracked (safe to delete)"
			case gitStatusTrackedClean:
				statusMsg = "wrong location, tracked but unmodified (safe to delete)"
			case gitStatusTrackedModified:
				statusMsg = "wrong location, tracked with local modifications (manual review needed)"
				hasModifiedFiles = true
			default:
				statusMsg = "wrong location (inside source repo)"
			}
			details = append(details, fmt.Sprintf("%s: %s", sf.path, statusMsg))
			continue
		}

		// Check content of files in correct locations
		missing := c.checkSettings(sf.path, sf.agentType)
		if len(missing) > 0 {
			sf.missing = missing
			c.staleSettings = append(c.staleSettings, sf)
			hasStaleFiles = true
			details = append(details, fmt.Sprintf("%s: missing %s", sf.path, strings.Join(missing, ", ")))
		}
	}

	if len(c.staleSettings) == 0 {
		return &CheckResult{
			Name:    c.Name(),
			Status:  StatusOK,
			Message: "All Claude settings.json files are up to date",
		}
	}

	// Build appropriate message and fix hint
	var message string
	var fixHint string

	if hasMissingFiles && !hasStaleFiles {
		message = fmt.Sprintf("Found %d agent(s) missing settings.json", len(c.staleSettings))
		fixHint = "Run 'gt up --restore' to restart agents and create settings"
	} else if hasStaleFiles && !hasMissingFiles {
		message = fmt.Sprintf("Found %d stale Claude config file(s)", len(c.staleSettings))
		if hasModifiedFiles {
			fixHint = "Run 'gt doctor --fix' to fix safe issues. Files with local modifications require manual review."
		} else {
			fixHint = "Run 'gt doctor --fix' to delete stale files, then 'gt up --restore' to create new settings"
		}
	} else {
		message = fmt.Sprintf("Found %d Claude settings issue(s)", len(c.staleSettings))
		if hasModifiedFiles {
			fixHint = "Run 'gt doctor --fix' to fix safe issues, then 'gt up --restore'. Files with local modifications require manual review."
		} else {
			fixHint = "Run 'gt doctor --fix' to delete stale files, then 'gt up --restore' to create new settings"
		}
	}

	return &CheckResult{
		Name:    c.Name(),
		Status:  StatusError,
		Message: message,
		Details: details,
		FixHint: fixHint,
	}
}

// findSettingsFiles locates all .claude/settings.json files and identifies their agent type.
// Settings are now installed in gastown-managed parent directories (crew/, polecats/,
// witness/, refinery/) and passed via --settings flag. Old settings.local.json files
// in working directories are detected as stale.
func (c *ClaudeSettingsCheck) findSettingsFiles(townRoot string) []staleSettingsInfo {
	var files []staleSettingsInfo

	// Check for STALE settings at town root (~/gt/.claude/settings.json)
	// This is WRONG - settings here pollute ALL child workspaces via directory traversal.
	staleTownRootSettings := filepath.Join(townRoot, ".claude", "settings.json")
	if fileExists(staleTownRootSettings) {
		files = append(files, staleSettingsInfo{
			path:          staleTownRootSettings,
			agentType:     "mayor",
			sessionName:   "hq-mayor",
			wrongLocation: true,
			gitStatus:     c.getGitFileStatus(staleTownRootSettings),
			missing:       []string{"stale settings.json at town root (should not exist)"},
		})
	}
	// Also check for stale settings.local.json at town root
	staleTownRootLocal := filepath.Join(townRoot, ".claude", "settings.local.json")
	if fileExists(staleTownRootLocal) {
		files = append(files, staleSettingsInfo{
			path:          staleTownRootLocal,
			agentType:     "mayor",
			sessionName:   "hq-mayor",
			wrongLocation: true,
			gitStatus:     c.getGitFileStatus(staleTownRootLocal),
			missing:       []string{"stale settings.local.json at town root (should not exist)"},
		})
	}

	// Check for STALE CLAUDE.md at town root (~/gt/CLAUDE.md)
	// This is WRONG if it contains Mayor-specific instructions that would be inherited
	// by ALL agents via directory traversal. However, a short identity anchor file
	// (created by priming) that just says "run gt prime" is intentional and safe.
	staleTownRootCLAUDEmd := filepath.Join(townRoot, "CLAUDE.md")
	if fileExists(staleTownRootCLAUDEmd) && !isIdentityAnchor(staleTownRootCLAUDEmd) {
		files = append(files, staleSettingsInfo{
			path:          staleTownRootCLAUDEmd,
			agentType:     "mayor",
			sessionName:   "hq-mayor",
			wrongLocation: true,
			gitStatus:     c.getGitFileStatus(staleTownRootCLAUDEmd),
			missing:       []string{"should be at mayor/CLAUDE.md, not town root"},
		})
	}

	// Town-level: mayor - check for stale settings.local.json (should be settings.json)
	mayorStaleLocal := filepath.Join(townRoot, "mayor", ".claude", "settings.local.json")
	if fileExists(mayorStaleLocal) {
		files = append(files, staleSettingsInfo{
			path:          mayorStaleLocal,
			agentType:     "mayor",
			sessionName:   "hq-mayor",
			wrongLocation: true,
			missing:       []string{"stale settings.local.json (should be settings.json)"},
		})
	}
	// Check for correct settings.json
	mayorSettings := filepath.Join(townRoot, "mayor", ".claude", "settings.json")
	mayorWorkDir := filepath.Join(townRoot, "mayor")
	if fileExists(mayorSettings) {
		files = append(files, staleSettingsInfo{
			path:        mayorSettings,
			agentType:   "mayor",
			sessionName: "hq-mayor",
		})
	} else if dirExists(mayorWorkDir) {
		files = append(files, staleSettingsInfo{
			path:        mayorSettings,
			agentType:   "mayor",
			sessionName: "hq-mayor",
			missingFile: true,
		})
	}

	// Town-level: deacon - check for stale settings.local.json (should be settings.json)
	deaconStaleLocal := filepath.Join(townRoot, "deacon", ".claude", "settings.local.json")
	if fileExists(deaconStaleLocal) {
		files = append(files, staleSettingsInfo{
			path:          deaconStaleLocal,
			agentType:     "deacon",
			sessionName:   "hq-deacon",
			wrongLocation: true,
			missing:       []string{"stale settings.local.json (should be settings.json)"},
		})
	}
	// Check for correct settings.json
	deaconSettings := filepath.Join(townRoot, "deacon", ".claude", "settings.json")
	deaconWorkDir := filepath.Join(townRoot, "deacon")
	if fileExists(deaconSettings) {
		files = append(files, staleSettingsInfo{
			path:        deaconSettings,
			agentType:   "deacon",
			sessionName: "hq-deacon",
		})
	} else if dirExists(deaconWorkDir) {
		files = append(files, staleSettingsInfo{
			path:        deaconSettings,
			agentType:   "deacon",
			sessionName: "hq-deacon",
			missingFile: true,
		})
	}

	// Find rig directories
	entries, err := os.ReadDir(townRoot)
	if err != nil {
		return files
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		rigName := entry.Name()
		rigPath := filepath.Join(townRoot, rigName)

		// Skip known non-rig directories
		if rigName == "mayor" || rigName == "deacon" || rigName == "daemon" ||
			rigName == ".git" || rigName == "docs" || rigName[0] == '.' {
			continue
		}

		// Check for witness settings
		witnessDir := filepath.Join(rigPath, "witness")
		if dirExists(witnessDir) {
			// CORRECT: witness/.claude/settings.json (parent directory)
			witnessCorrectSettings := filepath.Join(witnessDir, ".claude", "settings.json")
			if fileExists(witnessCorrectSettings) {
				files = append(files, staleSettingsInfo{
					path:        witnessCorrectSettings,
					agentType:   "witness",
					rigName:     rigName,
					sessionName: session.WitnessSessionName(session.PrefixFor(rigName)),
				})
			} else {
				files = append(files, staleSettingsInfo{
					path:        witnessCorrectSettings,
					agentType:   "witness",
					rigName:     rigName,
					sessionName: session.WitnessSessionName(session.PrefixFor(rigName)),
					missingFile: true,
				})
			}
			// STALE: old settings.local.json in parent directory (not a customer repo)
			witnessParentStaleLocal := filepath.Join(witnessDir, ".claude", "settings.local.json")
			if fileExists(witnessParentStaleLocal) {
				files = append(files, staleSettingsInfo{
					path:          witnessParentStaleLocal,
					agentType:     "witness",
					rigName:       rigName,
					sessionName:   session.WitnessSessionName(session.PrefixFor(rigName)),
					wrongLocation: true,
					missing:       []string{"stale settings.local.json (settings now in witness/.claude/settings.json)"},
				})
			}
			// STALE: old settings in workdir (rig/) — skip if tracked in customer repo
			for _, staleFile := range []string{"settings.json", "settings.local.json"} {
				stalePath := filepath.Join(witnessDir, "rig", ".claude", staleFile)
				if fileExists(stalePath) {
					gs := c.getGitFileStatus(stalePath)
					if gs != gitStatusTrackedClean && gs != gitStatusTrackedModified {
						files = append(files, staleSettingsInfo{
							path:          stalePath,
							agentType:     "witness",
							rigName:       rigName,
							sessionName:   session.WitnessSessionName(session.PrefixFor(rigName)),
							wrongLocation: true,
							missing:       []string{"stale settings in workdir (settings now in witness/.claude/settings.json)"},
						})
					}
				}
			}
		}

		// Check for refinery settings
		refineryDir := filepath.Join(rigPath, "refinery")
		if dirExists(refineryDir) {
			// CORRECT: refinery/.claude/settings.json (parent directory)
			refineryCorrectSettings := filepath.Join(refineryDir, ".claude", "settings.json")
			if fileExists(refineryCorrectSettings) {
				files = append(files, staleSettingsInfo{
					path:        refineryCorrectSettings,
					agentType:   "refinery",
					rigName:     rigName,
					sessionName: session.RefinerySessionName(session.PrefixFor(rigName)),
				})
			} else {
				files = append(files, staleSettingsInfo{
					path:        refineryCorrectSettings,
					agentType:   "refinery",
					rigName:     rigName,
					sessionName: session.RefinerySessionName(session.PrefixFor(rigName)),
					missingFile: true,
				})
			}
			// STALE: old settings.local.json in parent directory (not a customer repo)
			refineryParentStaleLocal := filepath.Join(refineryDir, ".claude", "settings.local.json")
			if fileExists(refineryParentStaleLocal) {
				files = append(files, staleSettingsInfo{
					path:          refineryParentStaleLocal,
					agentType:     "refinery",
					rigName:       rigName,
					sessionName:   session.RefinerySessionName(session.PrefixFor(rigName)),
					wrongLocation: true,
					missing:       []string{"stale settings.local.json (settings now in refinery/.claude/settings.json)"},
				})
			}
			// STALE: old settings in workdir (rig/) — skip if tracked in customer repo
			for _, staleFile := range []string{"settings.json", "settings.local.json"} {
				stalePath := filepath.Join(refineryDir, "rig", ".claude", staleFile)
				if fileExists(stalePath) {
					gs := c.getGitFileStatus(stalePath)
					if gs != gitStatusTrackedClean && gs != gitStatusTrackedModified {
						files = append(files, staleSettingsInfo{
							path:          stalePath,
							agentType:     "refinery",
							rigName:       rigName,
							sessionName:   session.RefinerySessionName(session.PrefixFor(rigName)),
							wrongLocation: true,
							missing:       []string{"stale settings in workdir (settings now in refinery/.claude/settings.json)"},
						})
					}
				}
			}
		}

		// Check for crew settings
		crewDir := filepath.Join(rigPath, "crew")
		if dirExists(crewDir) {
			// CORRECT: crew/.claude/settings.json (shared parent directory)
			crewCorrectSettings := filepath.Join(crewDir, ".claude", "settings.json")
			if fileExists(crewCorrectSettings) {
				files = append(files, staleSettingsInfo{
					path:        crewCorrectSettings,
					agentType:   "crew",
					rigName:     rigName,
				})
			} else {
				files = append(files, staleSettingsInfo{
					path:        crewCorrectSettings,
					agentType:   "crew",
					rigName:     rigName,
					missingFile: true,
				})
			}
			// STALE: old settings.local.json in parent directory
			crewParentStaleLocal := filepath.Join(crewDir, ".claude", "settings.local.json")
			if fileExists(crewParentStaleLocal) {
				files = append(files, staleSettingsInfo{
					path:          crewParentStaleLocal,
					agentType:     "crew",
					rigName:       rigName,
					wrongLocation: true,
					missing:       []string{"stale settings.local.json in parent (settings now in crew/.claude/settings.json)"},
				})
			}
			// STALE: old settings in individual crew member workdirs
			crewEntries, _ := os.ReadDir(crewDir)
			for _, crewEntry := range crewEntries {
				if !crewEntry.IsDir() || crewEntry.Name() == ".claude" {
					continue
				}
				// Both settings.json and settings.local.json in workdirs are stale
				for _, staleFile := range []string{"settings.json", "settings.local.json"} {
					stalePath := filepath.Join(crewDir, crewEntry.Name(), ".claude", staleFile)
					if fileExists(stalePath) {
						gs := c.getGitFileStatus(stalePath)
						if gs != gitStatusTrackedClean && gs != gitStatusTrackedModified {
							files = append(files, staleSettingsInfo{
								path:          stalePath,
								agentType:     "crew",
								rigName:       rigName,
								sessionName:   session.CrewSessionName(session.PrefixFor(rigName), crewEntry.Name()),
								wrongLocation: true,
								missing:       []string{"stale settings in workdir (settings now in crew/.claude/settings.json)"},
							})
						}
					}
				}
			}
		}

		// Check for polecat settings
		polecatsDir := filepath.Join(rigPath, "polecats")
		if dirExists(polecatsDir) {
			// CORRECT: polecats/.claude/settings.json (shared parent directory)
			polecatCorrectSettings := filepath.Join(polecatsDir, ".claude", "settings.json")
			if fileExists(polecatCorrectSettings) {
				files = append(files, staleSettingsInfo{
					path:        polecatCorrectSettings,
					agentType:   "polecat",
					rigName:     rigName,
				})
			} else {
				files = append(files, staleSettingsInfo{
					path:        polecatCorrectSettings,
					agentType:   "polecat",
					rigName:     rigName,
					missingFile: true,
				})
			}
			// STALE: old settings.local.json in parent directory
			polecatParentStaleLocal := filepath.Join(polecatsDir, ".claude", "settings.local.json")
			if fileExists(polecatParentStaleLocal) {
				files = append(files, staleSettingsInfo{
					path:          polecatParentStaleLocal,
					agentType:     "polecat",
					rigName:       rigName,
					wrongLocation: true,
					missing:       []string{"stale settings.local.json in parent (settings now in polecats/.claude/settings.json)"},
				})
			}
			// STALE: old settings in individual polecat workdirs
			polecatEntries, _ := os.ReadDir(polecatsDir)
			for _, pcEntry := range polecatEntries {
				if !pcEntry.IsDir() || pcEntry.Name() == ".claude" {
					continue
				}
				// Intermediate-level (polecats/<name>/) — always Gas Town artifacts
				for _, staleFile := range []string{"settings.json", "settings.local.json"} {
					stalePath := filepath.Join(polecatsDir, pcEntry.Name(), ".claude", staleFile)
					if fileExists(stalePath) {
						files = append(files, staleSettingsInfo{
							path:          stalePath,
							agentType:     "polecat",
							rigName:       rigName,
							sessionName:   session.PolecatSessionName(session.PrefixFor(rigName), pcEntry.Name()),
							wrongLocation: true,
							missing:       []string{"stale settings in intermediate dir (settings now in polecats/.claude/settings.json)"},
						})
					}
				}
				// Worktree-level (polecats/<name>/<rig>/) — skip if tracked
				for _, staleFile := range []string{"settings.json", "settings.local.json"} {
					stalePath := filepath.Join(polecatsDir, pcEntry.Name(), rigName, ".claude", staleFile)
					if fileExists(stalePath) {
						gs := c.getGitFileStatus(stalePath)
						if gs != gitStatusTrackedClean && gs != gitStatusTrackedModified {
							files = append(files, staleSettingsInfo{
								path:          stalePath,
								agentType:     "polecat",
								rigName:       rigName,
								sessionName:   session.PolecatSessionName(session.PrefixFor(rigName), pcEntry.Name()),
								wrongLocation: true,
								missing:       []string{"stale settings in workdir (settings now in polecats/.claude/settings.json)"},
							})
						}
					}
				}
			}
		}

		// Check for STALE rig-root settings (<rig>/.claude/settings.json).
		// Legacy pattern predating the per-role architecture. Superseded by
		// per-role files (witness/.claude/, polecats/.claude/, etc.).
		// Skip if tracked in a customer repo — could be intentional.
		for _, staleFile := range []string{"settings.json", "settings.local.json"} {
			rigRootSettings := filepath.Join(rigPath, ".claude", staleFile)
			if fileExists(rigRootSettings) {
				gs := c.getGitFileStatus(rigRootSettings)
				if gs != gitStatusTrackedClean && gs != gitStatusTrackedModified {
					files = append(files, staleSettingsInfo{
						path:          rigRootSettings,
						agentType:     "rig-root",
						rigName:       rigName,
						wrongLocation: true,
						gitStatus:     gs,
						missing:       []string{"legacy rig-root settings (superseded by per-role files)"},
					})
				}
			}
		}
	}

	return files
}

// checkSettings compares a settings file against the expected template.
// Returns a list of what's missing. Stop-hook expectations are role-specific
// to match the canonical hook templates in internal/hooks/config.go (#3648):
//
//   - polecat → `gt tap polecat-stop-check` (idle-polecat catcher)
//   - everyone else → `gt costs record` (autonomous cost accounting)
//
// Without this, doctor never converged for polecats: hooks sync wrote
// polecat-stop-check, doctor demanded costs record, fix deleted the file,
// the daemon recreated the same polecat-stop-check file, repeat forever.
func (c *ClaudeSettingsCheck) checkSettings(path, agentType string) []string {
	var missing []string

	// Read the actual settings
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{"unreadable"}
	}

	var actual map[string]any
	if err := json.Unmarshal(data, &actual); err != nil {
		return []string{"invalid JSON"}
	}

	// Check for required elements based on template
	// All templates should have:
	// 1. enabledPlugins
	// 2. SessionStart hook with prime --hook
	// 3. Stop hook (role-specific pattern — see expectedStopPattern)
	// Check enabledPlugins
	if _, ok := actual["enabledPlugins"]; !ok {
		missing = append(missing, "enabledPlugins")
	}

	// Check hooks
	hooks, ok := actual["hooks"].(map[string]any)
	if !ok {
		return append(missing, "hooks")
	}

	// Check SessionStart hook has prime --hook (either via PATH export or resolved {{GT_BIN}})
	if !c.hookHasPattern(hooks, "SessionStart", "prime --hook") {
		missing = append(missing, "SessionStart hook (prime --hook)")
	}

	// Check Stop hook against the expected pattern for this role.
	expected := expectedStopPattern(agentType)
	if !c.hookHasPattern(hooks, "Stop", expected) {
		missing = append(missing, fmt.Sprintf("Stop hook (%s)", expected))
	}

	return missing
}

// expectedStopPattern returns the substring that should appear in the role's
// Stop hook command. Mirrors the templates in internal/hooks/config.go's
// DefaultOverrides — when those change, this must change too.
func expectedStopPattern(agentType string) string {
	switch agentType {
	case "polecat", "polecats":
		return "polecat-stop-check"
	default:
		return "costs record"
	}
}

// getGitFileStatus determines the git status of a file.
// Returns untracked, tracked-clean, tracked-modified, ignored, or unknown.
func (c *ClaudeSettingsCheck) getGitFileStatus(filePath string) gitFileStatus {
	dir := filepath.Dir(filePath)
	fileName := filepath.Base(filePath)

	// Check if we're in a git repo
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		return gitStatusUnknown
	}

	// Check if file is tracked
	cmd = exec.Command("git", "-C", dir, "ls-files", fileName)
	output, err := cmd.Output()
	if err != nil {
		return gitStatusUnknown
	}

	if len(strings.TrimSpace(string(output))) == 0 {
		// File is not tracked - check if it's gitignored
		cmd = exec.Command("git", "-C", dir, "check-ignore", "-q", fileName)
		if err := cmd.Run(); err == nil {
			// Exit code 0 means file is ignored
			return gitStatusIgnored
		}
		// File is not tracked and not ignored
		return gitStatusUntracked
	}

	// File is tracked - check if modified
	cmd = exec.Command("git", "-C", dir, "diff", "--quiet", fileName)
	if err := cmd.Run(); err != nil {
		// Non-zero exit means file has changes
		return gitStatusTrackedModified
	}

	// Also check for staged changes
	cmd = exec.Command("git", "-C", dir, "diff", "--cached", "--quiet", fileName)
	if err := cmd.Run(); err != nil {
		return gitStatusTrackedModified
	}

	return gitStatusTrackedClean
}

// hookHasPattern checks if a hook contains a specific pattern.
func (c *ClaudeSettingsCheck) hookHasPattern(hooks map[string]any, hookName, pattern string) bool {
	hookList, ok := hooks[hookName].([]any)
	if !ok {
		return false
	}

	for _, hook := range hookList {
		hookMap, ok := hook.(map[string]any)
		if !ok {
			continue
		}
		innerHooks, ok := hookMap["hooks"].([]any)
		if !ok {
			continue
		}
		for _, inner := range innerHooks {
			innerMap, ok := inner.(map[string]any)
			if !ok {
				continue
			}
			cmd, ok := innerMap["command"].(string)
			if ok && strings.Contains(cmd, pattern) {
				return true
			}
		}
	}
	return false
}

// Fix deletes stale settings files. Agents auto-install correct settings on restart.
// Files with local modifications are skipped to avoid losing user changes.
func (c *ClaudeSettingsCheck) Fix(ctx *CheckContext) error {
	var errors []string
	var skipped []string
	var needsRestart bool
	t := tmux.NewTmux()

	for _, sf := range c.staleSettings {
		// Skip files that aren't stale (correct settings.json files)
		if !sf.wrongLocation && len(sf.missing) == 0 {
			continue
		}

		// Skip missing file entries — these are informational only (file doesn't exist yet)
		if sf.missingFile {
			continue
		}

		// Skip tracked files — even if unmodified, deleting a tracked file
		// modifies the customer repo. Require manual review.
		if sf.gitStatus == gitStatusTrackedModified {
			skipped = append(skipped, fmt.Sprintf("%s: tracked with local modifications, skipping", sf.path))
			continue
		}
		if sf.gitStatus == gitStatusTrackedClean {
			skipped = append(skipped, fmt.Sprintf("%s: tracked in customer repo, skipping", sf.path))
			continue
		}

		// Delete the stale settings file
		if err := os.Remove(sf.path); err != nil {
			errors = append(errors, fmt.Sprintf("failed to delete %s: %v", sf.path, err))
			continue
		}
		fmt.Printf("  Deleted stale: %s\n", sf.path)
		needsRestart = true

		claudeDir := filepath.Dir(sf.path)

		// Only remove the parent .claude directory for wrong-location files.
		// For correct-location stale files, the directory is the RIGHT place —
		// removing it creates a race window where the daemon could recreate
		// settings before the fix does (gt-99u).
		if sf.wrongLocation {
			_ = os.Remove(claudeDir) // Best-effort, will fail if not empty
		}

		// Rig-root settings: just delete. Per-role files are authoritative.
		// There is no correct "rig-root" settings location to recreate at.
		if sf.agentType == "rig-root" {
			fmt.Printf("\n  %s Rig-root settings removed. Per-role settings in %s/{witness,polecats,...}/.claude/ are authoritative.\n", style.Warning.Render("⚠"), sf.rigName)
			continue
		}

		// Handle town-root files: redirect to mayor/ instead of recreating at root.
		// Town-root settings pollute ALL agents via directory traversal.
		// This handles both settings.json and settings.local.json at the town root.
		if sf.agentType == "mayor" && !strings.Contains(sf.path, "/mayor/") {
			mayorDir := filepath.Join(ctx.TownRoot, "mayor")

			if strings.HasSuffix(claudeDir, ".claude") {
				// Town-root .claude/settings{.local}.json → recreate at mayor/.claude/
				if err := os.MkdirAll(mayorDir, 0755); err == nil {
					runtimeConfig := config.ResolveRoleAgentConfig("mayor", ctx.TownRoot, mayorDir)
					_ = runtime.EnsureSettingsForRole(mayorDir, mayorDir, "mayor", runtimeConfig)
				}
			}

			// Town-root files were inherited by ALL agents via directory traversal.
			// Warn user to restart agents - don't auto-kill sessions as that's too disruptive,
			// especially since deacon runs gt doctor automatically which would create a loop.
			fmt.Printf("\n  %s Town-root settings were moved. Restart agents to pick up new config:\n", style.Warning.Render("⚠"))
			fmt.Printf("      gt up --restore\n\n")
			continue
		}

		// Recreate settings at the correct location using EnsureSettingsForRole.
		// For rig roles, compute settingsDir from role+rig path.
		// For town-level roles (mayor/deacon), settingsDir == workDir.
		settingsDir := filepath.Dir(claudeDir)
		workDir := settingsDir
		rigPath := ""
		if sf.rigName != "" {
			rigPath = filepath.Join(ctx.TownRoot, sf.rigName)
			sd := config.RoleSettingsDir(sf.agentType, rigPath)
			if sd != "" {
				settingsDir = sd
				// Use settingsDir as workDir too — the fix is about recreating settings
				// at the correct location, not provisioning slash commands. The actual
				// worktree workDir will be set correctly on agent restart.
				workDir = sd
			}
		}
		runtimeConfig := config.ResolveRoleAgentConfig(sf.agentType, ctx.TownRoot, rigPath)
		if err := runtime.EnsureSettingsForRole(settingsDir, workDir, sf.agentType, runtimeConfig); err != nil {
			errors = append(errors, fmt.Sprintf("failed to recreate settings for %s: %v", sf.path, err))
			continue
		}

		// Only cycle patrol roles if --restart-sessions was explicitly passed.
		// This prevents unexpected session restarts during routine --fix operations.
		// Crew and polecats are spawned on-demand and won't auto-restart anyway.
		if ctx.RestartSessions {
			if sf.agentType == "witness" || sf.agentType == "refinery" ||
				sf.agentType == "deacon" || sf.agentType == "mayor" {
				running, _ := t.HasSession(sf.sessionName)
				if running {
					// Cycle the agent by killing and letting gt up restart it.
					// Use KillSessionWithProcesses to ensure all descendant processes are killed.
					_ = t.KillSessionWithProcesses(sf.sessionName)
				}
			}
		}
	}

	// Report skipped files as warnings, not errors
	if len(skipped) > 0 {
		for _, s := range skipped {
			fmt.Printf("  Warning: %s\n", s)
		}
	}

	// Tell user to restart agents so they create correct settings
	if needsRestart && !ctx.RestartSessions {
		fmt.Printf("\n  %s Restart agents to create new settings:\n", style.Warning.Render("⚠"))
		fmt.Printf("      gt up --restore\n")
		fmt.Printf("\n  If you had custom Claude settings edits, re-apply them via 'gt hooks override <role>'.\n\n")
	}

	if len(errors) > 0 {
		return fmt.Errorf("%s", strings.Join(errors, "; "))
	}
	return nil
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// isIdentityAnchor checks if a CLAUDE.md file is the Gas Town town-root
// identity file. This includes both the minimal bootstrap anchor (<20 lines)
// and the expanded version with operational norms (Dolt awareness,
// communication hygiene, etc.). Both formats are intentional Gas Town files
// and should NOT be flagged as "wrong location".
//
// A Gas Town CLAUDE.md is identified by:
// - Starting with "# Gas Town" (the standard header)
// - Containing "prime" (the recovery instruction)
func isIdentityAnchor(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	content := string(data)
	return strings.HasPrefix(content, "# Gas Town") && strings.Contains(content, "prime")
}
