package cmd

import (
	"testing"

	"github.com/spf13/cobra"
)

// isDoneCommand gates `gt done` behind a polecat-ownership check in
// persistentPreRun. It used to match ANY command named "done" anywhere in the
// tree, so `gt dog done` was run through a gate demanding
// BD_ACTOR=<rig>/polecats/<name> and dogs got "gt done is for polecats only
// (BD_ACTOR=dog)". Both dogs in the pack hit it the same day and were stranded
// showing "working" with their molecules already closed.
func TestIsDoneCommandMatchesOnlyTopLevelDone(t *testing.T) {
	root := &cobra.Command{Use: "gt"}

	topLevelDone := &cobra.Command{Use: "done"}
	root.AddCommand(topLevelDone)

	dog := &cobra.Command{Use: "dog"}
	dogDone := &cobra.Command{Use: "done"}
	dog.AddCommand(dogDone)
	root.AddCommand(dog)

	// A third group, to show the fix is not special-cased to dogs.
	crew := &cobra.Command{Use: "crew"}
	crewDone := &cobra.Command{Use: "done"}
	crew.AddCommand(crewDone)
	root.AddCommand(crew)

	if !isDoneCommand(topLevelDone) {
		t.Error("gt done must still be gated; the ownership check exists for it")
	}
	if isDoneCommand(dogDone) {
		t.Error("gt dog done was gated as a polecat command — this is the bug that stranded alpha and bravo")
	}
	if isDoneCommand(crewDone) {
		t.Error("gt crew done was gated as a polecat command")
	}
	if isDoneCommand(dog) || isDoneCommand(root) {
		t.Error("a command not named done must never match")
	}
	if isDoneCommand(nil) {
		t.Error("nil must not match")
	}
}

// A deeper nesting must not match either: only the root's own child counts.
func TestIsDoneCommandRejectsDeeplyNestedDone(t *testing.T) {
	root := &cobra.Command{Use: "gt"}
	group := &cobra.Command{Use: "group"}
	sub := &cobra.Command{Use: "sub"}
	deep := &cobra.Command{Use: "done"}
	sub.AddCommand(deep)
	group.AddCommand(sub)
	root.AddCommand(group)

	if isDoneCommand(deep) {
		t.Error("a done nested three levels down matched the top-level gate")
	}
}
