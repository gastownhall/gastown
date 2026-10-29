//go:build !windows

package agentlink

import (
	"fmt"
	"os"
	"syscall"
)

// flockAcquire opens (creating with 0600) the lock file at path and takes an
// exclusive advisory flock on it, blocking until it is free. The returned
// func releases the lock and closes the file. It follows internal/lock's
// FlockAcquire; it is duplicated here so agentlink, which the server also
// imports, does not depend on internal/lock's tmux and config imports.
//
// flock locks belong to the open file description, so two acquisitions in
// one process through separate opens exclude each other as well as other
// processes.
func flockAcquire(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: internal lock file
	if err != nil {
		return nil, fmt.Errorf("open lock %s: %w", path, err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("flock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
