package cigate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractTicket(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{"jira line with title", "attached_molecule: x\n\nJira: AA-851 — Implement the HARD GATE", "AA-851"},
		{"jira line bare", "some text\nJira: AA-851\nmore", "AA-851"},
		{"ticket alias", "Ticket: proj-42", "PROJ-42"},
		{"lowercase key", "jira: aa-99", "AA-99"},
		{"no line", "just a description", ""},
		{"empty", "", ""},
		{"key not on own line", "fixes Jira: AA-1 inline", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ExtractTicket(tt.desc); got != tt.want {
				t.Errorf("ExtractTicket(%q) = %q, want %q", tt.desc, got, tt.want)
			}
		})
	}
}

func TestRunEscalationCmd(t *testing.T) {
	t.Run("passes context via environment", func(t *testing.T) {
		outFile := filepath.Join(t.TempDir(), "esc.txt")
		cmd := `echo "$GT_CIGATE_EVENT|$GT_CIGATE_TICKET|$GT_CIGATE_BRANCH|$GT_CIGATE_AGENT" > ` + outFile
		err := RunEscalationCmd(cmd, t.TempDir(), Escalation{
			Event:  EventPendingTimeout,
			Ticket: "AA-851",
			Branch: "polecat/furiosa",
			Agent:  "openclaw/polecats/furiosa",
			Detail: "CI pending too long",
		})
		if err != nil {
			t.Fatalf("RunEscalationCmd: %v", err)
		}
		data, err := os.ReadFile(outFile)
		if err != nil {
			t.Fatalf("reading output: %v", err)
		}
		got := strings.TrimSpace(string(data))
		want := "pending_timeout|AA-851|polecat/furiosa|openclaw/polecats/furiosa"
		if got != want {
			t.Errorf("escalation env = %q, want %q", got, want)
		}
	})

	t.Run("empty command errors", func(t *testing.T) {
		if err := RunEscalationCmd("  ", ".", Escalation{}); err == nil {
			t.Error("want error for empty escalation_cmd")
		}
	})

	t.Run("failing command returns error with output", func(t *testing.T) {
		err := RunEscalationCmd("echo tracker down >&2; exit 3", ".", Escalation{Event: EventCIStatusError})
		if err == nil || !strings.Contains(err.Error(), "tracker down") {
			t.Errorf("want failure containing command output, got %v", err)
		}
	})
}
