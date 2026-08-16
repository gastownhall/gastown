//go:build windows

package cmd

import (
	"fmt"

	"github.com/jonbaldie/gastown/internal/style"
)

// cleanupOrphanedClaude is a Windows stub.
// Orphan cleanup requires Unix-specific signals (SIGTERM/SIGKILL).
func cleanupOrphanedClaude(graceSecs int) {
	fmt.Printf("  %s Orphan cleanup not supported on Windows\n",
		style.Dim.Render("○"))
}

// verifyNoOrphans is a Windows stub.
func verifyNoOrphans() {
	fmt.Printf("  %s Orphan verification not supported on Windows\n",
		style.Dim.Render("○"))
}
