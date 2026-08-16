package session

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/constants"
)

func TestPolicyFor_RoleTable(t *testing.T) {
	boot := policyFor(constants.RoleBoot, Work{})
	if boot.WaitForAgent || boot.AcceptBypass || boot.ReadyFatal {
		t.Fatalf("boot waits for nothing, got %+v", boot)
	}

	refinery := policyFor(constants.RoleRefinery, Work{})
	if refinery.WaitForAgent {
		t.Fatal("refinery must not wait for the agent pane")
	}
	if !refinery.ReadyFatal {
		t.Fatal("refinery ready wait is fatal")
	}
}

func TestPolicyFor_CrewInteractive(t *testing.T) {
	attended := policyFor(constants.RoleCrew, Work{Interactive: true})
	if attended.WaitForAgent || attended.AcceptBypass {
		t.Fatalf("attended crew skips waits, got %+v", attended)
	}

	unattended := policyFor(constants.RoleCrew, Work{})
	if !unattended.WaitForAgent || !unattended.AcceptBypass {
		t.Fatalf("unattended crew waits and dismisses dialogs, got %+v", unattended)
	}
}
