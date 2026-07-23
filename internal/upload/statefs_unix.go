//go:build !windows

package upload

import (
	"errors"
	"os"
)

func replaceStateFile(source, destination string) error {
	return os.Rename(source, destination)
}

func syncStateDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close() //nolint:errcheck
	if err := dir.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
