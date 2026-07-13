package refinery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/rig"
	"github.com/steveyegge/gastown/internal/testutil"
)

func TestDefaultMergeQueueConfig(t *testing.T) {
	cfg := DefaultMergeQueueConfig()

	if !cfg.Enabled {
		t.Error("expected Enabled to be true by default")
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("expected PollInterval to be 30s, got %v", cfg.PollInterval)
	}
	if cfg.MaxConcurrent != 1 {
		t.Errorf("expected MaxConcurrent to be 1, got %d", cfg.MaxConcurrent)
	}
	if cfg.OnConflict != "assign_back" {
		t.Errorf("expected OnConflict to be 'assign_back', got %q", cfg.OnConflict)
	}
	if cfg.StaleClaimTimeout != DefaultStaleClaimTimeout {
		t.Errorf("expected StaleClaimTimeout to be %v, got %v", DefaultStaleClaimTimeout, cfg.StaleClaimTimeout)
	}
	if !cfg.AutoPush {
		t.Error("expected AutoPush to be true by default")
	}
}

func TestIsConflictTaskForMR(t *testing.T) {
	task := &beads.Issue{Description: `Resolve merge conflicts for branch polecat/nux/gt-real

## Metadata
- Original MR: gt-mr1
- Branch: polecat/nux/gt-real
- Conflict with: main@abc123
- Original issue: gt-real
- Retry count: 1`}

	if !isConflictTaskForMR(task, "gt-mr1", "gt-real") {
		t.Fatal("expected task metadata to verify")
	}
	if isConflictTaskForMR(task, "gt-other", "gt-real") {
		t.Fatal("task verified for wrong MR")
	}
	if isConflictTaskForMR(task, "gt-mr1", "gt-other") {
		t.Fatal("task verified for wrong source issue")
	}
	if isConflictTaskForMR(task, "gt-mr", "gt-real") {
		t.Fatal("task verified for MR prefix")
	}
	if isConflictTaskForMR(task, "gt-mr1", "gt-rea") {
		t.Fatal("task verified for source issue prefix")
	}
}

