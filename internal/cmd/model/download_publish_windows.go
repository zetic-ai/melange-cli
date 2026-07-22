//go:build windows

package model

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func publishDownloadedFile(tmp, dest string, force bool) error {
	source, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return err
	}
	destination, err := windows.UTF16PtrFromString(dest)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if force {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	if err := windows.MoveFileEx(source, destination, flags); err != nil {
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) {
			return fmt.Errorf("%w: %v", os.ErrExist, err)
		}
		return err
	}
	return nil
}
