//go:build windows

package testutil

import (
	"fmt"
	"testing"
)

// RequireManagedDoltEndpoint requires an externally managed endpoint, which
// is not provisioned by the Windows CI environment.
func RequireManagedDoltEndpoint(t *testing.T) string {
	t.Helper()
	t.Skip("managed Dolt endpoint not configured on Windows CI")
	return ""
}

// EnsureManagedDoltEndpointForTestMain reports the missing managed endpoint.
func EnsureManagedDoltEndpointForTestMain() error {
	return fmt.Errorf("managed Dolt endpoint not configured on Windows CI")
}

// ManagedDoltAddr returns empty string on Windows.
func ManagedDoltAddr() string { return "" }

// ManagedDoltPort returns empty string on Windows.
func ManagedDoltPort() string { return "" }