func TestEngineerFirstOpenBlockerUsesDependencySemantics(t *testing.T) {
	e := &Engineer{}
	tests := []struct {
		name  string
		issue *beads.Issue
		want  string
	}{
		{
			name:  "open blocking dependency blocks",
			issue: &beads.Issue{Dependencies: []beads.IssueDep{{ID: "gt-blocker", Status: "open", DependencyType: "blocks"}}},
			want:  "gt-blocker",
		},
		{
			name:  "external blocker ID is normalized",
			issue: &beads.Issue{Dependencies: []beads.IssueDep{{ID: "external:gt:gt-blocker", Status: "open", DependencyType: "waits-for"}}},
			want:  "gt-blocker",
		},
		{
			name:  "closed blocking dependency is resolved",
			issue: &beads.Issue{Dependencies: []beads.IssueDep{{ID: "gt-closed", Status: "closed", DependencyType: "blocks"}}},
		},
		{
			name:  "tombstone blocking dependency is resolved",
			issue: &beads.Issue{Dependencies: []beads.IssueDep{{ID: "gt-tombstone", Status: "tombstone", DependencyType: "blocks"}}},
		},
		{
			name:  "closed merge-block without merge reason still blocks",
			issue: &beads.Issue{Dependencies: []beads.IssueDep{{ID: "gt-closed-only", Status: "closed", DependencyType: "merge-blocks"}}},
			want:  "gt-closed-only",
		},
		{
			name:  "merged merge-block is resolved",
			issue: &beads.Issue{Dependencies: []beads.IssueDep{{ID: "gt-merged", Status: "closed", DependencyType: "merge-blocks", CloseReason: "Merged in gt-wisp"}}},
		},
		{
			name:  "raw blocked_by fallback uses shared normalization",
			issue: &beads.Issue{BlockedBy: []string{"external:gt:gt-raw-blocker"}},
			want:  "gt-raw-blocker",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := e.firstOpenBlocker(tt.issue); got != tt.want {
				t.Fatalf("firstOpenBlocker() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEngineerTerminalCloseClearsAgentActiveMRUsesTownBeadsDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell script mock for bd")
	}

	townRoot := t.TempDir()
	rigName := "gastown"
	rigPath := filepath.Join(townRoot, rigName)
	mayorRig := filepath.Join(rigPath, "mayor", "rig")
	townBeadsDir := filepath.Join(townRoot, ".beads")
	rigBeadsDir := filepath.Join(mayorRig, ".beads")

	for _, dir := range []string{
		filepath.Join(townRoot, "mayor"),
		townBeadsDir,
		rigBeadsDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatalf("write town.json: %v", err)
	}
	if err := beads.WriteRoutes(townBeadsDir, []beads.Route{
		{Prefix: "hq-", Path: "."},
		{Prefix: "gt-", Path: filepath.Join(rigName, "mayor", "rig")},
	}); err != nil {
		t.Fatalf("write routes: %v", err)
	}

	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := fmt.Sprintf(`#!/bin/sh
LOG=%q
printf 'env=%%s args=%%s\n' "${BEADS_DIR:-<unset>}" "$*" >> "$LOG"
cmd=""
for arg in "$@"; do
  case "$arg" in
    --*) ;;
    *) cmd="$arg"; break ;;
  esac
done
case "$cmd" in
	version|update|close)
		exit 0
		;;
	show)
		case "$*" in
			*gt-wisp-mr*)
				printf '%%s\n' '[{"id":"gt-wisp-mr","title":"MR","issue_type":"task","labels":["gt:merge-request"],"status":"open","description":"branch: polecat/rust/gt-test\nsource_issue: gt-test\nagent_bead: gt-gastown-polecat-rust"}]'
				;;
			*)
				printf '%%s\n' '[{"id":"gt-gastown-polecat-rust","title":"gt-gastown-polecat-rust","issue_type":"task","labels":["gt:agent"],"status":"open","description":"role_type: polecat\nrig: gastown\nagent_state: idle\nactive_mr: gt-wisp-mr"}]'
				;;
		esac
		exit 0
		;;
	*)
    exit 0
    ;;
esac
`, logPath)
	if err := os.WriteFile(filepath.Join(binDir, "bd"), []byte(script), 0755); err != nil {
		t.Fatalf("write mock bd: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := &Engineer{
		rig:    &rig.Rig{Name: rigName, Path: rigPath},
		beads:  beads.NewWithBeadsDir(mayorRig, rigBeadsDir),
		output: io.Discard,
	}
	if err := e.closeMRWithReason(&MRInfo{ID: "gt-wisp-mr", AgentBead: "gt-gastown-polecat-rust"}, "rejected: test"); err != nil {
		t.Fatalf("closeMRWithReason: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read mock log: %v", err)
	}
	logOutput := string(logBytes)
	for _, line := range strings.Split(strings.TrimSpace(logOutput), "\n") {
		if strings.Contains(line, "gt-gastown-polecat-rust") && strings.Contains(line, "env="+rigBeadsDir) {
			t.Fatalf("refinery active_mr cleanup used rig BEADS_DIR; log:\n%s", logOutput)
		}
	}
	if !strings.Contains(logOutput, "env="+townBeadsDir+" args=show gt-gastown-polecat-rust") || !strings.Contains(logOutput, "env="+townBeadsDir+" args=update gt-gastown-polecat-rust") {
		t.Fatalf("refinery active_mr cleanup did not use town BEADS_DIR; log:\n%s", logOutput)
	}
}

func TestEngineerCloseMRWithReasonRecordsMergeCommitAndClearsActiveMR(t *testing.T) {
	e, b, mrIssue, agentIssue, _ := setupEngineerTerminalCloseTest(t, "gt-wisp-old")

	if err := e.closeMRWithReason(&MRInfo{ID: mrIssue.ID, AgentBead: agentIssue.ID}, string(CloseReasonMerged), "abc123"); err != nil {
		t.Fatalf("closeMRWithReason: %v", err)
	}

	assertIssueStatus(t, b, mrIssue.ID, string(beads.StatusClosed))
	assertAgentActiveMR(t, b, agentIssue.ID, "")
	assertMRCloseReason(t, b, mrIssue.ID, string(CloseReasonMerged))
	issue, err := b.Show(mrIssue.ID)
	if err != nil {
		t.Fatalf("show MR %s: %v", mrIssue.ID, err)
	}
	fields := beads.ParseMRFields(issue)
	if fields.MergeCommit != "abc123" {
		t.Fatalf("MR merge_commit = %q, want abc123", fields.MergeCommit)
	}
}

func TestEngineerCloseMRWithReasonRejectsAndClearsMatchingActiveMR(t *testing.T) {
	e, b, mrIssue, agentIssue, srcIssue := setupEngineerTerminalCloseTest(t, "gt-wisp-old")

	if err := e.closeMRWithReason(&MRInfo{ID: mrIssue.ID, AgentBead: agentIssue.ID}, "rejected: policy failed"); err != nil {
		t.Fatalf("closeMRWithReason: %v", err)
	}

	assertIssueStatus(t, b, mrIssue.ID, string(beads.StatusClosed))
	assertIssueStatus(t, b, srcIssue.ID, string(beads.StatusOpen))
	assertAgentActiveMR(t, b, agentIssue.ID, "")
	assertMRCloseReason(t, b, mrIssue.ID, string(CloseReasonRejected))
}

func TestEngineerCloseMRWithReasonAlreadyTerminalRetriesActiveMRCleanup(t *testing.T) {
	e, b, mrIssue, agentIssue, _ := setupEngineerTerminalCloseTest(t, "gt-wisp-old")
	issue, err := b.Show(mrIssue.ID)
	if err != nil {
		t.Fatalf("show MR %s: %v", mrIssue.ID, err)
	}
	fields := beads.ParseMRFields(issue)
	fields.CloseReason = string(CloseReasonRejected)
	desc := beads.SetMRFields(issue, fields)
	if err := b.Update(mrIssue.ID, beads.UpdateOptions{Description: &desc}); err != nil {
		t.Fatalf("record close_reason: %v", err)
	}
	if err := b.CloseWithReason("rejected: policy failed", mrIssue.ID); err != nil {
		t.Fatalf("close MR issue: %v", err)
	}

	if err := e.closeMRWithReason(&MRInfo{ID: mrIssue.ID, AgentBead: agentIssue.ID}, "rejected: policy failed"); err != nil {
		t.Fatalf("closeMRWithReason retry: %v", err)
	}

	assertAgentActiveMR(t, b, agentIssue.ID, "")
}

func TestEngineerCloseMRWithReasonDoesNotClearNewerActiveMR(t *testing.T) {
	e, b, mrIssue, agentIssue, _ := setupEngineerTerminalCloseTest(t, "gt-wisp-newer")

	if err := e.closeMRWithReason(&MRInfo{ID: mrIssue.ID, AgentBead: agentIssue.ID}, "rejected: policy failed"); err != nil {
		t.Fatalf("closeMRWithReason: %v", err)
	}

	assertAgentActiveMR(t, b, agentIssue.ID, "gt-wisp-newer")
}

func TestEngineerCloseMRWithReasonNormalizesSuperseded(t *testing.T) {
	e, b, mrIssue, agentIssue, _ := setupEngineerTerminalCloseTest(t, "gt-wisp-old")

	if err := e.closeMRWithReason(&MRInfo{ID: mrIssue.ID, AgentBead: agentIssue.ID}, "superseded by gt-wisp-new"); err != nil {
		t.Fatalf("closeMRWithReason: %v", err)
	}

	assertAgentActiveMR(t, b, agentIssue.ID, "")
	assertMRCloseReason(t, b, mrIssue.ID, string(CloseReasonSuperseded))
}

func setupEngineerTerminalCloseTest(t *testing.T, activeMR string) (*Engineer, *beads.Beads, *beads.Issue, *beads.Issue, *beads.Issue) {
	t.Helper()
	testutil.RequireDoltContainer(t)
	port, _ := strconv.Atoi(testutil.DoltContainerPort())
	rigPath := t.TempDir()
	b := beads.NewIsolatedWithPort(rigPath, port)
	if err := b.Init("gt"); err != nil {
		t.Skipf("bd init unavailable: %v", err)
	}

	srcIssue, err := b.Create(beads.CreateOptions{Title: "Implement feature X", Labels: []string{"gt:task"}})
	if err != nil {
		t.Fatalf("create source issue: %v", err)
	}
	agentIssue, err := b.Create(beads.CreateOptions{
		Title:       "Polecat nux",
		Labels:      []string{"gt:agent"},
		Description: "role_type: polecat\nrig: testrig\nagent_state: working\nactive_mr: " + activeMR,
	})
	if err != nil {
		t.Fatalf("create agent issue: %v", err)
	}
	mrIssue, err := b.Create(beads.CreateOptions{
		Title:       "MR for feature X",
		Labels:      []string{"gt:merge-request"},
		Description: "branch: polecat/test/gt-xyz\nsource_issue: " + srcIssue.ID + "\nworker: test\ntarget: main\nagent_bead: " + agentIssue.ID,
	})
	if err != nil {
		t.Fatalf("create MR issue: %v", err)
	}
	if activeMR == "gt-wisp-old" {
		if err := b.UpdateAgentActiveMR(agentIssue.ID, mrIssue.ID); err != nil {
			t.Fatalf("set active_mr: %v", err)
		}
	}

	e := &Engineer{
		rig:    &rig.Rig{Name: "testrig", Path: rigPath},
		beads:  b,
		output: io.Discard,
	}
	return e, b, mrIssue, agentIssue, srcIssue
}

func TestEngineer_LoadConfig_NoFile(t *testing.T) {
	// Create a temp directory without config.json
	tmpDir, err := os.MkdirTemp("", "engineer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	r := &rig.Rig{
		Name: "test-rig",
		Path: tmpDir,
	}

	e := NewEngineer(r)

	// Should not error with missing config file
	if err := e.LoadConfig(); err != nil {
		t.Errorf("unexpected error with missing config: %v", err)
	}

	// Should use defaults
	if e.config.PollInterval != 30*time.Second {
		t.Errorf("expected default PollInterval, got %v", e.config.PollInterval)
	}
}

func TestEngineer_LoadConfig_WithMergeQueue(t *testing.T) {
	// Create a temp directory with config.json
	tmpDir, err := os.MkdirTemp("", "engineer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Write config file
	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"enabled":             true,
			"poll_interval":       "10s",
			"max_concurrent":      2,
			"run_tests":           false,
			"test_command":        "make test",
			"stale_claim_timeout": "1h",
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: tmpDir,
	}

	e := NewEngineer(r)

	if err := e.LoadConfig(); err != nil {
		t.Errorf("unexpected error loading config: %v", err)
	}

	// Check that config values were loaded
	if e.config.PollInterval != 10*time.Second {
		t.Errorf("expected PollInterval 10s, got %v", e.config.PollInterval)
	}
	if e.config.MaxConcurrent != 2 {
		t.Errorf("expected MaxConcurrent 2, got %d", e.config.MaxConcurrent)
	}
	if e.config.RunTests != false {
		t.Errorf("expected RunTests false, got %v", e.config.RunTests)
	}
	if e.config.TestCommand != "make test" {
		t.Errorf("expected TestCommand 'make test', got %q", e.config.TestCommand)
	}
	if e.config.StaleClaimTimeout != 1*time.Hour {
		t.Errorf("expected StaleClaimTimeout 1h, got %v", e.config.StaleClaimTimeout)
	}

	// Check that defaults are preserved for unspecified fields
	if e.config.OnConflict != "assign_back" {
		t.Errorf("expected OnConflict default 'assign_back', got %q", e.config.OnConflict)
	}
	// auto_push not set in config — default (true) should be preserved
	if !e.config.AutoPush {
		t.Error("expected AutoPush default true when not in config")
	}
}

func TestEngineer_LoadConfig_AutoPushDisabled(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engineer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
		"merge_queue": map[string]interface{}{
			"auto_push": false,
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: tmpDir,
	}

	e := NewEngineer(r)
	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if e.config.AutoPush {
		t.Error("expected AutoPush to be false when explicitly disabled in config")
	}
}

func TestEngineer_LoadConfig_NoMergeQueueSection(t *testing.T) {
	// Create a temp directory with config.json without merge_queue
	tmpDir, err := os.MkdirTemp("", "engineer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Write config file without merge_queue
	config := map[string]interface{}{
		"type":    "rig",
		"version": 1,
		"name":    "test-rig",
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: tmpDir,
	}

	e := NewEngineer(r)

	if err := e.LoadConfig(); err != nil {
		t.Errorf("unexpected error loading config: %v", err)
	}

	// Should use all defaults
	if e.config.PollInterval != 30*time.Second {
		t.Errorf("expected default PollInterval, got %v", e.config.PollInterval)
	}
}

func TestEngineer_LoadConfig_InvalidPollInterval(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engineer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	config := map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"poll_interval": "not-a-duration",
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "test-rig",
		Path: tmpDir,
	}

	e := NewEngineer(r)

	err = e.LoadConfig()
	if err == nil {
		t.Error("expected error for invalid poll_interval")
	}
}

func TestEngineer_LoadConfig_InvalidStaleClaimTimeout(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engineer-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name    string
		timeout string
	}{
		{"not a duration", "not-a-duration"},
		{"zero", "0s"},
		{"negative", "-5m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]interface{}{
				"merge_queue": map[string]interface{}{
					"stale_claim_timeout": tt.timeout,
				},
			}

			data, _ := json.MarshalIndent(config, "", "  ")
			if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
				t.Fatal(err)
			}

			r := &rig.Rig{
				Name: "test-rig",
				Path: tmpDir,
			}

			e := NewEngineer(r)

			err := e.LoadConfig()
			if err == nil {
				t.Errorf("expected error for stale_claim_timeout %q", tt.timeout)
			}
		})
	}
}

