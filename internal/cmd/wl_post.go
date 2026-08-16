package cmd

import (
	"fmt"
	"strings"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var (
	wlPostTitle       string
	wlPostDescription string
	wlPostProject     string
	wlPostType        string
	wlPostPriority    int
	wlPostEffort      string
	wlPostTags        string
)

var wlPostCmd = &cobra.Command{
	Use:   "post",
	Short: "Post a new wanted item to the commons",
	Long: `Post a new wanted item to the Wasteland commons (shared wanted board).

Creates a wanted item with a unique w-<hash> ID and inserts it into the
wl-commons database. Phase 1 (wild-west): direct write to main branch.

The posted_by field is set to the rig's DoltHub org (DOLTHUB_ORG) or
falls back to the directory name.

Examples:
  gt wl post --title "Fix auth bug" --project gastown --type bug
  gt wl post --title "Add federation sync" --type feature --priority 1 --effort large
  gt wl post --title "Update docs" --tags "docs,federation" --effort small`,
	RunE: runWlPost,
}

func init() {
	wlPostCmd.Flags().StringVar(&wlPostTitle, "title", "", "Title of the wanted item (required)")
	wlPostCmd.Flags().StringVarP(&wlPostDescription, "description", "d", "", "Detailed description")
	wlPostCmd.Flags().StringVar(&wlPostProject, "project", "", "Project name (e.g., gastown, beads)")
	wlPostCmd.Flags().StringVar(&wlPostType, "type", "", "Item type: feature, bug, design, rfc, docs")
	wlPostCmd.Flags().IntVar(&wlPostPriority, "priority", 2, "Priority: 0=critical, 1=high, 2=medium, 3=low, 4=backlog")
	wlPostCmd.Flags().StringVar(&wlPostEffort, "effort", "medium", "Effort level: trivial, small, medium, large, epic")
	wlPostCmd.Flags().StringVar(&wlPostTags, "tags", "", "Comma-separated tags (e.g., 'go,auth,federation')")

	_ = wlPostCmd.MarkFlagRequired("title")

	wlCmd.AddCommand(wlPostCmd)
}

func runWlPost(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	var tags []string
	if wlPostTags != "" {
		for _, t := range strings.Split(wlPostTags, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				tags = append(tags, t)
			}
		}
	}

	if err := validatePostInputs(wlPostType, wlPostEffort, wlPostPriority); err != nil {
		return err
	}

	store := doltserver.NewWLCommons(townRoot)

	wlCfg, err := wasteland.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading wasteland config: %w", err)
	}

	item := &doltserver.WantedItem{
		ID:          doltserver.GenerateWantedID(wlPostTitle),
		Title:       wlPostTitle,
		Description: wlPostDescription,
		Project:     wlPostProject,
		Type:        wlPostType,
		Priority:    wlPostPriority,
		Tags:        tags,
		PostedBy:    wlCfg.RigHandle,
		EffortLevel: wlPostEffort,
	}

	if err := postWanted(store, item); err != nil {
		return err
	}

	fmt.Printf("%s Posted wanted item: %s\n", style.Bold.Render("✓"), style.Bold.Render(item.ID))
	fmt.Printf("  Title:    %s\n", item.Title)
	if item.Project != "" {
		fmt.Printf("  Project:  %s\n", item.Project)
	}
	if item.Type != "" {
		fmt.Printf("  Type:     %s\n", item.Type)
	}
	fmt.Printf("  Priority: %d\n", item.Priority)
	fmt.Printf("  Effort:   %s\n", item.EffortLevel)
	if len(item.Tags) > 0 {
		fmt.Printf("  Tags:     %s\n", strings.Join(item.Tags, ", "))
	}
	fmt.Printf("  Posted by: %s\n", item.PostedBy)

	return nil
}

// validatePostInputs validates the type, effort, and priority fields.
func validatePostInputs(itemType, effort string, priority int) error {
	validTypes := map[string]bool{
		"feature": true, "bug": true, "design": true, "rfc": true, "docs": true,
	}
	if itemType != "" && !validTypes[itemType] {
		return fmt.Errorf("invalid type %q: must be one of feature, bug, design, rfc, docs", itemType)
	}

	validEfforts := map[string]bool{
		"trivial": true, "small": true, "medium": true, "large": true, "epic": true,
	}
	if !validEfforts[effort] {
		return fmt.Errorf("invalid effort %q: must be one of trivial, small, medium, large, epic", effort)
	}

	if priority < 0 || priority > 4 {
		return fmt.Errorf("invalid priority %d: must be 0-4", priority)
	}

	return nil
}

// postWanted contains the testable business logic for posting a wanted item.
func postWanted(store doltserver.WLCommonsStore, item *doltserver.WantedItem) error {
	if err := store.EnsureDB(); err != nil {
		return fmt.Errorf("ensuring wl-commons database: %w", err)
	}

	if err := store.InsertWanted(item); err != nil {
		return fmt.Errorf("posting wanted item: %w", err)
	}

	return nil
}
