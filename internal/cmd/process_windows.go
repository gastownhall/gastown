//go:build windows

package cmd

import "github.com/jonbaldie/gastown/internal/process"

func isProcessRunning(pid int) bool {
	return process.Alive(pid)
}