func TestNewEngineer(t *testing.T) {
	r := &rig.Rig{
		Name: "test-rig",
		Path: "/tmp/test-rig",
	}

	e := NewEngineer(r)

	if e.rig != r {
		t.Error("expected rig to be set")
	}
	if e.beads == nil {
		t.Error("expected beads client to be initialized")
	}
	if e.git == nil {
		t.Error("expected git client to be initialized")
	}
	if e.config == nil {
		t.Error("expected config to be initialized with defaults")
	}
}

func TestEngineer_LoadConfig_WithGates(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engineer-gates-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	config := map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"gates": map[string]interface{}{
				"test": map[string]interface{}{
					"cmd":     "go test ./...",
					"timeout": "5m",
				},
				"lint": map[string]interface{}{
					"cmd":     "golangci-lint run",
					"timeout": "2m",
				},
				"build": map[string]interface{}{
					"cmd": "go build ./...",
				},
			},
			"gates_parallel": true,
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)

	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error loading config: %v", err)
	}

	if len(e.config.Gates) != 3 {
		t.Fatalf("expected 3 gates, got %d", len(e.config.Gates))
	}
	if e.config.Gates["test"].Cmd != "go test ./..." {
		t.Errorf("expected test gate cmd 'go test ./...', got %q", e.config.Gates["test"].Cmd)
	}
	if e.config.Gates["test"].Timeout != 5*time.Minute {
		t.Errorf("expected test gate timeout 5m, got %v", e.config.Gates["test"].Timeout)
	}
	if e.config.Gates["lint"].Timeout != 2*time.Minute {
		t.Errorf("expected lint gate timeout 2m, got %v", e.config.Gates["lint"].Timeout)
	}
	if e.config.Gates["build"].Timeout != 0 {
		t.Errorf("expected build gate timeout 0 (no timeout), got %v", e.config.Gates["build"].Timeout)
	}
	if !e.config.GatesParallel {
		t.Error("expected gates_parallel to be true")
	}
}

