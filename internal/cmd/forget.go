package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

func init() {
	forgetCmd.GroupID = GroupWork
	rootCmd.AddCommand(forgetCmd)
}

var forgetCmd = &cobra.Command{
	Use:   "forget <key>",
	Short: "Remove a stored memory",
	Long: `Remove a memory from the beads key-value store.

The key should match the short name shown by 'gt memories'.
For typed memories, use type/key format or just the key (searches all types).

Examples:
  gt forget refinery-worktree
  gt forget feedback/dont-mock-db
  gt forget hooks-package-structure`,
	Args: cobra.ExactArgs(1),
	RunE: runForget,
}

func runForget(cmd *cobra.Command, args []string) error {
	key := args[0]

	// Strip memory. prefix if the user included it
	key = strings.TrimPrefix(key, memoryKeyPrefix)

	// Support type/key format (e.g., "feedback/dont-mock-db")
	if slashIdx := strings.Index(key, "/"); slashIdx > 0 {
		memType := key[:slashIdx]
		shortKey := key[slashIdx+1:]
		if _, ok := validMemoryTypes[memType]; ok {
			fullKey := memoryKeyPrefix + memType + "." + shortKey
			existing, err := bdKvGet(fullKey)
			if err != nil || existing == "" {
				return fmt.Errorf("memory %q not found", key)
			}
			if err := bdForget(memType + "." + shortKey); err != nil {
				return fmt.Errorf("removing memory: %w", err)
			}
			fmt.Printf("%s Forgot memory: %s\n", style.Success.Render("✓"), style.Bold.Render(key))
			return nil
		}
	}

	// Try typed key first (memory.<type>.<key> for each known type)
	for _, t := range memoryTypeOrder {
		fullKey := memoryKeyPrefix + t + "." + key
		existing, _ := bdKvGet(fullKey)
		if existing != "" {
			if err := bdForget(t + "." + key); err != nil {
				return fmt.Errorf("removing memory: %w", err)
			}
			displayKey := key
			if t != "general" {
				displayKey = t + "/" + key
			}
			fmt.Printf("%s Forgot memory: %s\n", style.Success.Render("✓"), style.Bold.Render(displayKey))
			return nil
		}
	}

	// Try legacy untyped key (memory.<key>)
	fullKey := memoryKeyPrefix + key
	existing, err := bdKvGet(fullKey)
	if err != nil || existing == "" {
		return fmt.Errorf("memory %q not found", key)
	}

	if err := bdForget(key); err != nil {
		return fmt.Errorf("removing memory: %w", err)
	}

	fmt.Printf("%s Forgot memory: %s\n", style.Success.Render("✓"), style.Bold.Render(key))
	return nil
}
