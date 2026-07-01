package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
)

type issueTrackerBackend interface {
	Name() string
	CommandName() string
	EnsureAvailable(autoInstall bool) error
	PreflightInstall(townPath string, requestedDoltPort int) error
	InitializeStorage(townPath string) error
	InitArgs(townPath string) []string
	WaitUntilReady(townPath string) error
	PostInit(townPath string) error
	ShowPostInstallDoltHint() bool
}

func selectedIssueTrackerBackend() issueTrackerBackend {
	return selectedIssueTrackerBackendForTown("")
}

func selectedIssueTrackerBackendForTown(townPath string) issueTrackerBackend {
	return issueTrackerBackendForKind(deps.EffectiveIssueTrackerBackend(townPath))
}

func issueTrackerBackendForKind(kind deps.IssueTrackerBackend) issueTrackerBackend {
	if kind == deps.IssueTrackerBackendMinibeads {
		return miniBeadsIssueTracker{}
	}
	return beadsDoltIssueTracker{}
}

type beadsDoltIssueTracker struct{}

func (beadsDoltIssueTracker) Name() string {
	return "beads"
}

func (beadsDoltIssueTracker) CommandName() string {
	return deps.IssueTrackerCommandName(deps.IssueTrackerBackendDefault)
}

func (beadsDoltIssueTracker) EnsureAvailable(autoInstall bool) error {
	return deps.EnsureBeads(autoInstall)
}

func (beadsDoltIssueTracker) PreflightInstall(townPath string, requestedDoltPort int) error {
	if err := ensureInstallDoltReady(); err != nil {
		return err
	}

	// Preflight: ensure dolt identity before any workspace mutations.
	// This prevents a partial install that can't be retried without --force.
	if err := doltserver.EnsureDoltIdentity(); err != nil {
		return fmt.Errorf("dolt identity setup failed (required for beads): %w\n\nTo fix, run:\n  dolt config --global --add user.name \"Your Name\"\n  dolt config --global --add user.email \"you@example.com\"", err)
	}

	// Preflight: check Dolt port availability before creating any files.
	// A port conflict would leave a partial install that needs --force to retry.
	port := doltserver.DefaultPort
	if requestedDoltPort != 0 {
		port = requestedDoltPort
		os.Setenv("GT_DOLT_PORT", strconv.Itoa(port))
	} else if p := os.Getenv("GT_DOLT_PORT"); p != "" {
		if envPort, err := strconv.Atoi(p); err == nil {
			port = envPort
		}
	}
	externalTestDolt := useExternalTestDoltServer(port)
	if err := doltserver.CheckPortAvailable(port); err != nil {
		// Port is in use, but if a Dolt server is already running for this
		// same town, we can reuse it instead of starting a new one.
		if canReuseInstallDoltServer(townPath, port) || externalTestDolt {
			fmt.Printf("   %s Using existing Dolt server on port %d\n",
				style.Dim.Render("ℹ"), port)
			return nil
		}

		pid, dataDir := doltserver.PortHolder(port)
		msg := fmt.Sprintf("Dolt port %d is already in use", port)
		if pid > 0 && dataDir != "" {
			msg += fmt.Sprintf("\nPort is held by dolt PID %d serving %s", pid, dataDir)
		} else if pid > 0 {
			msg += fmt.Sprintf("\nPort is held by PID %d", pid)
		}
		msg += "\n\nAnother Gas Town instance is using this port. Specify a free port:"
		origArgs := strings.Join(os.Args[1:], " ")
		if freePort := doltserver.FindFreePort(port + 1); freePort > 0 {
			msg += fmt.Sprintf("\n\n  gt %s --dolt-port %d", origArgs, freePort)
		} else {
			msg += fmt.Sprintf("\n\n  gt %s --dolt-port <port>", origArgs)
		}
		return fmt.Errorf("%s", msg)
	}

	return nil
}

func (beadsDoltIssueTracker) InitializeStorage(townPath string) error {
	port := doltserver.DefaultConfig(townPath).Port
	externalTestDolt := useExternalTestDoltServer(port)
	if externalTestDolt {
		return nil
	}

	// Set up Dolt: identity -> init-rig hq -> server start. This ordering
	// works because InitRig falls through to `dolt init` when the server is not
	// running yet. Identity was verified in preflight above.
	if _, _, err := doltserver.InitRig(townPath, "hq"); err != nil {
		return fmt.Errorf("initializing HQ Dolt database: %w", err)
	}

	// Start the Dolt server. It stays running after install; stop it with
	// `gt dolt stop` when not needed.
	if err := doltserver.Start(townPath); err != nil {
		if !strings.Contains(err.Error(), "already running") {
			return fmt.Errorf("starting Dolt server for beads: %w", err)
		}
	}
	return nil
}

func (beadsDoltIssueTracker) InitArgs(townPath string) []string {
	cfg := doltserver.DefaultConfig(townPath)
	// gt install --force preserves town state; bd reinit flags would destroy town beads.
	return []string{"init", "--prefix", "hq", "--server",
		"--server-port", strconv.Itoa(cfg.Port)}
}

func (beadsDoltIssueTracker) WaitUntilReady(townPath string) error {
	// Dolt server is required. TCP reachability alone is not sufficient; wait
	// for MySQL protocol readiness.
	cfg := doltserver.DefaultConfig(townPath)
	dsn := buildDoltDSNFromConfig(cfg, "", dsnOpts{})
	var lastErr error
	for attempt := 0; attempt < 20; attempt++ {
		db, err := sql.Open("mysql", dsn)
		if err == nil {
			err = db.Ping()
			db.Close()
		}
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("Dolt server is not ready after 10s: %w", lastErr)
}

func (beadsDoltIssueTracker) PostInit(townPath string) error {
	if err := doltserver.EnsureMetadata(townPath, "hq"); err != nil {
		return fmt.Errorf("ensuring hq metadata: %w", err)
	}
	return nil
}

func (beadsDoltIssueTracker) ShowPostInstallDoltHint() bool {
	return true
}

type miniBeadsIssueTracker struct{}

func (miniBeadsIssueTracker) Name() string {
	return "minibeads"
}

func (miniBeadsIssueTracker) CommandName() string {
	return deps.IssueTrackerCommandName(deps.IssueTrackerBackendMinibeads)
}

func (miniBeadsIssueTracker) EnsureAvailable(autoInstall bool) error {
	return deps.EnsureBeadsForBackend(deps.IssueTrackerBackendMinibeads, autoInstall)
}

func (miniBeadsIssueTracker) PreflightInstall(_ string, _ int) error {
	return nil
}

func (miniBeadsIssueTracker) InitializeStorage(_ string) error {
	return nil
}

func (miniBeadsIssueTracker) InitArgs(_ string) []string {
	return []string{"init", "--prefix", "hq"}
}

func (miniBeadsIssueTracker) WaitUntilReady(_ string) error {
	return nil
}

func (miniBeadsIssueTracker) PostInit(_ string) error {
	return nil
}

func (miniBeadsIssueTracker) ShowPostInstallDoltHint() bool {
	return false
}
