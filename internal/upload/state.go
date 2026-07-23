package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ErrStateCorrupt reports a state file that exists but cannot be parsed.
// Callers may treat it as missing (after warning) and rebuild the session
// state from the server.
var ErrStateCorrupt = errors.New("state file corrupt")

// State is the persisted record of an in-flight upload session, stored as
// <StateDir>/<session-id>.json.
//
// It contains resumable session URIs, which are bearer credentials: the
// file is written 0600 in a 0700 directory and its contents must never be
// printed or logged.
type State struct {
	SessionID string       `json:"session_id"`
	Repo      string       `json:"repo"` // ACCOUNT/NAME
	Tag       string       `json:"tag"`
	CreatedAt time.Time    `json:"created_at"`
	Files     []*StateFile `json:"files"`
}

// StateFile tracks one manifest file's upload progress. Offset is a hint
// only — the GCS committed offset is authoritative on resume.
type StateFile struct {
	ClientFileID  string `json:"client_file_id"`
	LocalPath     string `json:"local_path"`
	CanonicalPath string `json:"canonical_path"`
	UploadURL     string `json:"upload_url,omitempty"`  // signed resumable-start URL
	SessionURI    string `json:"session_uri,omitempty"` // resumable session URI (credential)
	Size          int64  `json:"size"`
	CRC32C        string `json:"crc32c"`
	Offset        int64  `json:"offset"`
	Uploaded      bool   `json:"uploaded"`
}

// StateDir returns the platform-appropriate directory for upload state files
// (mirroring config.ConfigDir's pattern):
//
//   - Linux/macOS: ${XDG_STATE_HOME:-~/.local/state}/melange/uploads
//   - Windows:     %LocalAppData%\melange\uploads
func StateDir() (string, error) {
	return stateDirFor(runtime.GOOS)
}

// stateDirFor is StateDir with the GOOS injected so the Windows branch is
// unit-testable without a Windows CI leg.
func stateDirFor(goos string) (string, error) {
	if goos == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "melange", "uploads"), nil
		}
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		if goos != "windows" {
			base = os.Getenv("HOME")
		}
		if base == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolving state directory: %w", err)
			}
			base = home
		}
		base = filepath.Join(base, ".local", "state")
	}
	return filepath.Join(base, "melange", "uploads"), nil
}

// statePath validates the session id (it becomes a path component) and
// returns the state file path for it.
func statePath(sessionID string) (string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".json"), nil
}

// ValidateSessionID rejects identifiers that cannot be used as one opaque
// local state/lock name. Quoted errors keep control bytes inert.
func ValidateSessionID(sessionID string) error {
	if !validSessionID(sessionID) {
		return fmt.Errorf("invalid upload session id %q", sessionID)
	}
	return nil
}

// validSessionID accepts only [A-Za-z0-9._-]+ (no separators, no traversal).
func validSessionID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// Save writes the state file atomically with 0600 permissions (dir 0700).
func (s *State) Save() error {
	path, err := statePath(s.SessionID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := secureStateDir(dir); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding upload state: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "."+s.SessionID+"-*.tmp")
	if err != nil {
		return fmt.Errorf("creating upload state temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("securing upload state temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("writing upload state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("syncing upload state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing upload state: %w", err)
	}
	if err := replaceStateFile(tmpPath, path); err != nil {
		return fmt.Errorf("writing upload state: %w", err)
	}
	if err := syncStateDirectory(dir); err != nil {
		return fmt.Errorf("syncing upload state directory: %w", err)
	}
	return nil
}

// LoadState reads the state file for sessionID. A missing file surfaces as
// os.ErrNotExist and an unparsable one as ErrStateCorrupt, so callers can
// fall back to server-side session state in both cases.
func LoadState(sessionID string) (*State, error) {
	path, err := statePath(sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("%w: delete %s or rerun --resume %s to rebuild from the server (%v)",
			ErrStateCorrupt, path, sessionID, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("securing upload state: %w", err)
	}
	return &st, nil
}

// RemoveState deletes the state file for sessionID; a missing file is not
// an error (removal is idempotent).
func RemoveState(sessionID string) error {
	path, err := statePath(sessionID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncStateDirectory(filepath.Dir(path))
}

func secureStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("securing state directory: %w", err)
	}
	return nil
}
