package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
)

var memoriesType string
var memoriesSearch string

func init() {
	memoriesCmd.Flags().StringVar(&memoriesType, "type", "", "Deprecated no-op: bd memories are untyped")
	_ = memoriesCmd.Flags().MarkDeprecated("type", "bd memories are untyped; the type is ignored")
	memoriesCmd.Flags().StringVar(&memoriesSearch, "search", "", "Case-insensitive filter on memory content")
	memoriesCmd.GroupID = GroupWork
	rootCmd.AddCommand(memoriesCmd)
}

var memoriesCmd = &cobra.Command{
	Use:     "memories",
	Short:   "List all stored memories",
	Long: `List all persistent memories from the beads memory store (bd memories).

Memories are managed by bd's first-class memory system and persist
across sessions. They are injected during gt prime to provide context
to agents.

Use --search for quick filtering.

--type is deprecated and ignored: bd memories are untyped.

Examples:
  gt memories
  gt memories --search "refinery"`,
	Args: cobra.NoArgs,
	RunE: runMemories,
}

func runMemories(cmd *cobra.Command, args []string) error {
	mems, err := bdMemoriesJSON()
	if err != nil {
		return fmt.Errorf("listing memories: %w", err)
	}

	// Filter by search term
	if memoriesSearch != "" {
		filtered := make(map[string]string)
		search := strings.ToLower(memoriesSearch)
		for k, v := range mems {
			if strings.Contains(strings.ToLower(v), search) {
				filtered[k] = v
			}
		}
		mems = filtered
	}

	if len(mems) == 0 {
		if memoriesSearch != "" {
			fmt.Println(style.Dim.Render("No memories matching " + style.Bold.Render(memoriesSearch) + "."))
		} else {
			fmt.Println(style.Dim.Render("No memories stored yet. Use gt remember to add one."))
		}
		return nil
	}

	keys := make([]string, 0, len(mems))
	for k := range mems {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Printf("Memories (%d):\n", len(keys))
	for _, k := range keys {
		fmt.Printf("  %s %s\n", style.Bold.Render(k+": "), mems[k])
	}
	return nil
}
