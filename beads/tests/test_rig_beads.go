package beads

import (
	"testing"
)

func TestRigBeads(t *testing.T) {
	// Test that rig beads are durable
	bead := Bead{
		Type: "rig",
		Data: []byte("test data"),
	}
	if !IsDurable(bead) {
		t.Errorf("rig beads should be durable")
	}
}
