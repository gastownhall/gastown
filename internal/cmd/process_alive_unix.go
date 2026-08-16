//go:build !windows

package cmd

import "github.com/jonbaldie/gastown/internal/process"

func processAlive(pid int) bool {
	return process.Alive(pid)
}
