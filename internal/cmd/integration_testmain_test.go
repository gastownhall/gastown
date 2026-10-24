//go:build integration

package cmd

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/testutil"
	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

func TestMain(m *testing.M) {
	// Force sequential test execution to avoid bd file locks on Windows.
	_ = flag.Set("test.parallel", "1")
	flag.Parse()

	// Cut the caller's session and town out of the environment (e.g. an agent
	// session's GT_ROLE and BEADS_DOLT_PORT=3307) before the container claims
	// the Dolt ports. These tests drive the gt binary on PATH, so keep it.
	restore := hermetic.Isolate(hermetic.WithInstalledGT())

	// Start an ephemeral Dolt container for this package's integration tests.
	// Tests like TestAgentWorktreesStayClean and TestBeadsRoutingFromTownRoot
	// spawn gt/bd subprocesses that create databases (e.g., "tr", "hq").
	// By routing to an isolated container (via GT_DOLT_PORT), those databases
	// are destroyed when the container is terminated at cleanup —
	// preventing orphan accumulation in the shared production Dolt data dir.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "integration TestMain: dolt setup: %v\n", err)
		restore()
		os.Exit(1)
	}
	if err := hermetic.Check(); err != nil {
		fmt.Fprintf(os.Stderr, "integration TestMain: %v\n", err)
		testutil.TerminateDoltContainer()
		restore()
		os.Exit(1)
	}

	code := m.Run()

	// Clean up the shared Dolt container.
	testutil.TerminateDoltContainer()
	restore()
	os.Exit(code)
}
