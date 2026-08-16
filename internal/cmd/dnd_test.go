package cmd

import (
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

func setupDndTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("bd", "beads")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestAddressToAgentBeadID(t *testing.T) {
	setupDndTestRegistry(t)
	tests := []struct {
		address  string
		expected string
	}{
		// Mayor and deacon use hq- prefix (town-level)
		{"mayor", "hq-mayor"},
		{"mayor/", "hq-mayor"},
		{"deacon", "hq-deacon"},
		{"deacon/", "hq-deacon"},
		{"deacon/dogs/alpha", "hq-dog-alpha"},
		{"deacon/dogs/my-dog", "hq-dog-my-dog"},
		{"gastown/witness", "gt-witness"},
		{"gastown/refinery", "gt-refinery"},
		{"gastown/alpha", "gt-alpha"},
		{"gastown/crew/max", "gt-crew-max"},
		{"gastown/polecats/alpha", "gt-alpha"},
		{"beads/polecats/beta", "bd-beta"},
		{"beads/witness", "bd-witness"},
		{"beads/beta", "bd-beta"},
		// Invalid addresses should return empty string
		{"deacon/dogs", ""},
		{"deacon/dogs/", ""},
		{"deacon/dogs/alpha/extra", ""},
		{"deacon/foo", ""},
		{"deaconer", ""},
		{"mayor/foo", ""},
		{"mayorer", ""},
		{"invalid", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.address, func(t *testing.T) {
			got := addressToAgentBeadID(tt.address)
			if got != tt.expected {
				t.Errorf("addressToAgentBeadID(%q) = %q, want %q", tt.address, got, tt.expected)
			}
		})
	}
}
