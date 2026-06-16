//go:build !integration

package cmd

import (
	"os"
	"os/exec"
	"testing"
)

func TestMain(m *testing.M) {
	resolveBdPath = func() (string, error) { return exec.LookPath("bd") }
	os.Exit(m.Run())
}
