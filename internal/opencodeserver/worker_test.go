package opencodeserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/gastown/internal/nudge"
)

func TestLifecycleErrorCompletesInFlightPrompt(t *testing.T) {
	lifecycle := newSessionLifecycle("ses_test", Status{Type: "idle"})
	if !lifecycle.BeginPrompt() {
		t.Fatal("BeginPrompt rejected an idle session")
	}
	if lifecycle.Observe(Event{Type: "session.error", Properties: EventProperties{SessionID: "ses_test"}}) {
		t.Fatal("session.error admitted another prompt before status reconciliation")
	}
	lifecycle.Reconcile(Status{Type: "idle"})
	if lifecycle.Ready() {
		t.Fatal("one idle snapshot cleared an errored prompt too early")
	}
	lifecycle.Reconcile(Status{Type: "idle"})
	if !lifecycle.Ready() {
		t.Fatal("lifecycle remained wedged after confirmed idle status")
	}
}

func TestLifecycleIgnoresStaleErrorWhenNewPromptBecomesBusy(t *testing.T) {
	lifecycle := newSessionLifecycle("ses_test", Status{Type: "idle"})
	if !lifecycle.BeginPrompt() {
		t.Fatal("BeginPrompt rejected an idle session")
	}
	lifecycle.Observe(Event{Type: "session.error", Properties: EventProperties{SessionID: "ses_test"}})
	lifecycle.Reconcile(Status{Type: "busy"})
	lifecycle.Observe(Event{Type: "session.idle", Properties: EventProperties{SessionID: "ses_test"}})
	if !lifecycle.Ready() {
		t.Fatal("busy-to-idle transition did not complete after a stale error")
	}
}

func TestDeliverQueuedNudgesWhenIdle(t *testing.T) {
	t.Parallel()
	var prompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = w.Write([]byte(`{}`))
		case "/session/ses_test/prompt_async":
			var body struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			prompt = body.Parts[0].Text
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	gtSession := "gt-rig-worker"
	for _, message := range []string{"first"} {
		if err := nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: message}); err != nil {
			t.Fatal(err)
		}
	}
	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	delivered, err := deliverQueuedNudges(context.Background(), client, townRoot, gtSession, "ses_test")
	if err != nil || !delivered {
		t.Fatalf("deliverQueuedNudges = %v, %v", delivered, err)
	}
	if !strings.Contains(prompt, "first") || !strings.Contains(prompt, "mayor") {
		t.Fatalf("prompt = %q", prompt)
	}
	if pending, _ := nudge.Pending(townRoot, gtSession); pending != 0 {
		t.Fatalf("pending = %d, want 0", pending)
	}
}

func TestDeliverQueuedNudgesKeepsQueueWhileBusy(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ses_test":{"type":"busy"}}`))
	}))
	defer server.Close()

	townRoot := t.TempDir()
	gtSession := "gt-rig-worker"
	_ = nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: "wait"})
	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	delivered, err := deliverQueuedNudges(context.Background(), client, townRoot, gtSession, "ses_test")
	if err != nil || delivered {
		t.Fatalf("deliverQueuedNudges = %v, %v", delivered, err)
	}
	if pending, _ := nudge.Pending(townRoot, gtSession); pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
}

func TestDeliverQueuedNudgesRequeuesFailedPrompt(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/session/status" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	townRoot := t.TempDir()
	gtSession := "gt-rig-worker"
	_ = nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: "retry"})
	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	delivered, err := deliverQueuedNudges(context.Background(), client, townRoot, gtSession, "ses_test")
	if err == nil || delivered {
		t.Fatalf("deliverQueuedNudges = %v, %v, want failure", delivered, err)
	}
	if pending, _ := nudge.Pending(townRoot, gtSession); pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}
}

