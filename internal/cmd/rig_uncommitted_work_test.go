package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/git"
	"github.com/jonbaldie/gastown/internal/polecat"
	"github.com/jonbaldie/gastown/internal/rig"
)

func stubUncommittedWorkCheckDeps(
	t *testing.T,
	listFn func(*rig.Rig) ([]*polecat.Polecat, error),
	checkFn func(string) (*git.UncommittedWorkStatus, error),
	isTTYFn func() bool,
	promptFn func(string) bool,
) {
	t.Helper()

	oldList := listPolecatsForWorkCheck
	oldCheck := checkPolecatWorkStatus
	oldIsTTY := isStdinTerminal
	oldPrompt := promptYesNoUnsafeProceed

	listPolecatsForWorkCheck = listFn
	checkPolecatWorkStatus = checkFn
	isStdinTerminal = isTTYFn
	promptYesNoUnsafeProceed = promptFn

	t.Cleanup(func() {
		listPolecatsForWorkCheck = oldList
		checkPolecatWorkStatus = oldCheck
		isStdinTerminal = oldIsTTY
		promptYesNoUnsafeProceed = oldPrompt
	})
}

func testRig() *rig.Rig {
	return &rig.Rig{
		Name: "testrig",
		Path: "/tmp/testrig",
	}
}

func TestCheckUncommittedWork_ListErrorBlocksWithoutForce(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return nil, errors.New("list failed")
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			t.Fatalf("check should not be called when list fails")
			return nil, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called without --force")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "stop", false)
	})

	if proceed {
		t.Fatal("expected proceed=false when polecat listing fails without --force")
	}
	if !strings.Contains(output, "Could not check polecats for uncommitted work") {
		t.Fatalf("expected list-error warning, got: %q", output)
	}
	if !strings.Contains(output, "--force") || !strings.Contains(output, "--nuclear") {
		t.Fatalf("expected override hint, got: %q", output)
	}
}

func TestCheckUncommittedWork_ListErrorForceTTYPrompts(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return nil, errors.New("list failed")
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			t.Fatalf("check should not be called when list fails")
			return nil, nil
		},
		func() bool { return true },
		func(question string) bool {
			if question != "Proceed anyway?" {
				t.Fatalf("unexpected prompt question: %q", question)
			}
			return true
		},
	)

	proceed := checkUncommittedWork(testRig(), "testrig", "shutdown", true)
	if !proceed {
		t.Fatal("expected proceed=true after force+TTY confirmation")
	}
}

func TestCheckUncommittedWork_PolecatStatusErrorBlocks(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return nil, errors.New("git status failed")
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called without --force")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "restart", false)
	})

	if proceed {
		t.Fatal("expected proceed=false when polecat status check fails")
	}
	if !strings.Contains(output, "Could not verify uncommitted work for") {
		t.Fatalf("expected status-check error warning, got: %q", output)
	}
	if !strings.Contains(output, "alpha") {
		t.Fatalf("expected polecat name in warning, got: %q", output)
	}
}

func TestCheckUncommittedWork_DirtyForceNonTTYBlocks(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return &git.UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"README.md"},
			}, nil
		},
		func() bool { return false },
		func(string) bool {
			t.Fatalf("prompt should not be called in non-TTY mode")
			return false
		},
	)

	var proceed bool
	output := captureStdout(t, func() {
		proceed = checkUncommittedWork(testRig(), "testrig", "stop", true)
	})

	if proceed {
		t.Fatal("expected proceed=false for force in non-TTY mode")
	}
	if !strings.Contains(output, "--force") || !strings.Contains(output, "interactive terminal") {
		t.Fatalf("expected non-TTY force hint, got: %q", output)
	}
}

func TestCheckUncommittedWork_DirtyForceTTYPrompts(t *testing.T) {
	stubUncommittedWorkCheckDeps(
		t,
		func(*rig.Rig) ([]*polecat.Polecat, error) {
			return []*polecat.Polecat{
				{Name: "alpha", ClonePath: "/tmp/alpha"},
			}, nil
		},
		func(string) (*git.UncommittedWorkStatus, error) {
			return &git.UncommittedWorkStatus{
				HasUncommittedChanges: true,
				ModifiedFiles:         []string{"README.md"},
			}, nil
		},
		func() bool { return true },
		func(question string) bool {
			if question != "Proceed anyway?" {
				t.Fatalf("unexpected prompt question: %q", question)
			}
			return true
		},
	)

	proceed := checkUncommittedWork(testRig(), "testrig", "stop", true)
	if !proceed {
		t.Fatal("expected proceed=true after force+TTY confirmation")
	}
}
