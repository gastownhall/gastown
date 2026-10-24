//go:build windows

package opencodeserver

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSameDirectoryTreatsWindowsShortAndLongPathsAsEqual(t *testing.T) {
	dir := t.TempDir()
	longPath := windowsPathName(t, dir, windows.GetLongPathName)
	shortPath := windowsPathName(t, longPath, windows.GetShortPathName)
	if strings.EqualFold(filepath.Clean(longPath), filepath.Clean(shortPath)) {
		t.Skip("8.3 short path names are unavailable on this volume")
	}
	if !sameDirectory(longPath, shortPath) {
		t.Fatalf("sameDirectory(%q, %q) = false", longPath, shortPath)
	}
}

func windowsPathName(t *testing.T, path string, convert func(*uint16, *uint16, uint32) (uint32, error)) string {
	t.Helper()
	source, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]uint16, 32768)
	length, err := convert(source, &buffer[0], uint32(len(buffer)))
	if err != nil {
		t.Fatal(err)
	}
	if length == 0 || length >= uint32(len(buffer)) {
		t.Fatalf("invalid converted path length %d", length)
	}
	return windows.UTF16ToString(buffer[:length])
}
