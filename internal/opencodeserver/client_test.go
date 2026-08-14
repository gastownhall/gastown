package opencodeserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientLifecycleRequests(t *testing.T) {
	t.Parallel()

	const directory = "/worktree"
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "opencode" || pass != "secret" {
			t.Fatalf("BasicAuth = %q/%q/%v, want opencode/secret/true", user, pass, ok)
		}
		if r.URL.Path != "/global/health" && r.Header.Get("X-OpenCode-Directory") != directory {
			t.Fatalf("X-OpenCode-Directory = %q, want %q", r.Header.Get("X-OpenCode-Directory"), directory)
		}
		calls = append(calls, r.Method+" "+r.URL.EscapedPath())

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/global/health":
			_ = json.NewEncoder(w).Encode(Health{Healthy: true, Version: "1.18.16"})
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			if body["title"] != "Gas Town gt-rig-worker" || body["agent"] != "build" {
				t.Fatalf("create body = %#v", body)
			}
			model, _ := body["model"].(map[string]any)
			if model["providerID"] != "opencode-go" || model["id"] != "grok-4.5" || model["variant"] != "high" {
				t.Fatalf("model body = %#v", model)
			}
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_test", Directory: directory})
		case r.Method == http.MethodGet && r.URL.Path == "/session/status":
			_ = json.NewEncoder(w).Encode(map[string]Status{"ses_test": {Type: "busy"}})
		case r.Method == http.MethodPost && r.URL.Path == "/session/ses_test/prompt_async":
			var body struct {
				Parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"parts"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode prompt body: %v", err)
			}
			if len(body.Parts) != 1 || body.Parts[0].Type != "text" || body.Parts[0].Text != "work now" {
				t.Fatalf("prompt body = %#v", body)
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/session/ses_test/abort":
			_ = json.NewEncoder(w).Encode(true)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "opencode", "secret", directory, server.Client())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx := context.Background()

	health, err := client.Health(ctx)
	if err != nil || !health.Healthy || health.Version != "1.18.16" {
		t.Fatalf("Health = %#v, %v", health, err)
	}
	session, err := client.CreateSession(ctx, CreateSessionOptions{
		Title:   "Gas Town gt-rig-worker",
		Agent:   "build",
		Model:   "opencode-go/grok-4.5",
		Variant: "high",
	})
	if err != nil || session.ID != "ses_test" {
		t.Fatalf("CreateSession = %#v, %v", session, err)
	}
	status, err := client.Status(ctx, session.ID)
	if err != nil || status.Type != "busy" {
		t.Fatalf("Status = %#v, %v", status, err)
	}
	if err := client.PromptAsync(ctx, session.ID, "work now"); err != nil {
		t.Fatalf("PromptAsync: %v", err)
	}
	if err := client.Abort(ctx, session.ID); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	want := []string{
		"GET /global/health",
		"POST /session",
		"GET /session/status",
		"POST /session/ses_test/prompt_async",
		"POST /session/ses_test/abort",
	}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestClientMissingStatusIsIdle(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	status, err := client.Status(context.Background(), "ses_idle")
	if err != nil || !status.Idle() {
		t.Fatalf("Status = %#v, %v, want idle", status, err)
	}
}

func TestClientRejectsNonLoopbackURL(t *testing.T) {
	t.Parallel()
	if _, err := NewClient("https://example.com", "opencode", "secret", t.TempDir(), nil); err == nil {
		t.Fatal("NewClient accepted non-loopback URL")
	}
}

func TestClientBoundsErrorBody(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("x", maxErrorBody+1024)))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = client.PromptAsync(context.Background(), "ses_test", "hello")
	if err == nil {
		t.Fatal("PromptAsync succeeded, want error")
	}
	if len(err.Error()) > maxErrorBody+256 {
		t.Fatalf("error length = %d, want bounded", len(err.Error()))
	}
}
