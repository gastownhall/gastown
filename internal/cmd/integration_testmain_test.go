//go:build integration

package cmd

import (
	"flag"
	"fmt"
	"os"
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestMain(m *testing.M) {
	// Force sequential test execution to avoid bd file locks on Windows.
	_ = flag.Set("test.parallel", "1")
	flag.Parse()

	// Sweep any dolt sql-server processes already orphaned by a prior run's
	// deleted temp town dir (gtf-4cj) before this run adds its own.
	if stopped, err := doltserver.ReapOrphanedDeletedDoltServers(); err != nil {
		fmt.Fprintf(os.Stderr, "integration TestMain: pre-run orphan sweep: %v\n", err)
	} else if stopped > 0 {
		fmt.Fprintf(os.Stderr, "integration TestMain: reaped %d dolt server(s) orphaned by a prior run\n", stopped)
	}

	// Start an ephemeral Dolt container for this package's integration tests.
	// Tests like TestAgentWorktreesStayClean and TestBeadsRoutingFromTownRoot
	// spawn gt/bd subprocesses that create databases (e.g., "tr", "hq").
	// By routing to an isolated container (via GT_DOLT_PORT), those databases
	// are destroyed when the container is terminated at cleanup —
	// preventing orphan accumulation in the shared production Dolt data dir.
	if err := testutil.EnsureDoltContainerForTestMain(); err != nil {
		fmt.Fprintf(os.Stderr, "integration TestMain: dolt setup: %v\n", err)
		os.Exit(1)
	}

	code := m.Run()

	// Clean up the shared Dolt container.
	testutil.TerminateDoltContainer()

	// Catch any real dolt sql-server this run itself spawned and orphaned
	// (e.g. a per-test t.Cleanup that never ran because the process was
	// killed/panicked before Cleanup, or the town dir was removed while the
	// server was still shutting down) before exiting.
	if stopped, err := doltserver.ReapOrphanedDeletedDoltServers(); err != nil {
		fmt.Fprintf(os.Stderr, "integration TestMain: post-run orphan sweep: %v\n", err)
	} else if stopped > 0 {
		fmt.Fprintf(os.Stderr, "integration TestMain: reaped %d dolt server(s) orphaned this run\n", stopped)
	}

	os.Exit(code)
}
