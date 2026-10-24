package mail

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

func TestMain(m *testing.M) {
	// Keep tests away from the caller's session, town and Dolt server.
	restore := hermetic.Isolate()
	if err := hermetic.Check(); err != nil {
		fmt.Fprintf(os.Stderr, "mail TestMain: %v\n", err)
		restore()
		os.Exit(1)
	}

	code := m.Run()
	testutil.TerminateDoltContainer()
	restore()
	os.Exit(code)
}
