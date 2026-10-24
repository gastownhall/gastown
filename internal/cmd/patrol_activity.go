package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
)

// PatrolActivity describes the active patrol wisp hooked to a patrol agent.
//
// Every `gt patrol report` closes the current patrol wisp and creates a fresh
// one, so the wisp's age is the time since the agent last completed a patrol
// cycle. A live session whose patrol wisp keeps aging past the await backoff
// cap has stopped looping — typically it ended its turn after
// `gt patrol report` or after answering a nudge instead of re-entering
// await-signal/await-event. Session liveness alone cannot see this.
type PatrolActivity struct {
	ID         string `json:"id"`
	UpdatedAt  string `json:"updated_at,omitempty"`
	AgeSeconds int64  `json:"age_seconds"` // meaningful only when UpdatedAt is set
}

// latestPatrolActivity picks the most recently updated patrol wisp for
// patrolMolName out of an agent's active work. Returns nil when no patrol
// wisp is hooked.
func latestPatrolActivity(issues []*beads.Issue, patrolMolName string, now time.Time) *PatrolActivity {
	var latest *beads.Issue
	var latestAt time.Time
	for _, issue := range issues {
		if issue == nil || !strings.HasPrefix(issue.Title, patrolMolName) {
			continue
		}
		ts := beadRecencyTime(issue)
		if latest == nil || ts.After(latestAt) {
			latest, latestAt = issue, ts
		}
	}
	if latest == nil {
		return nil
	}

	activity := &PatrolActivity{ID: latest.ID}
	if !latestAt.IsZero() {
		activity.UpdatedAt = latestAt.UTC().Format(time.RFC3339)
		if age := now.Sub(latestAt); age > 0 {
			activity.AgeSeconds = int64(age.Seconds())
		}
	}
	return activity
}

// lookupPatrolActivity reads (without side effects) the active patrol wisp
// assigned to assignee in the town beads. Unlike findActivePatrol it never
// closes stale patrols, so status commands can call it safely.
func lookupPatrolActivity(townRoot, assignee, patrolMolName string) (*PatrolActivity, error) {
	issues, err := listAssignedActiveWorkAcrossStatuses(beads.New(townRoot), assignee)
	if err != nil {
		return nil, err
	}
	return latestPatrolActivity(issues, patrolMolName, time.Now()), nil
}

// formatPatrolActivity renders a one-line human summary for status output.
func formatPatrolActivity(activity *PatrolActivity) string {
	if activity == nil {
		return "(no patrol hooked)"
	}
	if activity.UpdatedAt == "" {
		return fmt.Sprintf("%s (last update unknown)", activity.ID)
	}
	age := time.Duration(activity.AgeSeconds) * time.Second
	return fmt.Sprintf("%s (last update %s ago)", activity.ID, age.Round(time.Second))
}