func TestDeliverQueuedNudgesRecognizesAcceptedPromptAfterResponseError(t *testing.T) {
	t.Parallel()
	var messageExists bool
	var prompts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session/status":
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/session/ses_test/message/"):
			if !messageExists {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/session/ses_test/prompt_async":
			var body struct {
				MessageID string `json:"messageID"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasPrefix(body.MessageID, "msg") {
				t.Fatalf("messageID = %q", body.MessageID)
			}
			prompts++
			messageExists = true
			http.Error(w, "response lost after acceptance", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	gtSession := "gt-rig-worker"
	if err := nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: "once"}); err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	delivered, err := deliverQueuedNudges(context.Background(), client, townRoot, gtSession, "ses_test")
	if err != nil || !delivered || prompts != 1 {
		t.Fatalf("delivery = %v, %v, prompts=%d", delivered, err, prompts)
	}
	if pending, _ := nudge.Pending(townRoot, gtSession); pending != 0 {
		t.Fatalf("pending = %d, want 0", pending)
	}
}

func TestDeliverQueuedNudgesRecoversExistingMessageWithoutWedging(t *testing.T) {
	t.Parallel()
	var prompts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/session/status":
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(r.URL.Path, "/session/ses_test/message/"):
			_, _ = w.Write([]byte(`{}`))
		case r.URL.Path == "/session/ses_test/prompt_async":
			prompts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	gtSession := "gt-rig-worker"
	if err := nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: "already accepted"}); err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	lifecycle := newSessionLifecycle("ses_test", Status{})
	delivered, err := deliverQueuedNudgesWithLifecycle(context.Background(), client, lifecycle, townRoot, gtSession, "ses_test")
	if err != nil || !delivered || prompts != 0 {
		t.Fatalf("delivery = %v, %v, prompts=%d", delivered, err, prompts)
	}
	if lifecycle.Ready() || !lifecycle.InFlight() {
		t.Fatal("existing message recovery admitted another prompt before status confirmation")
	}
	lifecycle.Reconcile(Status{Type: "idle"})
	if lifecycle.Ready() {
		t.Fatal("one idle check completed recovered prompt")
	}
	lifecycle.Reconcile(Status{Type: "idle"})
	if !lifecycle.Ready() {
		t.Fatal("existing message recovery remained in flight after confirmed idle")
	}
}

func TestDeliverQueuedNudgesWaitsForBusyToIdleTransition(t *testing.T) {
	t.Parallel()
	var prompts int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/session/status":
			_, _ = w.Write([]byte(`{}`))
		case "/session/ses_test/prompt_async":
			prompts++
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	gtSession := "gt-rig-worker"
	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	lifecycle := newSessionLifecycle("ses_test", Status{})

	_ = nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: "first"})
	delivered, err := deliverQueuedNudgesWithLifecycle(context.Background(), client, lifecycle, townRoot, gtSession, "ses_test")
	if err != nil || !delivered {
		t.Fatalf("first delivery = %v, %v", delivered, err)
	}

	_ = nudge.Enqueue(townRoot, gtSession, nudge.QueuedNudge{Sender: "mayor", Message: "second"})
	delivered, err = deliverQueuedNudgesWithLifecycle(context.Background(), client, lifecycle, townRoot, gtSession, "ses_test")
	if err != nil || delivered {
		t.Fatalf("delivery before busy transition = %v, %v", delivered, err)
	}
	if prompts != 1 {
		t.Fatalf("prompts = %d, want 1", prompts)
	}
	if pending, _ := nudge.Pending(townRoot, gtSession); pending != 1 {
		t.Fatalf("pending = %d, want 1", pending)
	}

	lifecycle.Observe(Event{Type: "session.status", Properties: EventProperties{
		SessionID: "ses_test",
		Status:    Status{Type: "busy"},
	}})
	lifecycle.Observe(Event{Type: "session.idle", Properties: EventProperties{SessionID: "ses_test"}})
	delivered, err = deliverQueuedNudgesWithLifecycle(context.Background(), client, lifecycle, townRoot, gtSession, "ses_test")
	if err != nil || !delivered || prompts != 2 {
		t.Fatalf("delivery after idle = %v, %v, prompts=%d", delivered, err, prompts)
	}
}

func TestResolveWorkerSessionResumesSameWorkKey(t *testing.T) {
	t.Parallel()
	var creates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_existing":
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_existing", Directory: "/worktree"})
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			creates++
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_new", Directory: "/worktree"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	opts := WorkerOptions{TownRoot: townRoot, GasTownSession: "gt-rig-worker", Directory: "/worktree", WorkKey: "polecat/worker/gt-123"}
	if err := SaveState(townRoot, State{
		GasTownSession:  opts.GasTownSession,
		OpenCodeSession: "ses_existing",
		WorkKey:         opts.WorkKey,
		Directory:       opts.Directory,
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient(server.URL, "opencode", "secret", opts.Directory, server.Client())
	session, err := resolveWorkerSession(context.Background(), client, opts)
	if err != nil || session.ID != "ses_existing" || creates != 0 {
		t.Fatalf("resolveWorkerSession = %#v, %v, creates=%d", session, err, creates)
	}

	opts.WorkKey = "polecat/worker/gt-456"
	session, err = resolveWorkerSession(context.Background(), client, opts)
	if err != nil || session.ID != "ses_new" || creates != 1 {
		t.Fatalf("new work resolve = %#v, %v, creates=%d", session, err, creates)
	}
}

func TestResolveWorkerSessionDoesNotResumeWithoutWorkKey(t *testing.T) {
	t.Parallel()
	var gets, creates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_existing":
			gets++
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_existing", Directory: "/worktree"})
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			creates++
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_new", Directory: "/worktree"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	opts := WorkerOptions{TownRoot: townRoot, GasTownSession: "gt-rig-worker", Directory: "/worktree"}
	if err := SaveState(townRoot, State{
		GasTownSession:  opts.GasTownSession,
		OpenCodeSession: "ses_existing",
		Directory:       opts.Directory,
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}
	client, _ := NewClient(server.URL, "opencode", "secret", opts.Directory, server.Client())
	session, err := resolveWorkerSession(context.Background(), client, opts)
	if err != nil || session.ID != "ses_new" || gets != 0 || creates != 1 {
		t.Fatalf("resolveWorkerSession = %#v, %v, gets=%d creates=%d", session, err, gets, creates)
	}
}

func TestResolveWorkerSessionReturnsCorruptStateError(t *testing.T) {
	t.Parallel()
	var creates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/session" {
			creates++
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_new", Directory: "/worktree"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	townRoot := t.TempDir()
	opts := WorkerOptions{TownRoot: townRoot, GasTownSession: "gt-rig-worker", Directory: "/worktree", WorkKey: "feature/test"}
	statePath := StatePath(townRoot, opts.GasTownSession)
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}

	client, _ := NewClient(server.URL, "opencode", "secret", opts.Directory, server.Client())
	if _, err := resolveWorkerSession(context.Background(), client, opts); err == nil {
		t.Fatal("resolveWorkerSession silently replaced corrupt durable state")
	}
	if creates != 0 {
		t.Fatalf("creates = %d, want 0", creates)
	}
}

func TestResolveWorkerSessionReturnsTransientLookupError(t *testing.T) {
	t.Parallel()
	var creates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_existing":
			http.Error(w, "temporary failure", http.StatusInternalServerError)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			creates++
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_new", Directory: "/worktree"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	opts := WorkerOptions{TownRoot: townRoot, GasTownSession: "gt-rig-worker", Directory: "/worktree", WorkKey: "feature/test"}
	if err := SaveState(townRoot, State{
		GasTownSession:  opts.GasTownSession,
		OpenCodeSession: "ses_existing",
		WorkKey:         opts.WorkKey,
		Directory:       opts.Directory,
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}

	client, _ := NewClient(server.URL, "opencode", "secret", opts.Directory, server.Client())
	if _, err := resolveWorkerSession(context.Background(), client, opts); err == nil {
		t.Fatal("resolveWorkerSession silently forked on a transient lookup failure")
	}
	if creates != 0 {
		t.Fatalf("creates = %d, want 0", creates)
	}
}

func TestResolveWorkerSessionCreatesWhenPriorSessionIsMissing(t *testing.T) {
	t.Parallel()
	var creates int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/session/ses_missing":
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			creates++
			_ = json.NewEncoder(w).Encode(Session{ID: "ses_new", Directory: "/worktree"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	townRoot := t.TempDir()
	opts := WorkerOptions{TownRoot: townRoot, GasTownSession: "gt-rig-worker", Directory: "/worktree", WorkKey: "feature/test"}
	if err := SaveState(townRoot, State{
		GasTownSession:  opts.GasTownSession,
		OpenCodeSession: "ses_missing",
		WorkKey:         opts.WorkKey,
		Directory:       opts.Directory,
		URL:             server.URL,
		Username:        "opencode",
		Password:        "secret",
	}); err != nil {
		t.Fatal(err)
	}

	client, _ := NewClient(server.URL, "opencode", "secret", opts.Directory, server.Client())
	session, err := resolveWorkerSession(context.Background(), client, opts)
	if err != nil || session.ID != "ses_new" || creates != 1 {
		t.Fatalf("resolveWorkerSession = %#v, %v, creates=%d", session, err, creates)
	}
}
