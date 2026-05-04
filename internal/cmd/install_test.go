package cmd

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/deps"
	"github.com/steveyegge/gastown/internal/doltserver"
)

func TestBuildBdInitArgs_AlwaysIncludesServerPort(t *testing.T) {
	townDir := t.TempDir()
	t.Setenv("GT_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	args := buildBdInitArgs(townDir)

	if len(args) != 7 {
		t.Fatalf("expected 7 args, got %d: %v", len(args), args)
	}
	if args[4] != "--server-port" {
		t.Fatalf("expected args[4] = --server-port, got %q", args[4])
	}
	if args[5] != "3307" {
		t.Fatalf("expected default port 3307, got %q", args[5])
	}
	if args[6] != "--force" {
		t.Fatalf("expected args[6] = --force, got %q", args[6])
	}
}

func TestBuildBdInitArgs_RespectsGTDoltPortEnv(t *testing.T) {
	townDir := t.TempDir()

	t.Setenv("GT_DOLT_PORT", "4400")

	args := buildBdInitArgs(townDir)

	if args[5] != "4400" {
		t.Fatalf("expected port 4400 from GT_DOLT_PORT, got %q", args[5])
	}
}

func TestBuildBdInitArgs_ConfigYAMLTakesPrecedence(t *testing.T) {
	townDir := t.TempDir()
	doltDataDir := filepath.Join(townDir, ".dolt-data")
	if err := os.MkdirAll(doltDataDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	configYAML := "listener:\n  host: 127.0.0.1\n  port: 5500\n"
	if err := os.WriteFile(filepath.Join(doltDataDir, "config.yaml"), []byte(configYAML), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	t.Setenv("GT_DOLT_PORT", "4400")

	args := buildBdInitArgs(townDir)

	if args[5] != "5500" {
		t.Fatalf("expected port 5500 from config.yaml (precedence over env), got %q", args[5])
	}
}

func TestBuildBdInitArgs_PortMatchesDefaultConfig(t *testing.T) {
	townDir := t.TempDir()

	args := buildBdInitArgs(townDir)
	cfg := doltserver.DefaultConfig(townDir)

	if args[5] != strconv.Itoa(cfg.Port) {
		t.Fatalf("port should match DefaultConfig: args=%q, config=%d", args[5], cfg.Port)
	}
}

func TestEnsureBeadsConfigYAML_CreatesWhenMissing(t *testing.T) {
	beadsDir := t.TempDir()

	if err := beads.EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(beadsDir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}

	got := string(data)
	want := "prefix: hq\nissue-prefix: hq\ndolt.idle-timeout: \"0\"\n"
	if got != want {
		t.Fatalf("config.yaml = %q, want %q", got, want)
	}
}

func TestEnsureBeadsConfigYAML_RepairsPrefixKeysAndPreservesOtherLines(t *testing.T) {
	beadsDir := t.TempDir()
	path := filepath.Join(beadsDir, "config.yaml")
	original := strings.Join([]string{
		"# existing settings",
		"prefix: wrong",
		"sync-branch: main",
		"issue-prefix: wrong",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := beads.EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "prefix: hq\n") {
		t.Fatalf("config.yaml missing repaired prefix: %q", text)
	}
	if !strings.Contains(text, "issue-prefix: hq\n") {
		t.Fatalf("config.yaml missing repaired issue-prefix: %q", text)
	}
	if !strings.Contains(text, "sync-branch: main\n") {
		t.Fatalf("config.yaml should preserve unrelated settings: %q", text)
	}
}

func TestEnsureBeadsConfigYAML_AddsMissingIssuePrefixKey(t *testing.T) {
	beadsDir := t.TempDir()
	path := filepath.Join(beadsDir, "config.yaml")
	if err := os.WriteFile(path, []byte("prefix: hq\n"), 0644); err != nil {
		t.Fatalf("write config.yaml: %v", err)
	}

	if err := beads.EnsureConfigYAML(beadsDir, "hq"); err != nil {
		t.Fatalf("EnsureConfigYAML: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "prefix: hq\n") {
		t.Fatalf("config.yaml missing prefix: %q", text)
	}
	if !strings.Contains(text, "issue-prefix: hq\n") {
		t.Fatalf("config.yaml missing issue-prefix: %q", text)
	}
}

func TestInstallFailsBeforeMutationWhenDoltMissing(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "missing-dolt-hq")
	gtBinary := buildGT(t)

	cmd := exec.Command(gtBinary, "install", hqPath, "--name", "missing-dolt-test")
	cmd.Env = installTestEnvWithFakeBD(t, tmpDir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gt install should fail when dolt is missing; output:\n%s", output)
	}

	out := string(output)
	if !strings.Contains(out, "dolt is required for gt install with beads enabled") {
		t.Fatalf("expected missing-dolt preflight error, got:\n%s", out)
	}
	if !strings.Contains(out, "--no-beads") {
		t.Fatalf("expected --no-beads fallback hint, got:\n%s", out)
	}
	if _, statErr := os.Stat(hqPath); !os.IsNotExist(statErr) {
		t.Fatalf("install should not create target HQ before missing-dolt failure; statErr=%v", statErr)
	}
}

func TestInstallNoBeadsAllowsMissingDolt(t *testing.T) {
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "no-beads-hq")
	gtBinary := buildGT(t)

	cmd := exec.Command(gtBinary, "install", hqPath, "--no-beads", "--name", "no-beads-test")
	cmd.Env = installTestEnvWithFakeBD(t, tmpDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt install --no-beads should succeed without dolt: %v\nOutput:\n%s", err, output)
	}

	if info, statErr := os.Stat(hqPath); statErr != nil {
		t.Fatalf("HQ root should exist: %v", statErr)
	} else if !info.IsDir() {
		t.Fatalf("HQ root should be a directory")
	}
	if _, statErr := os.Stat(filepath.Join(hqPath, ".beads")); !os.IsNotExist(statErr) {
		t.Fatalf("--no-beads install should not create .beads; statErr=%v", statErr)
	}
}

func TestInstallFailsBeforeMutationWhenDoltPortOccupiedByNonDolt(t *testing.T) {
	ln := listenAndHoldTCP(t)
	tmpDir := t.TempDir()
	hqPath := filepath.Join(tmpDir, "port-conflict-hq")
	gtBinary := buildGT(t)
	port := ln.Addr().(*net.TCPAddr).Port

	cmd := exec.Command(gtBinary, "install", hqPath,
		"--name", "port-conflict-test",
		"--dolt-port", strconv.Itoa(port),
	)
	cmd.Env = installTestEnvWithFakeBDAndDolt(t, tmpDir)
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("gt install should fail when a non-Dolt process owns the Dolt port; output:\n%s", output)
	}

	out := string(output)
	if !strings.Contains(out, "Dolt port") || !strings.Contains(out, "already in use") {
		t.Fatalf("expected Dolt port conflict error, got:\n%s", out)
	}
	if !strings.Contains(out, "--dolt-port") {
		t.Fatalf("expected --dolt-port recovery hint, got:\n%s", out)
	}
	if _, statErr := os.Stat(hqPath); !os.IsNotExist(statErr) {
		t.Fatalf("install should not create target HQ before port preflight failure; statErr=%v", statErr)
	}
}
func TestFormatInstallDoltError(t *testing.T) {
	tests := []struct {
		name      string
		status    deps.DoltStatus
		version   string
		detail    string
		goos      string
		want      []string
		wantNoErr bool
	}{
		{
			name:      "ok",
			status:    deps.DoltOK,
			wantNoErr: true,
		},
		{
			name:   "missing darwin suggests homebrew",
			status: deps.DoltNotFound,
			goos:   "darwin",
			want:   []string{"dolt is required", "brew install dolt", "--no-beads"},
		},
		{
			name:    "too old includes minimum",
			status:  deps.DoltTooOld,
			version: "1.0.0",
			goos:    "linux",
			want:    []string{"dolt 1.0.0 is too old", deps.MinDoltVersion, "Upgrade Dolt"},
		},
		{
			name:   "exec failed includes detail",
			status: deps.DoltExecFailed,
			detail: "permission denied",
			goos:   "linux",
			want:   []string{"'dolt version' failed", "permission denied", "Reinstall Dolt"},
		},
		{
			name:   "unknown fails closed",
			status: deps.DoltUnknown,
			detail: "unexpected output",
			goos:   "linux",
			want:   []string{"version could not be parsed", "unexpected output", "Reinstall Dolt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := formatInstallDoltError(tt.status, tt.version, tt.detail, tt.goos)
			if tt.wantNoErr {
				if err != nil {
					t.Fatalf("formatInstallDoltError returned error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("formatInstallDoltError returned nil, want error")
			}
			msg := err.Error()
			for _, want := range tt.want {
				if !strings.Contains(msg, want) {
					t.Fatalf("error missing %q:\n%s", want, msg)
				}
			}
		})
	}
}

func TestInstallDoltServerReuseRejectsNonMySQLPortPromptly(t *testing.T) {
	ln := listenAndHoldTCP(t)
	port := ln.Addr().(*net.TCPAddr).Port
	start := time.Now()
	if canReuseInstallDoltServer(t.TempDir(), port) {
		t.Fatal("non-MySQL listener should not be reusable as an existing Dolt server")
	}
	if elapsed := time.Since(start); elapsed > installDoltServerProbeTimeout+time.Second {
		t.Fatalf("non-MySQL listener probe took too long: %s", elapsed)
	}
}

func listenAndHoldTCP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	done := make(chan struct{})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				<-done
			}(conn)
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
	})
	return ln
}

