//go:build !linux

package fsutil

import "os"

// cloneFile reports that no copy-on-write clone was performed.
//
// Reflink cloning is wired up for Linux only. macOS clones through
// clonefile(2) rather than an ioctl on open descriptors, and APFS already
// shares extents for ordinary copies made by the Finder and by cp; Windows
// exposes nothing equivalent for this call shape. On those platforms CopyFile
// streams the bytes, which is correct everywhere.
func cloneFile(dst, src *os.File) (bool, error) {
	_, _ = dst, src
	return false, nil
}
