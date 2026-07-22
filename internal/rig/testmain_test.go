package rig

import (
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
)

func TestMain(m *testing.M) {
	// AddRig tests exercise real fallback paths even when most bd calls are
	// stubbed. Isolate the package so fixture rigs can never become databases on
	// the production town server.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "rig TestMain: Dolt setup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
