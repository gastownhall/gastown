package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/jonbaldie/gastown/internal/hooks"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	hooksScanJSON    bool
	hooksScanVerbose bool
)

var hooksScanCmd = &cobra.Command{
	Use:   "scan",
	Short: "Scan workspace for existing Claude Code hooks",
	Long: `Scan for .claude/settings.json files and display hooks by type.

Hook types:
  SessionStart     - Runs when Claude session starts
  PreCompact       - Runs before context compaction
  UserPromptSubmit - Runs before user prompt is submitted
  PreToolUse       - Runs before tool execution
  PostToolUse      - Runs after tool execution
  Stop             - Runs when Claude session stops
  WorktreeCreate   - Runs when agent worktree isolation creates a worktree
  WorktreeRemove   - Runs when agent worktree isolation removes a worktree

Examples:
  gt hooks scan              # List all hooks in workspace
  gt hooks scan --verbose    # Show hook commands
  gt hooks scan --json       # Output as JSON`,
	RunE: runHooksScan,
}

func init() {
	hooksCmd.AddCommand(hooksScanCmd)
	hooksScanCmd.Flags().BoolVar(&hooksScanJSON, "json", false, "Output as JSON")
	hooksScanCmd.Flags().BoolVarP(&hooksScanVerbose, "verbose", "v", false, "Show hook commands")
}

// HookInfo contains information about a discovered hook.
type HookInfo struct {
	Type     string   `json:"type"`     // Hook type (SessionStart, etc.)
	Location string   `json:"location"` // Path to the settings file
	Agent    string   `json:"agent"`    // Agent that owns this hook (e.g., "polecat/nux")
	Matcher  string   `json:"matcher"`  // Pattern matcher (empty = all)
	Commands []string `json:"commands"` // Hook commands
	Status   string   `json:"status"`   // "active" or "disabled"
}

// HooksOutput is the JSON output structure.
type HooksOutput struct {
	TownRoot string     `json:"town_root"`
	Hooks    []HookInfo `json:"hooks"`
	Count    int        `json:"count"`
}

func runHooksScan(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find all .claude/settings.json files via DiscoverTargets
	hookInfos, err := discoverHooks(townRoot)
	if err != nil {
		return fmt.Errorf("discovering hooks: %w", err)
	}

	if hooksScanJSON {
		return outputHooksJSON(townRoot, hookInfos)
	}

	return outputHooksHuman(townRoot, hookInfos)
}

// discoverHooks finds all Claude Code hooks in the workspace using hooks.DiscoverTargets.
func discoverHooks(townRoot string) ([]HookInfo, error) {
	targets, err := hooks.DiscoverTargets(townRoot)
	if err != nil {
		return nil, err
	}

	var infos []HookInfo

	for _, target := range targets {
		settings, err := hooks.LoadSettings(target.Path)
		if err != nil {
			continue // Skip files that can't be parsed
		}

		found := extractHookInfos(settings, target.Path, target.DisplayKey())
		infos = append(infos, found...)
	}

	return infos, nil
}

// extractHookInfos extracts HookInfo entries from a loaded settings file.
func extractHookInfos(settings *hooks.SettingsJSON, path, agent string) []HookInfo {
	var infos []HookInfo

	for _, eventType := range hooks.EventTypes {
		entries := settings.Hooks.GetEntries(eventType)
		for _, entry := range entries {
			var commands []string
			for _, h := range entry.Hooks {
				if h.Command != "" {
					commands = append(commands, h.Command)
				}
			}

			if len(commands) > 0 {
				infos = append(infos, HookInfo{
					Type:     eventType,
					Location: path,
					Agent:    agent,
					Matcher:  entry.Matcher,
					Commands: commands,
					Status:   "active",
				})
			}
		}
	}

	return infos
}

// parseHooksFile parses a .claude/settings.json file and extracts hooks.
func parseHooksFile(path, agent string) ([]HookInfo, error) {
	settings, err := hooks.LoadSettings(path)
	if err != nil {
		return nil, err
	}

	// LoadSettings returns zero-value for missing files; check if it was actually empty
	return extractHookInfos(settings, path, agent), nil
}

func outputHooksJSON(townRoot string, hookInfos []HookInfo) error {
	output := HooksOutput{
		TownRoot: townRoot,
		Hooks:    hookInfos,
		Count:    len(hookInfos),
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}

	fmt.Println(string(data))
	return nil
}

func outputHooksHuman(townRoot string, hookInfos []HookInfo) error {
	if len(hookInfos) == 0 {
		fmt.Println(style.Dim.Render("No Claude Code hooks found in workspace"))
		return nil
	}

	fmt.Printf("\n%s Claude Code Hooks\n", style.Bold.Render("🪝"))
	fmt.Printf("Town root: %s\n\n", style.Dim.Render(townRoot))

	// Group by hook type
	byType := make(map[string][]HookInfo)

	for _, h := range hookInfos {
		byType[h.Type] = append(byType[h.Type], h)
	}

	// Use canonical event type order, plus any extras
	typeOrder := make([]string, len(hooks.EventTypes))
	copy(typeOrder, hooks.EventTypes)
	for t := range byType {
		found := false
		for _, o := range typeOrder {
			if t == o {
				found = true
				break
			}
		}
		if !found {
			typeOrder = append(typeOrder, t)
		}
	}

	for _, hookType := range typeOrder {
		typeHooks := byType[hookType]
		if len(typeHooks) == 0 {
			continue
		}

		fmt.Printf("%s %s\n", style.Bold.Render("▸"), hookType)

		for _, h := range typeHooks {
			statusIcon := "●"
			if h.Status != "active" {
				statusIcon = "○"
			}

			matcherStr := ""
			if h.Matcher != "" {
				matcherStr = fmt.Sprintf(" [%s]", h.Matcher)
			}

			fmt.Printf("  %s %-25s%s\n", statusIcon, h.Agent, style.Dim.Render(matcherStr))

			if hooksScanVerbose {
				for _, cmd := range h.Commands {
					fmt.Printf("    %s %s\n", style.Dim.Render("→"), cmd)
				}
			}
		}
		fmt.Println()
	}

	fmt.Printf("%s %d hooks found\n", style.Dim.Render("Total:"), len(hookInfos))

	return nil
}
