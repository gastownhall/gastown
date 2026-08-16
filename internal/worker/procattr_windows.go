//go:build windows

package worker

import "syscall"

func serveSysProcAttr() *syscall.SysProcAttr {
	return nil
}
