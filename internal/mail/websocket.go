package mail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// PushNotification is the JSON payload written to the notification spool
// for cross-process delivery to dashboard WebSocket clients.
type PushNotification struct {
	Type      string `json:"type"`              // "nudge", "mail", "escalation"
	From      string `json:"from"`              // sender identity
	To        string `json:"to"`                // recipient address
	Subject   string `json:"subject"`           // subject line
	Body      string `json:"body"`              // full message body
	Priority  string `json:"priority"`          // urgency level
	ThreadID  string `json:"threadId"`          // associated bead/thread ID
	CreatedAt string `json:"createdAt"`         // ISO 8601 timestamp
}

// SpoolPath returns the path of the notification spool file in the town root.
// The spool is the cross-process bridge: CLI processes append notifications,
// the dashboard process reads and drains them.
func SpoolPath(townRoot string) string {
	return filepath.Join(townRoot, ".gt", "notifications.spool")
}

// SpoolPush appends a notification to the spool file for the dashboard
// process to relay to WebSocket clients. Best-effort: spool failures never
// block mail delivery (the mail bead in Dolt is the durable record).
func SpoolPush(townRoot string, n *PushNotification) error {
	if townRoot == "" {
		return nil
	}
	if n.CreatedAt == "" {
		n.CreatedAt = time.Now().Format(time.RFC3339)
	}
	data, err := json.Marshal(n)
	if err != nil {
		return err
	}
	spool := SpoolPath(townRoot)
	if err := os.MkdirAll(filepath.Dir(spool), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(spool, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}
