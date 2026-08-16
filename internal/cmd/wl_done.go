package cmd

import (
	"crypto/sha256"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jonbaldie/gastown/internal/doltserver"
	"github.com/jonbaldie/gastown/internal/style"
	"github.com/jonbaldie/gastown/internal/wasteland"
	"github.com/jonbaldie/gastown/internal/workspace"
	"github.com/spf13/cobra"
)

var wlDoneEvidence string

var wlDoneCmd = &cobra.Command{
	Use:   "done <wanted-id>",
	Short: "Submit completion evidence for a wanted item",
	Long: `Submit completion evidence for a claimed wanted item.

Inserts a completion record and updates the wanted item status to 'in_review'.
The item must be claimed by your rig.

The --evidence flag provides the evidence URL (PR link, commit hash, etc.).

A completion ID is generated as c-<hash> where hash is derived from the
wanted ID, rig handle, and timestamp.

Examples:
  gt wl done w-abc123 --evidence 'https://github.com/org/repo/pull/123'
  gt wl done w-abc123 --evidence 'commit abc123def'`,
	Args: cobra.ExactArgs(1),
	RunE: runWlDone,
}

func init() {
	wlDoneCmd.Flags().StringVar(&wlDoneEvidence, "evidence", "", "Evidence URL or description (required)")
	_ = wlDoneCmd.MarkFlagRequired("evidence")

	wlCmd.AddCommand(wlDoneCmd)
}

func runWlDone(cmd *cobra.Command, args []string) error {
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

	completionID := generateCompletionID(wantedID, rigHandle)

	dbName := wasteland.ResolveDBName(townRoot)
	if !doltserver.DatabaseExists(townRoot, dbName) {
		// Fallback for wl-commons clone-based workspaces (join creates .wasteland clone).
		if wlCfg.LocalDir == "" {
			return fmt.Errorf("database %q not found\nJoin a wasteland first with: gt wl join <org/db>", dbName)
		}
		if err := submitDoneInLocalClone(wlCfg.LocalDir, wantedID, rigHandle, wlDoneEvidence, completionID); err != nil {
			return err
		}
	} else {
		store := doltserver.NewWLCommons(townRoot)
		if err := submitDone(store, wantedID, rigHandle, wlDoneEvidence, completionID); err != nil {
			return err
		}
	}

	fmt.Printf("%s Completion submitted for %s\n", style.Bold.Render("✓"), wantedID)
	fmt.Printf("  Completion ID: %s\n", completionID)
	fmt.Printf("  Completed by: %s\n", rigHandle)
	fmt.Printf("  Evidence: %s\n", wlDoneEvidence)
	fmt.Printf("  Status: in_review\n")

	return nil
}

// submitDone contains the testable business logic for submitting a completion.
func submitDone(store doltserver.WLCommonsStore, wantedID, rigHandle, evidence, completionID string) error {
	item, err := store.QueryWanted(wantedID)
	if err != nil {
		return fmt.Errorf("querying wanted item: %w", err)
	}

	if item.Status != "claimed" {
		return fmt.Errorf("wanted item %s is not claimed (status: %s)", wantedID, item.Status)
	}

	if item.ClaimedBy != rigHandle {
		return fmt.Errorf("wanted item %s is claimed by %q, not %q", wantedID, item.ClaimedBy, rigHandle)
	}

	if err := store.SubmitCompletion(completionID, wantedID, rigHandle, evidence); err != nil {
		return fmt.Errorf("submitting completion: %w", err)
	}

	return nil
}

func submitDoneInLocalClone(localDir, wantedID, rigHandle, evidence, completionID string) error {
	script := fmt.Sprintf(`UPDATE wanted SET status='in_review', evidence_url='%s', updated_at=NOW()
  WHERE id='%s' AND status='claimed' AND claimed_by='%s';
INSERT IGNORE INTO completions (id, wanted_id, completed_by, evidence, completed_at)
  SELECT '%s', '%s', '%s', '%s', NOW()
  FROM wanted WHERE id='%s' AND status='in_review' AND claimed_by='%s'
  AND NOT EXISTS (SELECT 1 FROM completions WHERE wanted_id='%s');
CALL DOLT_ADD('-A');
CALL DOLT_COMMIT('-m', 'wl done: %s');`,
		doltserver.EscapeSQL(evidence), doltserver.EscapeSQL(wantedID), doltserver.EscapeSQL(rigHandle),
		doltserver.EscapeSQL(completionID), doltserver.EscapeSQL(wantedID), doltserver.EscapeSQL(rigHandle), doltserver.EscapeSQL(evidence),
		doltserver.EscapeSQL(wantedID), doltserver.EscapeSQL(rigHandle), doltserver.EscapeSQL(wantedID),
		doltserver.EscapeSQL(wantedID))

	cmd := exec.Command("dolt", "sql", "-q", script)
	cmd.Dir = localDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.ToLower(string(out))
		if strings.Contains(s, "nothing to commit") {
			return fmt.Errorf("wanted item %s is not claimed by %q or does not exist", wantedID, rigHandle)
		}
		return fmt.Errorf("submitting completion: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func generateCompletionID(wantedID, rigHandle string) string {
	now := time.Now().UTC().Format(time.RFC3339)
	h := sha256.Sum256([]byte(wantedID + "|" + rigHandle + "|" + now))
	return fmt.Sprintf("c-%x", h[:8])
}
