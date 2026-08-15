package cmd

import (
	"strings"
	"testing"
)

func TestMayorStartHelpDescribesConfiguredAgent(t *testing.T) {
	if strings.Contains(mayorStartCmd.Long, "launches Claude") {
		t.Fatalf("mayor start help is agent-specific: %q", mayorStartCmd.Long)
	}
	if !strings.Contains(mayorStartCmd.Long, "configured agent") {
		t.Fatalf("mayor start help does not describe configured-agent behavior: %q", mayorStartCmd.Long)
	}
}
