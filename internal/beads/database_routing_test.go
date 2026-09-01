package beads

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRoutingTestBeadsDir creates a .beads directory whose metadata selects a
// named Dolt database on a shared server, the shape every Gas Town rig has.
func writeRoutingTestBeadsDir(t *testing.T, database string) string {
	t.Helper()
	beadsDir := filepath.Join(t.TempDir(), ".beads")
	if err := os.MkdirAll(beadsDir, 0755); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"dolt_database":"` + database + `","dolt_server_host":"127.0.0.1","dolt_server_port":4407}`)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), metadata, 0644); err != nil {
		t.Fatal(err)
	}
	return beadsDir
}

// TestBDEnvPinsRoutingAwayFromContributorPlanningRepo guards hq-z871/hq-2f0m.
//
// Selecting a database is not the same as selecting a repository. With
// routing.mode=auto and git config beads.role=contributor (the configuration
// the gastown rig shipped with), bd 1.2.2 redirects both writes and reads to
// routing.contributor — reproduced end to end in an isolated two-repo sandbox:
// `bd create` from repo A minted the issue in repo B under B's prefix and
// reported success, `bd list` from A found it via the same redirect, and
// `bd dep` / `bd comment` on it then failed "embeddeddolt: store is read-only"
// because an auto-routed store is opened read-only even for write-intent
// callers. Nothing ever reached the database BEADS_DIR had selected.
//
// routing.default hijacks the target the same way with the mode left alone, so
// pinning the mode is NOT sufficient on its own — verified in the same sandbox:
// under BD_ROUTING_MODE=explicit a create still landed in the foreign repo
// until BD_ROUTING_DEFAULT=. was pinned as well.
func TestBDEnvPinsRoutingAwayFromContributorPlanningRepo(t *testing.T) {
	beadsDir := writeRoutingTestBeadsDir(t, "rigdb")
	// Values a seat can inherit from a contributor-configured shell; both
	// spellings, because bd honours the BEADS_ prefix for legacy compatibility.
	base := []string{
		"PATH=/usr/bin",
		"BD_ROUTING_MODE=auto",
		"BD_ROUTING_DEFAULT=/home/agent/.beads-planning",
		"BEADS_ROUTING_MODE=auto",
		"BEADS_ROUTING_DEFAULT=/home/agent/.beads-planning",
	}

	for _, tc := range []struct {
		name string
		env  []string
	}{
		{name: "pinned", env: BuildPinnedBDEnv(base, beadsDir)},
		{name: "routing", env: BuildRoutingBDEnv(base, beadsDir)},
		{name: "read-only pinned", env: BuildReadOnlyPinnedBDEnv(base, beadsDir)},
		{name: "read-only routing", env: BuildReadOnlyRoutingBDEnv(base, beadsDir)},
		{name: "mutation pinned", env: BuildMutationPinnedBDEnv(base, beadsDir)},
		{name: "mutation routing", env: BuildMutationRoutingBDEnv(base, beadsDir)},
		{name: "mutation neutral", env: BuildMutationNeutralBDEnv(base)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := envMap(tc.env)
			if got["BD_ROUTING_MODE"] != "explicit" {
				t.Fatalf("BD_ROUTING_MODE = %q, want explicit in %v", got["BD_ROUTING_MODE"], tc.env)
			}
			if got["BD_ROUTING_DEFAULT"] != "." {
				t.Fatalf("BD_ROUTING_DEFAULT = %q, want \".\" in %v", got["BD_ROUTING_DEFAULT"], tc.env)
			}
			// glibc getenv() returns the first match, so an inherited value
			// left in place would win over the appended one.
			for _, key := range []string{"BD_ROUTING_MODE=", "BD_ROUTING_DEFAULT="} {
				if count := countEnvPrefix(tc.env, key); count != 1 {
					t.Fatalf("%s count = %d, want 1 in %v", key, count, tc.env)
				}
			}
			for _, key := range []string{"BEADS_ROUTING_MODE", "BEADS_ROUTING_DEFAULT"} {
				if value, ok := got[key]; ok {
					t.Fatalf("%s should be stripped, got %q in %v", key, value, tc.env)
				}
			}
		})
	}
}

// TestPinnedBDEnvKeepsTheDatabaseSelectorBdActuallyReads guards the fix
// candidate recorded on hq-z871 against being adopted unmeasured.
//
// The bead proposed either dropping BEADS_DOLT_SERVER_DATABASE or emitting
// BEADS_DOLT_DATABASE in its place. Neither is right: bd 1.2.2 reads
// BEADS_DOLT_SERVER_DATABASE (configfile.Config.GetDoltDatabase,
// dolt.applyConfigDefaults, cmd/bd routing) and reads BEADS_DOLT_DATABASE
// nowhere, so the swap is a silent no-op that would leave the pinned
// subprocess with no database selector at all. Re-measured directly against
// the town's Dolt server: with the exact env BuildReadOnlyPinnedBDEnv
// produces, `bd show` on a bead that exists behaves identically with and
// without the variable, so the variable is not what broke gastown dispatch.
func TestPinnedBDEnvKeepsTheDatabaseSelectorBdActuallyReads(t *testing.T) {
	beadsDir := writeRoutingTestBeadsDir(t, "rigdb")
	env := BuildPinnedBDEnv([]string{"PATH=/usr/bin"}, beadsDir)
	got := envMap(env)

	if got["BEADS_DOLT_SERVER_DATABASE"] != "rigdb" {
		t.Fatalf("BEADS_DOLT_SERVER_DATABASE = %q, want rigdb in %v", got["BEADS_DOLT_SERVER_DATABASE"], env)
	}
	if value, ok := got["BEADS_DOLT_DATABASE"]; ok {
		t.Fatalf("BEADS_DOLT_DATABASE is not read by bd; emitting it (=%q) selects no database at all: %v", value, env)
	}

	// Routing mode still relies on bd prefix routing to pick the database, so
	// it must not carry one — pinning routing off must not have changed that.
	routing := envMap(BuildRoutingBDEnv([]string{"PATH=/usr/bin"}, beadsDir))
	if value, ok := routing["BEADS_DOLT_SERVER_DATABASE"]; ok {
		t.Fatalf("routing env must not pin a database, got %q", value)
	}
}
