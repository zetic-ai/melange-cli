//go:build windows

package upload

import (
	"sync"

	"golang.org/x/sys/windows"
)

// Concurrent replacements of the same destination can fail with
// ERROR_ACCESS_DENIED on Windows even when each source is an independent
// temporary file. Upload state writes are small, so serialize only the atomic
// replacement boundary instead of weakening durability or adding retries.
var replaceStateFileMu sync.Mutex

func replaceStateFile(source, destination string) error {
	replaceStateFileMu.Lock()
	defer replaceStateFileMu.Unlock()

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
