package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/opencodeserver"
)

func TestNudgeDeliveryModeUsesQueueForOpenCodeServer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "opencode" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(opencodeserver.Health{Healthy: true, Version: "1.18.16"})
	}))
	defer server.Close()

	townRoot := t.TempDir()
	sessionName := "gt-rig-worker"
	if err := opencodeserver.SaveState(townRoot, opencodeserver.State{
		GasTownSession:  sessionName,
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}
	release, err := opencodeserver.AcquireSessionLock(townRoot, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if got := nudgeDeliveryMode(townRoot, sessionName, NudgeModeImmediate); got != NudgeModeQueue {
		t.Fatalf("nudgeDeliveryMode = %q, want %q", got, NudgeModeQueue)
	}
}

func TestInjectStartPromptQueuesForOpenCodeServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(opencodeserver.Health{Healthy: true, Version: "1.18.16"})
	}))
	defer server.Close()

	townRoot := t.TempDir()
	sessionName := "gt-rig-worker"
	if err := opencodeserver.SaveState(townRoot, opencodeserver.State{
		GasTownSession:  sessionName,
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}
	release, err := opencodeserver.AcquireSessionLock(townRoot, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	if err := injectStartPrompt(townRoot, sessionName+":0.0", "gt-123", "Fix race", "urgent"); err != nil {
		t.Fatal(err)
	}
	queueDir := filepath.Join(townRoot, ".runtime", "nudge_queue", sessionName)
	entries, err := os.ReadDir(queueDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("queued files = %d, want 1", len(entries))
	}
}

func TestResolveSessionFromPaneRejectsEmptyTarget(t *testing.T) {
	if _, err := resolveSessionFromPane(""); err == nil {
		t.Fatal("resolveSessionFromPane accepted an empty pane target")
	}
}
