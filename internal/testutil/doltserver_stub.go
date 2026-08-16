//go:build !integration

package testutil

import (
	"fmt"
	"testing"
)

const integrationTagRequired = "Dolt test containers require -tags=integration"

// StartIsolatedDoltContainer skips when the integration tag is off so default
// `go test` does not compile testcontainers.
func StartIsolatedDoltContainer(t *testing.T) string {
	t.Helper()
	t.Skip(integrationTagRequired)
	return ""
}

// EnsureDoltContainerForTestMain reports that the integration tag is required.
// Callers that treat this as optional keep running non-Dolt tests.
func EnsureDoltContainerForTestMain() error {
	return fmt.Errorf("%s", integrationTagRequired)
}

func RequireDoltContainer(t *testing.T) {
	t.Helper()
	t.Skip(integrationTagRequired)
}

func DoltContainerAddr() string { return "" }

func DoltContainerPort() string { return "" }

func TerminateDoltContainer() {}

func DoltContainersEnabled() bool { return false }
