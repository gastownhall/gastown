package cmd

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var wlClaimCmd = &cobra.Command{
	Use:   "claim <wanted-id>",
	Short: "Claim a wanted item",
	Long: `Claim a wanted item on the shared wanted board.

Updates the wanted row: claimed_by=<your rig handle>, status='claimed'.
The item must exist and have status='open'.

In wild-west mode (Phase 1), this writes directly to the local wl-commons
database. In PR mode, this will create a DoltHub PR instead.

Examples:
  gt wl claim w-abc123`,
	Args: cobra.ExactArgs(1),
	RunE: runWlClaim,
}

func init() {
	wlCmd.AddCommand(wlClaimCmd)
}

func runWlClaim(cmd *cobra.Command, args []string) error {
	wantedID := args[0]

	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	wlCfg, err := wasteland.LoadConfig(townRoot)
	if err != nil {
		return fmt.Errorf("loading wasteland config: %w", err)
	}
	rigHandle := wlCfg.RigHandle

	dbName := wasteland.ResolveDBName(townRoot)
	var item *doltserver.WantedItem
	if !doltserver.DatabaseExists(townRoot, dbName) {
		// Fallback for wl-commons clone-based workspaces (join creates .wasteland clone).
		if wlCfg.LocalDir == "" {
			return fmt.Errorf("database %q not found\nJoin a wasteland first with: gt wl join <org/db>", dbName)
		}
		if err := claimWantedInLocalClone(wlCfg.LocalDir, wantedID, rigHandle); err != nil {
			return err
		}
		item = &doltserver.WantedItem{ID: wantedID, Status: "claimed", ClaimedBy: rigHandle}
	} else {
		store := doltserver.NewWLCommons(townRoot)
		var err error
		item, err = claimWanted(store, wantedID, rigHandle)
		if err != nil {
			return err
		}
	}

	fmt.Printf("%s Claimed %s\n", style.Bold.Render("✓"), wantedID)
	fmt.Printf("  Claimed by: %s\n", rigHandle)
	if item.Title != "" {
		fmt.Printf("  Title: %s\n", item.Title)
	}

	return nil
}

// claimWanted contains the testable business logic for claiming a wanted item.
// The returned WantedItem reflects pre-claim state (status "open", empty ClaimedBy);
// callers needing post-claim state should re-query.
func claimWanted(store doltserver.WLCommonsStore, wantedID, rigHandle string) (*doltserver.WantedItem, error) {
	item, err := store.QueryWanted(wantedID)
	if err != nil {
		return nil, fmt.Errorf("querying wanted item: %w", err)
	}

	if item.Status != "open" {
		return nil, fmt.Errorf("wanted item %s is not open (status: %s)", wantedID, item.Status)
	}

	if err := store.ClaimWanted(wantedID, rigHandle); err != nil {
		return nil, fmt.Errorf("claiming wanted item: %w", err)
	}

	return item, nil
}

func claimWantedInLocalClone(localDir, wantedID, rigHandle string) error {
	script := fmt.Sprintf(`UPDATE wanted SET claimed_by='%s', status='claimed', updated_at=NOW()
WHERE id='%s' AND status='open';
CALL DOLT_ADD('-A');
CALL DOLT_COMMIT('-m', 'wl claim: %s');`,
		doltserver.EscapeSQL(rigHandle), doltserver.EscapeSQL(wantedID), doltserver.EscapeSQL(wantedID))

	cmd := exec.Command("dolt", "sql", "-q", script)
	cmd.Dir = localDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.ToLower(string(out))
		if strings.Contains(s, "nothing to commit") {
			return fmt.Errorf("wanted item %s is not open or does not exist", wantedID)
		}
		return fmt.Errorf("claiming wanted item: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}
