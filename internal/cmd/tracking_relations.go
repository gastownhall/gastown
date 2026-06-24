package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	beadsdk "github.com/steveyegge/beads"
	"github.com/steveyegge/gastown/internal/beads"
)

var (
	addTrackingRelationFn    = addTrackingRelation
	removeTrackingRelationFn = removeTrackingRelation
)

func addTrackingRelation(townRoot, trackerID, issueID string) error {
	if err := mutateTrackingRelationViaStore(townRoot, trackerID, issueID, true); err != nil {
		return fallbackTrackingRelation(townRoot, trackerID, issueID, true, err)
	}
	return nil
}

func removeTrackingRelation(townRoot, trackerID, issueID string) error {
	if err := mutateTrackingRelationViaStore(townRoot, trackerID, issueID, false); err != nil {
		return fallbackTrackingRelation(townRoot, trackerID, issueID, false, err)
	}
	return nil
}

func mutateTrackingRelationViaStore(townRoot, trackerID, issueID string, add bool) error {
	resolvedBeads := beads.ResolveBeadsDir(townRoot)
	if resolvedBeads == "" {
		return fmt.Errorf("resolving town beads dir")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	b := beads.NewWithBeadsDir(townRoot, resolvedBeads)
	store, cleanup, err := b.OpenStore(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	targetID := trackingDependsOnID(townRoot, issueID)
	actor := os.Getenv("BD_ACTOR")
	if actor == "" {
		actor = detectSender()
	}

	if add {
		dep := &beadsdk.Dependency{
			IssueID:     trackerID,
			DependsOnID: targetID,
			Type:        beadsdk.DependencyType("tracks"),
		}
		return store.AddDependency(ctx, dep, actor)
	}

	return store.RemoveDependency(ctx, trackerID, targetID, actor)
}

func fallbackTrackingRelation(townRoot, trackerID, issueID string, add bool, storeErr error) error {
	targetID := trackingDependsOnID(townRoot, issueID)
	args := []string{"dep", "add", trackerID, targetID, "--type=tracks"}
	if !add {
		args = []string{"dep", "remove", trackerID, targetID, "--type=tracks"}
	}

	if out, err := BdCmd(args...).Dir(townRoot).WithAutoCommit().StripBeadsDir().CombinedOutput(); err != nil {
		output := strings.TrimSpace(string(out))
		if output == "" {
			return fmt.Errorf("tracking relation via store failed: %w; fallback bd path failed: %w", storeErr, err)
		}
		return fmt.Errorf("tracking relation via store failed: %w; fallback bd path failed: %w; output: %s", storeErr, err, output)
	}

	return nil
}

func trackingDependsOnID(townRoot, issueID string) string {
	if strings.HasPrefix(issueID, "external:") {
		return issueID
	}

	prefix := beads.ExtractPrefix(issueID)
	if prefix == "" {
		return issueID
	}

	if rigName := beads.GetRigNameForPrefix(townRoot, prefix); rigName != "" {
		return fmt.Sprintf("external:%s:%s", strings.TrimSuffix(prefix, "-"), issueID)
	}

	return issueID
}
