//go:build windows

package config

// Windows does not provide the same portable directory-fsync primitive as
// Unix. The temporary file itself is synced before MoveFileEx replaces the
// destination atomically.
func syncDir(string) error { return nil }
