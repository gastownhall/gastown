package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/mail"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var mailDirJSON bool

var mailDirectoryCmd = &cobra.Command{
	Use:     "directory",
	Aliases: []string{"dir", "addresses"},
	Short:   "List all valid mail recipient addresses",
	Long: `List all valid mail recipient addresses in the town.

Shows agent addresses, group addresses, queue addresses, channel addresses,
and well-known special addresses.

Examples:
  gt mail directory              # List all addresses
  gt mail directory --json       # JSON output`,
	Args: cobra.NoArgs,
	RunE: runMailDirectory,
}

// DirectoryEntry represents an address in the directory.
type DirectoryEntry struct {
	Address string `json:"address"`
	Type    string `json:"type"`
}

func init() {
	mailDirectoryCmd.Flags().BoolVar(&mailDirJSON, "json", false, "Output as JSON")
	mailCmd.AddCommand(mailDirectoryCmd)
}

func runMailDirectory(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	b := beads.New(townRoot)
	var entries []DirectoryEntry
	var warnings int

	// 1. Agent addresses
	agents, err := b.ListAgentBeads()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list agents: %v\n", err)
		warnings++
	} else {
		for id := range agents {
			addr := mail.AgentBeadIDToAddress(id)
			if addr != "" {
				entries = append(entries, DirectoryEntry{Address: addr, Type: "agent"})
			}
		}
	}

	// 2. Group addresses
	groups, err := b.ListGroupBeads()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list groups: %v\n", err)
		warnings++
	} else {
		for name := range groups {
			entries = append(entries, DirectoryEntry{Address: "group:" + name, Type: "group"})
		}
	}

	// 3. Queue addresses
	queues, err := b.ListQueueBeads()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list queues: %v\n", err)
		warnings++
	} else {
		for id, issue := range queues {
			if issue == nil {
				continue
			}
			fields := beads.ParseQueueFields(issue.Description)
			if fields.Name == "" {
				fmt.Fprintf(os.Stderr, "warning: queue %s has no name field, skipping\n", id)
				continue
			}
			entries = append(entries, DirectoryEntry{Address: "queue:" + fields.Name, Type: "queue"})
		}
	}

	// 4. Channel addresses
	channels, err := b.ListChannelBeads()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not list channels: %v\n", err)
		warnings++
	} else {
		for name := range channels {
			entries = append(entries, DirectoryEntry{Address: "channel:" + name, Type: "channel"})
		}
	}

	// 5. Well-known addresses
	wellKnown := []DirectoryEntry{
		{Address: "mayor/", Type: "well-known"},
		{Address: "--human", Type: "well-known"},
		{Address: "--self", Type: "well-known"},
		{Address: "@town", Type: "special"},
		{Address: "@crew", Type: "special"},
		{Address: "@witnesses", Type: "special"},
		{Address: "@overseer", Type: "special"},
	}
	entries = append(entries, wellKnown...)

	// Deduplicate (e.g., mayor/ may appear as both agent and well-known)
	seen := make(map[string]bool)
	deduped := entries[:0]
	for _, e := range entries {
		if !seen[e.Address] {
			seen[e.Address] = true
			deduped = append(deduped, e)
		}
	}
	entries = deduped

	// Sort by type then address
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type < entries[j].Type
		}
		return entries[i].Address < entries[j].Address
	})

	if mailDirJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	}

	// Text output grouped by type
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ADDRESS\tTYPE")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\n", e.Address, e.Type)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if warnings > 0 {
		fmt.Fprintf(os.Stdout, "\nListed %d addresses (%d warnings)\n", len(entries), warnings)
	} else {
		fmt.Fprintf(os.Stdout, "\nListed %d addresses\n", len(entries))
	}
	return nil
}
