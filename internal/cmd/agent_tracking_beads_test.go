package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveAgentTrackingBeadsDirPrefersCwdRigRedirectOverBeadsDir(t *testing.T) {
	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	t.Setenv("BEADS_DIR", townBeads)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	gotWorkDir, err := findCwdBeadsWorkDir()
	if err != nil {
		t.Fatalf("findCwdBeadsWorkDir() error = %v", err)
	}
	if gotWorkDir != rigWorkDir {
		t.Fatalf("findCwdBeadsWorkDir() = %q, want %q", gotWorkDir, rigWorkDir)
	}

	gotBeadsDir, err := resolveAgentTrackingBeadsDir()
	if err != nil {
		t.Fatalf("resolveAgentTrackingBeadsDir() error = %v", err)
	}
	if gotBeadsDir != rigBeads {
		t.Fatalf("resolveAgentTrackingBeadsDir() = %q, want %q", gotBeadsDir, rigBeads)
	}

	gotLocalWorkDir, err := findLocalBeadsDir()
	if err != nil {
		t.Fatalf("findLocalBeadsDir() error = %v", err)
	}
	if gotLocalWorkDir != townRoot {
		t.Fatalf("findLocalBeadsDir() = %q, want env parent %q", gotLocalWorkDir, townRoot)
	}
}

