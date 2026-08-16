package cmd

import (
	"reflect"
	"testing"

	"github.com/jonbaldie/gastown/internal/constants"
)

func TestPatrolConfigForRole(t *testing.T) {
	roleInfo := RoleInfo{TownRoot: "/town", Rig: "testrig"}

	tests := []struct {
		role    Role
		want    PatrolConfig
		wantErr bool
	}{
		{
			role: RoleDeacon,
			want: PatrolConfig{
				RoleName:      "deacon",
				PatrolMolName: constants.MolDeaconPatrol,
				BeadsDir:      "/town",
				Assignee:      "deacon",
			},
		},
		{
			role: RoleWitness,
			want: PatrolConfig{
				RoleName:      "witness",
				PatrolMolName: constants.MolWitnessPatrol,
				BeadsDir:      "/town",
				Assignee:      "testrig/witness",
			},
		},
		{
			role: RoleRefinery,
			want: PatrolConfig{
				RoleName:      "refinery",
				PatrolMolName: constants.MolRefineryPatrol,
				BeadsDir:      "/town",
				Assignee:      "testrig/refinery",
				ExtraVars:     []string{"rig=testrig", "target_branch=main"},
			},
		},
		{role: RoleMayor, wantErr: true},
		{role: RolePolecat, wantErr: true},
		{role: RoleCrew, wantErr: true},
		{role: RoleUnknown, wantErr: true},
		{role: Role(""), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			got, err := patrolConfigForRole(tt.role, roleInfo)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("patrolConfigForRole(%q) returned nil error", tt.role)
				}
				return
			}
			if err != nil {
				t.Fatalf("patrolConfigForRole(%q) returned error: %v", tt.role, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("patrolConfigForRole(%q) = %+v, want %+v", tt.role, got, tt.want)
			}
		})
	}
}

func TestPatrolNewCmd_Registered(t *testing.T) {
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
