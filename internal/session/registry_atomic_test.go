package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jonbaldie/gastown/internal/config"
)

func TestDefaultRegistrySwapAndPrefixFor(t *testing.T) {
	old := DefaultRegistry()
	defer SetDefaultRegistry(old)

	r := NewPrefixRegistry()
	r.Register("xy", "xrig")
	SetDefaultRegistry(r)

	if got := DefaultRegistry(); got != r {
		t.Fatalf("DefaultRegistry() did not return swapped registry")
	}
	if got := PrefixFor("xrig"); got != "xy" {
		t.Fatalf("PrefixFor(xrig) = %q, want %q", got, "xy")
	}
	if got := PrefixFor("unknown-rig"); got != DefaultPrefix {
		t.Fatalf("PrefixFor(unknown-rig) = %q, want %q", got, DefaultPrefix)
	}
}

func TestIsKnownSession_UsesDefaultRegistryAndHQPrefix(t *testing.T) {
	old := DefaultRegistry()
	defer SetDefaultRegistry(old)

	r := NewPrefixRegistry()
	r.Register("xy", "xrig")
	SetDefaultRegistry(r)

	if !IsKnownSession("hq-mayor") {
		t.Fatal("expected hq-mayor to always be known")
	}
	if !IsKnownSession("xy-worker") {
		t.Fatal("expected xy-worker to be known via registry prefix")
	}
	if IsKnownSession("zz-worker") {
		t.Fatal("expected zz-worker to be unknown")
	}
}

func TestInitRegistryLoadsAgentRegistry(t *testing.T) {
	// Regression test: InitRegistry must load settings/agents.json so that
	// config.GetProcessNames respects user-configured process_names overrides.
	//
	// Without this, any entry point that calls InitRegistry without also
	// calling config.LoadAgentRegistry (daemon, witness) uses the builtin
	// defaults. On NixOS the Claude binary is ".claude-unwrapped", which
	// isn't in the builtin list, so heartbeat thinks the agent is dead and
	// kills the session.
	//
	// NOTE: cannot use t.Parallel() — mutates global registries.
	old := DefaultRegistry()
	defer SetDefaultRegistry(old)
	config.ResetRegistryForTesting()
	t.Cleanup(config.ResetRegistryForTesting)

	townRoot := t.TempDir()

	// Create settings/agents.json with a process_names override.
	settingsDir := filepath.Join(townRoot, "settings")
	if err := os.MkdirAll(settingsDir, 0755); err != nil {
		t.Fatal(err)
	}
	registry := config.AgentRegistry{
		Version: config.CurrentAgentRegistryVersion,
		Agents: map[string]*config.AgentPresetInfo{
			"claude": {
				Name:         "claude",
				Command:      "claude",
				Args:         []string{"--dangerously-skip-permissions"},
				ProcessNames: []string{"node", "claude", ".claude-unwrapped"},
			},
		},
	}
	data, err := json.Marshal(registry)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "agents.json"), data, 0644); err != nil {
		t.Fatal(err)
	}

	// InitRegistry should load both session prefixes AND agent registry.
	if err := InitRegistry(townRoot); err != nil {
		t.Fatalf("InitRegistry: %v", err)
	}

	// Verify GetProcessNames returns the override from settings/agents.json.
	got := config.GetProcessNames("claude")
	want := []string{"node", "claude", ".claude-unwrapped"}
	if len(got) != len(want) {
		t.Fatalf("GetProcessNames(claude) = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("GetProcessNames(claude)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestInitRegistryNoAgentsJSON(t *testing.T) {
	// InitRegistry must not fail when settings/agents.json is absent.
	old := DefaultRegistry()
	defer SetDefaultRegistry(old)
	config.ResetRegistryForTesting()
	t.Cleanup(config.ResetRegistryForTesting)

	townRoot := t.TempDir()

	if err := InitRegistry(townRoot); err != nil {
		t.Fatalf("InitRegistry with no agents.json: %v", err)
	}
}
