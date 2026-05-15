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

// trackingRetryBaseDelay is the base backoff between tracking-relation
// retries; overridable in tests to keep them fast.
var trackingRetryBaseDelay = 200 * time.Millisecond

// trackingRetryMaxAttempts bounds addTrackingRelationWithRetry.
const trackingRetryMaxAttempts = 5

// isBeadNotVisibleErr reports whether err looks like a freshly-created bead
// that a follow-up read cannot yet see. The tracking write lands moments
// after `bd create`; on a cold cache the lookup can race the Dolt commit
// and report the issue as missing even though it exists.
func isBeadNotVisibleErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "no such issue")
}

// addTrackingRelationWithRetry wraps addTrackingRelationFn, retrying with
// exponential backoff while the target bead is not yet visible (Dolt
// read-after-write lag). Non-visibility is the only retryable class;
// every other error fails fast (gt-4032).
func addTrackingRelationWithRetry(townRoot, trackerID, issueID string) error {
	var err error
	for attempt := 0; attempt < trackingRetryMaxAttempts; attempt++ {
		if err = addTrackingRelationFn(townRoot, trackerID, issueID); err == nil {
			return nil
		}
		if !isBeadNotVisibleErr(err) {
			return err
		}
		if attempt < trackingRetryMaxAttempts-1 && trackingRetryBaseDelay > 0 {
			time.Sleep(trackingRetryBaseDelay << attempt)
		}
	}
	return err
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
	args := []string{"dep", "add", trackerID, issueID, "--type=tracks"}
	if !add {
		args = []string{"dep", "remove", trackerID, issueID, "--type=tracks"}
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
