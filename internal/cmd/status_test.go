package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/rig"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("create pipe: %v", err)
	}
	os.Stderr = w

	fn()

	_ = w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	_ = r.Close()

	return buf.String()
}

func TestDiscoverRigAgents_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "beads",
		Path:       filepath.Join(townRoot, "beads"),
		HasWitness: true,
	}

	allAgentBeads := map[string]*beads.Issue{
		"bd-beads-witness": {
			ID:         "bd-beads-witness",
			AgentState: "running",
			HookBead:   "bd-hook",
		},
	}
	allHookBeads := map[string]*beads.Issue{
		"bd-hook": {ID: "bd-hook", Title: "Pinned"},
	}

	agents := discoverRigAgents(map[string]bool{}, r, nil, allAgentBeads, allHookBeads, nil, true)
	if len(agents) != 1 {
		t.Fatalf("discoverRigAgents() returned %d agents, want 1", len(agents))
	}

	if agents[0].State != "running" {
		t.Fatalf("agent state = %q, want %q", agents[0].State, "running")
	}
	if !agents[0].HasWork {
		t.Fatalf("agent HasWork = false, want true")
	}
	if agents[0].WorkTitle != "Pinned" {
		t.Fatalf("agent WorkTitle = %q, want %q", agents[0].WorkTitle, "Pinned")
	}
}

func TestRenderAgentDetails_UsesRigPrefix(t *testing.T) {
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "bd-", Path: "beads/mayor/rig"},
	})

	agent := AgentRuntime{
		Name:    "witness",
		Address: "beads/witness",
		Role:    "witness",
		Running: true,
	}

	var buf bytes.Buffer
	renderAgentDetails(&buf, agent, "", nil, townRoot)
	output := buf.String()

	if !strings.Contains(output, "bd-beads-witness") {
		t.Fatalf("output %q does not contain rig-prefixed bead ID", output)
	}
}

func TestDiscoverRigAgents_ZombieSessionNotRunning(t *testing.T) {
	// Verify that a session in allSessions with value=false (zombie: tmux alive,
	// agent dead) results in agent.Running=false. This is the core fix for gt-bd6i3.
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "gastown",
		Path:       filepath.Join(townRoot, "gastown"),
		HasWitness: true,
	}

	// allSessions has the witness session but marked as zombie (false).
	// This simulates a tmux session that exists but whose agent process has died.
	allSessions := map[string]bool{
		"gt-gastown-witness": false, // zombie: tmux exists, agent dead
	}

	agents := discoverRigAgents(allSessions, r, nil, nil, nil, nil, true)
	for _, a := range agents {
		if a.Role == "witness" {
			if a.Running {
				t.Fatal("zombie witness session (allSessions=false) should show as not running")
			}
			return
		}
	}
	t.Fatal("witness agent not found in results")
}

func TestDiscoverRigAgents_MissingSessionNotRunning(t *testing.T) {
	// Verify that a session not in allSessions at all results in agent.Running=false.
	townRoot := t.TempDir()
	writeTestRoutes(t, townRoot, []beads.Route{
		{Prefix: "gt-", Path: "gastown/mayor/rig"},
	})

	r := &rig.Rig{
		Name:       "gastown",
		Path:       filepath.Join(townRoot, "gastown"),
		HasWitness: true,
	}

	// Empty sessions map - no tmux sessions exist at all
	allSessions := map[string]bool{}

	agents := discoverRigAgents(allSessions, r, nil, nil, nil, nil, true)
	for _, a := range agents {
		if a.Role == "witness" {
			if a.Running {
				t.Fatal("witness with no tmux session should show as not running")
			}
			return
		}
	}
	t.Fatal("witness agent not found in results")
}

func TestBuildStatusIndicator_ZombieShowsStopped(t *testing.T) {
	// Verify that a zombie agent (Running=false) shows ○ (stopped), not ● (running)
	agent := AgentRuntime{Running: false}
	indicator := buildStatusIndicator(agent)
	if strings.Contains(indicator, "●") {
		t.Fatal("zombie agent (Running=false) should not show ● indicator")
	}
}

