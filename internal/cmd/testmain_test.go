//go:build !integration

package cmd

import (
	"testing"

	"github.com/steveyegge/gastown/internal/testutil/hermetic"
)

// The integration build has its own TestMain.
func TestMain(m *testing.M) { hermetic.Main(m) }
