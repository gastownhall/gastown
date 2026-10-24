//go:build darwin

package util

import (
	"testing"
)

func TestParsePlistUint64_Found(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>SomeOtherKey</key>
	<string>hello</string>
	<key>APFSContainerFree</key>
	<integer>127566721024</integer>
	<key>APFSContainerSize</key>
	<integer>994662584320</integer>
</dict>
</plist>`)

	free, ok := parsePlistUint64(plist, "APFSContainerFree")
	if !ok {
		t.Fatal("expected APFSContainerFree to be found")
	}
	if free != 127566721024 {
		t.Errorf("APFSContainerFree = %d, want 127566721024", free)
	}

	size, ok := parsePlistUint64(plist, "APFSContainerSize")
	if !ok {
		t.Fatal("expected APFSContainerSize to be found")
	}
	if size != 994662584320 {
		t.Errorf("APFSContainerSize = %d, want 994662584320", size)
	}
}

func TestParsePlistUint64_Missing(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>Other</key><integer>1</integer></dict></plist>`)

	_, ok := parsePlistUint64(plist, "APFSContainerFree")
	if ok {
		t.Error("expected APFSContainerFree to be absent")
	}
}

func TestParsePlistUint64_KeyFollowedByNonInteger(t *testing.T) {
	plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict>
	<key>APFSContainerFree</key>
	<string>not-a-number</string>
	<key>APFSContainerSize</key>
	<integer>1000</integer>
</dict></plist>`)

	_, ok := parsePlistUint64(plist, "APFSContainerFree")
	if ok {
		t.Error("expected non-integer value to return not-found")
	}
	size, ok := parsePlistUint64(plist, "APFSContainerSize")
	if !ok || size != 1000 {
		t.Errorf("APFSContainerSize = %d, ok=%v; want 1000, true", size, ok)
	}
}

func TestInt8SliceToString(t *testing.T) {
	input := []int8{'a', 'p', 'f', 's', 0, 0, 0, 0}
	got := int8SliceToString(input)
	if got != "apfs" {
		t.Errorf("int8SliceToString = %q, want %q", got, "apfs")
	}
}

func TestGetDiskSpace_Darwin_APFS(t *testing.T) {
	// Integration test: on macOS the current dir is on APFS, so APFSContainerFree
	// should give us a non-zero available value.
	info, err := GetDiskSpace(".")
	if err != nil {
		t.Fatalf("GetDiskSpace(\".\") failed: %v", err)
	}
	if info.TotalBytes == 0 {
		t.Error("TotalBytes should be > 0")
	}
	if info.AvailableBytes == 0 {
		t.Error("AvailableBytes should be > 0 on a non-full disk")
	}
	if info.AvailableBytes > info.TotalBytes {
		t.Errorf("AvailableBytes (%d) > TotalBytes (%d)", info.AvailableBytes, info.TotalBytes)
	}
	if info.UsedPercent < 0 || info.UsedPercent > 100 {
		t.Errorf("UsedPercent = %.1f, want 0-100", info.UsedPercent)
	}
}

// TestAPFSPurgeablePreventsBlock reproduces the exact scenario from issue #3854:
// a 926 GB APFS disk where statfs reports 96% used (41 GB free) but 185 GB is
// purgeable space macOS can reclaim — the old code would block, the new code must not.
func TestAPFSPurgeablePreventsBlock(t *testing.T) {
	const GB = uint64(1024 * 1024 * 1024)
	containerTotal := 926 * GB
	purgeableSpace := 185 * GB
	statfsFree := 41 * GB // what df / old code saw
	containerFree := statfsFree + purgeableSpace

	// Inject a stub that returns the simulated diskutil values.
	orig := apfsContainerSpaceFn
	defer func() { apfsContainerSpaceFn = orig }()
	apfsContainerSpaceFn = func(_ string) (uint64, uint64, error) {
		return containerFree, containerTotal, nil
	}

	info, err := GetDiskSpace(".")
	if err != nil {
		t.Fatalf("GetDiskSpace failed: %v", err)
	}

	// The fix must report available = containerFree (41 + 185 = 226 GB).
	if info.AvailableBytes != containerFree {
		t.Errorf("AvailableBytes = %d, want %d (%.1f GB want %.1f GB)",
			info.AvailableBytes, containerFree,
			float64(info.AvailableBytes)/float64(GB), float64(containerFree)/float64(GB))
	}

	// Old code used a 95% threshold with statfs Bavail (~41 GB), so 95.6%
	// produced a false-positive CRITICAL block.
	const historicalCriticalPercent = 95.0
	oldUsed := containerTotal - statfsFree
	oldPct := float64(oldUsed) / float64(containerTotal) * 100
	if oldPct < historicalCriticalPercent {
		t.Errorf("test setup broken: old code would show %.1f%% (expected >= %.1f%%)", oldPct, historicalCriticalPercent)
	}

	// New code must be below the critical threshold.
	if info.UsedPercent >= DiskSpaceCriticalPercent {
		t.Errorf("UsedPercent = %.1f%% >= %.1f%% — fix failed, false positive would still block",
			info.UsedPercent, DiskSpaceCriticalPercent)
	}

	// Double-check CheckDiskSpace also clears.
	level, msg, err := CheckDiskSpace(".")
	if err != nil {
		t.Fatalf("CheckDiskSpace failed: %v", err)
	}
	if level == DiskSpaceCritical {
		t.Errorf("CheckDiskSpace returned CRITICAL with purgeable space included: %s", msg)
	}

	t.Logf("OLD: %.1f GB free, %.1f%% used → would block",
		float64(statfsFree)/float64(GB), oldPct)
	t.Logf("NEW: %.1f GB free (includes %.1f GB purgeable), %.1f%% used → passes",
		float64(info.AvailableBytes)/float64(GB), float64(purgeableSpace)/float64(GB), info.UsedPercent)
}

func TestApfsContainerSpace_CurrentMount(t *testing.T) {
	// The repo lives on an APFS volume; verify diskutil returns sensible numbers.
	// diskutil info requires an absolute path — use the user's home dir which is
	// always on the main APFS container on macOS.
	free, total, err := apfsContainerSpace("/System/Volumes/Data")
	if err != nil {
		t.Skipf("apfsContainerSpace failed (non-APFS environment?): %v", err)
	}
	if free == 0 {
		t.Error("expected non-zero free bytes from APFSContainerFree")
	}
	if total > 0 && free > total {
		t.Errorf("free (%d) > total (%d)", free, total)
	}
}
