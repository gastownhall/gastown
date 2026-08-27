//go:build !windows

package testutil

import (
	"testing"
)

func TestConfiguredDoltEndpointRequiresConfiguredPort(t *testing.T) {
	t.Setenv("GT_DOLT_HOST", "127.0.0.2")
	t.Setenv("GT_DOLT_PORT", "")
	t.Setenv("BEADS_DOLT_PORT", "")

	host, port := configuredDoltEndpoint()
	if host != "127.0.0.2" || port != "" {
		t.Fatalf("configuredDoltEndpoint() = %q:%q, want host with no invented port", host, port)
	}
}

func TestConfiguredDoltEndpointUsesManagedEnvironment(t *testing.T) {
	t.Setenv("GT_DOLT_HOST", "127.0.0.3")
	t.Setenv("GT_DOLT_PORT", "4319")
	t.Setenv("BEADS_DOLT_PORT", "9876")

	host, port := configuredDoltEndpoint()
	if host != "127.0.0.3" || port != "4319" {
		t.Fatalf("configuredDoltEndpoint() = %q:%q, want configured GT endpoint", host, port)
	}
}
