package uploadflow

// The functions in this file are moved essentially verbatim from
// internal/cmd/model/upload.go (CLI-PR3 Task 1). Presentation-only pieces —
// prompts, table rendering, hint printing, exporter output — stayed behind
// in the cobra adapter; everything that talks to the API, GCS, or the
// durable resume state lives here.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/text"
	"github.com/zetic-ai/melange-cli/internal/upload"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

// hashNoteThreshold is the file size above which a "hashing" progress note
// is emitted during manifest digesting.
const hashNoteThreshold = 100 * 1024 * 1024

// BuildSpecs digests the local files named by in. Usage-shaped problems
// (missing model file, duplicate basenames, invalid manifests) surface as
// *UsageError. The returned buckets echo in.Buckets or, for a manifest
// document, its declared buckets.
func BuildSpecs(in ManifestInputs, events Events) ([]upload.FileSpec, []upload.BucketSpec, error) {
	if events == nil {
		events = NopEvents{}
	}
	note := func(path string, size int64) {
		if size >= hashNoteThreshold {
			events.Note(fmt.Sprintf("Hashing %s (%s)...",
				text.SanitizeTerminalInline(filepath.Base(path)), text.FormatBytes(size)))
		}
	}

	var specs []upload.FileSpec
	buckets := in.Buckets
	var err error
	if in.InputManifest != "" {
		specs, buckets, err = upload.LoadManifestDocV2(in.InputManifest, note)
	} else {
		if in.ModelFile == "" {
			return nil, nil, &UsageError{Err: errors.New(
				"MODEL_FILE is required (or pass --input-manifest); see `melange model upload --help`")}
		}
		if len(in.Buckets) > 0 {
			specs, err = upload.BuildBucketedManifest(
				in.ModelFile, in.Inputs, in.External, in.Buckets, note)
		} else {
			specs, err = upload.BuildManifest(in.ModelFile, in.Inputs, in.External, note)
		}
	}
	if err != nil {
		if errors.Is(err, upload.ErrDuplicateFilename) || errors.Is(err, upload.ErrInvalidManifest) {
			return nil, nil, &UsageError{Err: err}
		}
		return nil, nil, err
	}
	return specs, buckets, nil
}

// ---------------------------------------------------------------------------
// create
// ---------------------------------------------------------------------------

// Run drives one fresh upload: create the session (Idempotency-Key
// 201/200-replay semantics), transfer all files, then complete. See Result
// for the partial-result-with-Lease contract on errors.
func (o *Orchestrator) Run(ctx context.Context, req Request) (*Result, error) {
	body := gen.CreateModelUploadJSONRequestBody{
		ManifestVersion: gen.N2,
		Files:           manifestFiles(req.Specs),
		Options:         ManifestOptions(req.Buckets),
	}
	var resp *gen.CreateModelUploadResult
	var err error
	for createAttempt := 0; createAttempt < 2; createAttempt++ {
		resp, err = o.Gen.CreateModelUploadWithResponse(ctx, req.Account, req.Name,
			&gen.CreateModelUploadParams{IdempotencyKey: api.NewIdempotencyKeyParam()}, body)
		if err != nil {
			return nil, err
		}
		if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
			if resp.StatusCode() != 409 {
				return nil, aerr
			}
			conflict := o.activeSessionConflict(ctx, req, aerr)
			if conflict.Stale && createAttempt == 0 {
				o.events().Note("The conflicting session finished while the upload was starting; retrying once.")
				continue
			}
			return nil, conflict
		}
		break
	}
	if resp == nil {
		return nil, errors.New("creating upload session produced no response")
	}
	session := resp.JSON201
	if session == nil {
		session = resp.JSON200 // Idempotency-Key replay of the same manifest
	}
	if session == nil {
		return nil, fmt.Errorf("unexpected response creating upload session (HTTP %d)", resp.StatusCode())
	}

	st, err := stateFromSession(session, req.Specs, req.Repo)
	if err != nil {
		return nil, err
	}
	lease, err := upload.AcquireSession(ctx, st.SessionID)
	if err != nil {
		return nil, fmt.Errorf("locking upload session %s: %w", st.SessionID, err)
	}
	if err := st.Save(); err != nil {
		return &Result{SessionID: st.SessionID, Repo: st.Repo, Lease: lease}, err
	}

	var total int64
	for _, s := range req.Specs {
		total += s.Size
	}
	o.events().Note(fmt.Sprintf("Upload session %s: %d files, %s to %s",
		text.SanitizeTerminalInline(st.SessionID), len(st.Files), text.FormatBytes(total),
		text.SanitizeTerminalInline(req.Repo)))

	return o.transferAndComplete(ctx, req.Account, req.Name, req.Wait, req.Timeout, st, lease)
}

