//go:build windows

package upload

import "golang.org/x/sys/windows"

func replaceStateFile(source, destination string) error {
	sourcePtr, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(sourcePtr, destinationPtr,
		windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// MOVEFILE_WRITE_THROUGH flushes the rename on Windows. Windows has no
// portable directory-fsync equivalent.
func syncStateDirectory(string) error { return nil }
