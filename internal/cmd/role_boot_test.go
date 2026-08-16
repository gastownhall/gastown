package cmd

import (
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/beads"
)

func TestParseRoleStringBoot(t *testing.T) {
	tests := []struct {
		input    string
		wantRole Role
		wantRig  string
		wantName string
	}{
		// Simple "boot" → RoleBoot
		{"boot", RoleBoot, "", ""},
		// Compound "deacon/boot" → RoleBoot
		{"deacon/boot", RoleBoot, "", ""},
		// Non-deacon compound should NOT match RoleBoot
		{"west/boot", Role("west/boot"), "", ""},
		// Extra path segments should NOT match RoleBoot
		{"deacon/boot/extra", Role("deacon/boot/extra"), "", ""},
		// Double-slash normalization
		{"gamestore//refinery", RoleRefinery, "gamestore", ""},
		{"gamestore//witness", RoleWitness, "gamestore", ""},
		{"gamestore///refinery", RoleRefinery, "gamestore", ""},
		{"gamestore/refinery/", RoleRefinery, "gamestore", ""},
		{"gamestore//polecats//alpha", RolePolecat, "gamestore", "alpha"},
	}

	for _, tt := range tests {
		role, rig, name := parseRoleString(tt.input)
		if role != tt.wantRole {
			t.Errorf("parseRoleString(%q) role = %v, want %v", tt.input, role, tt.wantRole)
		}
		if rig != tt.wantRig {
			t.Errorf("parseRoleString(%q) rig = %q, want %q", tt.input, rig, tt.wantRig)
		}
		if name != tt.wantName {
			t.Errorf("parseRoleString(%q) name = %q, want %q", tt.input, name, tt.wantName)
		}
	}
}

func TestGetRoleHomeBoot(t *testing.T) {
	townRoot := "/tmp/gt"
	got := getRoleHome(RoleBoot, "", "", townRoot)
	want := filepath.Join(townRoot, "deacon", "dogs", "boot")
	if got != want {
		t.Errorf("getRoleHome(RoleBoot) = %q, want %q", got, want)
	}
}

func TestIsTownLevelRoleBoot(t *testing.T) {
	tests := []struct {
		agentID string
		want    bool
	}{
		{"deacon/boot", true},
		{"deacon-boot", true},
		{"mayor", true},
		{"mayor/", true},
		{"deacon", true},
		{"deacon/", true},
		{"gastown/witness", false},
		{"west/boot", false},
		{"boot", false}, // bare "boot" is not a valid agentID
	}

	for _, tt := range tests {
		got := isTownLevelRole(tt.agentID)
		if got != tt.want {
			t.Errorf("isTownLevelRole(%q) = %v, want %v", tt.agentID, got, tt.want)
		}
	}
}

func TestActorStringBoot(t *testing.T) {
	info := RoleInfo{Role: RoleBoot}
	got := info.ActorString()
	want := "deacon-boot"
	if got != want {
		t.Errorf("ActorString() for RoleBoot = %q, want %q", got, want)
	}
}

func TestActorStringConsistentWithBDActorBoot(t *testing.T) {
	// ActorString() must match what BD_ACTOR is set to in config/env.go:57.
	// This is a snapshot value — if BD_ACTOR for boot changes in config/env.go,
	// update it here too.
	info := RoleInfo{Role: RoleBoot}
	actorString := info.ActorString()
	bdActor := "deacon-boot" // snapshot from internal/config/env.go:57
	if actorString != bdActor {
		t.Errorf("ActorString() = %q does not match BD_ACTOR = %q", actorString, bdActor)
	}
}

func TestBuildAgentBeadIDBoot(t *testing.T) {
	// RoleBoot should produce the town-level dog bead ID "hq-dog-boot"
	// via both the explicit role path and the identity-inference path.
	want := beads.DogBeadIDTown("boot")

	// Explicit role path
	got := buildAgentBeadID("deacon-boot", RoleBoot, "/tmp/gt")
	if got != want {
		t.Errorf("buildAgentBeadID(RoleBoot) = %q, want %q", got, want)
	}

	// Identity inference path (RoleUnknown + "deacon-boot" identity)
	got = buildAgentBeadID("deacon-boot", RoleUnknown, "/tmp/gt")
	if got != want {
		t.Errorf("buildAgentBeadID(RoleUnknown, \"deacon-boot\") = %q, want %q", got, want)
	}
}
