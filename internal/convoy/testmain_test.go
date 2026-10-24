package convoy

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

func TestMain(m *testing.M) {
	// Cut the caller's session and town out of the environment before the
	// container claims the Dolt ports.
	restore := hermetic.Isolate()

	// Start an ephemeral Dolt container for this package's tests.
	// setupTestStore sets BEADS_TEST_MODE=1, which causes the beads SDK
	// to create testdb_<hash> databases. By routing those to an isolated
	// container (via BEADS_DOLT_PORT), the databases are destroyed when the
	// container is terminated at cleanup — preventing orphan
	// accumulation in the shared production Dolt data dir.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "convoy TestMain: skipping — %v\n", err)
		restore()
		os.Exit(0)
	}
	if err := hermetic.Check(); err != nil {
		fmt.Fprintf(os.Stderr, "convoy TestMain: %v\n", err)
		testutil.TerminateDoltContainer()
		restore()
		os.Exit(1)
	}

	code := m.Run()

	testutil.TerminateDoltContainer()
	restore()
	os.Exit(code)
}