func TestEngineer_LoadConfig_GateInvalidTimeout(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "engineer-gates-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name    string
		timeout string
	}{
		{"not a duration", "not-a-duration"},
		{"negative", "-5m"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := map[string]interface{}{
				"merge_queue": map[string]interface{}{
					"gates": map[string]interface{}{
						"bad": map[string]interface{}{
							"cmd":     "echo test",
							"timeout": tt.timeout,
						},
					},
				},
			}

			data, _ := json.MarshalIndent(config, "", "  ")
			if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
				t.Fatal(err)
			}

			r := &rig.Rig{Name: "test-rig", Path: tmpDir}
			e := NewEngineer(r)

			err := e.LoadConfig()
			if err == nil {
				t.Errorf("expected error for gate timeout %q", tt.timeout)
			}
		})
	}
}

func TestEngineer_LoadConfig_GatePhase(t *testing.T) {
	tmpDir := t.TempDir()

	config := map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"gates": map[string]interface{}{
				"lint": map[string]interface{}{
					"cmd": "golangci-lint run",
				},
				"test": map[string]interface{}{
					"cmd":   "go test ./...",
					"phase": "pre-merge",
				},
				"build-check": map[string]interface{}{
					"cmd":   "go build ./...",
					"phase": "post-squash",
				},
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)

	if err := e.LoadConfig(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// lint has no phase — should default to pre-merge
	if e.config.Gates["lint"].Phase != GatePhasePreMerge {
		t.Errorf("lint phase = %q, want %q", e.config.Gates["lint"].Phase, GatePhasePreMerge)
	}
	if e.config.Gates["test"].Phase != GatePhasePreMerge {
		t.Errorf("test phase = %q, want %q", e.config.Gates["test"].Phase, GatePhasePreMerge)
	}
	if e.config.Gates["build-check"].Phase != GatePhasePostSquash {
		t.Errorf("build-check phase = %q, want %q", e.config.Gates["build-check"].Phase, GatePhasePostSquash)
	}
}

func TestEngineer_LoadConfig_GateInvalidPhase(t *testing.T) {
	tmpDir := t.TempDir()

	config := map[string]interface{}{
		"merge_queue": map[string]interface{}{
			"gates": map[string]interface{}{
				"bad": map[string]interface{}{
					"cmd":   "echo test",
					"phase": "during-lunch",
				},
			},
		},
	}

	data, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(tmpDir, "config.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{Name: "test-rig", Path: tmpDir}
	e := NewEngineer(r)

	err := e.LoadConfig()
	if err == nil {
		t.Fatal("expected error for invalid phase")
	}
	if !strings.Contains(err.Error(), "invalid phase") {
		t.Errorf("error = %q, want substring 'invalid phase'", err.Error())
	}
}

func TestRunGatesForPhase_FiltersCorrectly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses Unix shell commands")
	}

	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	e.config.Gates = map[string]*GateConfig{
		"pre-lint":   {Cmd: "true", Phase: GatePhasePreMerge},
		"pre-test":   {Cmd: "true", Phase: GatePhasePreMerge},
		"post-build": {Cmd: "true", Phase: GatePhasePostSquash},
	}

	// Pre-merge phase should only run pre-lint and pre-test
	preResult := e.runGatesForPhase(context.Background(), GatePhasePreMerge)
	if !preResult.Success {
		t.Errorf("pre-merge gates failed: %s", preResult.Error)
	}

	// Post-squash phase should only run post-build
	postResult := e.runGatesForPhase(context.Background(), GatePhasePostSquash)
	if !postResult.Success {
		t.Errorf("post-squash gates failed: %s", postResult.Error)
	}
}

