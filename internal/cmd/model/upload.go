package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
	"github.com/zetic-ai/melange-cli/internal/upload"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

// hashNoteThreshold is the file size above which a "hashing" progress note
// is printed to stderr during manifest digesting.
const hashNoteThreshold = 100 * 1024 * 1024

// sessionStateUploading is the active-session state (ADR-5 vocabulary,
// compared case-insensitively).
const sessionStateUploading = "UPLOADING"

type uploadOptions struct {
	f *cmdutil.Factory

	repo          string
	inputs        []string
	external      []string
	inputManifest string
	bucket        string
	dryRun        bool
	doWait        bool
	timeout       time.Duration
	inactivity    time.Duration
	resumeID      string
	cancelID      string
	sessions      bool
	exporter      *cmdutil.Exporter

	account string
	name    string
}

func newCmdUpload(f *cmdutil.Factory) *cobra.Command {
	opts := &uploadOptions{f: f}

	cmd := &cobra.Command{
		Use:   "upload MODEL_FILE [flags]",
		Short: "Upload a model to a repository",
		Long: `Upload a model (with optional sample inputs and external data) to a
repository through a resumable upload session.

The flow: a manifest with per-file sizes and CRC32C checksums opens a
session; bytes stream to signed storage URLs with resumable uploads; the
session is completed, the server verifies every checksum, registers the
model, and starts conversion. --wait then polls conversion status until
the model is ready.

-R ACCOUNT/REPO is always required — uploads never fall back to a default
repository. Interrupting an upload (Ctrl-C) preserves the session; resume
it with --resume SESSION_ID (already-uploaded bytes are never re-sent) or
discard it with --cancel SESSION_ID. --sessions lists sessions.

--dry-run prints the manifest that would be uploaded — including the
destination layout — without any network calls.

Exit codes: 0 success, 1 upload/verification/conversion failure, 2 usage
error, 4 not authenticated, 130 interrupted (session preserved).`,
		Example: `  # Upload a model with two sample inputs and wait for conversion
  melange model upload -R zetic/whisper-tiny model.onnx --input audio.bin --input mask.bin --wait

  # Preview the manifest without uploading
  melange model upload -R zetic/whisper-tiny model.onnx --dry-run

  # Resume an interrupted upload
  melange model upload --resume up_ab12cd -R zetic/whisper-tiny

  # List and clean up sessions
  melange model upload --sessions -R zetic/whisper-tiny
  melange model upload --cancel up_ab12cd -R zetic/whisper-tiny`,
		Args: cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUploadCommand(cmd.Context(), opts, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&opts.repo, "repo", "R", "", "Target repository as `ACCOUNT/REPO` (required)")
	fl.StringArrayVar(&opts.inputs, "input", nil, "Sample input `file` (repeatable; order defines input_index)")
	fl.StringArrayVar(&opts.external, "external-data", nil, "External data `file`, e.g. ONNX external weights (repeatable)")
	fl.StringVar(&opts.inputManifest, "input-manifest", "", "JSON manifest `file` describing all files (alternative to flags)")
	fl.StringVar(&opts.bucket, "bucket", "", "Bucket dims as `INDEX:DIMS` (not yet supported)")
	fl.BoolVar(&opts.dryRun, "dry-run", false, "Print the manifest without creating a session or uploading")
	fl.BoolVar(&opts.doWait, "wait", false, "After upload, wait until conversion reaches a terminal state")
	fl.DurationVar(&opts.timeout, "timeout", 30*time.Minute, "Maximum time to wait with --wait")
	fl.DurationVar(&opts.inactivity, "inactivity-timeout", 2*time.Minute, "Per-chunk stall timeout during uploads")
	fl.StringVar(&opts.resumeID, "resume", "", "Resume the upload `session`")
	fl.StringVar(&opts.cancelID, "cancel", "", "Cancel the upload `session`")
	fl.BoolVar(&opts.sessions, "sessions", false, "List upload sessions for the repository")
	cmdutil.AddJSONFlags(cmd, &opts.exporter)

	return cmd
}

func runUploadCommand(ctx context.Context, opts *uploadOptions, args []string) error {
	if opts.bucket != "" {
		return cmdutil.FlagError{Err: errors.New(
			"bucketed .pt2 uploads not yet supported by the CLI; use the dashboard or melange api")}
	}
	modes := 0
	for _, on := range []bool{opts.resumeID != "", opts.cancelID != "", opts.sessions} {
		if on {
			modes++
		}
	}
	if modes > 1 {
		return cmdutil.FlagError{Err: errors.New("--resume, --cancel, and --sessions are mutually exclusive")}
	}
	if (opts.cancelID != "" || opts.sessions) && (len(args) > 0 || len(opts.inputs) > 0 ||
		len(opts.external) > 0 || opts.inputManifest != "") {
		return cmdutil.FlagError{Err: errors.New("file arguments cannot be combined with --cancel/--sessions")}
	}
	if opts.dryRun && modes > 0 {
		return cmdutil.FlagError{Err: errors.New("--dry-run cannot be combined with --resume, --cancel, or --sessions")}
	}
	if opts.inputManifest != "" && (len(args) > 0 || len(opts.inputs) > 0 || len(opts.external) > 0) {
		return cmdutil.FlagError{Err: errors.New(
			"--input-manifest cannot be combined with MODEL_FILE, --input, or --external-data")}
	}

	account, name, err := splitRepoFlag(opts.repo)
	if err != nil {
		return err
	}
	opts.account, opts.name = account, name

	switch {
	case opts.sessions:
		return runSessions(ctx, opts)
	case opts.cancelID != "":
		return runCancel(ctx, opts)
	case opts.resumeID != "":
		return runResume(ctx, opts, args)
	}

	specs, err := buildSpecs(opts, args)
	if err != nil {
		return err
	}
	if opts.dryRun {
		return renderDryRun(opts, specs)
	}
	return runUpload(ctx, opts, specs)
}

// buildSpecs digests the local files named by flags or --input-manifest.
// Usage-shaped problems (missing args, duplicate basenames) map to exit 2.
func buildSpecs(opts *uploadOptions, args []string) ([]upload.FileSpec, error) {
	ios := opts.f.IOStreams
	note := func(path string, size int64) {
		if size >= hashNoteThreshold {
			fmt.Fprintf(ios.ErrOut, "Hashing %s (%s)...\n", filepath.Base(path), text.FormatBytes(size))
		}
	}

	var specs []upload.FileSpec
	var err error
	if opts.inputManifest != "" {
		specs, err = upload.LoadManifestDoc(opts.inputManifest, note)
	} else {
		if len(args) != 1 {
			return nil, cmdutil.FlagError{Err: errors.New(
				"MODEL_FILE is required (or pass --input-manifest); see `melange model upload --help`")}
		}
		specs, err = upload.BuildManifest(args[0], opts.inputs, opts.external, note)
	}
	if err != nil {
		if errors.Is(err, upload.ErrDuplicateFilename) {
			return nil, cmdutil.FlagError{Err: err}
		}
		return nil, err
	}
	return specs, nil
}

// ---------------------------------------------------------------------------
// dry run
// ---------------------------------------------------------------------------

// dryRunFile is the documented --json shape of one dry-run manifest entry.
type dryRunFile struct {
	ClientFileID  string `json:"client_file_id"`
	Role          string `json:"role"`
	Path          string `json:"path"`
	Filename      string `json:"filename"`
	Size          int64  `json:"size"`
	CRC32C        string `json:"crc32c"`
	SHA256        string `json:"sha256"`
	InputIndex    *int   `json:"input_index,omitempty"`
	CanonicalPath string `json:"canonical_path"`
}

// renderDryRun prints the normalized manifest. It is mutation-free: no
// network calls, no session, no state file.
func renderDryRun(opts *uploadOptions, specs []upload.FileSpec) error {
	ios := opts.f.IOStreams
	var total int64
	for _, s := range specs {
		total += s.Size
	}
	fmt.Fprintf(ios.ErrOut, "Dry run: %d files, %s total would be uploaded to %s (no session created)\n",
		len(specs), text.FormatBytes(total), opts.repo)

	if opts.exporter != nil {
		files := make([]dryRunFile, len(specs))
		for i, s := range specs {
			files[i] = dryRunFile{
				ClientFileID:  s.ClientFileID,
				Role:          s.Role,
				Path:          s.Path,
				Filename:      s.Filename,
				Size:          s.Size,
				CRC32C:        s.CRC32C,
				SHA256:        s.SHA256,
				CanonicalPath: upload.CanonicalPathPreview(s),
			}
			if s.Role == upload.RoleInput {
				idx := s.InputIndex
				files[i].InputIndex = &idx
			}
		}
		return opts.exporter.Write(ios, map[string]any{
			"repo":       opts.repo,
			"dry_run":    true,
			"files":      files,
			"total_size": total,
		})
	}

	tp := tableprinter.New(ios)
	tp.HeaderRow("role", "file", "size", "crc32c", "destination")
	isTTY := ios.IsStdoutTTY()
	for _, s := range specs {
		tp.AddField(s.Role)
		tp.AddField(s.Path, tableprinter.WithTruncate(false))
		if isTTY {
			tp.AddField(text.FormatBytes(s.Size))
		} else {
			tp.AddField(strconv.FormatInt(s.Size, 10))
		}
		tp.AddField(s.CRC32C)
		tp.AddField(upload.CanonicalPathPreview(s), tableprinter.WithTruncate(false))
		tp.EndRow()
	}
	return tp.Render()
}

// ---------------------------------------------------------------------------
// real upload
// ---------------------------------------------------------------------------

func runUpload(ctx context.Context, opts *uploadOptions, specs []upload.FileSpec) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}

	body := gen.CreateModelUploadJSONRequestBody{
		ManifestVersion: gen.N2,
		Files:           manifestFiles(specs),
	}
	resp, err := g.CreateModelUploadWithResponse(ctx, opts.account, opts.name, body,
		withIdempotencyKey(newIdempotencyKey()))
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		if resp.StatusCode() == 409 {
			return activeSessionConflict(ctx, opts, g, aerr)
		}
		return aerr
	}
	session := resp.JSON201
	if session == nil {
		session = resp.JSON200 // Idempotency-Key replay of the same manifest
	}
	if session == nil {
		return fmt.Errorf("unexpected response creating upload session (HTTP %d)", resp.StatusCode())
	}

	st, err := stateFromSession(session, specs, opts.repo)
	if err != nil {
		return err
	}
	if err := st.Save(); err != nil {
		return err
	}

	var total int64
	for _, s := range specs {
		total += s.Size
	}
	fmt.Fprintf(opts.f.IOStreams.ErrOut, "Upload session %s: %d files, %s to %s\n",
		st.SessionID, len(st.Files), text.FormatBytes(total), opts.repo)

	return transferAndComplete(ctx, opts, g, st)
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

