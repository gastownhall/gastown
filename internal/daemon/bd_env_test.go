package daemon

import (
	"strings"
	"testing"
)

// TestBdReadOnlyEnv verifies that bdReadOnlyEnv returns an environment slice
// containing exactly one BD_DOLT_AUTO_COMMIT=off entry, regardless of any
// pre-existing BD_DOLT_AUTO_COMMIT in the parent process env.
func TestBdReadOnlyEnv(t *testing.T) {
	tests := []struct {
		name    string
		preset  string
		setting bool
	}{
		{name: "unset parent", setting: false},
		{name: "parent has off", preset: "off", setting: true},
		{name: "parent has on", preset: "on", setting: true},
		{name: "parent has stale value", preset: "batched", setting: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setting {
				t.Setenv("BD_DOLT_AUTO_COMMIT", tc.preset)
			} else {
				t.Setenv("BD_DOLT_AUTO_COMMIT", "")
			}

			env := bdReadOnlyEnv()

			var count int
			var value string
			for _, e := range env {
				if strings.HasPrefix(e, "BD_DOLT_AUTO_COMMIT=") {
					count++
					value = strings.TrimPrefix(e, "BD_DOLT_AUTO_COMMIT=")
				}
			}

			if count != 1 {
				t.Errorf("found %d BD_DOLT_AUTO_COMMIT entries, want exactly 1", count)
			}
			if value != "off" {
				t.Errorf("BD_DOLT_AUTO_COMMIT = %q, want %q", value, "off")
			}
		})
	}
}
