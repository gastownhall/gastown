// Package hermetic keeps Go tests from reaching the live Gas Town that the
// caller's session belongs to.
//
// `go test` run from an agent session inherits that session's environment
// (GT_ROLE, GT_ROOT, BEADS_DOLT_PORT=3307, TMUX, ...) and, for polecat and crew
// worktrees, a working directory inside the town. Without isolation, tests
// that exercise gt code paths create databases on the production Dolt server,
// send real nudges and mail, write to the town's event feed, and can even run
// `gt done` for the caller's own hooked bead.
//
// Every test package calls Main (or Isolate and Check) from its TestMain. The
// package depends only on the standard library so that any package, including
// beads, config, tmux and workspace, can use it without an import cycle.
package hermetic

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// IsolatedVar marks a process whose environment has already been isolated by
// Isolate. Child processes inherit it, so a test binary re-executed as a
// helper subprocess keeps the environment its parent test chose instead of
// being re-isolated.
const IsolatedVar = "GT_TEST_HERMETIC"

// TownSearchCeilingVar lists directories (separated by os.PathListSeparator)
// that town-root discovery must not walk up into, like Git's
// GIT_CEILING_DIRECTORIES. Isolate sets it to the parent of the module root
// so that a test running in a worktree inside a town cannot discover that
// town from its working directory. It must match config.TownSearchCeilingEnv.
const TownSearchCeilingVar = "GT_CEILING_DIRECTORIES"

// productionDoltPort is the port both gt and the beads SDK fall back to when
// no port is configured. A developer's (or agent's) live Dolt server listens
// here, so tests must never end up resolving to it implicitly.
const productionDoltPort = "3307"

// doltPortEnvVars select the Dolt server port. They are pointed at a port with
// no listener rather than unset: with no port configured, gt and the beads SDK
// fall back to 3307, which is exactly where a live server usually runs.
var doltPortEnvVars = []string{
	"GT_DOLT_PORT",
	"BEADS_DOLT_SERVER_PORT", // highest precedence in the beads SDK
	"BEADS_DOLT_PORT",
}

// inheritedEnvVars identify the caller's session or locate its town, Dolt
// server, tmux server or remote endpoints. Agent sessions export them; a test
// that inherits them acts as that agent against the live town.
var inheritedEnvVars = []string{
	// Town location.
	"GT_ROOT",
	"GT_TOWN_ROOT",
	"GT_TOWN",
	"GT_CWD",

	// Agent identity and current work.
	"GT_ROLE",
	"GT_RIG",
	"GT_POLECAT",
	"GT_CREW",
	"GT_MAYOR",
	"GT_DOG_NAME",
	"GT_REFINERY_WORKER",
	"GT_DAEMON",
	"GT_AGENT",
	"GT_ACCOUNT",
	"GT_SESSION",
	"GT_SESSION_ID",
	"GT_SESSION_ID_ENV",
	"GT_RUN",
	"GT_BRANCH",
	"GT_POLECAT_PATH",
	"GT_ISSUE",
	"GT_WORK_BEAD",
	"GT_WORK_RIG",
	"GT_WORK_MOL",
	"GT_HOOK_SOURCE",
	"BD_ACTOR",
	"BEADS_AGENT_NAME",
	"CLAUDE_SESSION_ID",

	// tmux server of the caller's session.
	"TMUX",
	"TMUX_PANE",
	"GT_TOWN_SOCKET",
	"GT_TMUX_SOCKET",

	// Dolt server and beads data.
	"GT_DOLT_HOST",
	"GT_DOLT_USER",
	"GT_DOLT_PASSWORD",
	"GT_DOLT_DATA",
	"BEADS_DIR",
	"BEADS_DB",
	"BD_DB",
	"BEADS_DOLT_HOST",
	"BEADS_DOLT_SERVER_HOST",
	"BEADS_DOLT_SERVER_SOCKET",
	"BEADS_DOLT_SERVER_USER",
	"BEADS_DOLT_PASSWORD",
	"BEADS_DOLT_DATABASE",
	"BEADS_DOLT_SERVER_DATABASE",
	"BEADS_DOLT_DATA_DIR",
	"BEADS_DOLT_SERVER_MODE",
	"BEADS_DOLT_SHARED_SERVER",
	"BEADS_SHARED_SERVER_DIR",

	// Remote endpoints that would receive the test's traffic.
	"GT_PROXY_URL",
	"GT_PROXY_KEY",
	"GT_PROXY_CERT",
	"GT_PROXY_CA",
	"GT_OTEL_LOGS_URL",
	"GT_OTEL_METRICS_URL",
	"BD_OTEL_LOGS_URL",
	"BD_OTEL_METRICS_URL",
}