func TestRunGate_Success(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()

	result := e.runGate(context.Background(), "echo-test", &GateConfig{
		Cmd: "echo hello",
	})

	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if result.Name != "echo-test" {
		t.Errorf("expected name 'echo-test', got %q", result.Name)
	}
}

func TestRunGate_Failure(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()

	result := e.runGate(context.Background(), "fail-test", &GateConfig{
		Cmd: "exit 1",
	})

	if result.Success {
		t.Error("expected failure")
	}
	if result.Name != "fail-test" {
		t.Errorf("expected name 'fail-test', got %q", result.Name)
	}
}

func TestRunGate_EmptyCmd(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()

	result := e.runGate(context.Background(), "empty", &GateConfig{
		Cmd: "",
	})

	if result.Success {
		t.Error("expected failure for empty cmd")
	}
}

func TestRunGate_Timeout(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()

	result := e.runGate(context.Background(), "slow", &GateConfig{
		Cmd:     "sleep 10",
		Timeout: 100 * time.Millisecond,
	})

	if result.Success {
		t.Error("expected timeout failure")
	}
	if !strings.Contains(result.Error, "timed out") {
		t.Errorf("expected timeout error, got: %s", result.Error)
	}
}

func TestRunGates_Sequential_AllPass(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	e.config.Gates = map[string]*GateConfig{
		"a": {Cmd: "true"},
		"b": {Cmd: "true"},
		"c": {Cmd: "true"},
	}
	e.config.GatesParallel = false

	result := e.runGates(context.Background())
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
}

func TestRunGates_Sequential_StopsOnFirstFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("gate commands run via sh -c; touch with Windows paths breaks under MSYS2 shell")
	}
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard

	// Create a marker file to track which gates ran
	markerDir := t.TempDir()
	e.config.Gates = map[string]*GateConfig{
		"a_pass": {Cmd: fmt.Sprintf("touch %s/a", markerDir)},
		"b_fail": {Cmd: "exit 1"},
		"c_skip": {Cmd: fmt.Sprintf("touch %s/c", markerDir)},
	}
	e.config.GatesParallel = false

	result := e.runGates(context.Background())
	if result.Success {
		t.Error("expected failure")
	}

	// Gate "a_pass" should have run
	if _, err := os.Stat(filepath.Join(markerDir, "a")); os.IsNotExist(err) {
		t.Error("gate 'a_pass' should have run")
	}
	// Gate "c_skip" should NOT have run (stopped after b_fail)
	if _, err := os.Stat(filepath.Join(markerDir, "c")); !os.IsNotExist(err) {
		t.Error("gate 'c_skip' should not have run after failure")
	}
}

func TestRunGates_Parallel_AllPass(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	e.config.Gates = map[string]*GateConfig{
		"a": {Cmd: "true"},
		"b": {Cmd: "true"},
		"c": {Cmd: "true"},
	}
	e.config.GatesParallel = true

	result := e.runGates(context.Background())
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
}

func TestRunGates_Parallel_AnyFailure(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	e.config.Gates = map[string]*GateConfig{
		"pass1": {Cmd: "true"},
		"fail1": {Cmd: "exit 1"},
		"pass2": {Cmd: "true"},
	}
	e.config.GatesParallel = true

	result := e.runGates(context.Background())
	if result.Success {
		t.Error("expected failure when any gate fails")
	}
	if !result.TestsFailed {
		t.Error("expected TestsFailed to be true")
	}
	if !strings.Contains(result.Error, "fail1") {
		t.Errorf("expected error to mention 'fail1', got: %s", result.Error)
	}
}

