package cmd

import (
	"strings"
	"testing"
)

func TestWitnessRestartAgentFlag(t *testing.T) {
	flag := witnessRestartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected witness restart to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestWitnessStartAgentFlag(t *testing.T) {
	flag := witnessStartCmd.Flags().Lookup("agent")
	if flag == nil {
		t.Fatal("expected witness start to define --agent flag")
	}
	if flag.DefValue != "" {
		t.Errorf("expected default agent override to be empty, got %q", flag.DefValue)
	}
	if !strings.Contains(flag.Usage, "overrides town default") {
		t.Errorf("expected --agent usage to mention overrides town default, got %q", flag.Usage)
	}
}

func TestWitnessStartForegroundFlagHidden(t *testing.T) {
	flag := witnessStartCmd.Flags().Lookup("foreground")
	if flag == nil {
		t.Fatal("expected hidden compatibility --foreground flag")
	}
	if !flag.Hidden {
		t.Fatal("expected --foreground to be hidden")
	}
	if strings.Contains(witnessStartCmd.Long, "--foreground") {
		t.Fatalf("witness start help should not advertise --foreground:\n%s", witnessStartCmd.Long)
	}
}
