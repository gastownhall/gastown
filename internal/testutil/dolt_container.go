package testutil

import "testing"

// SkipWithoutDoltContainer skips tests that need the shared Dolt test
// container. Default `go test` (no integration tag) always skips. With
// -tags=integration, tests skip when Docker did not start a container.
func SkipWithoutDoltContainer(t *testing.T) {
	t.Helper()
	if !DoltContainersEnabled() {
		t.Skip("Dolt test container not running; need -tags=integration and Docker")
	}
}