// gtShimMessage is printed by the gt stub Isolate puts first on PATH.
const gtShimMessage = "gt: the installed gt is unavailable in hermetic tests (it would act on the caller's town); build gt and prepend it to PATH if a test needs it"

// Option adjusts Isolate.
type Option func(*options)

type options struct {
	keepInstalledGT bool
}

// WithInstalledGT keeps the gt binary found on PATH reachable. Integration
// tests that drive a freshly built gt use it; the gt subprocesses still
// inherit the isolated environment.
func WithInstalledGT() Option {
	return func(o *options) { o.keepInstalledGT = true }
}

// helperSubprocess records that Isolate found IsolatedVar already set: this
// process is a helper re-executed by an isolated test, and its environment was
// shaped deliberately by that test.
var helperSubprocess bool

// Main isolates the test process with Isolate, verifies it with Check, and
// runs the tests. Use it as the whole body of a package's TestMain:
//
//	func TestMain(m *testing.M) { hermetic.Main(m) }
func Main(m *testing.M) {
	restore := Isolate()
	if err := Check(); err != nil {
		fmt.Fprintf(os.Stderr, "hermetic: %v\n", err)
		restore()
		os.Exit(1)
	}
	code := m.Run()
	restore()
	os.Exit(code)
}

// Isolate makes the test process hermetic with respect to the caller's town:
//
//   - it clears the session identity, town location, tmux and remote endpoint
//     variables inherited from the caller (see inheritedEnvVars);
//   - it points every Dolt port variable at a local port with no listener and
//     disables beads SDK server auto-start;
//   - it gives tmux a private socket directory (TMUX_TMPDIR), so tmux commands
//     cannot reach the caller's tmux server and its agent sessions;
//   - it stops town-root discovery at the module root (TownSearchCeilingVar),
//     so a working directory inside a town does not lead tests back to it;
//   - it shadows any installed gt on PATH with a stub that fails, because an
//     installed gt predating these guards would still find and act on the
//     caller's town (see WithInstalledGT).
//
// Call it at the top of TestMain, before m.Run and before starting any test
// Dolt server or tmux server (which then override what they need). The
// returned function restores the previous environment; TestMain callers can
// ignore it.
func Isolate(opts ...Option) (restore func()) {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	helperSubprocess = os.Getenv(IsolatedVar) == "1"
	if helperSubprocess {
		return func() {}
	}

	saved := map[string]*string{}
	save := func(key string) {
		if _, done := saved[key]; done {
			return
		}
		if v, ok := os.LookupEnv(key); ok {
			saved[key] = &v
		} else {
			saved[key] = nil
		}
	}
	set := func(key, value string) {
		save(key)
		_ = os.Setenv(key, value) //nolint:tenv // intentional process-wide env for TestMain
	}

	for _, key := range inheritedEnvVars {
		save(key)
		_ = os.Unsetenv(key)
	}

	port := unusedLocalPort()
	for _, key := range doltPortEnvVars {
		set(key, port)
	}
	set("BEADS_DOLT_AUTO_START", "0")

	var tmuxDir string
	if dir, err := os.MkdirTemp(shortTempBase(), "gt-tmux-"); err == nil {
		tmuxDir = dir
		set("TMUX_TMPDIR", dir)
	}

	if root := moduleRoot(); root != "" {
		set(TownSearchCeilingVar, filepath.Dir(root))
	}

	var binDir string
	if !o.keepInstalledGT {
		if dir, err := writeGTShim(); err == nil {
			binDir = dir
			set("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
		}
	}

	set(IsolatedVar, "1")

	return func() {
		for key, v := range saved {
			if v == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *v) //nolint:tenv // restoring process-wide env
			}
		}
		if tmuxDir != "" {
			_ = os.RemoveAll(tmuxDir)
		}
		if binDir != "" {
			_ = os.RemoveAll(binDir)
		}
	}
}

