package hermetic

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestMain(m *testing.M) { Main(m) }

// setAgentSessionEnv simulates the environment a Gas Town polecat session
// exports: identity, town root, production Dolt port, tmux client, and friends.
func setAgentSessionEnv(t *testing.T) {
	t.Helper()
	t.Setenv(IsolatedVar, "")
	t.Setenv("GT_ROLE", "gastown/polecats/nitro")
	t.Setenv("GT_RIG", "gastown")
	t.Setenv("GT_POLECAT", "nitro")
	t.Setenv("GT_SESSION", "gs-nitro")
	t.Setenv("BD_ACTOR", "gastown/polecats/nitro")
	t.Setenv("GT_ROOT", "/live/town")
	t.Setenv("GT_TOWN_ROOT", "/live/town")
	t.Setenv("TMUX", "/tmp/tmux-1000/gt-live,123,0")
	t.Setenv("TMUX_PANE", "%87")
	t.Setenv("GT_DOLT_PORT", productionDoltPort)
	t.Setenv("BEADS_DOLT_SERVER_PORT", productionDoltPort)
	t.Setenv("BEADS_DOLT_PORT", productionDoltPort)
	t.Setenv("BEADS_DOLT_AUTO_START", "1")
	t.Setenv("BEADS_DIR", "/live/town/.beads")
	t.Setenv("BEADS_DOLT_SERVER_SOCKET", "/live/town/dolt.sock")
	t.Setenv("GT_DOLT_HOST", "db.example.com")
	t.Setenv("TMUX_TMPDIR", "")
	t.Setenv(TownSearchCeilingVar, "")
}

func TestIsolate_OverridesInheritedAgentEnv(t *testing.T) {
	setAgentSessionEnv(t)

	restore := Isolate()
	defer restore()

	for _, key := range inheritedEnvVars {
		if v, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q survived isolation, want unset", key, v)
		}
	}

	port := os.Getenv("GT_DOLT_PORT")
	if port == "" || port == productionDoltPort {
		t.Fatalf("GT_DOLT_PORT = %q, want an isolated non-production port", port)
	}
	for _, key := range doltPortEnvVars {
		if got := os.Getenv(key); got != port {
			t.Errorf("%s = %q, want %q (all port variables must agree)", key, got, port)
		}
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("isolated port %q is not numeric: %v", port, err)
	}
	if portAccepts(n) {
		t.Errorf("isolated port %d accepts connections; it must have no listener", n)
	}
	if got := os.Getenv("BEADS_DOLT_AUTO_START"); got != "0" {
		t.Errorf("BEADS_DOLT_AUTO_START = %q, want 0", got)
	}

	tmuxDir := os.Getenv("TMUX_TMPDIR")
	if info, err := os.Stat(tmuxDir); tmuxDir == "" || err != nil || !info.IsDir() {
		t.Errorf("TMUX_TMPDIR = %q, want a private existing directory (stat err: %v)", tmuxDir, err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// This package lives at internal/testutil/hermetic in the module.
	wantCeiling := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(wd))))
	if got := os.Getenv(TownSearchCeilingVar); got != wantCeiling {
		t.Errorf("%s = %q, want parent of module root %q", TownSearchCeilingVar, got, wantCeiling)
	}

	if got := os.Getenv(IsolatedVar); got != "1" {
		t.Errorf("%s = %q, want 1", IsolatedVar, got)
	}
	if err := Check(); err != nil {
		t.Errorf("Check after Isolate: %v", err)
	}
}

func TestIsolate_RestoreReturnsPreviousEnv(t *testing.T) {
	setAgentSessionEnv(t)
	t.Setenv("BEADS_DOLT_SERVER_HOST", "")
	if err := os.Unsetenv("BEADS_DOLT_SERVER_HOST"); err != nil {
		t.Fatal(err)
	}

	restore := Isolate()
	tmuxDir := os.Getenv("TMUX_TMPDIR")
	restore()

	if got := os.Getenv("BEADS_DOLT_SERVER_PORT"); got != productionDoltPort {
		t.Errorf("BEADS_DOLT_SERVER_PORT = %q after restore, want %q", got, productionDoltPort)
	}
	if got := os.Getenv("GT_ROLE"); got != "gastown/polecats/nitro" {
		t.Errorf("GT_ROLE = %q after restore, want gastown/polecats/nitro", got)
	}
	if _, ok := os.LookupEnv("BEADS_DOLT_SERVER_HOST"); ok {
		t.Error("BEADS_DOLT_SERVER_HOST was unset before Isolate but is set after restore")
	}
	if _, err := os.Stat(tmuxDir); !os.IsNotExist(err) {
		t.Errorf("private TMUX_TMPDIR %q still exists after restore (err: %v)", tmuxDir, err)
	}
}

