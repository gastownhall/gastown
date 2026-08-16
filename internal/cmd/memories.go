package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jonbaldie/gastown/internal/style"
	"github.com/spf13/cobra"
)

var memoriesTypeFilter string

func init() {
	memoriesCmd.Flags().StringVar(&memoriesTypeFilter, "type", "", "Filter by memory type: feedback, project, user, reference, general")
	memoriesCmd.GroupID = GroupWork
	rootCmd.AddCommand(memoriesCmd)
}

var memoriesCmd = &cobra.Command{
	Use:   "memories [search-term]",
	Short: "List or search stored memories",
	Long: `List or search memories stored in the beads key-value store.

Without arguments, lists all memories. With a search term, filters
memories whose key or value contains the term (case-insensitive).

Use --type to filter by memory category:
  feedback   Guidance or corrections from users
  project    Ongoing work context, goals, deadlines
  user       Info about the user's role and preferences
  reference  Pointers to external resources
  general    Uncategorized memories

Examples:
  gt memories                    # List all memories
  gt memories --type feedback    # Show only behavioral corrections
  gt memories refinery           # Search for memories about refinery`,
	Args: cobra.MaximumNArgs(1),
	RunE: runMemories,
}

func runMemories(cmd *cobra.Command, args []string) error {
	kvs, err := bdKvListJSON()
	if err != nil {
		return fmt.Errorf("listing memories: %w", err)
	}

	var search string
	if len(args) > 0 {
		search = strings.ToLower(args[0])
	}

	typeFilter := strings.ToLower(strings.TrimSpace(memoriesTypeFilter))
	if typeFilter != "" {
		if _, ok := validMemoryTypes[typeFilter]; !ok {
			return fmt.Errorf("invalid memory type %q — valid types: feedback, project, user, reference, general", typeFilter)
		}
	}

	// Filter for memory.* keys and optional search/type
	type memory struct {
		memType  string
		shortKey string
		value    string
	}
	var memories []memory

	for k, v := range kvs {
		if !strings.HasPrefix(k, memoryKeyPrefix) {
			continue
		}

		memType, shortKey := parseMemoryKey(k)

		if typeFilter != "" && memType != typeFilter {
			continue
		}

		if search != "" {
			if !strings.Contains(strings.ToLower(shortKey), search) &&
				!strings.Contains(strings.ToLower(v), search) &&
				!strings.Contains(strings.ToLower(memType), search) {
				continue
			}
		}

		memories = append(memories, memory{memType: memType, shortKey: shortKey, value: v})
	}

	sort.Slice(memories, func(i, j int) bool {
		if memories[i].memType != memories[j].memType {
			return memTypeRank(memories[i].memType) < memTypeRank(memories[j].memType)
		}
		return memories[i].shortKey < memories[j].shortKey
	})

	if len(memories) == 0 {
		if search != "" {
			fmt.Printf("No memories matching %q\n", search)
		} else if typeFilter != "" {
			fmt.Printf("No %s memories stored.\n", typeFilter)
		} else {
			fmt.Println("No memories stored. Use 'gt remember \"insight\"' to add one.")
		}
		return nil
	}

	header := "Memories"
	if typeFilter != "" {
		header = fmt.Sprintf("Memories [%s]", typeFilter)
	}
	if search != "" {
		header = fmt.Sprintf("%s matching %q", header, search)
	}
	fmt.Printf("%s (%d):\n\n", style.Bold.Render(header), len(memories))

	lastType := ""
	for _, m := range memories {
		if m.memType != lastType {
			if lastType != "" {
				fmt.Println()
			}
			fmt.Printf("  %s\n", style.Dim.Render("["+m.memType+"]"))
			lastType = m.memType
		}
		fmt.Printf("  %s\n", style.Bold.Render(m.shortKey))
		fmt.Printf("    %s\n\n", m.value)
	}

	return nil
}

// memTypeRank returns the sort order for a memory type (lower = first).
func memTypeRank(memType string) int {
	for i, t := range memoryTypeOrder {
		if t == memType {
			return i
		}
	}
	return len(memoryTypeOrder)
}
