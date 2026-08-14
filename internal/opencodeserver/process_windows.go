//go:build windows

package opencodeserver

import (
	"errors"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

func terminateProcessTree(pid int) error {
	children, err := snapshotProcessChildren()
	if err != nil {
		return err
	}

	var killErr error
	var kill func(uint32)
	kill = func(parent uint32) {
		for _, child := range children[parent] {
			kill(child)
			killErr = errors.Join(killErr, terminatePID(child))
		}
	}
	kill(uint32(pid))
	killErr = errors.Join(killErr, terminatePID(uint32(pid)))
	return killErr
}

func snapshotProcessChildren() (map[uint32][]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)

	children := make(map[uint32][]uint32)
	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		entry.Size = uint32(unsafe.Sizeof(entry))
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if errors.Is(err, syscall.ERROR_NO_MORE_FILES) {
				break
			}
			return nil, err
		}
	}
	return children, nil
}

func terminatePID(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return nil
		}
		return err
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	const stillActive = 259
	if err := windows.GetExitCodeProcess(handle, &exitCode); err == nil && exitCode != stillActive {
		return nil
	}
	return windows.TerminateProcess(handle, 1)
}
