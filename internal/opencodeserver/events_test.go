package opencodeserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEventsParsesSessionLifecycle(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/event" {
			http.NotFound(w, r)
			return
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "opencode" || password != "secret" {
			t.Fatalf("BasicAuth = %q/%q/%v", user, password, ok)
		}
		if r.Header.Get("X-OpenCode-Directory") != "/worktree" {
			t.Fatalf("directory header = %q", r.Header.Get("X-OpenCode-Directory"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, ": connected\r\n\r\n")
		fmt.Fprint(w, "data: {\"type\":\"session.status\",\r\n")
		fmt.Fprint(w, "data: \"properties\":{\"sessionID\":\"ses_test\",\"status\":{\"type\":\"busy\"}}}\r\n\r\n")
		fmt.Fprint(w, "data: {\"type\":\"session.idle\",\"properties\":{\"sessionID\":\"ses_test\"}}\r\n\r\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "opencode", "secret", "/worktree", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := client.Events(ctx)
	if err != nil {
		cancel()
		t.Fatalf("Events: %v", err)
	}
	defer func() {
		cancel()
		stream.Close()
	}()

	busy := <-stream.Events()
	if busy.Type != "session.status" || busy.SessionID() != "ses_test" || busy.Status().Type != "busy" {
		t.Fatalf("busy event = %#v", busy)
	}
	idle := <-stream.Events()
	if idle.Type != "session.idle" || idle.SessionID() != "ses_test" || !idle.Status().Idle() {
		t.Fatalf("idle event = %#v", idle)
	}
}

func TestEventsReportsOversizedEvent(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", string(make([]byte, maxEventSize+1)))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case err := <-stream.Errors():
		if err == nil {
			t.Fatal("event stream returned nil error")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for oversized event error")
	}
}

func TestEventsReportsInvalidJSON(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: not-json\n\n")
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, err := client.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	select {
	case err := <-stream.Errors():
		if err == nil {
			t.Fatal("event stream returned nil error")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for invalid event error")
	}
}

func TestEventsRejectsWrongContentType(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client, _ := NewClient(server.URL, "opencode", "secret", t.TempDir(), server.Client())
	if stream, err := client.Events(context.Background()); err == nil {
		stream.Close()
		t.Fatal("Events accepted non-SSE response")
	}
}
