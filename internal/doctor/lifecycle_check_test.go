package doctor

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func TestLifecycleHygieneFixUsesDeaconIdentity(t *testing.T) {
	t.Setenv("GT_ROLE", "mayor")
	t.Setenv("BD_ACTOR", "mayor/")
	t.Setenv("GT_LIFECYCLE_HELPER", "1")
	original := lifecycleExecCommand
	t.Cleanup(func() { lifecycleExecCommand = original })
	lifecycleExecCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleHygieneCommandHelper$", "--")
		cmd.Args = append(cmd.Args, args...)
		return cmd
	}

	check := NewLifecycleHygieneCheck()
	check.staleMessages = []staleMessage{{ID: "hq-wisp-stale"}}
	if err := check.Fix(&CheckContext{TownRoot: t.TempDir()}); err != nil {
		t.Fatalf("Fix() failed: %v", err)
	}
}

func TestLifecycleHygieneFixReportsDeleteFailure(t *testing.T) {
	t.Setenv("GT_LIFECYCLE_HELPER", "fail")
	original := lifecycleExecCommand
	t.Cleanup(func() { lifecycleExecCommand = original })
	lifecycleExecCommand = func(_ string, args ...string) *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLifecycleHygieneCommandHelper$", "--")
		cmd.Args = append(cmd.Args, args...)
		return cmd
	}

	check := NewLifecycleHygieneCheck()
	check.staleMessages = []staleMessage{{ID: "hq-wisp-stale"}}
	err := check.Fix(&CheckContext{TownRoot: t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "assignee is deacon") {
		t.Fatalf("Fix() did not preserve delete failure output: %v", err)
	}
}

func TestLifecycleHygieneCommandHelper(t *testing.T) {
	mode := os.Getenv("GT_LIFECYCLE_HELPER")
	if mode == "" {
		return
	}
	wantArgs := []string{"mail", "delete", "hq-wisp-stale"}
	separator := slices.Index(os.Args, "--")
	if separator < 0 || !slices.Equal(os.Args[separator+1:], wantArgs) {
		os.Exit(2)
	}
	if os.Getenv("GT_ROLE") != "deacon" || os.Getenv("BD_ACTOR") != "deacon/" {
		os.Exit(3)
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "assignee is deacon: simulated refusal")
		os.Exit(4)
	}
	os.Exit(0)
}