// ManifestOptions converts validated local bucket declarations to the exact
// OpenAPI wire shape. A nil pointer omits options for ordinary models.
func ManifestOptions(specs []upload.BucketSpec) *gen.ManifestOptions {
	if len(specs) == 0 {
		return nil
	}
	buckets := make([]gen.ManifestBucket, len(specs))
	for i, bucket := range specs {
		buckets[i] = gen.ManifestBucket{Index: bucket.Index, Dims: bucket.Dims}
	}
	return &gen.ManifestOptions{Buckets: &buckets}
}

// manifestFiles converts local specs into the wire manifest.
func manifestFiles(specs []upload.FileSpec) []gen.ManifestFile {
	files := make([]gen.ManifestFile, len(specs))
	for i, s := range specs {
		mf := gen.ManifestFile{
			ClientFileId: s.ClientFileID,
			Role:         gen.ManifestFileRole(s.Role),
			Filename:     s.Filename,
			Size:         int(s.Size),
			Crc32c:       s.CRC32C,
		}
		if s.SHA256 != "" {
			sha := s.SHA256
			mf.Sha256 = &sha
		}
		if s.Role == upload.RoleInput {
			idx := s.InputIndex
			mf.InputIndex = &idx
			if s.BucketIndex != nil {
				bucket := *s.BucketIndex
				mf.BucketIndex = &bucket
			}
		}
		files[i] = mf
	}
	return files
}

// stateFromSession joins the server's issued files with the local specs.
func stateFromSession(session *gen.ModelUploadResponse, specs []upload.FileSpec, repo string) (*upload.State, error) {
	issued := make(map[string]gen.IssuedSessionFile, len(session.Files))
	for _, f := range session.Files {
		issued[f.ClientFileId] = f
	}
	st := &upload.State{
		SessionID: session.Id,
		Repo:      repo,
		Tag:       session.Tag,
		CreatedAt: time.Now().UTC(),
	}
	for _, s := range specs {
		isf, ok := issued[s.ClientFileID]
		if !ok {
			return nil, fmt.Errorf("server response is missing file %s (%s)", s.ClientFileID, s.Filename)
		}
		st.Files = append(st.Files, &upload.StateFile{
			ClientFileID:  s.ClientFileID,
			LocalPath:     s.Path,
			CanonicalPath: isf.CanonicalPath,
			UploadURL:     deref(isf.UploadUrl),
			Size:          s.Size,
			CRC32C:        s.CRC32C,
		})
	}
	return st, nil
}

// activeSessionConflict turns a 409 on create into a typed, state-aware
// conflict. ConflictError.Stale is true when the conflicting session became
// terminal during conflict resolution, so the caller may safely retry
// session creation once.
func (o *Orchestrator) activeSessionConflict(ctx context.Context, req Request, orig error) *ConflictError {
	preferredID := ""
	var apiErr *api.Error
	if errors.As(orig, &apiErr) {
		preferredID = apiErr.ActiveUploadID
	}
	sessionID, state, stale := o.resolveActiveSession(ctx, req, preferredID)
	if stale {
		return &ConflictError{Stale: true, Err: orig}
	}
	return &ConflictError{SessionID: sessionID, State: state, Err: orig}
}

// resolveActiveSession prefers the structured conflict ID. Its detail endpoint
// provides the authoritative state; the list endpoint is a compatibility
// fallback for older servers and for a transient detail lookup failure.
func (o *Orchestrator) resolveActiveSession(ctx context.Context, req Request, preferredID string) (string, string, bool) {
	if preferredID != "" {
		if resp, err := o.Gen.GetModelUploadWithResponse(ctx, req.Account, req.Name, preferredID); err == nil &&
			api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body) == nil && resp.JSON200 != nil {
			state := string(resp.JSON200.State)
			if activeUploadSessionState(state) {
				return preferredID, state, false
			}
			if terminalSessionState(state) {
				return preferredID, state, true
			}
		}
	}

	resp, err := o.Gen.ListModelUploadsWithResponse(ctx, req.Account, req.Name)
	if err != nil || api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body) != nil || resp.JSON200 == nil {
		return preferredID, "", false
	}
	for _, session := range resp.JSON200.Results {
		if preferredID != "" && session.Id != preferredID {
			continue
		}
		state := string(session.State)
		if activeUploadSessionState(state) {
			return session.Id, state, false
		}
	}
	// The create returned an active-slot conflict, but the authoritative list
	// now contains no active slot holder. The old session crossed a terminal
	// boundary during the race; one create retry is safe.
	return preferredID, "", true
}

