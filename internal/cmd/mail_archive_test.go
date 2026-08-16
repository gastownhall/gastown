package cmd

import (
	"testing"
	"time"

	"github.com/jonbaldie/gastown/internal/mail"
)

func TestStaleMessagesForSession(t *testing.T) {
	sessionStart := time.Date(2026, 1, 24, 2, 0, 0, 0, time.UTC)
	messages := []*mail.Message{
		{ID: "msg-1", Subject: "Older", Timestamp: sessionStart.Add(-2 * time.Minute)},
		{ID: "msg-2", Subject: "Newer", Timestamp: sessionStart.Add(2 * time.Minute)},
		{ID: "msg-3", Subject: "Equal", Timestamp: sessionStart},
	}

	stale := staleMessagesForSession(messages, sessionStart)
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale message, got %d", len(stale))
	}
	if stale[0].Message.ID != "msg-1" {
		t.Fatalf("expected msg-1 stale, got %s", stale[0].Message.ID)
	}
}
