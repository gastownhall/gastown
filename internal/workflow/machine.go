package workflow

import "path/filepath"

var stepTables = map[Kind][]Step{
	KindTask: {"spec", "plan", "execute", "verify", "review", "merge"},
	KindFix:  {"report", "reproduce", "rca", "fix", "verify", "merge"},
	KindEpic: {"epic", "breakdown", "tasks", "verification"},
}

var artifactTables = map[Kind]map[Step]string{
	KindTask: {"spec": "spec", "plan": "plan", "review": "review"},
	KindFix:  {"report": "report", "rca": "rca"},
	KindEpic: {"epic": "epic", "breakdown": "breakdown", "verification": "verification"},
}

// Steps returns the ordered steps for kind k, or nil for an unknown kind.
// The returned slice is a copy and may be modified by the caller.
func Steps(k Kind) []Step {
	steps, ok := stepTables[k]
	if !ok {
		return nil
	}
	return append([]Step(nil), steps...)
}

// Next returns the step after s for kind k. It returns ("", false) when s is
// the last step, or when s or k is unknown.
func Next(k Kind, s Step) (Step, bool) {
	steps := stepTables[k]
	for i, step := range steps {
		if step == s {
			if i+1 < len(steps) {
				return steps[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

// ArtifactFor returns the artifact name produced at step s of kind k, or ""
// when the step has no artifact.
func ArtifactFor(k Kind, s Step) string {
	return artifactTables[k][s]
}

// ArtifactPath returns the path of artifact name for item itemID in rig:
// <townRoot>/<rig>/.gtb/items/<itemID>/<name>.md.
func ArtifactPath(townRoot, rig, itemID, name string) string {
	return filepath.Join(townRoot, rig, ".gtb", "items", itemID, name+".md")
}

// OutputDir returns the directory verified-run logs for item itemID are
// written to: <townRoot>/<rig>/.gtb/items/<itemID>/outputs.
func OutputDir(townRoot, rig, itemID string) string {
	return filepath.Join(townRoot, rig, ".gtb", "items", itemID, "outputs")
}
