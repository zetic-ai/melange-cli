package upload

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionLease holds the cross-process lock for one upload session. Commands
// retain it across state reconciliation, transfer, completion, or cancellation.
type SessionLease struct {
	file *os.File
}

// AcquireSession serializes all local work for sessionID across processes.
func AcquireSession(ctx context.Context, sessionID string) (*SessionLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := statePath(sessionID)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := secureStateDir(dir); err != nil {
		return nil, err
	}
	lockFile, err := os.OpenFile(path[:len(path)-len(".json")]+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening upload session lock: %w", err)
	}
	if err := lockFile.Chmod(0o600); err != nil {
		_ = lockFile.Close()
		return nil, fmt.Errorf("securing upload session lock: %w", err)
	}

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			_ = lockFile.Close()
			return nil, err
		}
		locked, err := tryLockFile(lockFile)
		if err != nil {
			_ = lockFile.Close()
			return nil, fmt.Errorf("locking upload session: %w", err)
		}
		if locked {
			return &SessionLease{file: lockFile}, nil
		}
		select {
		case <-ctx.Done():
			_ = lockFile.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Close releases the session lock.
func (l *SessionLease) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	if err := unlockFile(l.file); err != nil {
		_ = l.file.Close()
		return fmt.Errorf("unlocking upload session: %w", err)
	}
	if err := l.file.Close(); err != nil {
		return fmt.Errorf("closing upload session lock: %w", err)
	}
	l.file = nil
	return nil
}
