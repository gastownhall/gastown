package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jonbaldie/gastown/internal/beads"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

// Group command flags
var (
	groupJSON    bool
	groupMembers []string
)

var mailGroupCmd = &cobra.Command{
	Use:   "group",
	Short: "Manage mail groups",
	Long: `Create and manage mail distribution groups.

Groups are named collections of addresses used for mail distribution.
Members can be:
  - Direct addresses (gastown/crew/max)
  - Patterns (*/witness, gastown/*)
  - Other group names (nested groups)

Examples:
  gt mail group list                              # List all groups
  gt mail group show ops-team                     # Show group members
  gt mail group create ops-team gastown/witness gastown/crew/max
  gt mail group add ops-team deacon/
  gt mail group remove ops-team gastown/witness
  gt mail group delete ops-team`,
	RunE: requireSubcommand,
}

var groupListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all groups",
	Long:  "List all mail distribution groups.",
	Args:  cobra.NoArgs,
	RunE:  runGroupList,
}

var groupShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Show group details",
	Long:  "Display the members and metadata for a group.",
	Args:  cobra.ExactArgs(1),
	RunE:  runGroupShow,
}

var groupCreateCmd = &cobra.Command{
	Use:   "create <name> [members...]",
	Short: "Create a new group",
	Long: `Create a new mail distribution group.

Members can be specified as positional arguments or with --member flags.

Examples:
  gt mail group create ops-team gastown/witness gastown/crew/max
  gt mail group create ops-team --member gastown/witness --member gastown/crew/max`,
	Args: cobra.MinimumNArgs(1),
	RunE: runGroupCreate,
}

var groupAddCmd = &cobra.Command{
	Use:   "add <name> <member>",
	Short: "Add member to group",
	Long:  "Add a new member to an existing group.",
	Args:  cobra.ExactArgs(2),
	RunE:  runGroupAdd,
}

var groupRemoveCmd = &cobra.Command{
	Use:   "remove <name> <member>",
	Short: "Remove member from group",
	Long:  "Remove a member from an existing group.",
	Args:  cobra.ExactArgs(2),
	RunE:  runGroupRemove,
}

var groupDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a group",
	Long:  "Permanently delete a mail distribution group.",
	Args:  cobra.ExactArgs(1),
	RunE:  runGroupDelete,
}

func init() {
	// List flags
	groupListCmd.Flags().BoolVar(&groupJSON, "json", false, "Output as JSON")

	// Show flags
	groupShowCmd.Flags().BoolVar(&groupJSON, "json", false, "Output as JSON")

	// Create flags
	groupCreateCmd.Flags().StringArrayVar(&groupMembers, "member", nil, "Member to add (repeatable)")

	// Add subcommands
	mailGroupCmd.AddCommand(groupListCmd)
	mailGroupCmd.AddCommand(groupShowCmd)
	mailGroupCmd.AddCommand(groupCreateCmd)
	mailGroupCmd.AddCommand(groupAddCmd)
	mailGroupCmd.AddCommand(groupRemoveCmd)
	mailGroupCmd.AddCommand(groupDeleteCmd)

	mailCmd.AddCommand(mailGroupCmd)
}

func runGroupList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	b := beads.New(townRoot)
	groups, err := b.ListGroupBeads()
	if err != nil {
		return fmt.Errorf("listing groups: %w", err)
	}

	if groupJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(groups)
	}

	if len(groups) == 0 {
		fmt.Println("No groups defined.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tMEMBERS\tCREATED BY")
	for name, fields := range groups {
		memberCount := len(fields.Members)
		memberStr := fmt.Sprintf("%d member(s)", memberCount)
		if memberCount <= 3 {
			memberStr = strings.Join(fields.Members, ", ")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", name, memberStr, fields.CreatedBy)
	}
	return w.Flush()
}

func runGroupShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	b := beads.New(townRoot)
	_, fields, err := b.GetGroupByName(name)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return fmt.Errorf("group not found: %s", name)
		}
		return fmt.Errorf("getting group: %w", err)
	}

	if groupJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(fields)
	}

	fmt.Printf("Group: %s\n", fields.Name)
	fmt.Printf("Created by: %s\n", fields.CreatedBy)
	if fields.CreatedAt != "" {
		fmt.Printf("Created at: %s\n", fields.CreatedAt)
	}
	fmt.Println()
	fmt.Println("Members:")
	if len(fields.Members) == 0 {
		fmt.Println("  (no members)")
	} else {
		for _, m := range fields.Members {
			fmt.Printf("  - %s\n", m)
		}
	}
	return nil
}

