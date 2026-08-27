//go:build !windows

package testutil

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// RequireManagedDoltEndpoint requires an explicitly configured, externally
// managed Gas Town endpoint. It never creates a listener.
func RequireManagedDoltEndpoint(t *testing.T) string {
	t.Helper()
	if err := EnsureManagedDoltEndpointForTestMain(); err != nil {
		t.Skip(err)
	}
	_, port := configuredDoltEndpoint()
	return port
}

// EnsureManagedDoltEndpointForTestMain adopts the configured GT endpoint. It never
// starts a container or local sql-server, and therefore cannot allocate an
// alternate test port or leak a server after the test process exits.
func EnsureManagedDoltEndpointForTestMain() error {
	if strings.TrimSpace(os.Getenv("GT_TEST_EXTERNAL_DOLT")) == "" {
		return fmt.Errorf("Dolt tests require explicit GT_TEST_EXTERNAL_DOLT=1; no test-owned server will be started")
	}
	host, port := configuredDoltEndpoint()
	if port == "" {
		return fmt.Errorf("Dolt tests require GT_DOLT_PORT or BEADS_DOLT_PORT from the managed Gas Town endpoint")
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
	if err != nil {
		return fmt.Errorf("managed Gas Town Dolt at %s is unreachable: %w", net.JoinHostPort(host, port), err)
	}
	_ = conn.Close()
	return nil
}

func configuredDoltEndpoint() (host, port string) {
	host = strings.TrimSpace(os.Getenv("GT_DOLT_HOST"))
	if host == "" {
		host = "127.0.0.1"
	}
	if port := strings.TrimSpace(os.Getenv("GT_DOLT_PORT")); port != "" {
		return host, port
	}
	if port := strings.TrimSpace(os.Getenv("BEADS_DOLT_PORT")); port != "" {
		return host, port
	}
	return host, ""
}

func ManagedDoltAddr() string {
	host, port := configuredDoltEndpoint()
	return net.JoinHostPort(host, port)
}

func ManagedDoltPort() string {
	_, port := configuredDoltEndpoint()
	return port
}
