package cmd

import (
	"testing"
)

func TestRunPatrolNew_UnsupportedRole(t *testing.T) {
	validRoles := []string{"deacon", "witness", "refinery"}
	invalidRoles := []string{"mayor", "polecat", "crew", "unknown", ""}
	roleInfo := RoleInfo{TownRoot: "/town", Rig: "testrig"}

	for _, role := range validRoles {
		if _, err := patrolConfigForRole(Role(role), roleInfo); err != nil {
			t.Errorf("patrolConfigForRole(%q) returned error: %v", role, err)
		}
	}

	for _, role := range invalidRoles {
		if _, err := patrolConfigForRole(Role(role), roleInfo); err == nil {
			t.Errorf("patrolConfigForRole(%q) returned nil error", role)
		}
	}
}

func TestPatrolNewCmd_Registered(t *testing.T) {
	// Verify the command is properly registered
	found := false
	for _, cmd := range patrolCmd.Commands() {
		if cmd.Use == "new" {
			found = true
			break
		}
	}
	if !found {
		t.Error("patrol new command not registered")
	}
}

func TestPatrolNewCmd_HasRoleFlag(t *testing.T) {
	flag := patrolNewCmd.Flags().Lookup("role")
	if flag == nil {
		t.Error("patrol new command missing --role flag")
	}
}
