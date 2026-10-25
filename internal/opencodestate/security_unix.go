//go:build !windows

package opencodestate

import "os"

func protectCredentialPath(path string, directory bool) error {
	if directory {
		return os.Chmod(path, 0700)
	}
	return os.Chmod(path, 0600)
}