// activeSessionConflict turns a 409 on create into actionable remediation:
// it names the active session and the exact resume/cancel commands.
func activeSessionConflict(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, orig error) error {
	sessionID := ""
	if resp, err := g.ListModelUploadsWithResponse(ctx, opts.account, opts.name); err == nil &&
		api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body) == nil && resp.JSON200 != nil {
		for _, s := range resp.JSON200.Results {
			if strings.EqualFold(s.State, sessionStateUploading) {
				sessionID = s.Id
				break
			}
		}
	}
	if sessionID == "" {
		return fmt.Errorf("%w\nList sessions with: melange model upload --sessions -R %s", orig, opts.repo)
	}
	errOut := opts.f.IOStreams.ErrOut
	fmt.Fprintf(errOut, "✗ An upload session is already active for %s: %s\n", opts.repo, sessionID)
	fmt.Fprintf(errOut, "\nResume it:  melange model upload --resume %s -R %s\n", sessionID, opts.repo)
	fmt.Fprintf(errOut, "Cancel it:  melange model upload --cancel %s -R %s\n", sessionID, opts.repo)
	return cmdutil.ErrSilent
}

// transferAndComplete uploads all pending files then completes the session.
// Interrupts (Ctrl-C) preserve the session and print the resume command.
func transferAndComplete(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, st *upload.State) error {
	up := &upload.Uploader{
		Client:       bareHTTPClient(opts.f),
		StallTimeout: opts.inactivity,
	}
	if err := transferAll(ctx, opts, g, st, up); err != nil {
		if errors.Is(err, context.Canceled) {
			printResumeHint(opts, st)
			return canceledSilently{}
		}
		return fmt.Errorf("%w\nThe session is preserved; resume with: melange model upload --resume %s -R %s",
			err, st.SessionID, st.Repo)
	}
	return completeAndReport(ctx, opts, g, st)
}

