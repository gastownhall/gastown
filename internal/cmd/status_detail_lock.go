package cmd

import (
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

func tryStatusDetailLock(townRoot string) (func(), bool) {
	if townRoot == "" {
		return func() {}, true
	}

	dir := filepath.Join(townRoot, "daemon")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return func() {}, false
	}

	fileLock := flock.New(filepath.Join(dir, "status-detail.lock"))
	locked, err := fileLock.TryLock()
	if err != nil || !locked {
		return func() {}, false
	}

	return func() { _ = fileLock.Unlock() }, true
}
