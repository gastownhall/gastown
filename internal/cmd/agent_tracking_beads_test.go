package cmd

import (
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
  list)
    printf '[{"id":"gt-gastown-refinery","issue_type":"agent","status":"open","labels":["gt:agent","idle:2"],"description":"role_type: refinery\\nrig: gastown"}]\n'
    ;;
  mol)
    printf '{"wisps":[]}\n'
    ;;
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

func TestRunAgentStatePersistsTwoCyclesInTownFallbackLedger(t *testing.T) {
	townRoot, rigBeads := setupAgentLedgerTown(t, "ccs", "ccs")
	townBeads := filepath.Join(townRoot, ".beads")
	currentWorkDir := filepath.Join(townRoot, "other", "refinery", "rig")
	currentBeads := filepath.Join(currentWorkDir, ".beads")
	if err := os.MkdirAll(currentBeads, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := installAgentLedgerBD(t)
	t.Setenv("TEST_RIG_BEADS", rigBeads)
	t.Setenv("TEST_TOWN_BEADS", townBeads)
	t.Setenv("TEST_RIG_LIST", `[]`)
	townRecord := `[{"id":"ccs-witness","issue_type":"agent","labels":["gt:agent","idle:0"],"status":"open","description":"role_type: witness\nrig: ccs"}]`
	t.Setenv("TEST_TOWN_LIST", townRecord)
	t.Setenv("TEST_TOWN_SHOW", townRecord)

	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(oldWd) }()
	if err := os.Chdir(currentWorkDir); err != nil {
		t.Fatal(err)
	}

	oldSet, oldIncr, oldDel, oldJSON := agentStateSet, agentStateIncr, agentStateDel, agentStateJSON
	t.Cleanup(func() {
		agentStateSet, agentStateIncr, agentStateDel, agentStateJSON = oldSet, oldIncr, oldDel, oldJSON
	})
	agentStateIncr, agentStateDel, agentStateJSON = "", nil, false
	for _, cycle := range []string{"1", "2"} {
		agentStateSet = []string{"idle=" + cycle}
		if err := runAgentState(nil, []string{"ccs-witness"}); err != nil {
			t.Fatalf("runAgentState cycle %s: %v", cycle, err)
		}
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "cmd=update") {
			continue
		}
		updates++
		if !strings.Contains(line, "BEADS_DIR="+townBeads) {
			t.Fatalf("state update escaped town source ledger: %s", line)
		}
	}
	if updates != 2 {
		t.Fatalf("town state updates = %d, want 2; log:\n%s", updates, data)
	}
	if strings.Contains(string(data), "cmd=create") {
		t.Fatalf("state persistence created a patrol/identity wisp:\n%s", data)
	}
}