func TestBuildStatusIndicator_AliveShowsRunning(t *testing.T) {
	// Verify that an alive agent (Running=true) shows ● (running)
	agent := AgentRuntime{Running: true}
	indicator := buildStatusIndicator(agent)
	if strings.Contains(indicator, "○") {
		t.Fatal("alive agent (Running=true) should not show ○ indicator")
	}
}

func TestBuildStatusIndicator_DNDMutedShowsBadge(t *testing.T) {
	agent := AgentRuntime{Running: true, NotificationLevel: beads.NotifyMuted}
	indicator := buildStatusIndicator(agent)
	if !strings.Contains(indicator, "🔕") {
		t.Fatalf("expected muted indicator to include 🔕, got %q", indicator)
	}
}

func TestOutputStatusText_IncludesDNDSection(t *testing.T) {
	status := TownStatus{
		Name:     "gt",
		Location: "/tmp/gt",
		DND: &DNDInfo{
			Enabled: true,
			Level:   beads.NotifyMuted,
			Agent:   "hq-mayor",
		},
	}

	var buf bytes.Buffer
	if err := outputStatusText(&buf, status); err != nil {
		t.Fatalf("outputStatusText error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "DND:") {
		t.Fatalf("expected DND section in status output, got: %q", out)
	}
	if !strings.Contains(out, "on") {
		t.Fatalf("expected DND state 'on' in status output, got: %q", out)
	}
}

func TestModelCrashRecoveryStatusSurfacesConfirmedAndExhaustedIncidents(t *testing.T) {
	townRoot := t.TempDir()
	stateDir := filepath.Join(townRoot, "deacon")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	state := `{
		"version": 1,
		"sessions": {
			"rig/polecats/toast": {
				"session_name": "gt-toast",
				"incident_id": "model-crash-toast",
				"local_restarts": 1,
				"recovery_action": "local-restart",
				"recovery_exhausted": false
			},
			"deacon": {
				"session_name": "gt-deacon",
				"incident_id": "model-crash-deacon",
				"local_restarts": 1,
				"recovery_action": "awaiting-local-probe",
				"recovery_exhausted": true
			}
		},
		"alerts": {}
	}`
	if err := os.WriteFile(filepath.Join(stateDir, "model-crash-supervisor.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}

	recovery := loadModelCrashRecoveryStatus(townRoot)
	if recovery == nil || recovery.Confirmed != 2 || recovery.Exhausted != 1 {
		t.Fatalf("model-crash status summary = %#v, want 2 confirmed / 1 exhausted", recovery)
	}
	if !recovery.WatchdogUnavailable || recovery.WatchdogError == "" {
		t.Fatalf("provisioned status hid unavailable watchdog: %#v", recovery)
	}
	status := TownStatus{Name: "gt", Location: townRoot, ModelCrashRecovery: recovery}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"model_crash_recovery"`, `"model-crash-toast"`, `"recovery_exhausted":true`, `"watchdog_unavailable":true`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("status JSON missing %q: %s", want, data)
		}
	}

	var out bytes.Buffer
	if err := outputStatusText(&out, status); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Model crash recovery:", "watchdog unavailable", "2 confirmed", "1 exhausted", "rig/polecats/toast", "deacon"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status text missing %q: %s", want, out.String())
		}
	}
}

func TestModelCrashRecoveryStatusOmitsUnprovisionedTown(t *testing.T) {
	townRoot := t.TempDir()
	if got := loadModelCrashRecoveryStatus(townRoot); got != nil {
		t.Fatalf("unprovisioned town exposed local watchdog failure: %#v", got)
	}
	marker := filepath.Join(townRoot, "bin", "gt-lmstudio-watchdog")
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	got := loadModelCrashRecoveryStatus(townRoot)
	if got == nil || !got.WatchdogUnavailable {
		t.Fatalf("installed watchdog contract silently opted out: %#v", got)
	}
}

func TestRunStatusWatch_RejectsZeroInterval(t *testing.T) {
	oldInterval := statusInterval
	oldWatch := statusWatch
	defer func() {
		statusInterval = oldInterval
		statusWatch = oldWatch
	}()

	statusInterval = 0
	statusWatch = true

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for zero interval, got nil")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error %q should mention 'positive'", err.Error())
	}
}

func TestRunStatusWatch_RejectsNegativeInterval(t *testing.T) {
	oldInterval := statusInterval
	oldWatch := statusWatch
	defer func() {
		statusInterval = oldInterval
		statusWatch = oldWatch
	}()

	statusInterval = -5
	statusWatch = true

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for negative interval, got nil")
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Errorf("error %q should mention 'positive'", err.Error())
	}
}

func TestRunStatusWatch_RejectsJSONCombo(t *testing.T) {
	oldJSON := statusJSON
	oldWatch := statusWatch
	oldInterval := statusInterval
	defer func() {
		statusJSON = oldJSON
		statusWatch = oldWatch
		statusInterval = oldInterval
	}()

	statusJSON = true
	statusWatch = true
	statusInterval = 2

	err := runStatusWatch(nil, nil)
	if err == nil {
		t.Fatal("expected error for --json + --watch, got nil")
	}
	if !strings.Contains(err.Error(), "cannot be used together") {
		t.Errorf("error %q should mention 'cannot be used together'", err.Error())
	}
}

func TestTryStatusDetailLockContention(t *testing.T) {
	townRoot := t.TempDir()

	release, ok := tryStatusDetailLock(townRoot)
	if !ok {
		t.Fatal("first status detail lock should be acquired")
	}

	if release2, ok := tryStatusDetailLock(townRoot); ok {
		release2()
		t.Fatal("second status detail lock should fail while first is held")
	}

	release()

	release3, ok := tryStatusDetailLock(townRoot)
	if !ok {
		t.Fatal("status detail lock should be reusable after release")
	}
	release3()
}

func TestIsKnownAgent(t *testing.T) {
	t.Parallel()

	// All agent presets should be recognized
	for _, name := range config.ListAgentPresets() {
		t.Run(name+"_known", func(t *testing.T) {
			if !isKnownAgent(name) {
				t.Errorf("isKnownAgent(%q) = false, want true", name)
			}
		})
	}

	// Non-agents should not be recognized
	for _, name := range []string{"bash", "node", ""} {
		t.Run(name+"_unknown", func(t *testing.T) {
			if isKnownAgent(name) {
				t.Errorf("isKnownAgent(%q) = true, want false", name)
			}
		})
	}
}

func TestIsAgentWrapper(t *testing.T) {
	t.Parallel()
	tests := []struct {
		base string
		want bool
	}{
		{"node", true},
		{"bun", true},
		{"npx", true},
		{"bunx", true},
		{"claude", false},
		{"pi", false},
		{"bash", false},
	}

	for _, tt := range tests {
		t.Run(tt.base, func(t *testing.T) {
			if got := isAgentWrapper(tt.base); got != tt.want {
				t.Errorf("isAgentWrapper(%q) = %v, want %v", tt.base, got, tt.want)
			}
		})
	}
}

func TestParseRuntimeInfo(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cmdline string
		want    string
	}{
		{
			name:    "claude with model",
			cmdline: "claude\x00--model\x00opus\x00--dangerously-skip-permissions",
			want:    "claude/opus",
		},
		{
			name:    "pi with model",
			cmdline: "pi\x00-e\x00gastown-hooks.js\x00--model\x00google-antigravity/gemini-3-flash",
			want:    "pi/google-antigravity/gemini-3-flash",
		},
		{
			name:    "cgroup-wrap then claude",
			cmdline: "cgroup-wrap\x00claude\x00--model\x00opus\x00--dangerously-skip-permissions",
			want:    "claude/opus",
		},
		{
			name:    "opencode with -m flag",
			cmdline: "opencode\x00-m\x00kimi-for-coding/kimi-k2.5",
			want:    "opencode/kimi-for-coding/kimi-k2.5",
		},
		{
			name:    "empty cmdline",
			cmdline: "",
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseRuntimeInfo(tt.cmdline)
			if got != tt.want {
				t.Errorf("parseRuntimeInfo(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestParseRuntimeInfo_PiBare(t *testing.T) {
	t.Parallel()
	// Bare pi (no --model flag) calls readPiDefaults() which reads
	// ~/.pi/agent/settings.json. The result is either "pi" (if no settings)
	// or "pi/<default-model>" (if settings exist). Both are valid.
	cmdline := "pi\x00-e\x00gastown-hooks.js"
	got := parseRuntimeInfo(cmdline)
	if !strings.HasPrefix(got, "pi") {
		t.Errorf("parseRuntimeInfo(pi bare) = %q, want prefix 'pi'", got)
	}
}

func TestOpenCodeModelInfo(t *testing.T) {
	t.Parallel()
	if got := openCodeModelInfo(`{"lsp":true,"model":"opencode-go/qwen3.7-max"}`); got != "opencode-go/qwen3.7-max" {
		t.Fatalf("openCodeModelInfo() = %q, want opencode-go/qwen3.7-max", got)
	}
	if got := openCodeModelInfo(`{"lsp":true}`); got != "" {
		t.Fatalf("openCodeModelInfo() without model = %q, want empty", got)
	}
	if got := openCodeModelInfo(`not-json`); got != "" {
		t.Fatalf("openCodeModelInfo() invalid JSON = %q, want empty", got)
	}
}

func TestResolveAgentDisplay_RunningSessionUsesEffectiveAliasAndModel(t *testing.T) {
	oldEnvironment := statusSessionEnvironment
	oldDetector := statusRuntimeDetector
	t.Cleanup(func() {
		statusSessionEnvironment = oldEnvironment
		statusRuntimeDetector = oldDetector
	})

	statusSessionEnvironment = func(_ string, key string) (string, error) {
		switch key {
		case "GT_AGENT":
			return "opencode-go", nil
		case "OPENCODE_CONFIG_CONTENT":
			return `{"lsp":true,"model":"opencode-go/qwen3.7-max"}`, nil
		default:
			return "", os.ErrNotExist
		}
	}
	statusRuntimeDetector = func(string) string { return "" }

	alias, info := resolveAgentDisplay(t.TempDir(), "", "polecat", "jasper", "cy-jasper", true)
	if alias != "opencode-go" {
		t.Fatalf("alias = %q, want opencode-go", alias)
	}
	if info != "opencode-go/qwen3.7-max" {
		t.Fatalf("info = %q, want opencode-go/qwen3.7-max", info)
	}
}

func TestResolveAgentDisplay_StoppedAgentUsesRigRoleSetting(t *testing.T) {
	townRoot := t.TempDir()
	rigPath := filepath.Join(townRoot, "canary")
	if err := os.MkdirAll(rigPath, 0o755); err != nil {
		t.Fatalf("mkdir rig: %v", err)
	}

	townSettings := config.NewTownSettings()
	townSettings.DefaultAgent = "claude"
	if err := config.SaveTownSettings(config.TownSettingsPath(townRoot), townSettings); err != nil {
		t.Fatalf("save town settings: %v", err)
	}
	rigSettings := config.NewRigSettings()
	rigSettings.Agent = "codex"
	rigSettings.RoleAgents = make(map[string]string)
	rigSettings.RoleAgents["witness"] = "gemini"
	if err := config.SaveRigSettings(config.RigSettingsPath(rigPath), rigSettings); err != nil {
		t.Fatalf("save rig settings: %v", err)
	}

	alias, _ := resolveAgentDisplay(townRoot, rigPath, "witness", "witness", "", false)
	if alias != "gemini" {
		t.Fatalf("alias = %q, want rig role alias gemini", alias)
	}
}

func TestBuildInfoFromConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		rc   *config.RuntimeConfig
		want string
	}{
		{
			name: "claude with model",
			rc:   &config.RuntimeConfig{Command: "claude", Args: []string{"--model", "opus"}},
			want: "claude/opus",
		},
		{
			name: "cgroup-wrap claude",
			rc:   &config.RuntimeConfig{Command: "cgroup-wrap", Args: []string{"claude", "--model", "opus"}},
			want: "claude/opus",
		},
		{
			name: "pi bare",
			rc:   &config.RuntimeConfig{Command: "pi", Args: []string{"-e", "hooks.js"}},
			want: "pi",
		},
		{
			name: "opencode with -m",
			rc:   &config.RuntimeConfig{Command: "opencode", Args: []string{"-m", "gpt-5"}},
			want: "opencode/gpt-5",
		},
		{
			name: "opencode model from environment config",
			rc: &config.RuntimeConfig{
				Command: "gt-opencode",
				Env: map[string]string{
					"OPENCODE_CONFIG_CONTENT": `{"lsp":true,"model":"lmstudio/qwen/qwen3.6-35b-a3b"}`,
				},
			},
			want: "lmstudio/qwen/qwen3.6-35b-a3b",
		},
		{
			name: "empty command",
			rc:   &config.RuntimeConfig{Command: ""},
			want: "claude",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildInfoFromConfig(tt.rc)
			if got != tt.want {
				t.Errorf("buildInfoFromConfig(%s) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestIsAgentCmdline(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"claude direct", "claude\x00--model\x00opus", true},
		{"pi direct", "pi\x00-e\x00hooks.js", true},
		{"node wrapper with pi", "node\x00/path/to/pi\x00-e\x00hooks.js", true},
		{"bun wrapper with opencode", "bun\x00/path/to/opencode", true},
		{"bash not agent", "bash\x00-c\x00echo hi", false},
		{"node without agent", "node\x00/path/to/server.js", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isAgentCmdline(tt.cmdline)
			if got != tt.want {
				t.Errorf("isAgentCmdline(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestCountRunningAgents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status TownStatus
		want   int
	}{
		{
			name:   "empty status",
			status: TownStatus{},
			want:   0,
		},
		{
			name: "global agents only",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: true},
					{Name: "deacon", Running: false},
				},
			},
			want: 1,
		},
		{
			name: "rig agents only",
			status: TownStatus{
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: true},
							{Name: "witness", Running: true},
						},
					},
				},
			},
			want: 2,
		},
		{
			name: "mixed global and rig agents",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: true},
				},
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: true},
							{Name: "witness", Running: false},
						},
					},
					{
						Agents: []AgentRuntime{
							{Name: "polecat-2", Running: true},
						},
					},
				},
			},
			want: 3,
		},
		{
			name: "all not running",
			status: TownStatus{
				Agents: []AgentRuntime{
					{Name: "mayor", Running: false},
				},
				Rigs: []RigStatus{
					{
						Agents: []AgentRuntime{
							{Name: "polecat-1", Running: false},
						},
					},
				},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countRunningAgents(tt.status)
			if got != tt.want {
				t.Errorf(
					"countRunningAgents() = %d, want %d",
					got, tt.want,
				)
			}
		})
	}
}

func TestExtractBaseName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		cmdline string
		want    string
	}{
		{"claude\x00--model\x00opus", "claude"},
		{"/usr/bin/node\x00/path/pi", "node"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := extractBaseName(tt.cmdline)
			if got != tt.want {
				t.Errorf("extractBaseName(%q) = %q, want %q", tt.cmdline, got, tt.want)
			}
		})
	}
}
