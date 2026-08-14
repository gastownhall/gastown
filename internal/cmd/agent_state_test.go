package cmd

import (
	"strings"
	"testing"
)

func TestParseAgentBeadLabels(t *testing.T) {
	tests := []struct {
		name       string
		stdout     []byte
		stderr     []byte
		agentBead  string
		wantLabels []string
		wantErr    string
	}{
		{
			name:       "valid response with labels",
			stdout:     []byte(`[{"id":"gt-test","labels":["idle:3","gt:agent"]}]`),
			stderr:     nil,
			agentBead:  "gt-test",
			wantLabels: []string{"idle:3", "gt:agent"},
			wantErr:    "",
		},
		{
			name:       "valid response with no labels",
			stdout:     []byte(`[{"id":"gt-test","labels":[]}]`),
			stderr:     nil,
			agentBead:  "gt-test",
			wantLabels: []string{},
			wantErr:    "",
		},
		{
			name:       "valid response with null labels",
			stdout:     []byte(`[{"id":"gt-test","labels":null}]`),
			stderr:     nil,
			agentBead:  "gt-test",
			wantLabels: nil,
			wantErr:    "",
		},
		{
			name:      "empty stdout with stderr",
			stdout:    []byte{},
			stderr:    []byte("database mismatch: client expects dolt but daemon has different backend"),
			agentBead: "gt-test",
			wantErr:   "database mismatch",
		},
		{
			name:      "empty stdout without stderr",
			stdout:    []byte{},
			stderr:    nil,
			agentBead: "gt-test",
			wantErr:   "agent bead query returned no output: gt-test",
		},
		{
			name:      "empty array response",
			stdout:    []byte(`[]`),
			stderr:    nil,
			agentBead: "gt-test",
			wantErr:   "agent bead not found: gt-test",
		},
		{
			name:      "invalid JSON",
			stdout:    []byte(`{not valid json`),
			stderr:    nil,
			agentBead: "gt-test",
			wantErr:   "parsing agent bead response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels, err := parseAgentBeadLabels(tt.stdout, tt.stderr, tt.agentBead)

			if tt.wantErr != "" {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.wantErr)
					return
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if len(labels) != len(tt.wantLabels) {
				t.Errorf("got %d labels, want %d", len(labels), len(tt.wantLabels))
				return
			}

			for i, label := range labels {
				if label != tt.wantLabels[i] {
					t.Errorf("labels[%d] = %q, want %q", i, label, tt.wantLabels[i])
				}
			}
		})
	}
}
