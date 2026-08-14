//go:build windows

package opencodeserver

import (
	"errors"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

func configureServerCommand(*exec.Cmd) {}

func attachServerProcessGuard(pid int) (func() error, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	closeJob := func() error { return windows.CloseHandle(job) }
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = closeJob()
		return nil, err
	}
	children, err := snapshotProcessChildren()
	if err != nil {
		_ = closeJob()
		return nil, err
	}
	if _, err := assignPIDToJob(job, uint32(pid), false); err != nil {
		_ = closeJob()
		return nil, err
	}
	if _, err := assignDescendantsToJob(job, uint32(pid), children); err != nil {
		_ = closeJob()
		return nil, err
	}
	// A child can appear between the initial snapshot and assignment. Once a
	// sweep finds no unassigned descendants, future children inherit the job.
	settled := false
	for range 8 {
		children, err = snapshotProcessChildren()
		if err != nil {
			_ = closeJob()
			return nil, err
		}
		assigned, err := assignDescendantsToJob(job, uint32(pid), children)
		if err != nil {
			_ = closeJob()
			return nil, err
		}
		if !assigned {
			settled = true
			break
		}
	}
	if !settled {
		_ = closeJob()
		return nil, errors.New("OpenCode process tree did not settle while attaching job guard")
	}
	return closeJob, nil
}

func assignDescendantsToJob(job windows.Handle, root uint32, children map[uint32][]uint32) (bool, error) {
	assignedAny := false
	visited := make(map[uint32]bool)
	var assign func(uint32) error
	assign = func(parent uint32) error {
		if visited[parent] {
			return nil
		}
		visited[parent] = true
		for _, child := range children[parent] {
			assigned, err := assignPIDToJob(job, child, true)
			if err != nil {
				return err
			}
			assignedAny = assignedAny || assigned
			if err := assign(child); err != nil {
				return err
			}
		}
		return nil
	}
	return assignedAny, assign(root)
}

func assignPIDToJob(job windows.Handle, pid uint32, allowExited bool) (bool, error) {
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		pid,
	)
	if err != nil {
		if allowExited && errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return false, nil
		}
		return false, err
	}
	defer windows.CloseHandle(process)

	inJob, err := processIsInJob(process, job)
	if err != nil {
		return false, err
	}
	if inJob {
		return false, nil
	}
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return false, err
	}
	return true, nil
}

func processIsInJob(process, job windows.Handle) (bool, error) {
	var result int32
	ok, _, callErr := isProcessInJob.Call(
		uintptr(process),
		uintptr(job),
		uintptr(unsafe.Pointer(&result)),
	)
	if ok == 0 {
		return false, callErr
	}
	return result != 0, nil
}
