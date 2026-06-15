package gastown

import (
	"testing"
)

func TestWitnessStartup(t *testing.T) {
	// Test that witness startup succeeds
	if err := StartWitness(); err != nil {
		t.Errorf("witness startup failed: %v", err)
	}
}
