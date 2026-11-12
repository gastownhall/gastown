package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/mail"
)

// [si-zgo] THE PARITY GATE, READING WHAT THE COMMAND ACTUALLY EMITS.
//
// The defect: `gt mail directory` advertised `@crew`; `gt mail send @crew` answered "invalid group
// address". A vocabulary one side exports and the other never accepts.
//
// WHY THIS TEST LIVES HERE AND NOT NEXT TO THE VOCABULARY. My first version of this gate sat in
// internal/mail and took its "advertised" set from mail.GroupAddresses(). That is the vocabulary
// itself, not the directory's output — so both sides of the "round trip" derived from one source
// and the gate could not see the directory emitting anything extra. It was a tautology wearing a
// round trip's clothes, which is the exact failure this bead is about.
//
// It was caught by RUNNING THE ACCEPTANCE CRITERION rather than reasoning about it: appending a
// rogue `@ghosts` entry to the directory left the suite green. That mutation now reds here.
//
// So: the left side is DATA — the JSON the command really prints. The right side is BEHAVIOUR —
// what mail.ParseGroupAddress really accepts. Derivation cannot make those trivially equal.

// directoryEntries runs the real directory command and returns what it printed.
func directoryEntries(t *testing.T) []DirectoryEntry {
	t.Helper()
	townRoot := setupTestTownForCrewList(t, map[string][]string{"rig-a": {"alice"}})
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
	return entries
}

func TestEveryGroupAddressTheDirectoryPrintsIsRoutable(t *testing.T) {
	entries := directoryEntries(t)

	var advertised []string
	for _, e := range entries {
		if strings.HasPrefix(e.Address, "@") {
			advertised = append(advertised, e.Address)
		}
	}

	// DENOMINATOR ASSERT. "Every advertised address routes" is trivially satisfied by advertising
	// nothing, and an empty result here would otherwise render exactly like a clean pass.
	if len(advertised) == 0 {
		t.Fatal("the directory printed ZERO group addresses — every assertion below would " +
			"pass vacuously, so this is a failure, not a clean run")
	}
	t.Logf("directory printed %d group address(es)", len(advertised))

	for _, addr := range advertised {
		// Arity-1 groups are advertised as a pattern, "@crew/<rig>". Substitute a concrete
		// qualifier: the parser's contract is about SHAPE; a real send resolves the rig.
		probe := strings.Replace(addr, "/<rig>", "/somerig", 1)
		probe = strings.Replace(probe, "/<name>", "/somename", 1)

		if mail.ParseGroupAddress(probe) == nil {
			t.Errorf("`gt mail directory` advertises %q but the send path refuses %q.\n"+
				"That is si-zgo: a vocabulary one side exports and the other never accepts. "+
				"Add it to mail.GroupVocabulary rather than printing it here.", addr, probe)
		}
	}
}

func TestEveryRoutableGroupIsPrintedByTheDirectory(t *testing.T) {
	if len(mail.GroupVocabulary) == 0 {
		t.Fatal("mail.GroupVocabulary is EMPTY — the assertions below would pass vacuously")
	}

	printed := make(map[string]bool)
	for _, e := range directoryEntries(t) {
		printed[e.Address] = true
	}

	for _, spec := range mail.GroupVocabulary {
		// spec.Address() renders the correctly-arity'd form; spelling it here would be the
		// third copy of the vocabulary this bead exists to remove.
		if !printed[spec.Address()] {
			t.Errorf("the send path accepts group %q (arity %d) but `gt mail directory` never "+
				"prints %q — a sendable audience no reader can discover. That direction is how "+
				"@dogs/@refineries/@deacons stayed invisible.",
				spec.Name, spec.Arity(), spec.Address())
		}
	}
}
