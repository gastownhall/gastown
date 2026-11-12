package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/polecat"
	"github.com/steveyegge/gastown/internal/tmux"
)

func TestSessionInfoJSONOutput(t *testing.T) {
	info := &polecat.SessionInfo{
		Polecat:   "alpha",
		SessionID: "gt-alpha",
		Running:   true,
		RigName:   "gastown",
		Attached:  false,
		Created:   time.Date(2026, 2, 20, 10, 0, 0, 0, time.UTC),
		Windows:   1,
	}

	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["polecat"] != "alpha" {
		t.Errorf("polecat = %v, want alpha", parsed["polecat"])
	}
	if parsed["session_id"] != "gt-alpha" {
		t.Errorf("session_id = %v, want gt-alpha", parsed["session_id"])
	}
	if parsed["running"] != true {
		t.Errorf("running = %v, want true", parsed["running"])
	}
	if parsed["rig_name"] != "gastown" {
		t.Errorf("rig_name = %v, want gastown", parsed["rig_name"])
	}
}

func TestSessionStatusCmdJSONFlagWiring(t *testing.T) {
	// Verify --json flag is registered on the session status command.
	// This catches regressions where flag binding is accidentally removed,
	// which would silently break formulas that depend on --json output.
	f := sessionStatusCmd.Flags().Lookup("json")
	if f == nil {
		t.Fatal("session status command missing --json flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %q, want \"false\"", f.DefValue)
	}
}

func TestSessionHealthCmdFlagWiring(t *testing.T) {
	if sessionCmd.Commands() == nil {
		t.Fatal("session command has no subcommands")
	}

	f := sessionHealthCmd.Flags().Lookup("json")
	if f == nil {
		t.Fatal("session health command missing --json flag")
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %q, want \"false\"", f.DefValue)
	}

	f = sessionHealthCmd.Flags().Lookup("max-inactivity")
	if f == nil {
		t.Fatal("session health command missing --max-inactivity flag")
	}
	if f.DefValue != "0s" {
		t.Errorf("--max-inactivity default = %q, want \"0s\"", f.DefValue)
	}
}

func TestSessionRestartCmdAgentFlagWiring(t *testing.T) {
	f := sessionRestartCmd.Flags().Lookup("agent")
	if f == nil {
		t.Fatal("session restart command missing --agent flag")
	}
	if f.DefValue != "" {
		t.Errorf("--agent default = %q, want empty", f.DefValue)
	}
}

type fakeSessionRestartManager struct {
	running    bool
	stopped    bool
	started    bool
	force      bool
	startName  string
	startOpts  polecat.SessionStartOptions
	worktree   string
	branch     string
	hook       string
	assignment string
}

func (m *fakeSessionRestartManager) IsRunning(string) (bool, error) {
	return m.running && !m.stopped, nil
}

func (m *fakeSessionRestartManager) Stop(_ string, force bool) error {
	m.stopped = true
	m.force = force
	return nil
}

func (m *fakeSessionRestartManager) Start(name string, opts polecat.SessionStartOptions) error {
	m.started = true
	m.startName = name
	m.startOpts = opts
	return nil
}

func TestRunSessionRestartAgentOverridePreservesPolecatState(t *testing.T) {
	manager := &fakeSessionRestartManager{
		running:    true,
		worktree:   "/town/rig/polecats/toast",
		branch:     "polecat/toast",
		hook:       "gt-work",
		assignment: "gt-work",
	}
	originalFactory := sessionRestartManagerForRig
	originalValidator := sessionRestartValidateAgent
	originalAgent := sessionRestartAgent
	originalForce := sessionForce
	t.Cleanup(func() {
		sessionRestartManagerForRig = originalFactory
		sessionRestartValidateAgent = originalValidator
		sessionRestartAgent = originalAgent
		sessionForce = originalForce
	})
	var requestedRig string
	sessionRestartManagerForRig = func(rigName string) (sessionRestartManager, error) {
		requestedRig = rigName
		return manager, nil
	}
	sessionRestartValidateAgent = func(string, string) error { return nil }
	sessionRestartAgent = "opencode-go"
	sessionForce = true

	if err := runSessionRestart(nil, []string{"rig/toast"}); err != nil {
		t.Fatal(err)
	}
	if requestedRig != "rig" || !manager.stopped || !manager.force || !manager.started || manager.startName != "toast" {
		t.Fatalf("restart lifecycle rig=%q manager=%#v", requestedRig, manager)
	}
	wantOpts := polecat.SessionStartOptions{Agent: "opencode-go"}
	if manager.startOpts != wantOpts {
		t.Fatalf("restart options = %#v, want only agent override %#v", manager.startOpts, wantOpts)
	}
	if manager.worktree != "/town/rig/polecats/toast" || manager.branch != "polecat/toast" ||
		manager.hook != "gt-work" || manager.assignment != "gt-work" {
		t.Fatalf("restart mutated polecat state: %#v", manager)
	}
}

