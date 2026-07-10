package cmd

import (
	"strings"
	"testing"
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

func TestRefineryStartForceFlag(t *testing.T) {
	flag := refineryStartCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("expected refinery start to define --force flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("expected default force to be false, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "upstream_url") {
		t.Errorf("expected --force usage to mention upstream_url, got %q", flag.Usage)
	}
}

func TestRefineryRestartForceFlag(t *testing.T) {
	flag := refineryRestartCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("expected refinery restart to define --force flag")
	}
	if flag.DefValue != "false" {
		t.Errorf("expected default force to be false, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "upstream_url") {
		t.Errorf("expected --force usage to mention upstream_url, got %q", flag.Usage)
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
