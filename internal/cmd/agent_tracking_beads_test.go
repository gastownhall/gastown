package cmd

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveAgentTrackingBeadsDirUsesTownFromTownAndRigCwd(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
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

	for _, cwd := range []string{rigWorkDir, townRoot} {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("chdir %s: %v", cwd, err)
		}
		gotBeadsDir, err := resolveAgentTrackingBeadsDir()
		if err != nil {
			t.Fatalf("resolveAgentTrackingBeadsDir() from %s error = %v", cwd, err)
		}
		if gotBeadsDir != townBeads {
			t.Fatalf("resolveAgentTrackingBeadsDir() from %s = %q, want town %q", cwd, gotBeadsDir, townBeads)
		}
	}

	gotLocalWorkDir, err := findLocalBeadsDir()
	if err != nil {
		t.Fatalf("findLocalBeadsDir() error = %v", err)
	}
	if gotLocalWorkDir != townRoot {
		t.Fatalf("findLocalBeadsDir() = %q, want env parent %q", gotLocalWorkDir, townRoot)
	}
}

func TestRunAgentStateUsesTownBeadsDirFromTownAndRigCwd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	townRoot := filepath.Join(tmp, "gt")
	townBeads := filepath.Join(townRoot, ".beads")
	rigWorkDir := filepath.Join(townRoot, "gastown", "refinery", "rig")
	rigRedirect := filepath.Join(rigWorkDir, ".beads")
	rigBeads := filepath.Join(townRoot, "gastown", "mayor", "rig", ".beads")

	for _, dir := range []string{filepath.Join(townRoot, "mayor"), townBeads, rigRedirect, rigBeads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rigRedirect, "redirect"), []byte("../../mayor/rig/.beads"), 0o644); err != nil {
		t.Fatalf("write rig redirect: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte(`{"name":"test"}`), 0o644); err != nil {
		t.Fatalf("write town marker: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rigBeads, "metadata.json"), []byte(`{"dolt_database":"rigdb","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0o644); err != nil {
		t.Fatalf("write rig metadata: %v", err)
	}
	if err := os.WriteFile(filepath.Join(townBeads, "metadata.json"), []byte(`{"dolt_database":"town","dolt_server_host":"127.0.0.1","dolt_server_port":3307}`), 0o644); err != nil {
		t.Fatalf("write town metadata: %v", err)
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

	for _, cwd := range []string{rigWorkDir, townRoot} {
		if err := os.Chdir(cwd); err != nil {
			t.Fatalf("chdir %s: %v", cwd, err)
		}
		if err := runAgentState(nil, []string{"gt-gastown-refinery"}); err != nil {
			t.Fatalf("runAgentState() from %s error = %v", cwd, err)
		}
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
		if !strings.Contains(line, "BEADS_DIR="+townBeads) {
			t.Fatalf("bd call was not pinned to town beads %q: %s\nfull log:\n%s", townBeads, line, log)
		}
		if strings.Contains(line, "BEADS_DIR="+rigBeads) {
			t.Fatalf("bd call used rig BEADS_DIR %q: %s\nfull log:\n%s", rigBeads, line, log)
		}
		if !strings.Contains(line, "DB=town") || strings.Contains(line, "DB=rigdb") {
			t.Fatalf("bd call was not pinned to town database: %s\nfull log:\n%s", line, log)
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

func TestGetAllAgentLabelsRejectsNonzeroJSONError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell fake bd")
	}

	binDir := t.TempDir()
	script := `#!/bin/sh
printf '%s\n' '{"error":"no issues found matching the provided IDs","schema_version":1}'
printf '%s\n' 'Error fetching gt-missing: no issue found matching "gt-missing"' >&2
exit 1
`
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	labels, err := getAllAgentLabels("gt-missing", t.TempDir())
	if err == nil {
		t.Fatalf("getAllAgentLabels() = %v, nil; want nonzero bd exit to fail", labels)
	}
	if !strings.Contains(err.Error(), "agent bead not found") {
		t.Fatalf("getAllAgentLabels() error = %q, want not-found diagnostic", err)
	}
}
