package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
)

func TestRunMailDirectory_WellKnownAddresses(t *testing.T) {
	// mail directory works even without beads — it lists well-known/special
	// addresses and gracefully warns on agent/group/queue/channel failures.
	townRoot := setupTestTownForCrewList(t, map[string][]string{
		"rig-a": {"alice"},
	})

	withCwd(t, townRoot)

	// Reset flag
	mailDirJSON = false
	defer func() { mailDirJSON = false }()

	output := captureStdout(t, func() {
		// runMailDirectory will warn on stderr about beads, but should not error
		if err := runMailDirectory(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runMailDirectory error: %v", err)
		}
	})

	// Should contain well-known addresses
	for _, addr := range []string{"mayor/", "--human", "--self"} {
		if !strings.Contains(output, addr) {
			t.Errorf("output should contain %q, got:\n%s", addr, output)
		}
	}

	// Should contain every group address — DERIVED from the vocabulary ([si-zgo]), not listed
	// here. This block used to name @town/@crew/@witnesses/@overseer, which was a second copy of
	// the set: it asserted the bare `@crew` the send path rejects, and it never noticed that
	// @dogs/@refineries/@deacons were advertised nowhere.
	for _, addr := range mail.GroupAddresses() {
		if !strings.Contains(output, addr) {
			t.Errorf("output should contain %q, got:\n%s", addr, output)
		}
	}

	// Should contain header
	if !strings.Contains(output, "ADDRESS") || !strings.Contains(output, "TYPE") {
		t.Errorf("output should contain table header, got:\n%s", output)
	}
}

func TestRunMailDirectory_JSONOutput(t *testing.T) {
	townRoot := setupTestTownForCrewList(t, map[string][]string{
		"rig-a": {"alice"},
	})

	withCwd(t, townRoot)

	mailDirJSON = true
	defer func() { mailDirJSON = false }()

	output := captureStdout(t, func() {
		if err := runMailDirectory(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runMailDirectory error: %v", err)
		}
	})

	var entries []DirectoryEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		t.Fatalf("unmarshal JSON output: %v\nraw output:\n%s", err, output)
	}

	if len(entries) == 0 {
		t.Fatal("expected at least well-known entries, got none")
	}

	// Verify well-known addresses present
	addrSet := make(map[string]string) // address -> type
	for _, e := range entries {
		addrSet[e.Address] = e.Type
	}

	for _, addr := range []string{"--human", "--self"} {
		if _, ok := addrSet[addr]; !ok {
			t.Errorf("JSON output missing address %q", addr)
		}
	}
	// Group addresses derived, same reason as above ([si-zgo]). Note this is an EXACT-key check,
	// which is why it caught the change the substring check above missed: "@crew/<rig>" contains
	// "@crew", so a Contains assertion passes on both the correct and the defective rendering.
	for _, addr := range mail.GroupAddresses() {
		if _, ok := addrSet[addr]; !ok {
			t.Errorf("JSON output missing address %q", addr)
		}
	}
}

func TestRunMailDirectory_Deduplication(t *testing.T) {
	// Verify that mayor/ doesn't appear twice (agent + well-known)
	townRoot := setupTestTownForCrewList(t, map[string][]string{
		"rig-a": {"alice"},
	})

	withCwd(t, townRoot)

	mailDirJSON = true
	defer func() { mailDirJSON = false }()

	output := captureStdout(t, func() {
		if err := runMailDirectory(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runMailDirectory error: %v", err)
		}
	})

	var entries []DirectoryEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	seen := make(map[string]int)
	for _, e := range entries {
		seen[e.Address]++
	}

	for addr, count := range seen {
		if count > 1 {
			t.Errorf("address %q appears %d times, should be deduplicated to 1", addr, count)
		}
	}
}

func TestRunMailDirectory_SortOrder(t *testing.T) {
	townRoot := setupTestTownForCrewList(t, map[string][]string{
		"rig-a": {"alice"},
	})

	withCwd(t, townRoot)

	mailDirJSON = true
	defer func() { mailDirJSON = false }()

	output := captureStdout(t, func() {
		if err := runMailDirectory(&cobra.Command{}, nil); err != nil {
			t.Fatalf("runMailDirectory error: %v", err)
		}
	})

	var entries []DirectoryEntry
	if err := json.Unmarshal([]byte(output), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Verify sorted by type then address
	for i := 1; i < len(entries); i++ {
		prev, curr := entries[i-1], entries[i]
		if prev.Type > curr.Type {
			t.Errorf("entries not sorted by type: %q (%s) before %q (%s)",
				prev.Address, prev.Type, curr.Address, curr.Type)
		}
		if prev.Type == curr.Type && prev.Address > curr.Address {
			t.Errorf("entries with same type not sorted by address: %q before %q (type=%s)",
				prev.Address, curr.Address, prev.Type)
		}
	}
}

func TestDirectoryEntry_JSONTags(t *testing.T) {
	e := DirectoryEntry{Address: "mayor/", Type: "well-known"}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if raw["address"] != "mayor/" {
		t.Errorf("JSON key should be 'address', got: %v", raw)
	}
	if raw["type"] != "well-known" {
		t.Errorf("JSON key should be 'type', got: %v", raw)
	}
}

// withCwd changes to dir for the test and restores on cleanup.
func withCwd(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}