func installTestEnvWithFakeBD(t *testing.T, homeDir string) []string {
	t.Helper()
	return installTestEnv(t, homeDir, false)
}

func installTestEnvWithFakeBDAndDolt(t *testing.T, homeDir string) []string {
	t.Helper()
	return installTestEnv(t, homeDir, true)
}

func installTestEnv(t *testing.T, homeDir string, includeDolt bool) []string {
	t.Helper()

	binDir := t.TempDir()
	bdName := "bd"
	mode := os.FileMode(0755)
	content := "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then\n  echo 'bd version 999.0.0'\n  exit 0\nfi\necho 'fake bd only supports version' >&2\nexit 1\n"
	if runtime.GOOS == "windows" {
		bdName = "bd.bat"
		mode = 0644
		content = "@echo off\r\nif \"%1\"==\"version\" (\r\n  echo bd version 999.0.0\r\n  exit /b 0\r\n)\r\necho fake bd only supports version 1>&2\r\nexit /b 1\r\n"
	}
	if err := os.WriteFile(filepath.Join(binDir, bdName), []byte(content), mode); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	if includeDolt {
		doltName := "dolt"
		doltMode := os.FileMode(0755)
		doltContent := "#!/bin/sh\nif [ \"$1\" = \"version\" ]; then\n  echo 'dolt version 999.0.0'\n  exit 0\nfi\nif [ \"$1\" = \"config\" ] && [ \"$2\" = \"--global\" ] && [ \"$3\" = \"--get\" ]; then\n  case \"$4\" in\n    user.name) echo 'Gas Town Test'; exit 0 ;;\n    user.email) echo 'gastown-test@example.com'; exit 0 ;;\n  esac\nfi\necho 'fake dolt only supports version and config --global --get' >&2\nexit 1\n"
		if runtime.GOOS == "windows" {
			doltName = "dolt.bat"
			doltMode = 0644
			doltContent = "@echo off\r\nif \"%1\"==\"version\" (\r\n  echo dolt version 999.0.0\r\n  exit /b 0\r\n)\r\nif \"%1\"==\"config\" if \"%2\"==\"--global\" if \"%3\"==\"--get\" (\r\n  if \"%4\"==\"user.name\" (\r\n    echo Gas Town Test\r\n    exit /b 0\r\n  )\r\n  if \"%4\"==\"user.email\" (\r\n    echo gastown-test@example.com\r\n    exit /b 0\r\n  )\r\n)\r\necho fake dolt only supports version and config --global --get 1>&2\r\nexit /b 1\r\n"
		}
		if err := os.WriteFile(filepath.Join(binDir, doltName), []byte(doltContent), doltMode); err != nil {
			t.Fatalf("write fake dolt: %v", err)
		}
	}

	env := make([]string, 0, len(os.Environ())+6)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "HOME", "PATH", "BEADS_DIR", "BEADS_DB", "BEADS_DOLT_SERVER_DATABASE", "GT_DOLT_PORT":
			continue
		default:
			env = append(env, entry)
		}
	}

	return append(env,
		"HOME="+homeDir,
		"PATH="+binDir,
		"BEADS_DIR=",
		"BEADS_DB=",
		"BEADS_DOLT_SERVER_DATABASE=",
		"GT_DOLT_PORT=",
	)
}
