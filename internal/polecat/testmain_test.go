package polecat

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	// Tests may exercise production paths that invoke a real bd subprocess
	// after temporary PATH mocks have gone out of scope. Fail closed: bd must
	// never auto-start a test-owned Dolt server. Dolt-dependent tests explicitly
	// adopt the externally managed Gas Town endpoint explicitly.
	if err := os.Setenv("BEADS_DOLT_AUTO_START", "0"); err != nil {
		os.Exit(1)
	}
	code := m.Run()
	os.Exit(code)
}
