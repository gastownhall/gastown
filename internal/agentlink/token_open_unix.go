//go:build !windows

package agentlink

import (
	"os"

	"golang.org/x/sys/unix"
)

// openNoFollow opens path read-only, refusing a symlink at path.
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NOFOLLOW, 0)
}