func printResumeHint(opts *uploadOptions, st *upload.State) {
	errOut := opts.f.IOStreams.ErrOut
	fmt.Fprintf(errOut, "\nInterrupted. The upload session is preserved; already-uploaded bytes will not be re-sent.\n")
	fmt.Fprintf(errOut, "Resume with: melange model upload --resume %s -R %s\n", st.SessionID, st.Repo)
}

// transferAll uploads files sequentially. The per-file loop is the seam for
// future --concurrency support.
func transferAll(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, st *upload.State, up *upload.Uploader) error {
	for _, sf := range st.Files {
		if sf.Uploaded {
			continue
		}
		if err := transferOne(ctx, opts, g, st, up, sf); err != nil {
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
func transferOne(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, st *upload.State, up *upload.Uploader, sf *upload.StateFile) error {
	prog := newProgress(opts.f, filepath.Base(sf.LocalPath), sf.Size)
	reissued := false
	reissue := func() error {
		if reissued {
			return fmt.Errorf("upload URL for %s expired twice; try again or --cancel %s", sf.CanonicalPath, st.SessionID)
		}
		reissued = true
		if err := reissueURL(ctx, opts, g, st, sf); err != nil {
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
			saveState(st)
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
				prog.done()
				return nil
			}
			from = off
		}

		err := up.UploadFile(ctx, sf.SessionURI, sf.LocalPath, sf.Size, from, func(committed int64) {
			sf.Offset = committed
			prog.update(committed)
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
		prog.done()
		return nil
	}
}

// reissueURL fetches a fresh signed resumable-start URL for one file.
func reissueURL(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, st *upload.State, sf *upload.StateFile) error {
	resp, err := g.ReissueUploadFilesWithResponse(ctx, opts.account, opts.name, st.SessionID,
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

// completeAndReport finishes the session and reports the outcome. An HTTP
// 200 with state FAILED is a failed outcome (exit 1).
func completeAndReport(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, st *upload.State) error {
	ios := opts.f.IOStreams
	resp, err := g.CompleteModelUploadWithResponse(ctx, opts.account, opts.name, st.SessionID,
		withIdempotencyKey(newIdempotencyKey()))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			printResumeHint(opts, st)
			return canceledSilently{}
		}
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return aerr
	}
	out := resp.JSON200
	if out == nil {
		return fmt.Errorf("unexpected response completing upload session (HTTP %d)", resp.StatusCode())
	}

	if strings.EqualFold(out.State, "FAILED") {
		fmt.Fprintf(ios.ErrOut, "✗ Upload verification failed: %s (session %s)\n", deref(out.FailureCode), out.Id)
		fmt.Fprintf(ios.ErrOut, "Fix the reported file and upload again.\n")
		// FAILED is terminal: the session can never be resumed, so keeping
		// the state file (and its session URIs) would only mislead --resume.
		_ = upload.RemoveState(st.SessionID)
		if opts.exporter != nil {
			_ = opts.exporter.Write(ios, json.RawMessage(resp.Body))
		}
		return cmdutil.ErrSilent
	}

	if out.Model != nil {
		fmt.Fprintf(ios.ErrOut, "✓ Upload complete: model %s version %d (state %s)\n",
			out.Model.Key, out.Model.Version, strings.ToLower(out.State))
	} else {
		fmt.Fprintf(ios.ErrOut, "✓ Upload complete: session %s (state %s)\n", out.Id, strings.ToLower(out.State))
	}
	// The session reached a server-terminal outcome; local state is done.
	_ = upload.RemoveState(st.SessionID)

	if opts.doWait {
		if out.Model == nil {
			return errors.New("cannot --wait: the complete response carried no model reference")
		}
		return waitForModel(ctx, opts.f, g, opts.account, opts.name, out.Model.Key, opts.timeout, opts.exporter)
	}
	if opts.exporter != nil {
		return opts.exporter.Write(ios, json.RawMessage(resp.Body))
	}
	return nil
}

// ---------------------------------------------------------------------------
// resume
// ---------------------------------------------------------------------------

func runResume(ctx context.Context, opts *uploadOptions, args []string) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}

	st, err := upload.LoadState(opts.resumeID)
	switch {
	case err == nil:
		if st.Repo != opts.repo {
			return cmdutil.FlagError{Err: fmt.Errorf(
				"session %s belongs to %s, not %s", opts.resumeID, st.Repo, opts.repo)}
		}
	case errors.Is(err, os.ErrNotExist):
		st = nil // rebuild from the server below
	case errors.Is(err, upload.ErrStateCorrupt):
		fmt.Fprintf(opts.f.IOStreams.ErrOut, "! %v\n", err)
		st = nil // treat as missing: rebuild from the server below
	default:
		return err
	}

	// The server's session state is authoritative: a terminal session can
	// never be resumed, with or without local state.
	detail, err := fetchUploadSession(ctx, opts, g)
	if err != nil {
		return err
	}
	if terminalSessionState(detail.State) {
		_ = upload.RemoveState(opts.resumeID)
		return fmt.Errorf("session %s is %s; start a new upload", opts.resumeID, strings.ToLower(detail.State))
	}

	if st == nil {
		st, err = rebuildStateFromServer(ctx, opts, g, detail, args)
		if err != nil {
			return err
		}
	}

	fmt.Fprintf(opts.f.IOStreams.ErrOut, "Resuming upload session %s (%d files)\n", st.SessionID, len(st.Files))
	return transferAndComplete(ctx, opts, g, st)
}

// fetchUploadSession GETs one upload session's server-side detail.
func fetchUploadSession(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses) (*gen.ModelUploadDetailResponse, error) {
	resp, err := g.GetModelUploadWithResponse(ctx, opts.account, opts.name, opts.resumeID)
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
	for _, terminal := range []string{"FAILED", "CANCELED", "EXPIRED"} {
		if strings.EqualFold(state, terminal) {
			return true
		}
	}
	return false
}

// rebuildStateFromServer reconstructs upload state for --resume when the
// local state file is gone (or corrupt): server arrival status decides which
// files are already uploaded, local files are matched by their canonical
// destination, and fresh URLs are reissued for the remainder.
func rebuildStateFromServer(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses, detail *gen.ModelUploadDetailResponse, args []string) (*upload.State, error) {
	if len(args) == 0 && opts.inputManifest == "" {
		return nil, fmt.Errorf(
			"no local state found for session %s; pass the original MODEL_FILE/--input/--external-data (or --input-manifest) arguments so local files can be matched to the session", opts.resumeID)
	}
	specs, err := buildSpecs(opts, args)
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
		Repo:      opts.repo,
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
				"%s does not match any file in session %s (expected destination %s); pass the same files as the original upload", s.Path, opts.resumeID, canonical)
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
		rresp, err := g.ReissueUploadFilesWithResponse(ctx, opts.account, opts.name, opts.resumeID,
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

// ---------------------------------------------------------------------------
// cancel / sessions
// ---------------------------------------------------------------------------

func runCancel(ctx context.Context, opts *uploadOptions) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}
	resp, err := g.CancelModelUploadWithResponse(ctx, opts.account, opts.name, opts.cancelID)
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return aerr
	}
	_ = upload.RemoveState(opts.cancelID)

	ios := opts.f.IOStreams
	fmt.Fprintf(ios.ErrOut, "✓ Canceled upload session %s\n", opts.cancelID)
	if opts.exporter != nil {
		return opts.exporter.Write(ios, json.RawMessage(resp.Body))
	}
	return nil
}

