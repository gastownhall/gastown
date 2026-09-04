package util

import (
	"os"
	"testing"
)

func TestGetDiskSpace_CurrentDir(t *testing.T) {
	info, err := GetDiskSpace(".")
	if err != nil {
		t.Fatalf("GetDiskSpace(\".\") failed: %v", err)
	}

	if info.TotalBytes == 0 {
		t.Error("TotalBytes should be > 0")
	}

	if info.AvailableBytes > info.TotalBytes {
		t.Errorf("AvailableBytes (%d) > TotalBytes (%d)", info.AvailableBytes, info.TotalBytes)
	}

	if info.UsedPercent < 0 || info.UsedPercent > 100 {
		t.Errorf("UsedPercent = %.1f, want 0-100", info.UsedPercent)
	}
}

func TestGetDiskSpace_InvalidPath(t *testing.T) {
	_, err := GetDiskSpace("/nonexistent/path/that/should/not/exist")
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

func TestGetDiskSpace_TempDir(t *testing.T) {
	dir := t.TempDir()
	info, err := GetDiskSpace(dir)
	if err != nil {
		t.Fatalf("GetDiskSpace(%q) failed: %v", dir, err)
	}

	if info.AvailableMB() == 0 && info.TotalBytes > 0 {
		// Unlikely in test environment, but possible on truly full disks
		t.Log("WARNING: test filesystem reports 0 MB available")
	}
}

func TestDiskSpaceInfo_AvailableMB(t *testing.T) {
	info := &DiskSpaceInfo{AvailableBytes: 1024 * 1024 * 512}
	if got := info.AvailableMB(); got != 512 {
		t.Errorf("AvailableMB() = %d, want 512", got)
	}
}

func TestDiskSpaceInfo_AvailableGB(t *testing.T) {
	info := &DiskSpaceInfo{AvailableBytes: 1024 * 1024 * 1024 * 2}
	if got := info.AvailableGB(); got != 2.0 {
		t.Errorf("AvailableGB() = %f, want 2.0", got)
	}
}

func TestDiskSpaceInfo_AvailableHuman(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{500, "500 B"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
		{1024 * 1024 * 1024 * 2, "2.0 GB"},
	}

	for _, tt := range tests {
		info := &DiskSpaceInfo{AvailableBytes: tt.bytes}
		if got := info.AvailableHuman(); got != tt.want {
			t.Errorf("AvailableHuman() for %d bytes = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestFormatBytesHuman(t *testing.T) {
	tests := []struct {
		bytes uint64
		want  string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		got := FormatBytesHuman(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatBytesHuman(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestCheckDiskSpace_CurrentDir(t *testing.T) {
	level, msg, err := CheckDiskSpace(".")
	if err != nil {
		t.Fatalf("CheckDiskSpace(\".\") failed: %v", err)
	}
	info, err := GetDiskSpace(".")
	if err != nil {
		t.Fatalf("GetDiskSpace(\".\") failed: %v", err)
	}
	t.Logf("current filesystem: %s free, %.2f%% used => level=%s message=%q",
		info.AvailableHuman(), info.UsedPercent, level, msg)

	// In a normal test environment, disk should be OK
	if level == DiskSpaceCritical {
		t.Logf("Disk space is critical: %s", msg)
	}
}

func TestCheckDiskSpace_InvalidPath(t *testing.T) {
	_, _, err := CheckDiskSpace("/nonexistent/path/that/should/not/exist")
	if err == nil {
		t.Error("expected error for invalid path, got nil")
	}
}

func TestEvaluateDiskSpaceThresholds(t *testing.T) {
	const MB = uint64(1024 * 1024)
	tests := []struct {
		name        string
		availableMB uint64
		usedPercent float64
		want        DiskSpaceLevel
	}{
		{name: "below percentage limit", availableMB: 1024, usedPercent: 97.49, want: DiskSpaceOK},
		{name: "at percentage limit", availableMB: 1024, usedPercent: 97.5, want: DiskSpaceCritical},
		{name: "above percentage limit", availableMB: 1024, usedPercent: 97.51, want: DiskSpaceCritical},
		{name: "below absolute floor", availableMB: 499, usedPercent: 10, want: DiskSpaceCritical},
		{name: "at absolute floor", availableMB: 500, usedPercent: 10, want: DiskSpaceWarning},
		{name: "below warning floor", availableMB: 1023, usedPercent: 10, want: DiskSpaceWarning},
		{name: "at warning floor", availableMB: 1024, usedPercent: 10, want: DiskSpaceOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := &DiskSpaceInfo{
				AvailableBytes: tt.availableMB * MB,
				TotalBytes:     100 * 1024 * MB,
				UsedPercent:    tt.usedPercent,
			}
			got, message := evaluateDiskSpace(info)
			if got != tt.want {
				t.Fatalf("evaluateDiskSpace() = %s, want %s", got, tt.want)
			}
			t.Logf("available=%d MB used=%.2f%% => level=%s message=%q",
				tt.availableMB, tt.usedPercent, got, message)
		})
	}
}

func TestDiskSpaceLevel_String(t *testing.T) {
	tests := []struct {
		level DiskSpaceLevel
		want  string
	}{
		{DiskSpaceOK, "ok"},
		{DiskSpaceWarning, "warning"},
		{DiskSpaceCritical, "critical"},
		{DiskSpaceLevel(99), "unknown"},
	}

	for _, tt := range tests {
		if got := tt.level.String(); got != tt.want {
			t.Errorf("DiskSpaceLevel(%d).String() = %q, want %q", tt.level, got, tt.want)
		}
	}
}

func TestGetDiskSpace_Root(t *testing.T) {
	if os.Getuid() != 0 {
		// Test root filesystem availability (should work for all users)
		info, err := GetDiskSpace("/")
		if err != nil {
			t.Fatalf("GetDiskSpace(\"/\") failed: %v", err)
		}
		if info.TotalBytes == 0 {
			t.Error("root filesystem should have non-zero total bytes")
		}
	}
}