func TestRunGates_Empty(t *testing.T) {
	r := &rig.Rig{Name: "test-rig", Path: t.TempDir()}
	e := NewEngineer(r)
	e.workDir = t.TempDir()
	e.output = io.Discard
	e.config.Gates = nil

	result := e.runGates(context.Background())
	if !result.Success {
		t.Error("expected success with no gates configured")
	}
}

func TestEngineer_DeleteMergedBranchesConfig(t *testing.T) {
	// Test that DeleteMergedBranches is true by default
	cfg := DefaultMergeQueueConfig()
	if !cfg.DeleteMergedBranches {
		t.Error("expected DeleteMergedBranches to be true by default")
	}
}

func TestPolecatBranchAlwaysDeletedAfterMerge(t *testing.T) {
	// Polecat branches should be cleaned up regardless of DeleteMergedBranches config.
	// Non-polecat branches should only be deleted locally, never from the remote,
	// because the remote may be a contributor's fork with open upstream PRs. (GH#2669)
	tests := []struct {
		name                 string
		branch               string
		deleteMergedBranches bool
		wantLocalDelete      bool
		wantRemoteDelete     bool
	}{
		{"polecat branch with config true", "polecat/nux/gt-abc", true, true, true},
		{"polecat branch with config false", "polecat/nux/gt-abc", false, true, true},
		{"non-polecat branch with config true", "feature/my-thing", true, true, false},
		{"non-polecat branch with config false", "feature/my-thing", false, false, false},
		{"empty branch", "", false, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isPolecat := strings.HasPrefix(tt.branch, "polecat/")
			shouldDeleteLocal := tt.branch != "" && (tt.deleteMergedBranches || isPolecat)
			shouldDeleteRemote := tt.branch != "" && isPolecat
			if shouldDeleteLocal != tt.wantLocalDelete {
				t.Errorf("branch=%q deleteMerged=%v: got localDelete=%v, want %v",
					tt.branch, tt.deleteMergedBranches, shouldDeleteLocal, tt.wantLocalDelete)
			}
			if shouldDeleteRemote != tt.wantRemoteDelete {
				t.Errorf("branch=%q deleteMerged=%v: got remoteDelete=%v, want %v",
					tt.branch, tt.deleteMergedBranches, shouldDeleteRemote, tt.wantRemoteDelete)
			}
		})
	}
}

func TestPostMergeConvoyCheck_NoTownBeads(t *testing.T) {
	// postMergeConvoyCheck should silently return when town-level beads doesn't exist
	tmpDir, err := os.MkdirTemp("", "engineer-convoy-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create rig dir as a subdirectory of the "town root"
	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "testrig",
		Path: rigDir,
	}

	e := NewEngineer(r)
	var buf bytes.Buffer
	e.SetOutput(&buf)

	// Call with a nil-safe MR — should not panic
	mr := &MRInfo{
		ID:          "gt-test",
		SourceIssue: "gt-src",
		ConvoyID:    "hq-cv-abc",
	}
	e.postMergeConvoyCheck(mr)

	// Should produce no output (town .beads doesn't exist)
	if buf.Len() != 0 {
		t.Errorf("expected no output when town beads missing, got: %s", buf.String())
	}
}

func TestCheckAndCloseCompletedConvoys_UsesHardenedBDEnvs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	tmpDir := t.TempDir()
	townBeads := filepath.Join(tmpDir, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatal(err)
	}

	rigDir := filepath.Join(tmpDir, "l9")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	bdPath := filepath.Join(binDir, "bd")
	script := `#!/bin/sh
assert_town_read_env() {
  if [ "$BEADS_DIR" != "$TOWN_BEADS" ]; then
    echo "BEADS_DIR = $BEADS_DIR, want $TOWN_BEADS" >&2
    exit 1
  fi
  if [ "$BD_READONLY" != "true" ] || [ "$BD_DOLT_AUTO_COMMIT" != "off" ]; then
    echo "read env not read-only: BD_READONLY=$BD_READONLY BD_DOLT_AUTO_COMMIT=$BD_DOLT_AUTO_COMMIT" >&2
    exit 1
  fi
  if [ -n "$BEADS_DOLT_SERVER_DATABASE" ] || [ -n "$BEADS_DB" ] || [ -n "$BD_DB" ] || [ -n "$BEADS_DOLT_DATA_DIR" ]; then
    echo "stale target env leaked" >&2
    exit 1
  fi
}
assert_town_write_env() {
  if [ "$BEADS_DIR" != "$TOWN_BEADS" ]; then
    echo "BEADS_DIR = $BEADS_DIR, want $TOWN_BEADS" >&2
    exit 1
  fi
  if [ -n "$BD_READONLY" ] || [ "$BD_DOLT_AUTO_COMMIT" != "on" ]; then
    echo "write env not mutable: BD_READONLY=$BD_READONLY BD_DOLT_AUTO_COMMIT=$BD_DOLT_AUTO_COMMIT" >&2
    exit 1
  fi
}
assert_routing_read_env() {
  if [ -n "$BEADS_DIR" ] || [ -n "$BEADS_DOLT_SERVER_DATABASE" ]; then
    echo "routing env pinned target: BEADS_DIR=$BEADS_DIR DB=$BEADS_DOLT_SERVER_DATABASE" >&2
    exit 1
  fi
  if [ "$BD_READONLY" != "true" ] || [ "$BD_DOLT_AUTO_COMMIT" != "off" ]; then
    echo "routing read env not read-only: BD_READONLY=$BD_READONLY BD_DOLT_AUTO_COMMIT=$BD_DOLT_AUTO_COMMIT" >&2
    exit 1
  fi
}
case "$*" in
  "--allow-stale version")
    exit 0
    ;;
	  "--allow-stale list --status=open --json --limit=0 --flat"|"list --status=open --json --limit=0 --flat")
	assert_town_read_env
	    echo '[{"id":"hq-cv-l9","title":"Cross-rig convoy","status":"open","description":"","issue_type":"convoy"}]'
    ;;
  "--allow-stale dep list hq-cv-l9 --direction=down --type=tracks --json"|"dep list hq-cv-l9 --direction=down --type=tracks --json")
    assert_town_read_env
    echo '[{"id":"external:l9:l9-123","status":"closed"}]'
    ;;
  "--allow-stale show l9-123 --json"|"show l9-123 --json")
    assert_routing_read_env
    echo '[{"status":"closed"}]'
    ;;
  "--allow-stale close hq-cv-l9 -r All tracked issues completed"|"close hq-cv-l9 -r All tracked issues completed")
    assert_town_write_env
    exit 0
    ;;
  *)
    echo "unexpected bd args: $*" >&2
    exit 1
    ;;
esac
`
	if err := os.WriteFile(bdPath, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TOWN_BEADS", townBeads)
	t.Setenv("BEADS_DIR", "/wrong/.beads")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "wrong")
	t.Setenv("BEADS_DB", "/wrong.db")
	t.Setenv("BD_DB", "/wrong.bd")
	t.Setenv("BEADS_DOLT_DATA_DIR", "/wrong/data")
	t.Setenv("BD_READONLY", "true")
	t.Setenv("BD_DOLT_AUTO_COMMIT", "off")

	e := NewEngineer(&rig.Rig{
		Name: "l9",
		Path: rigDir,
	})

	closed := e.checkAndCloseCompletedConvoys(tmpDir, townBeads)
	if len(closed) != 1 {
		t.Fatalf("expected 1 closed convoy, got %d", len(closed))
	}
	if closed[0].ID != "hq-cv-l9" {
		t.Fatalf("closed convoy ID = %q, want hq-cv-l9", closed[0].ID)
	}
}