func runGroupCreate(cmd *cobra.Command, args []string) error {
	name := args[0]
	members := args[1:] // Positional members

	// Add --member flag values
	members = append(members, groupMembers...)

	if !isValidGroupName(name) {
		return fmt.Errorf("invalid group name %q: must be alphanumeric with dashes/underscores", name)
	}

	// Validate member patterns
	for _, m := range members {
		if !isValidMemberPattern(m) {
			return fmt.Errorf("invalid member pattern: %s", m)
		}
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Detect creator
	createdBy := os.Getenv("BD_ACTOR")
	if createdBy == "" {
		createdBy = "unknown"
	}

	b := beads.New(townRoot)

	// Check if group already exists
	existing, _, err := b.GetGroupByName(name)
	if err != nil && !errors.Is(err, beads.ErrNotFound) {
		return err
	}
	if existing != nil {
		return fmt.Errorf("group already exists: %s", name)
	}

	_, err = b.CreateGroupBead(name, &beads.GroupFields{
		Members:   members,
		CreatedBy: createdBy,
	})
	if err != nil {
		return fmt.Errorf("creating group: %w", err)
	}

	fmt.Printf("Created group %q with %d member(s)\n", name, len(members))
	return nil
}

func runGroupAdd(cmd *cobra.Command, args []string) error {
	name := args[0]
	member := args[1]

	if !isValidMemberPattern(member) {
		return fmt.Errorf("invalid member pattern: %s", member)
	}

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	b := beads.New(townRoot)
	if _, err := b.AddGroupMember(name, member); err != nil {
		return fmt.Errorf("adding member: %w", err)
	}

	fmt.Printf("Added %q to group %q\n", member, name)
	return nil
}

func runGroupRemove(cmd *cobra.Command, args []string) error {
	name := args[0]
	member := args[1]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	b := beads.New(townRoot)
	if _, err := b.RemoveGroupMember(name, member); err != nil {
		return fmt.Errorf("removing member: %w", err)
	}

	fmt.Printf("Removed %q from group %q\n", member, name)
	return nil
}

func runGroupDelete(cmd *cobra.Command, args []string) error {
	name := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	b := beads.New(townRoot)

	// Check if group exists
	_, _, err = b.GetGroupByName(name)
	if err != nil {
		if errors.Is(err, beads.ErrNotFound) {
			return fmt.Errorf("group not found: %s", name)
		}
		return err
	}

	if err := b.DeleteGroupBead(name); err != nil {
		return fmt.Errorf("deleting group: %w", err)
	}

	fmt.Printf("Deleted group %q\n", name)
	return nil
}

// isValidGroupName checks if a group name is valid.
// Group names must be alphanumeric with dashes and underscores.
func isValidGroupName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// isValidMemberPattern checks if a member pattern is syntactically valid.
// Valid patterns include:
// - Direct addresses: gastown/crew/max, mayor/, deacon/
// - Wildcards: */witness, gastown/*, gastown/crew/*
// - Special patterns: @town, @crew, @witnesses
// - Group names: ops-team
func isValidMemberPattern(pattern string) bool {
	if pattern == "" {
		return false
	}

	// @ patterns are valid
	if strings.HasPrefix(pattern, "@") {
		return len(pattern) > 1
	}

	// Path patterns with wildcards
	if strings.Contains(pattern, "/") {
		// Must have valid path segments
		parts := strings.Split(pattern, "/")
		for _, p := range parts {
			if p == "" && pattern[len(pattern)-1] != '/' {
				return false // Empty segment (except trailing /)
			}
		}
		return true
	}

	// Simple name (group reference) - use same validation as group names
	return isValidGroupName(pattern)
}
