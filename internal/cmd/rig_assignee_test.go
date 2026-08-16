package cmd

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

func setupAssigneeTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	reg.Register("st", "schema_tools")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestAssigneeToSessionName(t *testing.T) {
	setupAssigneeTestRegistry(t)
	tests := []struct {
		name           string
		assignee       string
		wantSession    string
		wantPersistent bool
	}{
		{
			name:           "two part polecat",
			assignee:       "schema_tools/nux",
			wantSession:    "st-nux",
			wantPersistent: false,
		},
		{
			name:           "three part crew",
			assignee:       "schema_tools/crew/fiddler",
			wantSession:    "st-crew-fiddler",
			wantPersistent: true,
		},
		{
			name:           "three part polecats",
			assignee:       "schema_tools/polecats/nux",
			wantSession:    "st-nux",
			wantPersistent: false,
		},
		{
			name:           "unknown three part role",
			assignee:       "schema_tools/refinery/rig",
			wantSession:    "",
			wantPersistent: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotSession, gotPersistent := assigneeToSessionName(tc.assignee)
			if gotSession != tc.wantSession {
				t.Fatalf("session = %q, want %q", gotSession, tc.wantSession)
			}
			if gotPersistent != tc.wantPersistent {
				t.Fatalf("persistent = %v, want %v", gotPersistent, tc.wantPersistent)
			}
		})
	}
}
