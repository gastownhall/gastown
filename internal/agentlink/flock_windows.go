//go:build windows

package agentlink

import "sync"

// Gas Town does not run on Windows in production (see internal/lock); a
// process-local mutex keeps the queue consistent within one process.
var windowsQueueMu sync.Mutex

func flockAcquire(string) (func(), error) {
	windowsQueueMu.Lock()
	return windowsQueueMu.Unlock, nil
}
