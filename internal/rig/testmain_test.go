package rig

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/testutil"
)

var (
	rigTestTownRoot string
	rigTestDoltPort string
)

func TestMain(m *testing.M) {
	code := runRigTests(m)
	os.Exit(code)
}

func runRigTests(m *testing.M) int {
	townRoot, err := os.MkdirTemp("", "gastown-rig-tests-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal/rig: create isolated town root: %v\n", err)
		return 1
	}

	containerStarted := false
	port := ""
	if runtime.GOOS == "windows" {
		port, err = unusedLocalPort()
	} else {
		err = testutil.EnsureDoltContainerForTestMain()
		if err == nil {
			containerStarted = true
			port = testutil.DoltContainerPort()
		}
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "internal/rig: start isolated Dolt server: %v\n", err)
		_ = os.RemoveAll(townRoot)
		return 1
	}
	if port == "" || port == strconv.Itoa(doltserver.DefaultPort) {
		fmt.Fprintf(os.Stderr, "internal/rig: unsafe isolated Dolt port %q\n", port)
		if containerStarted {
			testutil.TerminateDoltContainer()
		}
		_ = os.RemoveAll(townRoot)
		return 1
	}

	rigTestTownRoot = townRoot
	rigTestDoltPort = port
	if _, err := prepareRigTestTown(townRoot, port); err != nil {
		fmt.Fprintf(os.Stderr, "internal/rig: prepare isolated town: %v\n", err)
		if containerStarted {
			testutil.TerminateDoltContainer()
		}
		_ = os.RemoveAll(townRoot)
		return 1
	}
	if err := applyRigTestProcessEnv(townRoot, port); err != nil {
		fmt.Fprintf(os.Stderr, "internal/rig: configure isolated environment: %v\n", err)
		if containerStarted {
			testutil.TerminateDoltContainer()
		}
		_ = os.RemoveAll(townRoot)
		return 1
	}

	if containerStarted {
		running, _, runErr := doltserver.IsRunning(townRoot)
		if runErr != nil || !running {
			fmt.Fprintf(os.Stderr, "internal/rig: isolated Dolt server unavailable (running=%t): %v\n", running, runErr)
			testutil.TerminateDoltContainer()
			_ = os.RemoveAll(townRoot)
			return 1
		}
	}

	code := m.Run()
	if containerStarted {
		testutil.TerminateDoltContainer()
	}
	if err := os.RemoveAll(townRoot); err != nil {
		fmt.Fprintf(os.Stderr, "internal/rig: remove isolated town root: %v\n", err)
		code = 1
	}
	return code
}

func unusedLocalPort() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return "", err
	}
	return strconv.Itoa(port), nil
}

func prepareRigTestTown(root, port string) (*config.RigsConfig, error) {
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber <= 0 {
		return nil, fmt.Errorf("invalid isolated Dolt port %q", port)
	}

	for _, dir := range []string{
		filepath.Join(root, "mayor"),
		filepath.Join(root, ".beads"),
		filepath.Join(root, ".dolt-data"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	townConfig := &config.TownConfig{
		Type:      "town",
		Version:   config.CurrentTownVersion,
		Name:      "rig-manager-tests",
		CreatedAt: time.Now().UTC(),
	}
	if err := config.SaveTownConfig(filepath.Join(root, "mayor", "town.json"), townConfig); err != nil {
		return nil, fmt.Errorf("save town config: %w", err)
	}

	rigsConfig := &config.RigsConfig{
		Version: config.CurrentRigsVersion,
		Rigs:    make(map[string]config.RigEntry),
	}
	if err := config.SaveRigsConfig(filepath.Join(root, "mayor", "rigs.json"), rigsConfig); err != nil {
		return nil, fmt.Errorf("save rigs config: %w", err)
	}

	doltConfig := fmt.Sprintf("listener:\n  host: 127.0.0.1\n  port: %d\n", portNumber)
	if err := os.WriteFile(filepath.Join(root, ".dolt-data", "config.yaml"), []byte(doltConfig), 0o600); err != nil {
		return nil, fmt.Errorf("save isolated Dolt config: %w", err)
	}

	return rigsConfig, nil
}

func rigTestEnvironment(root, port string) map[string]string {
	return map[string]string{
		"GT_TOWN_ROOT":               root,
		"GT_ROOT":                    root,
		"GT_DOLT_HOST":               "127.0.0.1",
		"GT_DOLT_PORT":               port,
		"GT_DOLT_USER":               "root",
		"GT_DOLT_PASSWORD":           "",
		"GT_DOLT_DATA":               "",
		"GT_DOLT_IGNORE_CONFIG":      "",
		"BEADS_DIR":                  "",
		"BEADS_DB":                   "",
		"BD_DB":                      "",
		"BEADS_SHARED_SERVER_DIR":    "",
		"BEADS_DOLT_DATA_DIR":        "",
		"BEADS_DOLT_SERVER_DATABASE": "",
		"BEADS_DOLT_SERVER_HOST":     "127.0.0.1",
		"BEADS_DOLT_SERVER_PORT":     port,
		"BEADS_DOLT_PORT":            port,
		"BEADS_DOLT_SERVER_USER":     "root",
		"BEADS_DOLT_SERVER_PASSWORD": "",
		"BEADS_DOLT_AUTO_START":      "0",
		"DOLT_CLI_PASSWORD":          "",
		"BD_ACTOR":                   "rig-manager-test",
	}
}

func applyRigTestProcessEnv(root, port string) error {
	for key, value := range rigTestEnvironment(root, port) {
		var err error
		if value == "" {
			err = os.Unsetenv(key)
		} else {
			err = os.Setenv(key, value)
		}
		if err != nil {
			return fmt.Errorf("set %s: %w", key, err)
		}
	}
	return nil
}