func runSessions(ctx context.Context, opts *uploadOptions) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}
	resp, err := g.ListModelUploadsWithResponse(ctx, opts.account, opts.name)
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return aerr
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("unexpected response listing upload sessions (HTTP %d)", resp.StatusCode())
	}

	ios := opts.f.IOStreams
	if opts.exporter != nil {
		return opts.exporter.Write(ios, json.RawMessage(resp.Body))
	}
	sessions := resp.JSON200.Results
	if len(sessions) == 0 {
		if ios.IsStdoutTTY() {
			fmt.Fprintln(ios.ErrOut, "No upload sessions found")
		}
		return nil
	}

	isTTY := ios.IsStdoutTTY()
	now := time.Now()
	tp := tableprinter.New(ios)
	tp.HeaderRow("id", "state", "created", "expires", "files")
	for _, s := range sessions {
		tp.AddField(s.Id)
		tp.AddField(s.State)
		if isTTY {
			tp.AddField(text.RelativeTime(s.CreatedAt, now))
		} else {
			tp.AddField(s.CreatedAt.Format(time.RFC3339))
		}
		tp.AddField(s.ExpiresAt.Format(time.RFC3339))
		tp.AddField(strconv.Itoa(s.FileCount))
		tp.EndRow()
	}
	return tp.Render()
}

