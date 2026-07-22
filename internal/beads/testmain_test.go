package beads_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

func TestMain(m *testing.M) {
	// Several tests invoke the real bd CLI. Give the entire package a disposable
	// server before any test can inherit an agent session's production routing.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "beads TestMain: Dolt setup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
