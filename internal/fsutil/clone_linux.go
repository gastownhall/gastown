//go:build linux

package fsutil

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile attempts a copy-on-write clone of src into dst via the FICLONE
// ioctl, which btrfs, XFS with reflink=1, and overlayfs over either implement.
//
// It reports whether the clone happened. A false return with a nil error means
// the platform accepted the call but the filesystem cannot clone this pair, and
// the caller should copy the bytes instead. Only genuinely unexpected errors
// are returned.
func cloneFile(dst, src *os.File) (bool, error) {
	err := unix.IoctlFileClone(int(dst.Fd()), int(src.Fd()))
	if err == nil {
		return true, nil
	}

	switch {
	// Filesystem has no clone support (ext4, tmpfs, NFS, ...).
	case errors.Is(err, unix.ENOTTY), errors.Is(err, unix.EOPNOTSUPP), errors.Is(err, unix.ENOSYS):
		return false, nil
	// src and dst are on different filesystems; extents cannot be shared.
	case errors.Is(err, unix.EXDEV):
		return false, nil
	// Kernel rejected this specific pair, e.g. a file on a filesystem that
	// supports cloning but not for this inode (compressed or inline extents).
	case errors.Is(err, unix.EINVAL), errors.Is(err, unix.EBADF), errors.Is(err, unix.EPERM):
		return false, nil
	default:
		return false, err
	}
}
