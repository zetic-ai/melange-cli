//go:build !windows

package downloadstate

import (
	"errors"
	"os"
)

func replaceAtomic(source, destination string, replace bool) error {
	if !replace {
		if err := os.Link(source, destination); err != nil {
			return err
		}
		return nil
	}
	return os.Rename(source, destination)
}

func syncDirectory(path string) error {
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
