//go:build windows

package process

import (
	"math"
	"syscall"
)

const processStillActive = 259
const processQueryLimitedInformation = 0x1000

// Alive reports whether pid exists on Windows.
func Alive(pid int) bool {
	if pid <= 0 || pid > math.MaxUint32 {
		return false
	}
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = syscall.CloseHandle(handle) }()

	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == processStillActive
}
