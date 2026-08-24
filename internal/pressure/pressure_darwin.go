//go:build darwin

package pressure

import (
	"os/exec"
	"strconv"
	"strings"
)

// bytesPerGB — defined per-OS so platform files stay allocation-free of a
// shared constant import.
const bytesPerGB = 1024 * 1024 * 1024

// hostLoadAvg reads the 1-minute load average via sysctl on macOS.
func hostLoadAvg() float64 {
	out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
	if err != nil {
		return 0
	}
	// Format: "{1.23 4.56 7.89} 8"
	s := strings.Fields(strings.TrimSpace(string(out)))
	for _, f := range s {
		f = strings.Trim(f, "{}")
		v, err := strconv.ParseFloat(f, 64)
		if err == nil {
			return v // first numeric = 1m load
		}
	}
	return 0
}

// hostMemAvailableGB estimates available memory on macOS via vm_stat,
// converting the "free + inactive + speculative" page counts to GB.
func hostMemAvailableGB() float64 {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0
	}
	// vm_stat reports pages free / inactive / speculative; available ~= free+inactive+speculative.
	parts := vmStatFields(string(out), "free", "inactive", "speculative")
	if len(parts) < 3 {
		return 0
	}
	free, inactive, speculative := parts[0], parts[1], parts[2]
	if free == 0 && inactive == 0 && speculative == 0 {
		return 0
	}
	// Pages are 4096 bytes on Apple Silicon and Intel macs; pageSize via hw.pagesize.
	ps := vmPageSize()
	totalPages := free + inactive + speculative
	return float64(totalPages) * float64(ps) / bytesPerGB
}

func vmStatFields(s string, names ...string) []int64 {
	m := make(map[string]int64, len(names))
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, ":") {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		k := strings.TrimSpace(kv[0])
		var v int64
		if len(kv) > 1 {
			v, _ = strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64)
		}
		m[k] = v
	}
	out := make([]int64, len(names))
	for i, n := range names {
		out[i] = m[n]
	}
	return out
}

func vmPageSize() int {
	out, err := exec.Command("sysctl", "-n", "hw.pagesize").Output()
	if err != nil {
		return 4096
	}
	v, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || v <= 0 {
		return 4096
	}
	return v
}

// hostSwapTotalGB / hostSwapFreeGB read swap usage from vm.swapusage.
func hostSwapTotalGB() float64 {
	total, _ := vmSwapUsage()
	return total / bytesPerGB
}

func hostSwapFreeGB() float64 {
	total, used := vmSwapUsage()
	free := total - used
	if free < 0 {
		return 0
	}
	return free / bytesPerGB
}

// vmSwapUsage returns (total, used) swap in bytes from `sysctl vm.swapusage`.
func vmSwapUsage() (total, used float64) {
	out, err := exec.Command("sysctl", "-n", "vm.swapusage").Output()
	if err != nil {
		return 0, 0
	}
	// e.g. "vm.swapusage: total = 1024.00M bytes, used = 256.00M bytes, free = 768.00M bytes"
	line := string(out)
	total = parseMB(line, "total")
	used = parseMB(line, "used")
	return
}

func parseMB(line, key string) float64 {
	idx := strings.Index(line, key)
	if idx < 0 {
		return 0
	}
	rest := line[idx:]
	eq := strings.Index(rest, "=")
	if eq < 0 {
		return 0
	}
	rest = rest[eq+1:]
	var v float64
	if _, err := strconv.ParseFloat(strings.Fields(rest)[0], 64); err == nil {
		// already a plain number in bytes units (M suffix)
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0
	}
	v, _ = strconv.ParseFloat(fields[0], 64)
	switch {
	case strings.Contains(fields[0], "M"):
		return v * 1024 * 1024
	case strings.Contains(fields[0], "G"):
		return v * 1024 * 1024 * 1024
	case strings.Contains(fields[0], "K"):
		return v * 1024
	default:
		return v
	}
}