func TestRunSessionRestartValidatesAgentBeforeStopping(t *testing.T) {
	manager := &fakeSessionRestartManager{running: true}
	originalFactory := sessionRestartManagerForRig
	originalValidator := sessionRestartValidateAgent
	originalAgent := sessionRestartAgent
	t.Cleanup(func() {
		sessionRestartManagerForRig = originalFactory
		sessionRestartValidateAgent = originalValidator
		sessionRestartAgent = originalAgent
	})
	sessionRestartManagerForRig = func(string) (sessionRestartManager, error) {
		return manager, nil
	}
	validated := false
	sessionRestartValidateAgent = func(rigName, agent string) error {
		validated = rigName == "rig" && agent == "not-configured"
		return fmt.Errorf("unknown agent %q", agent)
	}
	sessionRestartAgent = "not-configured"

	if err := runSessionRestart(nil, []string{"rig/toast"}); err == nil {
		t.Fatal("invalid agent override restarted session")
	}
	if !validated {
		t.Fatal("agent override was not validated")
	}
	if manager.stopped || manager.started {
		t.Fatalf("invalid agent stopped or started session: %#v", manager)
	}
}

func TestSessionHealthReportJSONContract(t *testing.T) {
	report := newSessionHealthReport("gt-vault", tmux.AgentDead, 30*time.Minute)
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}
	if parsed["session"] != "gt-vault" {
		t.Errorf("session = %v, want gt-vault", parsed["session"])
	}
	if parsed["status"] != "agent-dead" {
		t.Errorf("status = %v, want agent-dead", parsed["status"])
	}
	if parsed["healthy"] != false {
		t.Errorf("healthy = %v, want false", parsed["healthy"])
	}
	if parsed["zombie"] != true {
		t.Errorf("zombie = %v, want true", parsed["zombie"])
	}
	if parsed["max_inactivity_seconds"] != float64(1800) {
		t.Errorf("max_inactivity_seconds = %v, want 1800", parsed["max_inactivity_seconds"])
	}
	if _, ok := parsed["model_crash"]; !ok {
		t.Fatal("session health JSON missing additive model_crash visibility")
	}
	for _, field := range []string{"incident_id", "recovery_action", "recovery_exhausted"} {
		if _, ok := parsed[field]; !ok {
			t.Fatalf("session health JSON missing additive %s visibility", field)
		}
	}
}

func TestSessionHealthCurrentModelCrashOverridesProcessHealth(t *testing.T) {
	report := newSessionHealthReport("gt-vault", tmux.SessionHealthy, 0)
	applyModelCrashHealth(&report, "sha256:fatal")

	if report.Healthy {
		t.Fatal("current model crash retained healthy=true")
	}
	if report.Status != "model-crashed" {
		t.Fatalf("status = %q, want model-crashed", report.Status)
	}
	if !report.ModelCrash || report.ModelCrashFingerprint != "sha256:fatal" {
		t.Fatalf("model crash visibility = %#v", report)
	}
}

func TestRunSessionHealthJSONSessionDead(t *testing.T) {
	oldJSON := sessionHealthJSON
	oldMaxInactivity := sessionHealthMaxInactivity
	oldStdout := os.Stdout
	t.Cleanup(func() {
		sessionHealthJSON = oldJSON
		sessionHealthMaxInactivity = oldMaxInactivity
		os.Stdout = oldStdout
	})

	sessionHealthJSON = true
	sessionHealthMaxInactivity = 0
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stdout = w

	err = runSessionHealth(sessionHealthCmd, []string{"gt-session-health-test-nonexistent"})
	if closeErr := w.Close(); closeErr != nil {
		t.Fatalf("closing pipe writer: %v", closeErr)
	}
	os.Stdout = oldStdout
	if err != nil {
		t.Fatalf("runSessionHealth failed: %v", err)
	}

	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading stdout pipe: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v\noutput: %s", err, string(data))
	}
	if parsed["session"] != "gt-session-health-test-nonexistent" {
		t.Errorf("session = %v, want gt-session-health-test-nonexistent", parsed["session"])
	}
	if parsed["status"] != "session-dead" {
		t.Errorf("status = %v, want session-dead", parsed["status"])
	}
	if parsed["healthy"] != false {
		t.Errorf("healthy = %v, want false", parsed["healthy"])
	}
	if parsed["zombie"] != false {
		t.Errorf("zombie = %v, want false", parsed["zombie"])
	}
}

func TestSessionInfoJSONOutputNotRunning(t *testing.T) {
	info := &polecat.SessionInfo{
		Polecat:   "beta",
		SessionID: "gt-beta",
		Running:   false,
		RigName:   "testrig",
	}

	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if parsed["running"] != false {
		t.Errorf("running = %v, want false", parsed["running"])
	}
}
