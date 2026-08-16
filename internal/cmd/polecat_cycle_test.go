package cmd

import (
	"reflect"
	"testing"

	"github.com/jonbaldie/gastown/internal/session"
)

func setupPolecatTestRegistry(t *testing.T) {
	t.Helper()
	reg := session.NewPrefixRegistry()
	reg.Register("gt", "gastown")
	reg.Register("gp", "greenplace")
	reg.Register("bd", "beads")
	reg.Register("mr", "my-rig")
	old := session.DefaultRegistry()
	session.SetDefaultRegistry(reg)
	t.Cleanup(func() { session.SetDefaultRegistry(old) })
}

func TestParsePolecatSessionName(t *testing.T) {
	setupPolecatTestRegistry(t)
	tests := []struct {
		name        string
		sessionName string
		wantRig     string
		wantPolecat string
		wantOk      bool
	}{
		// Valid polecat sessions (using rig prefixes)
		{
			name:        "simple polecat",
			sessionName: "gp-Toast",
			wantRig:     "greenplace",
			wantPolecat: "Toast",
			wantOk:      true,
		},
		{
			name:        "another polecat",
			sessionName: "gp-Nux",
			wantRig:     "greenplace",
			wantPolecat: "Nux",
			wantOk:      true,
		},
		{
			name:        "polecat in different rig",
			sessionName: "bd-Worker",
			wantRig:     "beads",
			wantPolecat: "Worker",
			wantOk:      true,
		},
		{
			name:        "hyphenated rig name",
			sessionName: "mr-Toast",
			wantRig:     "my-rig",
			wantPolecat: "Toast",
			wantOk:      true,
		},

		// Not polecat sessions (should return false)
		{
			name:        "crew session",
			sessionName: "gp-crew-jack",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "witness session",
			sessionName: "gp-witness",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "refinery session",
			sessionName: "gp-refinery",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "mayor session",
			sessionName: "hq-mayor",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "deacon session",
			sessionName: "hq-deacon",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "no known prefix",
			sessionName: "plaintext",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "empty string",
			sessionName: "",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "just gt prefix",
			sessionName: "gt-",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
		{
			name:        "no name after rig",
			sessionName: "gp-",
			wantRig:     "",
			wantPolecat: "",
			wantOk:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRig, gotPolecat, gotOk := parsePolecatSessionName(tt.sessionName)
			if gotRig != tt.wantRig || gotPolecat != tt.wantPolecat || gotOk != tt.wantOk {
				t.Errorf("parsePolecatSessionName(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tt.sessionName, gotRig, gotPolecat, gotOk, tt.wantRig, tt.wantPolecat, tt.wantOk)
			}
		})
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "simple lines",
			input: "a\nb\nc",
			want:  []string{"a", "b", "c"},
		},
		{
			name:  "trailing newline filtered",
			input: "a\nb\n",
			want:  []string{"a", "b"},
		},
		{
			name:  "multiple trailing newlines filtered",
			input: "a\n\n\n",
			want:  []string{"a"},
		},
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "single line no newline",
			input: "hello",
			want:  []string{"hello"},
		},
		{
			name:  "only newlines",
			input: "\n\n\n",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitLines(tt.input)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("splitLines(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}
