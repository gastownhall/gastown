package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// HookRegistry represents the hooks/registry.toml structure.
type HookRegistry struct {
	Hooks map[string]HookDefinition `toml:"hooks"`
}

// HookDefinition represents a single hook definition in the registry.
type HookDefinition struct {
	Description string   `toml:"description"`
	Event       string   `toml:"event"`
	Matchers    []string `toml:"matchers"`
	Command     string   `toml:"command"`
	Roles       []string `toml:"roles"`
	Scope       string   `toml:"scope"`
	Enabled     bool     `toml:"enabled"`
}

var (
	hooksRegistryAll     bool
	hooksRegistryVerbose bool
)

var hooksRegistryCmd = &cobra.Command{
	Use:   "registry",
	Short: "List available hooks from the registry",
	Long: `List all hooks defined in the hook registry.

The registry is at ~/gt/hooks/registry.toml and defines hooks that can be
installed for different roles (crew, polecat, witness, etc.).

Examples:
  gt hooks registry           # Show enabled hooks
  gt hooks registry --all     # Show all hooks including disabled`,
	RunE: runHooksRegistry,
}

func init() {
	hooksCmd.AddCommand(hooksRegistryCmd)
	hooksRegistryCmd.Flags().BoolVarP(&hooksRegistryAll, "all", "a", false, "Show all hooks including disabled")
	hooksRegistryCmd.Flags().BoolVarP(&hooksRegistryVerbose, "verbose", "v", false, "Show hook commands and matchers")
}

// LoadRegistry loads the hook registry from the town's hooks directory.
func LoadRegistry(townRoot string) (*HookRegistry, error) {
	registryPath := filepath.Join(townRoot, "hooks", "registry.toml")

	data, err := os.ReadFile(registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("hook registry not found at %s", registryPath)
		}
		return nil, fmt.Errorf("reading registry: %w", err)
	}

	var registry HookRegistry
	if _, err := toml.Decode(string(data), &registry); err != nil {
		return nil, fmt.Errorf("parsing registry: %w", err)
	}

	return &registry, nil
}

func runHooksRegistry(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwd()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	registry, err := LoadRegistry(townRoot)
	if err != nil {
		return err
	}

	if len(registry.Hooks) == 0 {
		fmt.Println(style.Dim.Render("No hooks defined in registry"))
		return nil
	}

	fmt.Printf("\n%s Hook Registry\n", style.Bold.Render("📋"))
	fmt.Printf("Source: %s\n\n", style.Dim.Render(filepath.Join(townRoot, "hooks", "registry.toml")))

	// Group by event type
	byEvent := make(map[string][]struct {
		name string
		def  HookDefinition
	})
	eventOrder := []string{"PreToolUse", "PostToolUse", "SessionStart", "PreCompact", "UserPromptSubmit", "Stop", "WorktreeCreate", "WorktreeRemove"}

	for name, def := range registry.Hooks {
		if !hooksRegistryAll && !def.Enabled {
			continue
		}
		byEvent[def.Event] = append(byEvent[def.Event], struct {
			name string
			def  HookDefinition
		}{name, def})
	}

	// Add any events not in the predefined order
	for event := range byEvent {
		found := false
		for _, o := range eventOrder {
			if event == o {
				found = true
				break
			}
		}
		if !found {
			eventOrder = append(eventOrder, event)
		}
	}

	count := 0
	for _, event := range eventOrder {
		hooks := byEvent[event]
		if len(hooks) == 0 {
			continue
		}

		fmt.Printf("%s %s\n", style.Bold.Render("▸"), event)

		for _, h := range hooks {
			count++
			statusIcon := "●"
			statusColor := style.Success
			if !h.def.Enabled {
				statusIcon = "○"
				statusColor = style.Dim
			}

			rolesStr := strings.Join(h.def.Roles, ", ")
			scopeStr := h.def.Scope

			fmt.Printf("  %s %s\n", statusColor.Render(statusIcon), style.Bold.Render(h.name))
			fmt.Printf("    %s\n", h.def.Description)
			fmt.Printf("    %s %s  %s %s\n",
				style.Dim.Render("roles:"), rolesStr,
				style.Dim.Render("scope:"), scopeStr)

			if hooksRegistryVerbose {
				fmt.Printf("    %s %s\n", style.Dim.Render("command:"), h.def.Command)
				for _, m := range h.def.Matchers {
					fmt.Printf("    %s %s\n", style.Dim.Render("matcher:"), m)
				}
			}
		}
		fmt.Println()
	}

	fmt.Printf("%s %d hooks in registry\n", style.Dim.Render("Total:"), count)

	return nil
}