func TestNotifyDeaconConvoyFeeding_SkipsWhenNoConvoyID(t *testing.T) {
	// notifyDeaconConvoyFeeding should skip when MR has no ConvoyID
	tmpDir, err := os.MkdirTemp("", "engineer-notify-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "testrig",
		Path: rigDir,
	}

	e := NewEngineer(r)
	var buf bytes.Buffer
	e.SetOutput(&buf)

	// MR without ConvoyID — should produce no output
	mr := &MRInfo{
		ID:          "gt-test",
		SourceIssue: "gt-src",
		ConvoyID:    "", // No convoy
	}
	e.notifyDeaconConvoyFeeding(mr)

	if buf.Len() != 0 {
		t.Errorf("expected no output when ConvoyID empty, got: %s", buf.String())
	}
}

func TestNotifyDeaconConvoyFeeding_AttemptsWhenConvoyID(t *testing.T) {
	// notifyDeaconConvoyFeeding should attempt to send mail when ConvoyID is set.
	// The send will fail (no beads setup in tmpdir) but we verify the attempt via output.
	tmpDir, err := os.MkdirTemp("", "engineer-notify-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	rigDir := filepath.Join(tmpDir, "testrig")
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatal(err)
	}

	r := &rig.Rig{
		Name: "testrig",
		Path: rigDir,
	}

	e := NewEngineer(r)
	var buf bytes.Buffer
	e.SetOutput(&buf)

	mr := &MRInfo{
		ID:          "gt-test",
		SourceIssue: "gt-src",
		ConvoyID:    "hq-cv-abc",
	}
	e.notifyDeaconConvoyFeeding(mr)

	output := buf.String()
	// Should have attempted to send — either success or warning about failure
	if !strings.Contains(output, "CONVOY_NEEDS_FEEDING") && !strings.Contains(output, "convoy feeding") {
		t.Errorf("expected output mentioning convoy notification, got: %s", output)
	}
}

func TestConvoyInfoDescriptionParsing(t *testing.T) {
	// Test that landConvoySwarm correctly parses Molecule from description
	tests := []struct {
		name        string
		description string
		wantMolID   string
	}{
		{
			name:        "with molecule",
			description: "Convoy tracking 2 issues\nOwner: mayor/\nMolecule: mol-release",
			wantMolID:   "mol-release",
		},
		{
			name:        "without molecule",
			description: "Convoy tracking 2 issues\nOwner: mayor/",
			wantMolID:   "",
		},
		{
			name:        "empty description",
			description: "",
			wantMolID:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := beads.ParseConvoyFields(&beads.Issue{Description: tt.description})
			var moleculeID string
			if fields != nil {
				moleculeID = fields.Molecule
			}
			if moleculeID != tt.wantMolID {
				t.Errorf("got molecule ID %q, want %q", moleculeID, tt.wantMolID)
			}
		})
	}
}