// ---------------------------------------------------------------------------
// progress
// ---------------------------------------------------------------------------

// progress renders per-file transfer progress on stderr: a single updating
// line on a TTY, one completion line otherwise.
type progress struct {
	f     *cmdutil.Factory
	tty   bool
	name  string
	total int64
}

func newProgress(f *cmdutil.Factory, name string, total int64) *progress {
	return &progress{f: f, tty: f.IOStreams.IsStderrTTY(), name: name, total: total}
}

func (p *progress) update(committed int64) {
	if !p.tty || p.total <= 0 {
		return
	}
	pct := committed * 100 / p.total
	fmt.Fprintf(p.f.IOStreams.ErrOut, "\r↑ %s  %d%% (%s/%s)   ",
		p.name, pct, text.FormatBytes(committed), text.FormatBytes(p.total))
}

func (p *progress) done() {
	if p.tty {
		fmt.Fprintf(p.f.IOStreams.ErrOut, "\r✓ %s uploaded (%s)          \n", p.name, text.FormatBytes(p.total))
		return
	}
	fmt.Fprintf(p.f.IOStreams.ErrOut, "✓ %s uploaded (%s)\n", p.name, text.FormatBytes(p.total))
}

// waitForModel polls conversion status until a terminal state, then prints
// the final status. Failed models exit 1; a timeout prints how to keep
// checking and exits 1.
func waitForModel(ctx context.Context, f *cmdutil.Factory, g *gen.ClientWithResponses, account, name, key string, timeout time.Duration, exporter *cmdutil.Exporter) error {
	ios := f.IOStreams
	var last *gen.ModelStatusResponse
	var raw []byte

	err := wait.Poll(ctx, wait.Options{
		Timeout: timeout,
		Jitter:  pollJitter,
		Sleep:   pollSleep,
		Now:     pollNow,
	}, func(ctx context.Context) (bool, error) {
		resp, err := g.GetModelStatusWithResponse(ctx, account, name, key)
		if err != nil {
			return false, err
		}
		if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
			return false, aerr
		}
		if resp.JSON200 == nil {
			return false, fmt.Errorf("unexpected response fetching model status (HTTP %d)", resp.StatusCode())
		}
		last = resp.JSON200
		raw = resp.Body
		return last.Terminal, nil
	})
	if errors.Is(err, wait.ErrTimeout) {
		fmt.Fprintf(ios.ErrOut, "Timed out after %s; the model is still processing.\n", timeout)
		fmt.Fprintf(ios.ErrOut, "Check again with: melange model status %s -R %s/%s\n", key, account, name)
		return cmdutil.ErrSilent
	}
	if err != nil {
		return err
	}

	if perr := printStatus(f, exporter, last, raw, key, account+"/"+name); perr != nil {
		return perr
	}
	if strings.EqualFold(string(last.State), string(gen.Failed)) {
		fmt.Fprintf(ios.ErrOut, "✗ Model processing failed: %s\n", deref(last.FailureCode))
		return cmdutil.ErrSilent
	}
	return nil
}
