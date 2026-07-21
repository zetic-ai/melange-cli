package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrStateCorrupt reports a state file that exists but cannot be parsed.
// Callers may treat it as missing (after warning) and rebuild the session
// state from the server.
var ErrStateCorrupt = errors.New("state file corrupt")

// State is the persisted record of an in-flight upload session, stored at
// ${XDG_STATE_HOME:-~/.local/state}/melange/uploads/<session-id>.json.
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

// StateDir returns the directory that holds upload state files:
// ${XDG_STATE_HOME:-~/.local/state}/melange/uploads.
func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "melange", "uploads"), nil
}

// statePath validates the session id (it becomes a path component) and
// returns the state file path for it.
func statePath(sessionID string) (string, error) {
	if !validSessionID(sessionID) {
		return "", fmt.Errorf("invalid upload session id %q", sessionID)
	}
	dir, err := StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".json"), nil
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding upload state: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("writing upload state: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("writing upload state: %w", err)
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
	return &st, nil
}

// RemoveState deletes the state file for sessionID; a missing file is not
// an error (removal is idempotent).
func RemoveState(sessionID string) error {
	path, err := statePath(sessionID)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
