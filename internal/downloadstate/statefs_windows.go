//go:build windows

package downloadstate

import "golang.org/x/sys/windows"

func replaceAtomic(source, destination string, replace bool) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_WRITE_THROUGH)
	if replace {
		flags |= windows.MOVEFILE_REPLACE_EXISTING
	}
	return windows.MoveFileEx(sourcePtr, destinationPtr, flags)
}

// MOVEFILE_WRITE_THROUGH flushes the rename on Windows. Windows does not
// expose a portable equivalent of fsync on an opened directory handle.
func syncDirectory(string) error { return nil }
