package workitem

import "strings"

// Snapshot is the small, package-neutral shape needed to decide whether a bead
// represents durable work or an internal runtime artifact.
type Snapshot struct {
	ID        string
	Title     string
	Type      string
	Labels    []string
	Ephemeral bool
}

// Assessment describes whether a bead can be used as concrete work.
type Assessment struct {
	Concrete bool
	Reason   string
}

var internalLabels = map[string]bool{
	"gt:sling-context": true,
	"gt:wisp":          true,
	"gt:message":       true,
	"gt:handoff":       true,
	"gt:merge-request": true,
	"gt:agent":         true,
	"gt:queue":         true,
	"gt:convoy":        true,
	"gt:role":          true,
	"gt:rig":           true,
}

var internalTypes = map[string]bool{
	"agent":         true,
	"convoy":        true,
	"epic":          true,
	"event":         true,
	"gate":          true,
	"merge-request": true,
	"message":       true,
	"molecule":      true,
	"queue":         true,
	"rig":           true,
	"role":          true,
	"sling-context": true,
	"wisp":          true,
}

// AssessConcrete returns whether the snapshot names a durable source issue that
// can be dispatched to a polecat or used as a merge-request source_issue.
func AssessConcrete(snapshot Snapshot) Assessment {
	id := strings.TrimSpace(snapshot.ID)
	if id == "" {
		return Assessment{Reason: "missing-id"}
	}
	if strings.EqualFold(id, "mol-polecat-work") {
		return Assessment{Reason: "formula-id"}
	}
	if snapshot.Ephemeral {
		return Assessment{Reason: "ephemeral"}
	}
	if strings.Contains(id, "-wisp-") {
		return Assessment{Reason: "wisp-id"}
	}
	for _, label := range snapshot.Labels {
		label = strings.TrimSpace(strings.ToLower(label))
		if internalLabels[label] {
			return Assessment{Reason: "internal-label:" + label}
		}
	}
	issueType := strings.TrimSpace(strings.ToLower(snapshot.Type))
	if internalTypes[issueType] {
		return Assessment{Reason: "internal-type:" + issueType}
	}
	return Assessment{Concrete: true}
}