func TestIsolate_KeepsEnvOfAlreadyIsolatedProcess(t *testing.T) {
	// A test binary re-executed as a helper subprocess inherits the marker and
	// the environment its parent test deliberately chose; it must survive.
	t.Setenv(IsolatedVar, "1")
	t.Setenv("GT_DOLT_PORT", "45678")
	t.Setenv("GT_ROOT", "/tmp/test-town")
	t.Setenv("GT_ROLE", "gastown/polecats/shiny")
	defer func() { helperSubprocess = false }()

	restore := Isolate()
	defer restore()

	for key, want := range map[string]string{
		"GT_DOLT_PORT": "45678",
		"GT_ROOT":      "/tmp/test-town",
		"GT_ROLE":      "gastown/polecats/shiny",
	} {
		if got := os.Getenv(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr bool
	}{
		{name: "isolated", env: nil},
		{name: "loopback host", env: map[string]string{"GT_DOLT_HOST": "127.0.0.1"}},
		{name: "test container port", env: map[string]string{"BEADS_DOLT_SERVER_PORT": "55001"}},
		// Container helpers that set only BEADS_DOLT_PORT left the inherited,
		// higher-precedence BEADS_DOLT_SERVER_PORT pointing at production.
		{name: "server port still production", env: map[string]string{"BEADS_DOLT_SERVER_PORT": productionDoltPort}, wantErr: true},
		{name: "gt port unset", env: map[string]string{"GT_DOLT_PORT": ""}, wantErr: true},
		{name: "socket set", env: map[string]string{"BEADS_DOLT_SERVER_SOCKET": "/tmp/dolt.sock"}, wantErr: true},
		{name: "remote host", env: map[string]string{"BEADS_DOLT_SERVER_HOST": "db.example.com"}, wantErr: true},
		{name: "agent role", env: map[string]string{"GT_ROLE": "gastown/polecats/nitro"}, wantErr: true},
		{name: "bd actor", env: map[string]string{"BD_ACTOR": "gastown/polecats/nitro"}, wantErr: true},
		{name: "town root", env: map[string]string{"GT_ROOT": "/live/town"}, wantErr: true},
		{name: "inside tmux", env: map[string]string{"TMUX": "/tmp/tmux-1000/gt-live,1,0"}, wantErr: true},
		{name: "shared tmux socket dir", env: map[string]string{"TMUX_TMPDIR": ""}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Start from the environment TestMain isolated.
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			err := Check()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheck_RejectsDiscoverableTown(t *testing.T) {
	// A worktree inside a town: without a ceiling, walking up from the
	// working directory finds the town and gt code would act on it.
	town := t.TempDir()
	if err := os.MkdirAll(filepath.Join(town, "mayor"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(town, "mayor", "town.json"), []byte(`{"name":"live"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(town, "gastown", "polecats", "nitro", "gastown")
	pkgDir := filepath.Join(worktree, "internal", "cmd")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(pkgDir)

	t.Setenv(TownSearchCeilingVar, "")
	if err := Check(); err == nil {
		t.Fatal("Check() = nil with a town discoverable from the working directory")
	}

	t.Setenv(TownSearchCeilingVar, filepath.Dir(worktree))
	if err := Check(); err != nil {
		t.Fatalf("Check() with ceiling above the worktree: %v", err)
	}
}

func TestCheck_TrustsHelperSubprocessEnv(t *testing.T) {
	// A helper subprocess may have had variables stripped or replaced by its
	// parent test; Check must not abort its TestMain.
	t.Setenv(IsolatedVar, "1")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("GT_ROLE", "gastown/polecats/shiny")
	defer func() { helperSubprocess = false }()

	Isolate()
	if err := Check(); err != nil {
		t.Fatalf("Check in helper subprocess: %v", err)
	}
}

func TestIsolate_ShadowsInstalledGT(t *testing.T) {
	setAgentSessionEnv(t)
	restore := Isolate()
	defer restore()

	out, err := exec.Command("gt", "rig", "boot", "testrig").CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		t.Fatalf("gt on PATH ran with err=%v output=%q; want the hermetic stub (exit 127)", err, out)
	}
	if !strings.Contains(string(out), "hermetic tests") {
		t.Errorf("gt stub output = %q, want explanation", out)
	}
}

func TestIsolate_WithInstalledGTKeepsPath(t *testing.T) {
	setAgentSessionEnv(t)
	before := os.Getenv("PATH")
	restore := Isolate(WithInstalledGT())
	defer restore()

	if got := os.Getenv("PATH"); got != before {
		t.Errorf("PATH changed with WithInstalledGT: %q -> %q", before, got)
	}
}
