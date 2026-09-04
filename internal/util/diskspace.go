package util

import (
	"fmt"
)

// DiskSpaceInfo contains filesystem space information.
type DiskSpaceInfo struct {
	// AvailableBytes is the number of bytes available to non-root users.
	AvailableBytes uint64

	// TotalBytes is the total filesystem capacity.
	TotalBytes uint64

	// UsedBytes is the number of bytes in use.
	UsedBytes uint64

	// UsedPercent is the usage percentage (0-100).
	UsedPercent float64
}

// AvailableMB returns available space in megabytes.
func (d *DiskSpaceInfo) AvailableMB() uint64 {
	return d.AvailableBytes / (1024 * 1024)
}

// AvailableGB returns available space in gigabytes (truncated).
func (d *DiskSpaceInfo) AvailableGB() float64 {
	return float64(d.AvailableBytes) / (1024 * 1024 * 1024)
}

// AvailableHuman returns a human-readable string for available space.
func (d *DiskSpaceInfo) AvailableHuman() string {
	return FormatBytesHuman(d.AvailableBytes)
}

// FormatBytesHuman formats bytes into a human-readable string.
func FormatBytesHuman(bytes uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)
	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.1f TB", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// Default thresholds for disk space checks.
const (
	// DiskSpaceMinimumMB is the absolute minimum free space (in MB) below which
	// operations that require significant disk I/O should be blocked.
	// 500 MB provides enough buffer for Dolt operations, git worktrees, etc.
	DiskSpaceMinimumMB uint64 = 500

	// DiskSpaceWarningMB is the threshold (in MB) at which warnings are emitted.
	// At 1 GB free, the system is at risk and should shed load.
	DiskSpaceWarningMB uint64 = 1024

	// DiskSpaceCriticalPercent is the usage percentage at or above which
	// operations should be blocked regardless of absolute free space.
	DiskSpaceCriticalPercent float64 = 97.5
)

// DiskSpaceLevel represents the severity of disk space status.
type DiskSpaceLevel int

const (
	// DiskSpaceOK means disk space is adequate.
	DiskSpaceOK DiskSpaceLevel = iota
	// DiskSpaceWarning means disk space is getting low.
	DiskSpaceWarning
	// DiskSpaceCritical means disk space is critically low — block new operations.
	DiskSpaceCritical
)

// String returns a human-readable label.
func (l DiskSpaceLevel) String() string {
	switch l {
	case DiskSpaceOK:
		return "ok"
	case DiskSpaceWarning:
		return "warning"
	case DiskSpaceCritical:
		return "critical"
	default:
		return "unknown"
	}
}

// CheckDiskSpace evaluates disk space at the given path and returns the level
// and a human-readable message. Returns DiskSpaceOK with empty message if fine.
func CheckDiskSpace(path string) (DiskSpaceLevel, string, error) {
	info, err := GetDiskSpace(path)
	if err != nil {
		return DiskSpaceOK, "", err
	}

	level, message := evaluateDiskSpace(info)
	return level, message, nil
}

func evaluateDiskSpace(info *DiskSpaceInfo) (DiskSpaceLevel, string) {
	availMB := info.AvailableMB()

	if availMB < DiskSpaceMinimumMB || info.UsedPercent >= DiskSpaceCriticalPercent {
		return DiskSpaceCritical,
			fmt.Sprintf("CRITICAL: only %s free (%.1f%% used) — disk space exhausted, operations blocked",
				info.AvailableHuman(), info.UsedPercent)
	}

	if availMB < DiskSpaceWarningMB {
		return DiskSpaceWarning,
			fmt.Sprintf("WARNING: only %s free (%.1f%% used) — disk space low, reduce workload",
				info.AvailableHuman(), info.UsedPercent)
	}

	return DiskSpaceOK, ""
}
