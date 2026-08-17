package cmd

import (
	"strings"
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

func TestPeekSessionNameAcceptsLongPolecatPath(t *testing.T) {
	// UAT: status/mail/sling identities use rig/polecats/name. Peek must
	// resolve that long form to the same session as the documented short form.
	short, err := peekSessionName("go_envconfig/rust")
	if err != nil {
		t.Fatalf("short form: %v", err)
	}
	long, err := peekSessionName("go_envconfig/polecats/rust")
	if err != nil {
		t.Fatalf("long form: %v", err)
	}
	if long != short {
		t.Fatalf("long polecat path resolved to %q, want same as short form %q", long, short)
	}
	want := session.PolecatSessionName(session.PrefixFor("go_envconfig"), "rust")
	if short != want {
		t.Fatalf("short form resolved to %q, want %q", short, want)
	}
}

func TestPeekSessionNameAliases(t *testing.T) {
	prefix := session.PrefixFor("isatty")
	tests := []struct {
		address string
		want    string
	}{
		{"isatty/crew/dave", session.CrewSessionName(prefix, "dave")},
		{"isatty/witness", session.WitnessSessionName(prefix)},
		{"isatty/refinery", session.RefinerySessionName(prefix)},
		{"mayor", session.MayorSessionName()},
		{"hq/mayor", session.MayorSessionName()},
		{"deacon", session.DeaconSessionName()},
		{"boot", session.BootSessionName()},
	}
	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got, err := peekSessionName(tt.address)
			if err != nil {
				t.Fatalf("peekSessionName(%q): %v", tt.address, err)
			}
			if got != tt.want {
				t.Fatalf("peekSessionName(%q) = %q, want %q", tt.address, got, tt.want)
			}
		})
	}
}

func TestPeekHelpMentionsLongPolecatForm(t *testing.T) {
	if !strings.Contains(peekCmd.Long, "rig/polecats/name") {
		t.Fatalf("help should mention the long polecat form:\n%s", peekCmd.Long)
	}
	if !strings.Contains(peekCmd.Long, "greenplace/polecats/furiosa") {
		t.Fatalf("help should show a long polecat example:\n%s", peekCmd.Long)
	}
}
