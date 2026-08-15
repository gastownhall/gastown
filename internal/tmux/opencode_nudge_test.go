package tmux

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/steveyegge/gastown/internal/nudge"
	"github.com/steveyegge/gastown/internal/opencodestate"
)

func TestNudgeSessionQueuesForOpenCodeServer(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"healthy": true})
	}))
	defer server.Close()

	townRoot := t.TempDir()
	sessionName := "gt-rig-worker"
	if err := opencodestate.Save(townRoot, opencodestate.State{
		GasTownSession:  sessionName,
		OpenCodeSession: "ses_test",
		Directory:       t.TempDir(),
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}
	release, err := opencodestate.AcquireSessionLock(townRoot, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	tmux := NewTmux()
	if err := tmux.NudgeSessionWithOpts(sessionName, "check your hook", NudgeOpts{TownRoot: townRoot}); err != nil {
		t.Fatalf("NudgeSessionWithOpts: %v", err)
	}
	drained, err := nudge.Drain(townRoot, sessionName)
	if err != nil {
		t.Fatal(err)
	}
	if len(drained) != 1 || drained[0].Message != "check your hook" || drained[0].Sender != "system" {
		t.Fatalf("drained = %#v", drained)
	}
}
