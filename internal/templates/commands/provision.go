// Package commands provides agent-agnostic command provisioning.
package commands

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonbaldie/gastown/internal/config"
)

//go:embed bodies/*.md
var bodiesFS embed.FS

// Field represents a frontmatter key-value pair.
type Field struct {
	Key   string
	Value string
}

// Command defines a slash command with agent-specific frontmatter.
type Command struct {
	Name        string
	Description string
	AgentFields map[string][]Field
}

// getAgentConfigDir returns the config directory for an agent from the preset registry.
// Returns empty string if the agent has no config directory.
func getAgentConfigDir(agent string) string {
	preset := config.GetAgentPresetByName(agent)
	if preset == nil {
		return ""
	}
	return preset.ConfigDir
}

// Commands is the registry of available commands.
var Commands = []Command{
	{
		Name:        "done",
		Description: "Signal work complete and submit to merge queue",
		AgentFields: map[string][]Field{
			"claude": {
				{"allowed-tools", "Bash(gt done:*), Bash(git status:*), Bash(git log:*), Bash(git add:*), Bash(git commit:*), Bash(git push:*), Bash(bd close:*)"},
				{"argument-hint", "[--status COMPLETED|ESCALATED|DEFERRED] [--pre-verified]"},
			},
		},
	},
	{
		Name:        "handoff",
		Description: "Hand off to fresh session, work continues from hook",
		AgentFields: map[string][]Field{
			"claude": {
				{"allowed-tools", "Bash(gt handoff:*)"},
				{"argument-hint", "[message]"},
			},
			// opencode: no extra fields, just description
		},
	},
	{
		Name:        "review",
		Description: "Review code changes with structured grading (A-F)",
		AgentFields: map[string][]Field{
			"claude": {
				{"allowed-tools", "Bash(git diff:*), Bash(git rev-parse:*), Bash(gh pr diff:*)"},
				{"argument-hint", "[--staged | --branch | --pr <url>]"},
			},
		},
	},
}

// BuildCommand assembles frontmatter + body for an agent.
func BuildCommand(cmd Command, agent string) (string, error) {
	body, err := bodiesFS.ReadFile("bodies/" + cmd.Name + ".md")
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}

	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("description: %s\n", cmd.Description))

	if fields, ok := cmd.AgentFields[agent]; ok {
		for _, f := range fields {
			b.WriteString(fmt.Sprintf("%s: %s\n", f.Key, f.Value))
		}
	}

	b.WriteString("---\n\n")
	b.Write(body)

	return b.String(), nil
}

// ProvisionFor provisions commands for an agent.
func ProvisionFor(workspacePath, agent string) error {
	agent = strings.ToLower(agent)
	configDir := getAgentConfigDir(agent)
	if configDir == "" {
		return fmt.Errorf("unknown agent or no config dir: %s", agent)
	}

	dir := filepath.Join(workspacePath, configDir, "commands")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating dir: %w", err)
	}

	for _, cmd := range Commands {
		path := filepath.Join(dir, cmd.Name+".md")

		// Don't overwrite existing
		if _, err := os.Stat(path); err == nil {
			continue
		}

		content, err := BuildCommand(cmd, agent)
		if err != nil {
			return fmt.Errorf("building %s: %w", cmd.Name, err)
		}

		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", cmd.Name, err)
		}
	}

	return nil
}

// MissingFor returns commands missing for an agent.
func MissingFor(workspacePath, agent string) []string {
	agent = strings.ToLower(agent)
	configDir := getAgentConfigDir(agent)
	if configDir == "" {
		return nil
	}

	dir := filepath.Join(workspacePath, configDir, "commands")
	var missing []string

	for _, cmd := range Commands {
		path := filepath.Join(dir, cmd.Name+".md")
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, cmd.Name)
		}
	}

	return missing
}

// FindByName returns the command with the given name, or nil if not found.
func FindByName(name string) *Command {
	for i := range Commands {
		if Commands[i].Name == name {
			return &Commands[i]
		}
	}
	return nil
}

// Names returns the names of all registered commands.
func Names() []string {
	names := make([]string, len(Commands))
	for i, cmd := range Commands {
		names[i] = cmd.Name
	}
	return names
}

// IsKnownAgent returns true if the agent has a config directory for command provisioning.
func IsKnownAgent(agent string) bool {
	return getAgentConfigDir(strings.ToLower(agent)) != ""
}
