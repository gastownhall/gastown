//go:build linux

package pressure

import (
	"os"
	"strconv"
	"strings"
)

const bytesPerGB = 1024 * 1024 * 1024

// hostLoadAvg reads the 1-minute load average from /proc/loadavg.
func hostLoadAvg() float64 {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil || len(b) == 0 {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

// hostMemAvailableGB reads MemAvailable from /proc/meminfo.
func hostMemAvailableGB() float64 {
	return meminfoField("/proc/meminfo", "MemAvailable:") / bytesPerGB
}

// hostSwapTotalGB reads SwapTotal from /proc/meminfo.
func hostSwapTotalGB() float64 {
	return meminfoField("/proc/meminfo", "SwapTotal:") / bytesPerGB
}

// hostSwapFreeGB reads SwapFree from /proc/meminfo.
func hostSwapFreeGB() float64 {
	return meminfoField("/proc/meminfo", "SwapFree:") / bytesPerGB
}

// meminfoField scans a /proc/meminfo-style file for a field whose line begins
// with prefix, returning the value in kB as bytes (field units are kB).
func meminfoField(path, prefix string) float64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			parts := strings.Fields(strings.TrimPrefix(line, prefix))
			if len(parts) == 0 {
				return 0
			}
			// /proc/meminfo uses kB; a missing unit means kB too.
			v, err := strconv.ParseFloat(parts[0], 64)
			if err != nil {
				return 0
			}
			return v * 1024
		}
	}
	return 0
}