func activeUploadSessionState(state string) bool {
	switch strings.ToUpper(state) {
	case SessionStateCreated, SessionStateUploading, SessionStateVerifying, SessionStateDispatchPending:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// transfer
// ---------------------------------------------------------------------------

// transferAndComplete uploads all pending files then completes the session.
// Transfer failures — including interrupts — preserve the session and
// surface as *SessionError with PhaseTransfer.
func (o *Orchestrator) transferAndComplete(ctx context.Context, account, name string,
	doWait bool, timeout time.Duration, st *upload.State, lease *upload.SessionLease,
) (*Result, error) {
	up := &upload.Uploader{
		Client:       o.Bare,
		StallTimeout: o.StallTimeout,
		Sleep:        o.TransferSleep,
	}
	if err := o.transferAll(ctx, account, name, st, up); err != nil {
		return &Result{SessionID: st.SessionID, Repo: st.Repo, Lease: lease},
			&SessionError{Phase: PhaseTransfer, SessionID: st.SessionID, Repo: st.Repo, Err: err}
	}
	return o.completeSession(ctx, account, name, st.SessionID, st.Repo, doWait, timeout, lease)
}

// transferAll uploads files sequentially. The per-file loop is the seam for
// future --concurrency support.
func (o *Orchestrator) transferAll(ctx context.Context, account, name string, st *upload.State, up *upload.Uploader) error {
	for _, sf := range st.Files {
		if sf.Uploaded {
			continue
		}
		if err := o.transferOne(ctx, account, name, st, up, sf); err != nil {
			return err
		}
		sf.Uploaded = true
		saveState(st)
	}
	return nil
}

// transferOne moves one file: open (or re-open) the resumable session, query
// the committed offset when resuming, stream the remaining chunks, and — on
// an expired signed URL or session — reissue through the API once and
// restart the file.
//
// Progress events forward server-committed offsets below the file size; the
// single committed == size event is emitted where the file completes.
func (o *Orchestrator) transferOne(ctx context.Context, account, name string, st *upload.State, up *upload.Uploader, sf *upload.StateFile) error {
	events := o.events()
	file := filepath.Base(sf.LocalPath)
	reissued := false
	reissue := func() error {
		if reissued {
			return fmt.Errorf("upload URL for %s expired twice; try again or --cancel %s", sf.CanonicalPath, st.SessionID)
		}
		reissued = true
		if err := o.reissueURL(ctx, account, name, st, sf); err != nil {
			return err
		}
		sf.SessionURI = ""
		sf.Offset = 0
		saveState(st)
		return nil
	}

	for {
		var from int64
		if sf.SessionURI == "" {
			if sf.UploadURL == "" {
				if err := reissue(); err != nil {
					return err
				}
			}
			uri, err := up.StartSession(ctx, sf.UploadURL)
			if errors.Is(err, upload.ErrSessionExpired) {
				if rerr := reissue(); rerr != nil {
					return rerr
				}
				continue
			}
			if err != nil {
				return err
			}
			sf.SessionURI = uri
			sf.Offset = 0
			if err := st.Save(); err != nil {
				// The session URI is a bearer credential and the only way to
				// resume without opening a new session. Never transfer a byte
				// until it is durably persisted.
				return fmt.Errorf("persisting resumable upload session before transfer: %w", err)
			}
		} else {
			// Resuming an existing session: the server's committed offset is
			// authoritative — the state offset is only a hint.
			off, done, err := up.QueryOffset(ctx, sf.SessionURI, sf.Size)
			if errors.Is(err, upload.ErrSessionExpired) {
				if rerr := reissue(); rerr != nil {
					return rerr
				}
				continue
			}
			if err != nil {
				return err
			}
			if done {
				sf.Offset = sf.Size
				events.Progress(file, sf.Size, sf.Size)
				return nil
			}
			from = off
		}

		err := up.UploadFile(ctx, sf.SessionURI, sf.LocalPath, sf.Size, from, func(committed int64) {
			sf.Offset = committed
			if committed < sf.Size {
				events.Progress(file, committed, sf.Size)
			}
			saveState(st)
		})
		if errors.Is(err, upload.ErrSessionExpired) {
			if rerr := reissue(); rerr != nil {
				return rerr
			}
			continue
		}
		if err != nil {
			return err
		}
		events.Progress(file, sf.Size, sf.Size)
		return nil
	}
}

// reissueURL fetches a fresh signed resumable-start URL for one file.
func (o *Orchestrator) reissueURL(ctx context.Context, account, name string, st *upload.State, sf *upload.StateFile) error {
	resp, err := o.Gen.ReissueUploadFilesWithResponse(ctx, account, name, st.SessionID,
		gen.ReissueUploadFilesJSONRequestBody{ClientFileIds: []string{sf.ClientFileID}})
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return fmt.Errorf("reissuing upload URL for %s: %w", sf.CanonicalPath, aerr)
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("unexpected response reissuing upload URL (HTTP %d)", resp.StatusCode())
	}
	for _, f := range resp.JSON200.Files {
		if f.ClientFileId == sf.ClientFileID && f.UploadUrl != nil {
			sf.UploadURL = *f.UploadUrl
			return nil
		}
	}
	return fmt.Errorf("server did not reissue an upload URL for %s", sf.ClientFileID)
}

// saveState persists progress best-effort: a failed save must never abort a
// running upload (the server offset query recovers on resume anyway).
func saveState(st *upload.State) {
	_ = st.Save()
}

// ---------------------------------------------------------------------------
// complete
// ---------------------------------------------------------------------------

// completeSession finishes the session. Completion is itself asynchronous:
// VERIFYING, DISPATCH_PENDING, and even CONVERTING may temporarily carry no
// model reference. Those responses keep local recovery state; Wait
// deliberately replays complete with fresh idempotency keys until the model
// reference or a terminal failure is observable.
func (o *Orchestrator) completeSession(ctx context.Context, account, name, sessionID, repo string,
	doWait bool, timeout time.Duration, lease *upload.SessionLease,
) (*Result, error) {
	waitStarted := o.clockNow()
	completionCtx := ctx
	cancelCompletion := func() {}
	if doWait {
		completionCtx, cancelCompletion = context.WithTimeoutCause(ctx, timeout, wait.ErrTimeout)
	}
	defer cancelCompletion()

	partial := &Result{SessionID: sessionID, Repo: repo, Lease: lease}
	fail := func(err error) (*Result, error) {
		return partial, &SessionError{Phase: PhaseComplete, SessionID: sessionID, Repo: repo, Err: err}
	}

	resp, err := o.Gen.CompleteModelUploadWithResponse(completionCtx, account, name, sessionID,
		&gen.CompleteModelUploadParams{IdempotencyKey: api.NewIdempotencyKeyParam()})
	if err != nil {
		if errors.Is(context.Cause(completionCtx), wait.ErrTimeout) {
			return fail(wait.ErrTimeout)
		}
		return fail(err)
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return fail(aerr)
	}
	if resp.JSON200 == nil {
		return partial, fmt.Errorf("unexpected response completing upload session (HTTP %d)", resp.StatusCode())
	}

	if doWait && resp.JSON200.Model == nil && RecoverableCompletionState(string(resp.JSON200.State)) {
		remaining := timeout - o.clockNow().Sub(waitStarted)
		if remaining <= 0 {
			return fail(wait.ErrTimeout)
		}
		resp, err = o.waitForCompletionModel(completionCtx, account, name, sessionID, remaining)
		if errors.Is(err, wait.ErrTimeout) {
			return fail(wait.ErrTimeout)
		}
		if err != nil {
			return fail(err)
		}
	}

	return &Result{
		SessionID:   sessionID,
		Repo:        repo,
		Response:    resp.JSON200,
		Completion:  json.RawMessage(resp.Body),
		Model:       modelJSONOrNil(resp.Body),
		WaitStarted: waitStarted,
		Lease:       lease,
	}, nil
}

func (o *Orchestrator) waitForCompletionModel(ctx context.Context, account, name, sessionID string,
	timeout time.Duration,
) (*gen.CompleteModelUploadResult, error) {
	var last *gen.CompleteModelUploadResult
	err := wait.Poll(ctx, wait.Options{
		Timeout: timeout,
		Jitter:  o.Jitter,
		Sleep:   o.Sleep,
		Now:     o.Now,
	}, func(ctx context.Context) (bool, error) {
		resp, err := o.Gen.CompleteModelUploadWithResponse(ctx, account, name, sessionID,
			&gen.CompleteModelUploadParams{IdempotencyKey: api.NewIdempotencyKeyParam()})
		if err != nil {
			return false, err
		}
		if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
			return false, aerr
		}
		if resp.JSON200 == nil {
			return false, fmt.Errorf("unexpected response completing upload session (HTTP %d)", resp.StatusCode())
		}
		last = resp
		out := resp.JSON200
		return out.Model != nil || strings.EqualFold(string(out.State), "FAILED") ||
			TerminalCompletionWithoutModel(out), nil
	})
	return last, err
}

