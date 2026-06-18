package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/gastown/internal/refinery"
)

func TestRefineryStartAgentFlag(t *testing.T) {
	flag := refineryStartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected refinery start to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestRefineryAttachAgentFlag(t *testing.T) {
	flag := refineryAttachCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected refinery attach to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestRefineryRestartAgentFlag(t *testing.T) {
	flag := refineryRestartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected refinery restart to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestRefineryStartForegroundFlagHidden(t *testing.T) {
	flag := refineryStartCmd.Flags().Lookup("foreground")
	if flag == nil {
		t.Fatal("expected hidden compatibility --foreground flag")
	}
	if !flag.Hidden {
		t.Fatal("expected --foreground to be hidden")
	}
	if strings.Contains(refineryStartCmd.Long, "--foreground") {
		t.Fatalf("refinery start help should not advertise --foreground:\n%s", refineryStartCmd.Long)
	}
}

func TestRefineryReadyHumanPrintsAnomaliesWhenNoReadyMRs(t *testing.T) {
	var buf bytes.Buffer
	writeRefineryReadyHuman(&buf, "gastown", nil, []*refinery.MRAnomaly{
		{
			ID:       "gt-mr-orphan",
			Branch:   "polecat/chrome/missing",
			Type:     "orphaned-branch",
			Assignee: "gastown/refinery",
			Age:      2*time.Minute + 3*time.Second,
			Detail:   "branch not found locally or on origin",
		},
	})

	out := buf.String()
	for _, want := range []string{
		"Ready MRs for 'gastown'",
		"(none ready)",
		"Queue anomalies",
		"orphaned-branch",
		"gt-mr-orphan",
		"polecat/chrome/missing",
		"gastown/refinery",
		"2m3s",
		"branch not found locally or on origin",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("ready output missing %q:\n%s", want, out)
		}
	}
}