// writeGTShim creates a directory holding a gt stub that prints gtShimMessage
// and fails, mirroring CI where gt is not on PATH for unit tests.
func writeGTShim() (string, error) {
	dir, err := os.MkdirTemp("", "gt-test-bin-")
	if err != nil {
		return "", err
	}
	name, script := "gt", "#!/bin/sh\necho \""+gtShimMessage+"\" >&2\nexit 127\n"
	if runtime.GOOS == "windows" {
		name, script = "gt.cmd", "@echo "+gtShimMessage+" 1>&2\r\n@exit /b 127\r\n"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

// Check reports an error if this process could still reach the caller's town:
// an inherited session variable survived, a Dolt port variable is unset or
// names the default production port, a Dolt socket or remote host is set,
// tmux has no private socket directory, or a town is discoverable from the
// working directory. Call it from TestMain after Isolate and after any test
// Dolt server has been started, so a helper that forgets one of the port
// variables fails loudly instead of silently using the inherited server.
//
// In a helper subprocess (see IsolatedVar) Check reports nil: the parent test
// already passed the check and may have changed variables on purpose for the
// child.
func Check() error {
	if helperSubprocess {
		return nil
	}
	for _, key := range []string{"GT_ROLE", "GT_ROOT", "GT_TOWN_ROOT", "BD_ACTOR", "TMUX", "BEADS_DOLT_SERVER_SOCKET"} {
		if v := os.Getenv(key); v != "" {
			return fmt.Errorf("%s=%q is set; tests must not inherit the caller's session", key, v)
		}
	}
	for _, key := range []string{"BEADS_DOLT_SERVER_HOST", "GT_DOLT_HOST"} {
		if v := os.Getenv(key); v != "" && v != "127.0.0.1" && v != "localhost" {
			return fmt.Errorf("%s=%q is set; tests must not target a non-local Dolt server", key, v)
		}
	}
	for _, key := range doltPortEnvVars {
		v := os.Getenv(key)
		if v == "" {
			return fmt.Errorf("%s is unset; gt and the beads SDK would fall back to port %s", key, productionDoltPort)
		}
		if v == productionDoltPort {
			return fmt.Errorf("%s=%s targets the default production Dolt port", key, v)
		}
	}
	if os.Getenv("TMUX_TMPDIR") == "" {
		return fmt.Errorf("TMUX_TMPDIR is unset; tmux commands would reach the caller's tmux server")
	}
	if town := discoverableTown(); town != "" {
		return fmt.Errorf("Gas Town workspace %s is discoverable from the working directory; set %s", town, TownSearchCeilingVar)
	}
	return nil
}

// shortTempBase returns a directory for the private tmux socket directory.
// Socket paths must fit in sun_path (about 104 bytes), so it prefers the short
// /tmp over a possibly long $TMPDIR, as tmux itself does.
func shortTempBase() string {
	if runtime.GOOS != "windows" {
		if info, err := os.Stat("/tmp"); err == nil && info.IsDir() {
			return "/tmp"
		}
	}
	return ""
}

// moduleRoot returns the nearest directory at or above the working directory
// that contains go.mod, or "" if there is none.
func moduleRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// discoverableTown walks up from the working directory the way town-root
// discovery does, honoring TownSearchCeilingVar, and returns the first
// directory containing mayor/town.json.
func discoverableTown() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	ceilings := map[string]bool{}
	for _, c := range filepath.SplitList(os.Getenv(TownSearchCeilingVar)) {
		if c != "" {
			ceilings[filepath.Clean(c)] = true
		}
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "mayor", "town.json")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir || ceilings[parent] {
			return ""
		}
		dir = parent
	}
}

// unusedLocalPort returns a loopback port with no listener. It reserves a port
// from the kernel, releases it, and confirms nothing accepts connections there.
func unusedLocalPort() string {
	for range 5 {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			break
		}
		port := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		if !portAccepts(port) {
			return strconv.Itoa(port)
		}
	}
	// Port 1 (tcpmux) is privileged and essentially never served, so test
	// servers started by unprivileged processes cannot claim it either.
	return "1"
}

func portAccepts(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
