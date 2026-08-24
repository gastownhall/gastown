package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	mailtest "github.com/steveyegge/gastown/internal/mail"
)

func TestHub_Notify_BroadcastsToClients(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r)
	}))
	defer srv.Close()

	wsURL := "ws" + srv.URL[4:] + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("could not open ws: %v", err)
	}
	defer ws.Close()

	// Give the hub time to register the client
	time.Sleep(50 * time.Millisecond)

	if hub.ClientCount() != 1 {
		t.Fatalf("expected 1 client, got %d", hub.ClientCount())
	}

	msg := &NotificationMessage{
		Type:    "nudge",
		From:    "mayor",
		Subject: "test subject",
		Body:    "test body",
		Priority: "high",
	}
	hub.Notify(msg)

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("could not read ws message: %v", err)
	}

	var received NotificationMessage
	if err := json.Unmarshal(data, &received); err != nil {
		t.Fatalf("could not unmarshal message: %v", err)
	}

	if received.Type != msg.Type {
		t.Errorf("Type = %q, want %q", received.Type, msg.Type)
	}
	if received.Subject != msg.Subject {
		t.Errorf("Subject = %q, want %q", received.Subject, msg.Subject)
	}
	if received.Body != msg.Body {
		t.Errorf("Body = %q, want %q", received.Body, msg.Body)
	}
	if received.From != msg.From {
		t.Errorf("From = %q, want %q", received.From, msg.From)
	}
	if received.CreatedAt == "" {
		t.Error("Expected non-empty CreatedAt")
	}
}

func TestHub_Notify_NoClients_DoesNotBlock(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	msg := &NotificationMessage{
		Type:  "escalation",
		From:  "mayor",
		Body:  "critical alert",
		Priority: "critical",
	}

	done := make(chan struct{})
	go func() {
		hub.Notify(msg)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Notify blocked when no clients connected")
	}
}

func TestHub_ClientCount_ZeroInitially(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	if hub.ClientCount() != 0 {
		t.Errorf("expected 0 clients, got %d", hub.ClientCount())
	}
}

func TestHub_DrainSpool_BroadcastsCliOriginatedNotifications(t *testing.T) {
	hub := NewHub()
	go hub.Run()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r)
	}))
	defer srv.Close()

	wsURL := "ws" + srv.URL[4:] + "/ws"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("could not open ws: %v", err)
	}
	defer ws.Close()

	time.Sleep(50 * time.Millisecond)

	// Simulate a CLI process writing to the spool (cross-process path).
	dir := t.TempDir()
	if err := mailtest.SpoolPush(dir, &mailtest.PushNotification{
		Type:    "mail",
		From:    "mayor",
		To:      "overseer",
		Subject: "spool relay",
		Body:    "cross-process body",
	}); err != nil {
		t.Fatalf("SpoolPush: %v", err)
	}

	// Drain like the dashboard process does.
	hub.DrainSpool(mailtest.SpoolPath(dir))

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, data, err := ws.ReadMessage()
	if err != nil {
		t.Fatalf("could not read ws message: %v", err)
	}

	var received NotificationMessage
	if err := json.Unmarshal(data, &received); err != nil {
		t.Fatalf("could not unmarshal message: %v", err)
	}
	if received.Subject != "spool relay" {
		t.Errorf("Subject = %q, want %q", received.Subject, "spool relay")
	}
	if received.To != "overseer" {
		t.Errorf("To = %q, want %q", received.To, "overseer")
	}

	// Spool must be drained (truncated to zero).
	info, err := os.Stat(mailtest.SpoolPath(dir))
	if err != nil {
		t.Fatalf("stat spool: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("spool size after drain = %d, want 0", info.Size())
	}
}
