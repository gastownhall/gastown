package dog

import (
	"fmt"

	"github.com/steveyegge/gastown/internal/beads"
)

// AgentIDPrefix is the canonical assignee prefix for dogs.
const AgentIDPrefix = "deacon/dogs/"

// AgentID returns the canonical agent address for a dog (deacon/dogs/<name>).
func AgentID(dogName string) string {
	return AgentIDPrefix + dogName
}

// HookedWork describes work still attached to a dog's hook.
type HookedWork struct {
	// ID is the bead/wisp ID.
	ID string `json:"id"`
	// Title is the bead title (may be empty).
	Title string `json:"title,omitempty"`
	// Formula is the attached formula name, when the hooked bead is a wisp.
	Formula string `json:"formula,omitempty"`
}

// HookedWorkFinder reports the work currently hooked to a dog.
// It returns (nil, nil) when the dog holds nothing.
type HookedWorkFinder interface {
	HookedWork(dogName string) (*HookedWork, error)
}

// BeadsHookedWorkFinder resolves hooked work from the town beads database.
type BeadsHookedWorkFinder struct {
	workDir string
}

// NewBeadsHookedWorkFinder creates a finder that queries beads in workDir.
// For dogs this is the town root — dogs are town-level agents and their wisps
// live in the town (hq) database.
func NewBeadsHookedWorkFinder(workDir string) *BeadsHookedWorkFinder {
	return &BeadsHookedWorkFinder{workDir: workDir}
}

// HookedWork returns the work hooked to dogName, or nil if there is none.
func (f *BeadsHookedWorkFinder) HookedWork(dogName string) (*HookedWork, error) {
	if f == nil || f.workDir == "" || dogName == "" {
		return nil, nil
	}

	assignee := AgentID(dogName)
	b := beads.New(f.workDir)

	// Dogs are normally dispatched formulas (ephemeral wisps), but a plain bead
	// can be slung to a dog too, so check both tables.
	for _, ephemeral := range []bool{true, false} {
		issues, err := b.List(beads.ListOptions{
			Status:    beads.StatusHooked,
			Assignee:  assignee,
			Priority:  -1,
			Ephemeral: ephemeral,
			Limit:     0,
		})
		if err != nil {
			return nil, fmt.Errorf("listing hooked work for %s: %w", assignee, err)
		}
		if w := firstHookedWork(issues, assignee); w != nil {
			return w, nil
		}
	}

	return nil, nil
}

// firstHookedWork returns the first issue actually assigned to assignee.
// The assignee re-check guards against a bd build that ignores the filter.
func firstHookedWork(issues []*beads.Issue, assignee string) *HookedWork {
	for _, issue := range issues {
		if issue == nil || issue.ID == "" || issue.Assignee != assignee {
			continue
		}
		w := &HookedWork{ID: issue.ID, Title: issue.Title}
		if fields := beads.ParseAttachmentFields(issue); fields != nil {
			w.Formula = fields.AttachedFormula
		}
		return w
	}
	return nil
}
