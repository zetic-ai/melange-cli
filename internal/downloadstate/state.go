// Package downloadstate serializes and persists one logical, billable model
// download authorization. It deliberately stores no signed URLs or tokens.
package downloadstate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"
)

const stateVersion = 2

const (
	statusPending   = "pending"
	statusCompleted = "completed"
)

var (
	ErrStateCorrupt  = errors.New("download replay state is corrupt")
	ErrStateMismatch = errors.New("download replay state does not match this download")
)

// Identity is the server-side billing scope. Local output is intentionally
// excluded: artifact count and names are disclosed only after authorization,
// so a charged attempt must survive a correction from file/stdout to a
// directory without rotating its key.
type Identity struct {
	Host    string `json:"host"`
	Account string `json:"account"`
	Repo    string `json:"repo"`
	Model   string `json:"model"`
	Target  string `json:"target"`
}

// Output identifies the local settlement intent separately from billing.
type Output struct {
	Mode string `json:"mode"`
	Path string `json:"path"`
}

// State is the durable authorization lifecycle. A completed tombstone is
// retained long enough to recognize processes that were already waiting and
// recovery outputs that failed after another process completed.
type State struct {
	Version         int       `json:"version"`
	Identity        Identity  `json:"identity"`
	IdempotencyKey  string    `json:"idempotency_key"`
	CreatedAt       time.Time `json:"created_at"`
	Status          string    `json:"status"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	CompletedOutput *Output   `json:"completed_output,omitempty"`
	Owners          []string  `json:"owners,omitempty"`
	Recoveries      []Output  `json:"recoveries,omitempty"`
}

// Lease holds the per-identity cross-process lock for the entire command.
type Lease struct {
	id         Identity
	output     Output
	state      State
	statePath  string
	markerPath string
	attemptID  string
	lock       *fileLock
	closed     bool
}

// Path returns the target-scoped per-user application state path.
func Path(id Identity) (string, error) {
	if err := validateIdentity(id); err != nil {
		return "", err
	}
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(id)
	if err != nil {
		return "", fmt.Errorf("encoding download replay identity: %w", err)
	}
	sum := sha256.Sum256(raw)
	return filepath.Join(dir, hex.EncodeToString(sum[:])+".json"), nil
}

// Acquire registers this process before waiting, locks the billing identity,
// and either reuses or creates its authorization key.
func Acquire(ctx context.Context, id Identity, output Output, generate func() string) (*Lease, error) {
	if err := validateIdentity(id); err != nil {
		return nil, err
	}
	if err := validateOutput(output); err != nil {
		return nil, err
	}
	path, err := Path(id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := secureDir(dir); err != nil {
		return nil, err
	}

	attemptID, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("creating download attempt identity: %w", err)
	}
	markerDir := stringsTrimSuffix(path, ".json") + ".attempts"
	if err := secureDir(markerDir); err != nil {
		return nil, err
	}
	// Marker publication and the completion snapshot share a short-lived
	// registry lock. This closes the race where a follower could publish its
	// marker after Complete listed the directory but before it saved the
	// tombstone. Never hold this lock while waiting for the main transfer lock.
	registry, err := acquireFileLock(ctx, markerDir+".lock")
	if err != nil {
		return nil, err
	}
	markerPath := filepath.Join(markerDir, attemptID+".json")
	marker := struct {
		ID        string    `json:"id"`
		PID       int       `json:"pid"`
		Output    Output    `json:"output"`
		CreatedAt time.Time `json:"created_at"`
	}{attemptID, os.Getpid(), output, time.Now().UTC()}
	if err := writeAtomic(markerPath, marker, false); err != nil {
		_ = registry.Close()
		return nil, fmt.Errorf("registering download attempt: %w", err)
	}
	if err := registry.Close(); err != nil {
		_ = os.Remove(markerPath)
		_ = syncDirectory(markerDir)
		return nil, err
	}

	lock, err := acquireFileLock(ctx, stringsTrimSuffix(path, ".json")+".lock")
	if err != nil {
		if removeErr := os.Remove(markerPath); removeErr == nil {
			_ = syncDirectory(markerDir)
		}
		return nil, err
	}
	lease := &Lease{
		id: id, output: output, statePath: path, markerPath: markerPath,
		attemptID: attemptID, lock: lock,
	}
	if err := lease.loadOrCreate(generate); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return lease, nil
}

// State returns a copy of the locked lifecycle state.
func (l *Lease) State() State { return l.state }

func (l *Lease) loadOrCreate(generate func() string) error {
	st, err := load(l.statePath, l.id)
	if err == nil {
		if st.Status == statusCompleted && !contains(st.Owners, l.attemptID) && !containsOutput(st.Recoveries, l.output) {
			return l.rotate(generate)
		}
		l.state = *st
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return l.rotate(generate)
}

func (l *Lease) rotate(generate func() string) error {
	key := generate()
	if key == "" {
		return errors.New("creating download replay state: empty idempotency key")
	}
	l.state = State{
		Version: stateVersion, Identity: l.id, IdempotencyKey: key,
		CreatedAt: time.Now().UTC(), Status: statusPending,
	}
	return l.save()
}

// PreserveRecovery records this output only when the authorization was
// already completed by another serialized process. A later process correcting
// or forcing that output will then reuse the same key.
func (l *Lease) PreserveRecovery() error {
	if l.closed {
		return errors.New("download replay lease is closed")
	}
	if l.state.Status != statusCompleted || containsOutput(l.state.Recoveries, l.output) {
		return nil
	}
	l.state.Recoveries = append(l.state.Recoveries, l.output)
	return l.save()
}

// Complete durably converts active state to a tombstone. Owners includes every
// process that registered before completion, including lock waiters.
func (l *Lease) Complete() error {
	if l.closed {
		return errors.New("download replay lease is closed")
	}
	markerDir := filepath.Dir(l.markerPath)
	registry, err := acquireFileLock(context.Background(), markerDir+".lock")
	if err != nil {
		return err
	}
	owners, err := activeAttemptIDs(markerDir)
	if err != nil {
		return errors.Join(err, registry.Close())
	}
	l.state.Status = statusCompleted
	l.state.CompletedAt = time.Now().UTC()
	completed := l.output
	l.state.CompletedOutput = &completed
	l.state.Owners = owners
	l.state.Recoveries = removeOutput(l.state.Recoveries, l.output)
	if err := l.save(); err != nil {
		return errors.Join(err, registry.Close())
	}
	return registry.Close()
}

func (l *Lease) save() error {
	return writeAtomic(l.statePath, &l.state, true)
}

// Close removes this process's registration and releases the OS lock.
func (l *Lease) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	var result error
	if err := os.Remove(l.markerPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = fmt.Errorf("removing download attempt registration: %w", err)
	} else if err := syncDirectory(filepath.Dir(l.markerPath)); err != nil {
		result = fmt.Errorf("syncing download attempt directory: %w", err)
	}
	if err := l.lock.Close(); err != nil && result == nil {
		result = err
	}
	return result
}

func load(path string, want Identity) (*State, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, corrupt(path, err)
	}
	if st.Version != stateVersion || st.IdempotencyKey == "" || (st.Status != statusPending && st.Status != statusCompleted) {
		return nil, corrupt(path, nil)
	}
	if st.Identity != want {
		return nil, fmt.Errorf("%w at %s; preserve it and contact support before retrying", ErrStateMismatch, path)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("securing download replay state: %w", err)
	}
	return &st, nil
}

func corrupt(path string, cause error) error {
	message := fmt.Sprintf("at %s; preserve it and contact support before retrying, because deleting it may cause another bandwidth charge", path)
	if cause != nil {
		message += ": " + cause.Error()
	}
	return fmt.Errorf("%w: %s", ErrStateCorrupt, message)
}

func writeAtomic(path string, value any, replace bool) error {
	if err := secureDir(filepath.Dir(path)); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding download replay state: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".download-state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) //nolint:errcheck
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceAtomic(tmpPath, path, replace); err != nil {
		return err
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("syncing download state directory: %w", err)
	}
	return nil
}

func activeAttemptIDs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			ids = append(ids, stringsTrimSuffix(entry.Name(), ".json"))
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func secureDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating download replay state directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("securing download replay state directory: %w", err)
	}
	return nil
}

func stateDir() (string, error) {
	if runtime.GOOS == "windows" {
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			return filepath.Join(localAppData, "melange", "downloads"), nil
		}
	}
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving download replay state directory: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "melange", "downloads"), nil
}

func validateIdentity(id Identity) error {
	if id.Host == "" || id.Account == "" || id.Repo == "" || id.Model == "" || id.Target == "" {
		return errors.New("invalid incomplete download replay identity")
	}
	return nil
}

func validateOutput(output Output) error {
	if output.Path == "" {
		return errors.New("invalid incomplete download output identity")
	}
	switch output.Mode {
	case "directory", "file", "stdout":
		return nil
	default:
		return fmt.Errorf("invalid download replay output mode %q", output.Mode)
	}
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsOutput(values []Output, want Output) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func removeOutput(values []Output, remove Output) []Output {
	out := values[:0]
	for _, value := range values {
		if value != remove {
			out = append(out, value)
		}
	}
	return out
}

func stringsTrimSuffix(value, suffix string) string {
	if len(value) >= len(suffix) && value[len(value)-len(suffix):] == suffix {
		return value[:len(value)-len(suffix)]
	}
	return value
}
