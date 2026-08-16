package cmd

import (
	"sort"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/beads"
)

// isBlockingDepType returns true for dependency types that block molecule step
// progress. Matches beads' canonical blocking types (AffectsReadyWork) except
// parent-child, which represents molecule→step hierarchy in this context.
// Unknown/custom types are non-blocking, matching beads' default behavior.
func isBlockingDepType(depType string) bool {
	switch depType {
	case "blocks", "conditional-blocks", "waits-for":
		return true
	default:
		return false
	}
}

// sortStepsBySequence sorts step issues by their sequence number suffix (.1, .2, etc.)
func sortStepsBySequence(steps []*beads.Issue) {
	sort.Slice(steps, func(i, j int) bool {
		return extractStepSequence(steps[i].ID) < extractStepSequence(steps[j].ID)
	})
}

// sortStepIDsBySequence sorts step ID strings by their sequence number suffix.
func sortStepIDsBySequence(ids []string) {
	sort.Slice(ids, func(i, j int) bool {
		return extractStepSequence(ids[i]) < extractStepSequence(ids[j])
	})
}

// extractStepSequence extracts the numeric sequence suffix from a step ID.
// E.g., "gt-mol.3" -> 3, "gt-mol.12" -> 12
func extractStepSequence(id string) int {
	if idx := strings.LastIndex(id, "."); idx >= 0 {
		if n, err := strconv.Atoi(id[idx+1:]); err == nil {
			return n
		}
	}
	return 999999 // Unknown sequence goes last
}
