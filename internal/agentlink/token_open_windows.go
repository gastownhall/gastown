//go:build windows

package agentlink

import "os"

// openNoFollow opens path read-only. Windows has no O_NOFOLLOW; the mode and
// regular-file checks on the open descriptor still apply.
func openNoFollow(path string) (*os.File, error) {
	return os.Open(path)
}
