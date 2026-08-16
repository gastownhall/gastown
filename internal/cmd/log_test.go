package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gtevents "github.com/jonbaldie/gastown/internal/events"
)

func TestRunLogCrashEmitsFeedSessionDeath(t *testing.T) {
	townRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(townRoot, "mayor"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(townRoot, "mayor", "town.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(townRoot); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	origAgent, origSession, origExitCode := crashAgent, crashSession, crashExitCode
	t.Cleanup(func() {
		crashAgent = origAgent
		crashSession = origSession
		crashExitCode = origExitCode
	})
	crashAgent = "gastown/polecats/rust"
	crashSession = "gt-gastown-rust"
	crashExitCode = 42

	if err := runLogCrash(nil, nil); err != nil {
		t.Fatalf("runLogCrash: %v", err)
	}

	townLog, err := os.ReadFile(filepath.Join(townRoot, "logs", "town.log"))
	if err != nil {
		t.Fatalf("read town log: %v", err)
	}
	if !strings.Contains(string(townLog), "[crash]") || !strings.Contains(string(townLog), "exit code 42") {
		t.Fatalf("town log missing crash entry: %s", townLog)
	}

	rawEvents, err := os.ReadFile(filepath.Join(townRoot, gtevents.EventsFile))
	if err != nil {
		t.Fatalf("read events log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawEvents)), "\n")
	if len(lines) != 1 {
		t.Fatalf("event count = %d, want 1: %s", len(lines), rawEvents)
	}

	var event gtevents.Event
	if err := json.Unmarshal([]byte(lines[0]), &event); err != nil {
		t.Fatalf("unmarshal event: %v", err)
	}
	if event.Type != gtevents.TypeSessionDeath {
		t.Fatalf("event type = %q, want %q", event.Type, gtevents.TypeSessionDeath)
	}
	if event.Actor != "gastown/polecats/rust" {
		t.Fatalf("actor = %q", event.Actor)
	}
	if event.Visibility != gtevents.VisibilityFeed {
		t.Fatalf("visibility = %q", event.Visibility)
	}
	assertPayloadString(t, event.Payload, "session", "gt-gastown-rust")
	assertPayloadString(t, event.Payload, "agent", "gastown/polecats/rust")
	assertPayloadString(t, event.Payload, "reason", "crashed with exit code 42")
	assertPayloadString(t, event.Payload, "caller", "gt log crash")
	if got, ok := event.Payload["exit_code"].(float64); !ok || got != 42 {
		t.Fatalf("exit_code = %#v, want 42", event.Payload["exit_code"])
	}
}

func assertPayloadString(t *testing.T, payload map[string]interface{}, key, want string) {
	t.Helper()
	if got, ok := payload[key].(string); !ok || got != want {
		t.Fatalf("payload[%q] = %#v, want %q", key, payload[key], want)
	}
}
