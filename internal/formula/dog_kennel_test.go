package formula

import (
	"strings"
	"testing"
)

// Dog formulas must end by running `gt dog done`. That command is what closes
// the dog's molecule and resets the dog to idle; a final step that only
// describes returning to the kennel leaves the molecule open and the dog stuck
// in the working state.
func TestDogFormulasFinishWithDogDone(t *testing.T) {
	for _, name := range []string{
		"mol-dog-backup",
		"mol-dog-compactor",
		"mol-dog-doctor",
		"mol-dog-jsonl",
		"mol-dog-phantom-db",
		"mol-dog-reaper",
		"mol-dog-stale-db",
		"mol-convoy-cleanup",
		"mol-convoy-feed",
		"mol-dep-propagate",
		"mol-digest-generate",
		"mol-orphan-scan",
		"mol-session-gc",
	} {
		t.Run(name, func(t *testing.T) {
			data, err := GetEmbeddedFormulaContent(name)
			if err != nil {
				t.Fatalf("load %s: %v", name, err)
			}
			f, err := Parse(data)
			if err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if len(f.Steps) == 0 {
				t.Fatalf("%s has no steps", name)
			}
			last := f.Steps[len(f.Steps)-1]
			if !strings.Contains(last.Description, "gt dog done") {
				t.Fatalf("%s final step %q does not run `gt dog done`", name, last.ID)
			}
			for _, step := range f.Steps[:len(f.Steps)-1] {
				if strings.Contains(step.Description, "gt dog done") {
					t.Fatalf("%s step %q runs `gt dog done` before the final step", name, step.ID)
				}
			}
		})
	}
}
