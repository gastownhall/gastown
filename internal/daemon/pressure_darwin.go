package daemon

import (
	"os/exec"
	"strconv"
	"strings"

	"github.com/jonbaldie/gastown/internal/util"
)

// loadAverage1Sysctl returns the 1-minute load average via sysctl on macOS.
func loadAverage1Sysctl() float64 {
	cmd := exec.Command("sysctl", "-n", "vm.loadavg")
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	// Output format: "{ 1.23 4.56 7.89 }"
	s := strings.TrimSpace(string(out))
	s = strings.Trim(s, "{ }")
	fields := strings.Fields(s)
	if len(fields) < 1 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

// availableMemoryGB returns approximate available memory in GB on macOS.
// Uses vm_stat to get free + inactive pages. Returns 0 if unavailable.
func availableMemoryGB() float64 {
	cmd := exec.Command("vm_stat")
	util.SetDetachedProcessGroup(cmd)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}

	var freePages, inactivePages uint64
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "Pages free:") {
			freePages = parseVMStatValue(line)
		} else if strings.HasPrefix(line, "Pages inactive:") {
			inactivePages = parseVMStatValue(line)
		}
	}

	// macOS page size is 16384 on Apple Silicon, 4096 on Intel
	pageSize := uint64(16384) // conservative default for M-series
	pageSizeCmd := exec.Command("sysctl", "-n", "hw.pagesize")
	util.SetDetachedProcessGroup(pageSizeCmd)
	out2, err := pageSizeCmd.Output()
	if err == nil {
		if ps, err := strconv.ParseUint(strings.TrimSpace(string(out2)), 10, 64); err == nil {
			pageSize = ps
		}
	}

	availBytes := (freePages + inactivePages) * pageSize
	return float64(availBytes) / (1024 * 1024 * 1024)
}

func parseVMStatValue(line string) uint64 {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) < 2 {
		return 0
	}
	s := strings.TrimSpace(parts[1])
	s = strings.TrimSuffix(s, ".")
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
