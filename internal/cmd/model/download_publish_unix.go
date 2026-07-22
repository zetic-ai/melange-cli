//go:build !windows

package model

import "os"

func publishDownloadedFile(tmp, dest string, force bool) error {
	if force {
		return os.Rename(tmp, dest)
	}
	// The temp file is created beside dest, so a hard link is an atomic
	// no-replace publication on Unix. Filesystems without hard-link support
	// fail explicitly rather than falling back to an overwriting rename.
	return os.Link(tmp, dest)
}
