package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/gastown/internal/atomicfile"
	"github.com/steveyegge/gastown/internal/config"
)

// trustFlags are the per-project fields Claude Code's startup trust dialogs
// write into .claude.json when a human accepts them. Pre-setting them lets
// headless agents start without an interactive prompt:
//   - hasTrustDialogAccepted: the folder-trust dialog
//   - hasClaudeMdExternalIncludesApproved / ...WarningShown: the dialog shown
//     when a project CLAUDE.md @-imports files outside the project directory
var trustFlags = []string{
	"hasTrustDialogAccepted",
	"hasClaudeMdExternalIncludesApproved",
	"hasClaudeMdExternalIncludesWarningShown",
}

// PreapproveProjectTrustForRuntime seeds Claude project trust for projectDir
// when rc targets Claude Code (the default). Other providers keep no
// .claude.json, so there is nothing to seed.
func PreapproveProjectTrustForRuntime(rc *config.RuntimeConfig, projectDir string) error {
	if rc != nil && rc.Provider != "" && rc.Provider != string(config.AgentClaude) {
		return nil
	}
	return PreapproveClaudeProjectTrust(projectDir)
}

// PreapproveClaudeProjectTrust marks projectDir as trusted in Claude Code's
// global state file (.claude.json). Claude Code keys trust dialogs off a
// per-project-directory entry in that file — settings.json (including
// bypassPermissions) cannot suppress them — so every freshly created worktree
// would otherwise stall on an interactive prompt when the repo's CLAUDE.md
// imports files outside the worktree (e.g. @~/.claude/...).
func PreapproveClaudeProjectTrust(projectDir string) error {
	configPath := claudeConfigJSONPath()
	if configPath == "" {
		return fmt.Errorf("cannot locate .claude.json: no CLAUDE_CONFIG_DIR and no home directory")
	}
	return preapproveProjectTrust(configPath, projectDir)
}

// claudeConfigJSONPath returns the path of Claude Code's global state file,
// honoring CLAUDE_CONFIG_DIR the same way Claude Code does. Returns "" if the
// location cannot be determined.
func claudeConfigJSONPath() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return filepath.Join(dir, ".claude.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude.json")
}

// preapproveProjectTrust sets the trust flags for projectDir in the
// .claude.json at configPath, preserving all other content. The file is
// created if missing.
func preapproveProjectTrust(configPath, projectDir string) error {
	// Top level and the projects map are kept as RawMessage so unrelated
	// fields round-trip byte-for-byte parsed JSON, not gastown's guess at
	// Claude Code's schema.
	doc := map[string]json.RawMessage{}
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", configPath, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	projects := map[string]json.RawMessage{}
	if raw, ok := doc["projects"]; ok {
		if err := json.Unmarshal(raw, &projects); err != nil {
			return fmt.Errorf("parsing projects in %s: %w", configPath, err)
		}
	}

	entry := map[string]json.RawMessage{}
	if raw, ok := projects[projectDir]; ok {
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("parsing project entry %s in %s: %w", projectDir, configPath, err)
		}
	}
	for _, flag := range trustFlags {
		entry[flag] = json.RawMessage("true")
	}

	entryRaw, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshaling project entry: %w", err)
	}
	projects[projectDir] = entryRaw

	projectsRaw, err := json.Marshal(projects)
	if err != nil {
		return fmt.Errorf("marshaling projects: %w", err)
	}
	doc["projects"] = projectsRaw

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", configPath, err)
	}
	// Atomic write: Claude Code sessions rewrite this file too, and a torn
	// write would corrupt state for every session on the machine.
	if err := atomicfile.WriteFile(configPath, out, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}
	return nil
}