func TestRunAgentStateUsesCwdRigBeadsDirWhenBeadsDirPointsTown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	metadata := []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), metadata, 0o644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(tmp, "bd.log")
	bdScript := `#!/bin/sh
printf 'cmd=%s BEADS_DIR=%s DB=%s READONLY=%s AUTO=%s\n' "$1" "${BEADS_DIR-}" "${BEADS_DOLT_SERVER_DATABASE-}" "${BD_READONLY-}" "${BD_DOLT_AUTO_COMMIT-}" >> "$BD_LOG"
case "$1" in
  show)
    printf '[{"labels":["gt:agent","idle:2"]}]\n'
    ;;
  update)
    ;;
  *)
    printf 'unexpected bd command: %s\n' "$1" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", townBeads)
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "town")
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	oldSet := agentStateSet
	oldIncr := agentStateIncr
	oldDel := agentStateDel
	oldJSON := agentStateJSON
	t.Cleanup(func() {
		agentStateSet = oldSet
		agentStateIncr = oldIncr
		agentStateDel = oldDel
		agentStateJSON = oldJSON
	})
	agentStateSet = []string{"idle=0"}
	agentStateIncr = ""
	agentStateDel = nil
	agentStateJSON = false

	if err := runAgentState(nil, []string{"gt-gastown-refinery"}); err != nil {
		t.Fatalf("runAgentState() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := strings.TrimSpace(string(data))
	if log == "" {
		t.Fatal("fake bd was not invoked")
	}

	for _, line := range strings.Split(log, "\n") {
		if !strings.Contains(line, "BEADS_DIR="+rigBeads) {
			t.Fatalf("bd call was not pinned to rig beads %q: %s\nfull log:\n%s", rigBeads, line, log)
		}
		if strings.Contains(line, "BEADS_DIR="+townBeads) {
			t.Fatalf("bd call used inherited town BEADS_DIR %q: %s\nfull log:\n%s", townBeads, line, log)
		}
		if !strings.Contains(line, "DB=rigdb") || strings.Contains(line, "DB=town") {
			t.Fatalf("bd call was not pinned to rig database: %s\nfull log:\n%s", line, log)
		}
		if strings.Contains(line, "cmd=show") {
			if !strings.Contains(line, "READONLY=true") || !strings.Contains(line, "AUTO=off") {
				t.Fatalf("bd read was not read-only pinned: %s\nfull log:\n%s", line, log)
			}
		}
		if strings.Contains(line, "cmd=update") {
			if !strings.Contains(line, "READONLY= ") || !strings.Contains(line, "AUTO=on") {
				t.Fatalf("bd mutation was not mutation pinned: %s\nfull log:\n%s", line, log)
			}
		}
	}
}

func TestAgentStateAndAwaitCommandsRouteHQAgentBeadFromRigCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp := t.TempDir()
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")
	mayorDir := filepath.Join(townRoot, "mayor")

	for _, dir := range []string{townBeads, rigRedirect, rigBeads, mayorDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(mayorDir, "town.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	for path, database := range map[string]string{townBeads: "towndb", rigBeads: "rigdb"} {
		metadata := []byte(`{"dolt_database":"` + database + `","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`)
		if err := os.WriteFile(filepath.Join(path, "metadata.json"), metadata, 0o644); err != nil {
			t.Fatalf("write metadata: %v", err)
		}
	}

	binDir := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir bin: %v", err)
	}
	logPath := filepath.Join(tmp, "bd.log")
	bdScript := `#!/bin/sh
printf 'cmd=%s BEADS_DIR=%s DB=%s\n' "$1" "${BEADS_DIR-}" "${BEADS_DOLT_SERVER_DATABASE-}" >> "$BD_LOG"
case "$1" in
  list)
    case "$*" in
      *--ephemeral*) printf '[]\n' ;;
      *)
        if [ "${BEADS_DOLT_SERVER_DATABASE-}" = "towndb" ]; then
          printf '[{"id":"gt-gastown-refinery","status":"open","description":"role_type: refinery\\nrig: gastown","labels":["gt:agent"]}]\n'
        else
          printf '[]\n'
        fi
        ;;
    esac
    ;;
  show)
    if [ "${BEADS_DOLT_SERVER_DATABASE-}" = "towndb" ]; then
      printf '[{"id":"gt-gastown-refinery","labels":["gt:agent","idle:0"]}]\n'
    else
      printf 'no issue found\n' >&2
      exit 1
    fi
    ;;
  update)
    ;;
  *)
    printf 'unexpected bd command: %s\n' "$1" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(bdScript), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BD_LOG", logPath)
	t.Setenv("BEADS_DIR", rigBeads)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(rigWorkDir); err != nil {
		t.Fatalf("chdir rig work dir: %v", err)
	}

	oldSet, oldIncr, oldDel, oldStateJSON := agentStateSet, agentStateIncr, agentStateDel, agentStateJSON
	oldResolveRole, oldResolveRig := agentsResolveRole, agentsResolveRig
	oldResolveJSON, oldResolveQuiet := agentsResolveJSON, agentsResolveQuiet
	oldResolveOut := agentsResolveCmd.OutOrStdout()
	oldChannel, oldTimeout := awaitEventChannel, awaitEventTimeout
	oldBackoffBase, oldBackoffMax := awaitEventBackoffBase, awaitEventBackoffMax
	oldBackoffMult, oldQuiet := awaitEventBackoffMult, awaitEventQuiet
	oldAgentBead, oldCleanup := awaitEventAgentBead, awaitEventCleanup
	oldContextInterval, oldMoleculeJSON := awaitEventContextCheckInterval, moleculeJSON
	oldSignalTimeout := awaitSignalTimeout
	oldSignalBackoffBase, oldSignalBackoffMax := awaitSignalBackoffBase, awaitSignalBackoffMax
	oldSignalBackoffMult, oldSignalQuiet := awaitSignalBackoffMult, awaitSignalQuiet
	oldSignalAgentBead := awaitSignalAgentBead
	t.Cleanup(func() {
		agentStateSet, agentStateIncr, agentStateDel, agentStateJSON = oldSet, oldIncr, oldDel, oldStateJSON
		agentsResolveRole, agentsResolveRig = oldResolveRole, oldResolveRig
		agentsResolveJSON, agentsResolveQuiet = oldResolveJSON, oldResolveQuiet
		agentsResolveCmd.SetOut(oldResolveOut)
		awaitEventChannel, awaitEventTimeout = oldChannel, oldTimeout
		awaitEventBackoffBase, awaitEventBackoffMax = oldBackoffBase, oldBackoffMax
		awaitEventBackoffMult, awaitEventQuiet = oldBackoffMult, oldQuiet
		awaitEventAgentBead, awaitEventCleanup = oldAgentBead, oldCleanup
		awaitEventContextCheckInterval, moleculeJSON = oldContextInterval, oldMoleculeJSON
		awaitSignalTimeout = oldSignalTimeout
		awaitSignalBackoffBase, awaitSignalBackoffMax = oldSignalBackoffBase, oldSignalBackoffMax
		awaitSignalBackoffMult, awaitSignalQuiet = oldSignalBackoffMult, oldSignalQuiet
		awaitSignalAgentBead = oldSignalAgentBead
	})

	agentsResolveRole = "refinery"
	agentsResolveRig = "gastown"
	agentsResolveJSON = false
	agentsResolveQuiet = false
	var resolveOut bytes.Buffer
	agentsResolveCmd.SetOut(&resolveOut)
	if err := runAgentsResolve(agentsResolveCmd, nil); err != nil {
		t.Fatalf("runAgentsResolve() error = %v", err)
	}
	if got := strings.TrimSpace(resolveOut.String()); got != "gt-gastown-refinery" {
		t.Fatalf("runAgentsResolve() output = %q, want HQ agent bead ID", got)
	}

	agentStateSet = []string{"idle=0"}
	agentStateIncr = ""
	agentStateDel = nil
	agentStateJSON = false
	if err := runAgentState(nil, []string{"gt-gastown-refinery"}); err != nil {
		t.Fatalf("runAgentState() error = %v", err)
	}

	awaitEventChannel = "routing-test"
	awaitEventTimeout = "1ms"
	awaitEventBackoffBase = ""
	awaitEventBackoffMax = ""
	awaitEventBackoffMult = 2
	awaitEventQuiet = true
	awaitEventAgentBead = "gt-gastown-refinery"
	awaitEventCleanup = true
	awaitEventContextCheckInterval = ""
	moleculeJSON = false
	if err := runMoleculeAwaitEvent(nil, nil); err != nil {
		t.Fatalf("runMoleculeAwaitEvent() error = %v", err)
	}

	awaitSignalTimeout = "1ms"
	awaitSignalBackoffBase = ""
	awaitSignalBackoffMax = ""
	awaitSignalBackoffMult = 2
	awaitSignalQuiet = true
	awaitSignalAgentBead = "gt-gastown-witness"
	if err := runMoleculeAwaitSignal(nil, nil); err != nil {
		t.Fatalf("runMoleculeAwaitSignal() error = %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake bd log: %v", err)
	}
	log := string(data)
	if !strings.Contains(log, "cmd=show ") || !strings.Contains(log, "DB=rigdb") {
		t.Fatalf("expected a rig-local probe before HQ fallback; log:\n%s", log)
	}
	if !strings.Contains(log, "DB=towndb") {
		t.Fatalf("expected HQ agent bead reads; log:\n%s", log)
	}
	for _, line := range strings.Split(strings.TrimSpace(log), "\n") {
		if strings.HasPrefix(line, "cmd=update ") && !strings.Contains(line, "DB=towndb") {
			t.Fatalf("agent bead update escaped HQ routing: %s\nfull log:\n%s", line, log)
		}
	}
}