func TestNotifyConvoyCompletionParsing(t *testing.T) {
	// Test that ParseConvoyFields.NotificationAddresses correctly extracts Owner/Notify
	tests := []struct {
		name        string
		description string
		wantAddrs   []string
	}{
		{
			name:        "owner and notify",
			description: "Convoy tracking 2 issues\nOwner: mayor/\nNotify: ops/",
			wantAddrs:   []string{"mayor/", "ops/"},
		},
		{
			name:        "owner only",
			description: "Owner: deacon/",
			wantAddrs:   []string{"deacon/"},
		},
		{
			name:        "no addresses",
			description: "Convoy tracking 1 issue",
			wantAddrs:   nil,
		},
		{
			name:        "duplicate addresses deduped",
			description: "Owner: mayor/\nNotify: mayor/",
			wantAddrs:   []string{"mayor/"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields := beads.ParseConvoyFields(&beads.Issue{Description: tt.description})
			addrs := fields.NotificationAddresses()

			if len(addrs) != len(tt.wantAddrs) {
				t.Errorf("got %d addresses, want %d", len(addrs), len(tt.wantAddrs))
				return
			}
			for i, addr := range addrs {
				if addr != tt.wantAddrs[i] {
					t.Errorf("addr[%d] = %q, want %q", i, addr, tt.wantAddrs[i])
				}
			}
		})
	}
}

func TestEngineerNotifyConvoyCompletion_StampsAndSkipsDuplicate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping on windows - shell stubs")
	}

	tmpDir := t.TempDir()
	townRoot := filepath.Join(tmpDir, "town")
	rigDir := filepath.Join(townRoot, "testrig")
	townBeads := filepath.Join(townRoot, ".beads")
	if err := os.MkdirAll(townBeads, 0755); err != nil {
		t.Fatalf("mkdir town beads: %v", err)
	}
	if err := os.MkdirAll(rigDir, 0755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	binDir := t.TempDir()
	statePath := filepath.Join(binDir, "notified.state")
	mailLogPath := filepath.Join(binDir, "mail.log")
	bdPath := filepath.Join(binDir, "bd")
	gtPath := filepath.Join(binDir, "gt")

	bdScript := `#!/bin/sh
STATE="` + statePath + `"
if [ "$1" = "--allow-stale" ]; then
  shift
fi
case "$1" in
  version)
    exit 0
    ;;
  show)
    if [ -f "$STATE" ]; then
      printf '%s\n' '[{"id":"hq-cv-ref","description":"Owner: mayor/\ncompletion_notified_at: 2026-05-25T02:30:00Z"}]'
    else
      printf '%s\n' '[{"id":"hq-cv-ref","description":"Owner: mayor/"}]'
    fi
    exit 0
    ;;
  update)
    touch "$STATE"
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(bdPath, []byte(bdScript), 0755); err != nil {
		t.Fatalf("write bd stub: %v", err)
	}

	gtScript := `#!/bin/sh
if [ "$1" = "mail" ] && [ "$2" = "send" ]; then
  echo "$@" >> "` + mailLogPath + `"
fi
exit 0
`
	if err := os.WriteFile(gtPath, []byte(gtScript), 0755); err != nil {
		t.Fatalf("write gt stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	e := NewEngineer(&rig.Rig{Name: "testrig", Path: rigDir})
	e.notifyConvoyCompletion(townRoot, "hq-cv-ref", "Refinery Duplicate Guard", "Owner: mayor/")
	e.notifyConvoyCompletion(townRoot, "hq-cv-ref", "Refinery Duplicate Guard", "Owner: mayor/")

	data, err := os.ReadFile(mailLogPath)
	if err != nil {
		t.Fatalf("read mail log: %v", err)
	}
	if got := strings.Count(string(data), "mail send"); got != 1 {
		t.Fatalf("mail sends = %d, want 1; log:\n%s", got, string(data))
	}
	log := string(data)
	if !strings.Contains(log, "--from convoy/hq-cv-ref") {
		t.Fatalf("mail send missing convoy sender; log:\n%s", log)
	}
	if !strings.Contains(log, "--no-notify") {
		t.Fatalf("mail send missing --no-notify; log:\n%s", log)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("completion notification state was not recorded: %v", err)
	}
}

func TestIsClaimStale(t *testing.T) {
	timeout := DefaultStaleClaimTimeout

	tests := []struct {
		name      string
		updatedAt string
		want      bool
		wantErr   bool
	}{
		{
			name:      "stale claim (> threshold)",
			updatedAt: time.Now().Add(-timeout - 5*time.Minute).Format(time.RFC3339),
			want:      true,
		},
		{
			name:      "recent claim (< threshold)",
			updatedAt: time.Now().Add(-5 * time.Minute).Format(time.RFC3339),
			want:      false,
		},
		{
			name:      "exactly at threshold",
			updatedAt: time.Now().Add(-timeout).Format(time.RFC3339),
			want:      true,
		},
		{
			name:      "just under threshold",
			updatedAt: time.Now().Add(-timeout + time.Second).Format(time.RFC3339),
			want:      false,
		},
		{
			name:      "empty timestamp",
			updatedAt: "",
			want:      false,
		},
		{
			name:      "invalid timestamp format",
			updatedAt: "not-a-timestamp",
			want:      false,
			wantErr:   true,
		},
		{
			name:      "wrong date format",
			updatedAt: "2026-01-14 12:00:00",
			want:      false,
			wantErr:   true,
		},
		{
			name:      "custom short timeout",
			updatedAt: time.Now().Add(-2 * time.Minute).Format(time.RFC3339),
			want:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			to := timeout
			if tt.name == "custom short timeout" {
				to = 1 * time.Minute // Test configurable timeout
			}
			got, err := isClaimStale(tt.updatedAt, to)
			if (err != nil) != tt.wantErr {
				t.Errorf("isClaimStale(%q) error = %v, wantErr %v", tt.updatedAt, err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("isClaimStale(%q) = %v, want %v", tt.updatedAt, got, tt.want)
			}
		})
	}
}
