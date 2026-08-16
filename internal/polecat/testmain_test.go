package polecat

import (
	"os"
	"testing"

	"github.com/jonbaldie/gastown/internal/testutil"
)

func TestMain(m *testing.M) {
	code := m.Run()
	testutil.TerminateDoltContainer()
	os.Exit(code)
}
