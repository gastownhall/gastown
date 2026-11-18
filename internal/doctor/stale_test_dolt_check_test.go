package doctor

import (
	"testing"

	"github.com/steveyegge/gastown/internal/doltserver"
)

func TestStaleTestDoltCheckThreshold(t *testing.T) {
	original := staleOwnedTestServersFn
	t.Cleanup(func() { staleOwnedTestServersFn = original })

	staleOwnedTestServersFn = func() []doltserver.TestServerOwnerMetadata {
		return make([]doltserver.TestServerOwnerMetadata, staleTestDoltErrorThreshold)
	}
	if got := NewStaleTestDoltCheck().Run(&CheckContext{}); got.Status != StatusWarning {
		t.Fatalf("threshold status = %v, want warning", got.Status)
	}

	staleOwnedTestServersFn = func() []doltserver.TestServerOwnerMetadata {
		return make([]doltserver.TestServerOwnerMetadata, staleTestDoltErrorThreshold+1)
	}
	if got := NewStaleTestDoltCheck().Run(&CheckContext{}); got.Status != StatusError {
		t.Fatalf("above-threshold status = %v, want error", got.Status)
	}
}
