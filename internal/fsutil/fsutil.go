// Package fsutil provides filesystem copy primitives that exploit
// copy-on-write cloning where the platform and filesystem support it.
//
// On a COW filesystem (btrfs, XFS with reflink=1, ZFS, APFS) a clone shares
// the underlying extents instead of duplicating them: the copy is near
// instantaneous and initially consumes no additional space. Support is decided
// per call at runtime, so the same code is correct on ext4, tmpfs and network
// filesystems, where it degrades to a streaming copy.
//
// Cloning is only possible within a single filesystem. Cross-device copies
// always fall back, which is not a failure mode worth reporting.
package fsutil

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// CopyFile copies src to dst, preferring a copy-on-write clone and falling
// back to a streaming copy. The destination is created or truncated and takes
// the source's permission bits.
func CopyFile(src, dst string) error {
	srcFile, err := os.Open(src) //nolint:gosec // G304: caller-controlled internal path
	if err != nil {
		return fmt.Errorf("opening %s: %w", src, err)
	}
	defer func() { _ = srcFile.Close() }()

	srcInfo, err := srcFile.Stat()
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if !srcInfo.Mode().IsRegular() {
		return fmt.Errorf("copying %s: not a regular file", src)
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode().Perm()) //nolint:gosec // G304: caller-controlled internal path
	if err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	defer func() { _ = dstFile.Close() }()

	// cloneFile is a no-op returning false on platforms and filesystems
	// without reflink support.
	if cloned, err := cloneFile(dstFile, srcFile); err != nil {
		return fmt.Errorf("cloning %s to %s: %w", src, dst, err)
	} else if cloned {
		return dstFile.Close()
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("copying %s to %s: %w", src, dst, err)
	}
	return dstFile.Close()
}

// CopyDir recursively copies the tree rooted at src into dst, creating dst if
// needed. Regular files go through CopyFile, so they are cloned where the
// filesystem allows it. Symlinks are recreated as symlinks rather than
// dereferenced; sockets, devices and FIFOs are skipped.
//
// Symlinks inside the tree are never followed, so a link pointing outside src
// is reproduced as a link and cannot redirect a write beyond dst.
func CopyDir(src, dst string) error {
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat %s: %w", src, err)
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("copying %s: not a directory", src)
	}
	return copyTree(src, dst, srcInfo.Mode().Perm())
}

// copyTree copies one directory level and recurses. It walks with os.ReadDir
// rather than filepath.WalkDir so that every path it acts on is built from an
// entry it has just read and type-checked, instead of one handed back by a
// callback after the walker has already stat'ed it.
func copyTree(src, dst string, perm fs.FileMode) error {
	if err := os.MkdirAll(dst, perm); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}

	for _, entry := range entries {
		srcPath := filepath.Join(src, entry.Name())
		dstPath := filepath.Join(dst, entry.Name())

		// Lstat, not Stat: a symlink must be reported as a symlink so it is
		// recreated rather than followed.
		info, err := os.Lstat(srcPath)
		if err != nil {
			return fmt.Errorf("stat %s: %w", srcPath, err)
		}

		switch {
		case info.IsDir():
			if err := copyTree(srcPath, dstPath, info.Mode().Perm()); err != nil {
				return err
			}

		case info.Mode()&fs.ModeSymlink != 0:
			if err := copySymlink(srcPath, dstPath); err != nil {
				return err
			}

		case info.Mode().IsRegular():
			if err := CopyFile(srcPath, dstPath); err != nil {
				return err
			}

		default:
			// Sockets, devices and named pipes carry no content worth
			// reproducing in a copy of a data directory.
		}
	}

	return nil
}

// copySymlink recreates src's link target at dst without dereferencing it.
func copySymlink(src, dst string) error {
	link, err := os.Readlink(src)
	if err != nil {
		return fmt.Errorf("reading link %s: %w", src, err)
	}
	// A stale destination would make Symlink fail with EEXIST.
	if err := os.Remove(dst); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replacing %s: %w", dst, err)
	}
	if err := os.Symlink(link, dst); err != nil {
		return fmt.Errorf("linking %s: %w", dst, err)
	}
	return nil
}