// RecoverableCompletionState reports whether a session state means all bytes
// are server-owned and completion can simply be replayed.
func RecoverableCompletionState(state string) bool {
	switch strings.ToUpper(state) {
	case SessionStateVerifying, SessionStateDispatchPending, "CONVERTING":
		return true
	default:
		return false
	}
}

// TerminalCompletionWithoutModel reports a completion response that is
// terminal yet carries no model reference: such a session can never yield a
// model and a new upload must be started.
func TerminalCompletionWithoutModel(out *gen.CompleteModelUploadResponse) bool {
	if out == nil || out.Model != nil {
		return false
	}
	switch strings.ToUpper(string(out.State)) {
	case "CANCELED", "EXPIRED":
		return true
	default:
		return false
	}
}

// CompletedModelJSON extracts the raw model object from an upload-complete
// response body, byte-exact.
func CompletedModelJSON(body []byte) (json.RawMessage, error) {
	var response struct {
		Model json.RawMessage `json:"model"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decoding completed upload model: %w", err)
	}
	if len(response.Model) == 0 || string(response.Model) == "null" {
		return nil, errors.New("decoding completed upload model: response carried no model reference")
	}
	return response.Model, nil
}

// modelJSONOrNil is CompletedModelJSON for Result population: absence (or a
// malformed body) is nil rather than an error, because Result.Response is
// the authoritative presence signal.
func modelJSONOrNil(body []byte) json.RawMessage {
	m, err := CompletedModelJSON(body)
	if err != nil {
		return nil
	}
	return m
}

// ---------------------------------------------------------------------------
// resume
// ---------------------------------------------------------------------------

// Resume continues the session: replay completion when the server already
// owns every byte, otherwise reconcile local state (rebuilding it from the
// server when missing or corrupt) and transfer the remainder. See Result for
// the partial-result-with-Lease contract on errors.
func (o *Orchestrator) Resume(ctx context.Context, sessionID string, opts ResumeOptions) (*Result, error) {
	lease, err := upload.AcquireSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("locking upload session %s: %w", sessionID, err)
	}
	partial := &Result{SessionID: sessionID, Repo: opts.Repo, Lease: lease}

	st, err := upload.LoadState(sessionID)
	switch {
	case err == nil:
		if st.Repo != opts.Repo {
			return partial, &UsageError{Err: fmt.Errorf(
				"session %s belongs to %s, not %s", sessionID, st.Repo, opts.Repo)}
		}
	case errors.Is(err, os.ErrNotExist):
		st = nil // rebuild from the server below
	case errors.Is(err, upload.ErrStateCorrupt):
		o.events().Note("! " + text.SanitizeTerminalInline(err.Error()))
		st = nil // treat as missing: rebuild from the server below
	default:
		return partial, err
	}

	// Once all bytes are server-owned, resuming means replaying completion;
	// local artifacts are no longer required. The replay is safe and uses a
	// fresh idempotency key so an earlier intermediate response is not cached.
	detail, err := o.fetchUploadSession(ctx, opts.Account, opts.Name, sessionID)
	if err != nil {
		return partial, err
	}
	if RecoverableCompletionState(string(detail.State)) {
		return o.completeSession(ctx, opts.Account, opts.Name, sessionID, opts.Repo,
			opts.Wait, opts.Timeout, lease)
	}

	// Other terminal sessions can never be resumed, with or without local
	// state.
	if terminalSessionState(string(detail.State)) {
		return partial, &TerminalStateError{SessionID: sessionID, State: string(detail.State)}
	}

	if st == nil {
		st, err = o.rebuildStateFromServer(ctx, opts, sessionID, detail)
		if err != nil {
			return partial, err
		}
	}

	o.events().Note(fmt.Sprintf("Resuming upload session %s (%d files)",
		text.SanitizeTerminalInline(st.SessionID), len(st.Files)))
	return o.transferAndComplete(ctx, opts.Account, opts.Name, opts.Wait, opts.Timeout, st, lease)
}

// fetchUploadSession GETs one upload session's server-side detail.
func (o *Orchestrator) fetchUploadSession(ctx context.Context, account, name, sessionID string) (*gen.ModelUploadDetailResponse, error) {
	resp, err := o.Gen.GetModelUploadWithResponse(ctx, account, name, sessionID)
	if err != nil {
		return nil, err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return nil, aerr
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response fetching upload session (HTTP %d)", resp.StatusCode())
	}
	return resp.JSON200, nil
}

// terminalSessionState reports whether a session state (ADR-5 vocabulary,
// compared case-insensitively) is terminal — such a session can never be
// resumed.
func terminalSessionState(state string) bool {
	for _, terminal := range []string{"FAILED", "CANCELED", "EXPIRED", "CONVERTING", "COMPLETED"} {
		if strings.EqualFold(state, terminal) {
			return true
		}
	}
	return false
}

// rebuildStateFromServer reconstructs upload state for a resume when the
// local state file is gone (or corrupt): server arrival status decides which
// files are already uploaded, local files are matched by their canonical
// destination, and fresh URLs are reissued for the remainder.
func (o *Orchestrator) rebuildStateFromServer(ctx context.Context, opts ResumeOptions, sessionID string, detail *gen.ModelUploadDetailResponse) (*upload.State, error) {
	if opts.BuildSpecs == nil {
		return nil, fmt.Errorf(
			"no local state found for session %s; pass the original MODEL_FILE/--input/--external-data (or --input-manifest) arguments so local files can be matched to the session", sessionID)
	}
	specs, err := opts.BuildSpecs()
	if err != nil {
		return nil, err
	}

	// Match local specs to server files by canonical destination.
	byCanonical := make(map[string]gen.ModelUploadFileStatus, len(detail.Files))
	for _, f := range detail.Files {
		byCanonical[f.CanonicalPath] = f
	}
	st := &upload.State{
		SessionID: detail.Id,
		Repo:      opts.Repo,
		Tag:       detail.Tag,
		CreatedAt: time.Now().UTC(),
	}
	var pending []string
	pendingFiles := map[string]*upload.StateFile{}
	for _, s := range specs {
		canonical := strings.Replace(upload.CanonicalPathPreview(s), "{tag}", detail.Tag, 1)
		server, ok := byCanonical[canonical]
		if !ok {
			return nil, fmt.Errorf(
				"%s does not match any file in session %s (expected destination %s); pass the same files as the original upload", s.Path, sessionID, canonical)
		}
		sf := &upload.StateFile{
			ClientFileID:  server.ClientFileId,
			LocalPath:     s.Path,
			CanonicalPath: canonical,
			Size:          s.Size,
			CRC32C:        s.CRC32C,
			Uploaded:      server.Uploaded,
		}
		if server.Uploaded {
			sf.Offset = s.Size
		} else {
			pending = append(pending, server.ClientFileId)
			pendingFiles[server.ClientFileId] = sf
		}
		st.Files = append(st.Files, sf)
	}

	if len(pending) > 0 {
		rresp, err := o.Gen.ReissueUploadFilesWithResponse(ctx, opts.Account, opts.Name, sessionID,
			gen.ReissueUploadFilesJSONRequestBody{ClientFileIds: pending})
		if err != nil {
			return nil, err
		}
		if aerr := api.GenError(rresp.StatusCode(), rresp.HTTPResponse, rresp.Body); aerr != nil {
			return nil, aerr
		}
		if rresp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected response reissuing upload URLs (HTTP %d)", rresp.StatusCode())
		}
		for _, f := range rresp.JSON200.Files {
			if sf, ok := pendingFiles[f.ClientFileId]; ok && f.UploadUrl != nil {
				sf.UploadURL = *f.UploadUrl
			}
		}
		for id, sf := range pendingFiles {
			if sf.UploadURL == "" {
				return nil, fmt.Errorf("server did not reissue an upload URL for %s", id)
			}
		}
	}

	if err := st.Save(); err != nil {
		return nil, err
	}
	return st, nil
}
