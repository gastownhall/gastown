package gastown

import (
	"testing"
)

func TestRigDB(t *testing.T) {
	// Test that rig DB is durable
	if !DBTypeSettings["rig"].Durable {
		t.Errorf("rig DB should be durable")
	}
}
