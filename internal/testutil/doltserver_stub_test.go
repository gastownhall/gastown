//go:build !integration

package testutil

import "testing"

func TestContainerStubsWithoutIntegrationTag(t *testing.T) {
	if err := EnsureDoltContainerForTestMain(); err == nil {
		t.Fatal("EnsureDoltContainerForTestMain() must fail without -tags=integration")
	}
	if DoltContainersEnabled() {
		t.Fatal("DoltContainersEnabled() must be false without -tags=integration")
	}
	if got := DoltContainerPort(); got != "" {
		t.Fatalf("DoltContainerPort() = %q, want empty", got)
	}
	if got := DoltContainerAddr(); got != "" {
		t.Fatalf("DoltContainerAddr() = %q, want empty", got)
	}
	TerminateDoltContainer()
}

func TestSkipWithoutDoltContainerSkipsWithoutIntegrationTag(t *testing.T) {
	t.Run("skip", func(t *testing.T) {
		SkipWithoutDoltContainer(t)
		t.Fatal("SkipWithoutDoltContainer should have skipped")
	})
}

func TestRequireDoltContainerSkipsWithoutIntegrationTag(t *testing.T) {
	t.Run("require", func(t *testing.T) {
		RequireDoltContainer(t)
		t.Fatal("RequireDoltContainer should have skipped")
	})
	t.Run("isolated", func(t *testing.T) {
		_ = StartIsolatedDoltContainer(t)
		t.Fatal("StartIsolatedDoltContainer should have skipped")
	})
}
