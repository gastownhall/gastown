//go:build windows

package atomicfile

import (
	"errors"
	"os"
	"sync"
	"syscall"
	"time"
)

// Windows readers commonly omit delete sharing, so replacement can fail until
// their short-lived handles close. Stripes prevent same-path writers from
// starving one another while bounded retries preserve atomic rename semantics.
var windowsRenameLocks [64]sync.Mutex

func replaceFile(oldPath, newPath string) error {
	lock := windowsRenameLock(newPath)
	lock.Lock()
	defer lock.Unlock()
	return replaceFileWith(oldPath, newPath, os.Rename)
}

func replaceFileWith(oldPath, newPath string, rename func(string, string) error) error {
	deadline := time.Now().Add(2 * time.Second)
	delay := time.Millisecond
	for {
		err := rename(oldPath, newPath)
		if err == nil || !windowsRenameContention(err) || time.Now().After(deadline) {
			return err
		}
		time.Sleep(delay)
		if delay < 25*time.Millisecond {
			delay *= 2
		}
	}
}

func windowsRenameLock(path string) *sync.Mutex {
	const offset32 = uint32(2166136261)
	const prime32 = uint32(16777619)
	hash := offset32
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		hash ^= uint32(c)
		hash *= prime32
	}
	return &windowsRenameLocks[hash%uint32(len(windowsRenameLocks))]
}

func windowsRenameContention(err error) bool {
	const (
		errorSharingViolation syscall.Errno = 32
		errorLockViolation    syscall.Errno = 33
	)
	return errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, errorSharingViolation) ||
		errors.Is(err, errorLockViolation)
}
