package model

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/iostreams"
	"github.com/zetic-ai/melange-cli/internal/tableprinter"
	"github.com/zetic-ai/melange-cli/internal/text"
	"github.com/zetic-ai/melange-cli/internal/upload"
	"github.com/zetic-ai/melange-cli/internal/uploadflow"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

type uploadOptions struct {
	f *cmdutil.Factory

	repo          string
	inputs        []string
	external      []string
	inputManifest string
	bucket        []string
	bucketSpecs   []upload.BucketSpec
	dryRun        bool
	doWait        bool
	timeout       time.Duration
	inactivity    time.Duration
	resumeID      string
	cancelID      string
	yes           bool
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
Once the server reports VERIFYING, DISPATCH_PENDING, or CONVERTING,
--resume replays completion and no longer needs the original local files.
Cancellation prompts for the exact session ID on a terminal; agents,
non-interactive runs, and --no-input must pass --yes explicitly.

When a signed upload URL expires, reissue intentionally mints a fresh URL
and carries no Idempotency-Key; create, complete, and cancel retain their
documented idempotency keys.

--dry-run prints the manifest that would be uploaded — including the
destination layout — without any network calls.

For a bucketed .pt2 model, repeat --bucket in declaration order and pass
one complete group of --input files for each bucket. For example, two
buckets and four inputs means two inputs per bucket: the first two inputs
belong to the first bucket and the next two to the second. Within every
bucket, input order defines input_index.

--input-manifest accepts this CLI-private local-file shape (it is not the
public API wire manifest):
  {
    "manifest_version": 2,
    "files": [
      {"path": "models/model.onnx", "role": "model"},
      {"path": "samples/audio.npy", "role": "input", "input_index": 0}
    ]
  }
path is required and is resolved relative to the manifest file. filename
is optional. input_index is optional for inputs and defaults by order;
bucket_index is valid for inputs when options.buckets is declared.

With --wait, structured output is
{"model": <created model>, "status": <final status>}; for example,
--jq .model.key returns the model key. Without --wait, --json remains the
raw upload-complete response.

Exit codes: 0 success, 1 upload/verification/conversion failure, 2 usage
error, 4 not authenticated, 130 interrupted (session preserved).`,
		Example: `  # Upload a model with two sample inputs and wait for conversion
  melange model upload -R zetic/whisper-tiny model.onnx --input audio.bin --input mask.bin --wait

  # Preview the manifest without uploading
  melange model upload -R zetic/whisper-tiny model.onnx --dry-run

  # A model-only manifest is valid; wait and print its stable model key
  melange model upload -R zetic/whisper-tiny model.onnx --wait --jq .model.key

  # Upload a .pt2 model with two shape buckets and two inputs per bucket
  melange model upload -R zetic/vision model.pt2 \
    --bucket 0:1x3x224x224 --input image-224.npy --input mask-224.npy \
    --bucket 1:1x3x384x384 --input image-384.npy --input mask-384.npy --wait

  # Resolve a resumable pre-completion session id

  session_id=$(melange model upload --sessions -R zetic/whisper-tiny --jq '.results | map(select(.state=="CREATED" or .state=="UPLOADING")) | first | .id // empty')

  # Do not accidentally send the literal JSON value null as an id
  [ -n "$session_id" ] || { echo "No resumable upload session found" >&2; exit 1; }

  # Resume it
  melange model upload --resume "$session_id" -R zetic/whisper-tiny

  # Or cancel it instead (scripts must opt in explicitly)
  melange model upload --cancel "$session_id" -R zetic/whisper-tiny --yes`,
		Args: cmdutil.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateWaitOptions(opts.doWait, opts.timeout, cmd.Flags().Changed("timeout")); err != nil {
				return err
			}
			return runUploadCommand(cmd.Context(), opts, args)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&opts.repo, "repo", "R", "", "Target repository as `ACCOUNT/REPO` (required)")
	fl.StringArrayVar(&opts.inputs, "input", nil, "Sample input `file` (repeatable; order defines input_index)")
	fl.StringArrayVar(&opts.external, "external-data", nil, "External data `file`, e.g. ONNX external weights (repeatable)")
	fl.StringVar(&opts.inputManifest, "input-manifest", "", "CLI-local JSON manifest `file` describing all files (alternative to flags)")
	fl.StringArrayVar(&opts.bucket, "bucket", nil, "`.pt2` bucket as `INDEX:DIMS` (repeatable; group --input files by bucket order)")
	fl.BoolVar(&opts.dryRun, "dry-run", false, "Print the manifest without creating a session or uploading")
	fl.BoolVar(&opts.doWait, "wait", false, "After upload, wait until conversion reaches a terminal state")
	fl.DurationVar(&opts.timeout, "timeout", 30*time.Minute, "Maximum time to wait with --wait")
	fl.DurationVar(&opts.inactivity, "inactivity-timeout", 2*time.Minute, "Per-chunk stall timeout during uploads")
	fl.StringVar(&opts.resumeID, "resume", "", "Resume the upload `session`")
	fl.StringVar(&opts.cancelID, "cancel", "", "Cancel the upload `session`")
	fl.BoolVarP(&opts.yes, "yes", "y", false, "Confirm upload-session cancellation without prompting")
	fl.BoolVar(&opts.sessions, "sessions", false, "List upload sessions for the repository")
	cmdutil.AddJSONFlags(cmd, &opts.exporter)

	return cmd
}

func runUploadCommand(ctx context.Context, opts *uploadOptions, args []string) error {
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
		len(opts.external) > 0 || len(opts.bucket) > 0 || opts.inputManifest != "") {
		return cmdutil.FlagError{Err: errors.New("file arguments cannot be combined with --cancel/--sessions")}
	}
	if opts.dryRun && modes > 0 {
		return cmdutil.FlagError{Err: errors.New("--dry-run cannot be combined with --resume, --cancel, or --sessions")}
	}
	if opts.yes && opts.cancelID == "" {
		return cmdutil.FlagError{Err: errors.New("--yes is only valid with --cancel")}
	}
	for _, item := range []struct {
		flag, sessionID string
	}{{"--resume", opts.resumeID}, {"--cancel", opts.cancelID}} {
		flag, sessionID := item.flag, item.sessionID
		if sessionID != "" {
			if err := upload.ValidateSessionID(sessionID); err != nil {
				return cmdutil.FlagError{Err: fmt.Errorf("%s: %w", flag, err)}
			}
		}
	}
	if opts.inputManifest != "" && (len(args) > 0 || len(opts.inputs) > 0 || len(opts.external) > 0 || len(opts.bucket) > 0) {
		return cmdutil.FlagError{Err: errors.New(
			"--input-manifest cannot be combined with MODEL_FILE, --input, --external-data, or --bucket")}
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

// buildSpecs digests the local files named by flags or --input-manifest via
// uploadflow.BuildSpecs. Usage-shaped problems (missing args, duplicate
// basenames) map to exit 2.
func buildSpecs(opts *uploadOptions, args []string) ([]upload.FileSpec, error) {
	in := uploadflow.ManifestInputs{
		Inputs:        opts.inputs,
		External:      opts.external,
		InputManifest: opts.inputManifest,
	}
	if len(args) == 1 {
		in.ModelFile = args[0]
	}
	if in.InputManifest == "" && in.ModelFile != "" && len(opts.bucket) > 0 {
		bucketSpecs, err := parseBucketFlags(opts.bucket)
		if err != nil {
			return nil, err
		}
		in.Buckets = bucketSpecs
	}

	specs, buckets, err := uploadflow.BuildSpecs(in, &uploadEvents{f: opts.f})
	if err != nil {
		var uerr *uploadflow.UsageError
		if errors.As(err, &uerr) {
			return nil, cmdutil.FlagError{Err: uerr.Err}
		}
		return nil, err
	}
	opts.bucketSpecs = buckets
	return specs, nil
}

func parseBucketFlags(values []string) ([]upload.BucketSpec, error) {
	buckets := make([]upload.BucketSpec, 0, len(values))
	for _, value := range values {
		parts := strings.SplitN(value, ":", 2)
		if len(parts) != 2 {
			return nil, cmdutil.FlagError{Err: fmt.Errorf(
				"invalid --bucket %q: expected INDEX:DIMxDIM", value)}
		}
		index, err := strconv.Atoi(parts[0])
		if err != nil {
			return nil, cmdutil.FlagError{Err: fmt.Errorf(
				"invalid --bucket %q: index must be an integer", value)}
		}
		var dims []int
		for _, raw := range strings.Split(parts[1], "x") {
			dim, err := strconv.Atoi(raw)
			if err != nil {
				return nil, cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --bucket %q: dimensions must be integers separated by x", value)}
			}
			dims = append(dims, dim)
		}
		buckets = append(buckets, upload.BucketSpec{Index: index, Dims: dims})
	}
	return buckets, nil
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
	BucketIndex   *int   `json:"bucket_index,omitempty"`
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
		len(specs), text.FormatBytes(total), text.SanitizeTerminalInline(opts.repo))

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
				if s.BucketIndex != nil {
					bucket := *s.BucketIndex
					files[i].BucketIndex = &bucket
				}
			}
		}
		result := map[string]any{
			"repo":       opts.repo,
			"dry_run":    true,
			"files":      files,
			"total_size": total,
		}
		if len(opts.bucketSpecs) > 0 {
			result["options"] = map[string]any{"buckets": opts.bucketSpecs}
		}
		return opts.exporter.Write(ios, result)
	}

	tp := tableprinter.New(ios)
	tp.HeaderRow("role", "file", "size", "crc32c", "destination")
	human := ios.HumanOutput()
	for _, s := range specs {
		tp.AddField(s.Role)
		tp.AddField(s.Path, tableprinter.WithTruncate(false))
		if human {
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
// real upload (thin adapters over internal/uploadflow)
// ---------------------------------------------------------------------------

func runUpload(ctx context.Context, opts *uploadOptions, specs []upload.FileSpec) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}
	res, err := uploadOrchestrator(opts, g).Run(ctx, uploadflow.Request{
		Account: opts.account,
		Name:    opts.name,
		Repo:    opts.repo,
		Specs:   specs,
		Buckets: opts.bucketSpecs,
		Wait:    opts.doWait,
		Timeout: opts.timeout,
	})
	return reportUploadOutcome(ctx, opts, g, res, err)
}

// uploadOrchestrator wires the flow to this command invocation: the CLI's
// progress/note rendering, the bare GCS client, and the poll test hooks.
func uploadOrchestrator(opts *uploadOptions, g *gen.ClientWithResponses) *uploadflow.Orchestrator {
	return &uploadflow.Orchestrator{
		Gen:          g,
		Events:       &uploadEvents{f: opts.f},
		Bare:         bareHTTPClient(opts.f),
		StallTimeout: opts.inactivity,
		Jitter:       pollJitter,
		Sleep:        pollSleep,
		Now:          pollNow,
	}
}

// uploadEvents renders uploadflow events on stderr: notes verbatim, per-file
// transfer progress through the shared progress renderer.
type uploadEvents struct {
	f    *cmdutil.Factory
	prog *progress
	file string
}

func (e *uploadEvents) Progress(file string, committed, total int64) {
	if e.prog == nil || e.file != file {
		e.prog = newProgress(e.f, file, total)
		e.file = file
	}
	if committed >= total {
		e.prog.done()
		e.prog = nil
		e.file = ""
		return
	}
	e.prog.update(committed)
}

func (e *uploadEvents) Note(msg string) {
	fmt.Fprintln(e.f.IOStreams.ErrOut, msg)
}

// reportUploadOutcome owns everything after the state machine: hint and
// error formatting, terminal-state cleanup, exporter output, and --wait
// conversion polling. It also owns closing the session lease on every
// non-nil result (mirroring the flow's partial-result contract).
func reportUploadOutcome(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses,
	res *uploadflow.Result, err error,
) error {
	if res != nil && res.Lease != nil {
		defer res.Lease.Close() //nolint:errcheck
	}
	if err != nil {
		return renderUploadFlowError(opts, err)
	}
	return completeReport(ctx, opts, g, res)
}

// renderUploadFlowError translates uploadflow's typed errors into the CLI's
// printed hints, usage errors, and exit codes.
func renderUploadFlowError(opts *uploadOptions, err error) error {
	var uerr *uploadflow.UsageError
	if errors.As(err, &uerr) {
		return cmdutil.FlagError{Err: uerr.Err}
	}

	var cerr *uploadflow.ConflictError
	if errors.As(err, &cerr) {
		if cerr.Stale {
			return fmt.Errorf("%w\nThe conflicting session is no longer active; retry the upload", cerr.Err)
		}
		if cerr.SessionID == "" {
			return fmt.Errorf("%w\nList sessions with: %s model upload --sessions -R %s", cerr.Err, opts.f.Edition.ProgramName(), opts.repo)
		}
		printActiveSessionGuidance(opts, cerr.SessionID, cerr.State)
		return cmdutil.ErrSilent
	}

	var terr *uploadflow.TerminalStateError
	if errors.As(err, &terr) {
		// Terminal sessions can never be resumed: keeping the state file
		// (and its session URIs) would only mislead a later --resume.
		warnRemoveUploadState(opts.f.IOStreams, terr.SessionID)
		return terr
	}

	var serr *uploadflow.SessionError
	if errors.As(err, &serr) {
		switch serr.Phase {
		case uploadflow.PhaseTransfer:
			if errors.Is(serr.Err, context.Canceled) {
				printResumeHint(opts, serr.SessionID, serr.Repo)
				return canceledSilently{}
			}
			return fmt.Errorf("%w\nThe session is preserved; resume with: %s model upload --resume %s -R %s",
				serr.Err, opts.f.Edition.ProgramName(), serr.SessionID, serr.Repo)
		default: // uploadflow.PhaseComplete
			if errors.Is(serr.Err, wait.ErrTimeout) {
				return completionTimeout(opts, serr.SessionID)
			}
			if errors.Is(serr.Err, context.Canceled) {
				printCompletionResumeHint(opts, serr.SessionID)
				return canceledSilently{}
			}
			return completionRecoveryError(opts, serr.SessionID, serr.Err)
		}
	}

	return err
}

func printResumeHint(opts *uploadOptions, sessionID, repo string) {
	errOut := opts.f.IOStreams.ErrOut
	fmt.Fprintf(errOut, "\nInterrupted. The upload session is preserved; already-uploaded bytes will not be re-sent.\n")
	fmt.Fprintf(errOut, "Resume with: %s model upload --resume %s -R %s\n",
		opts.f.Edition.ProgramName(), text.SanitizeTerminalInline(sessionID), text.SanitizeTerminalInline(repo))
}

func printActiveSessionGuidance(opts *uploadOptions, sessionID, state string) {
	errOut := opts.f.IOStreams.ErrOut
	safeSessionID := text.SanitizeTerminalInline(sessionID)
	normalizedState := strings.ToUpper(text.SanitizeTerminalInline(state))
	var b strings.Builder
	fmt.Fprintf(&b, "✗ An upload session is already active for %s: %s",
		text.SanitizeTerminalInline(opts.repo), safeSessionID)
	if normalizedState != "" {
		fmt.Fprintf(&b, " (%s)", normalizedState)
	}
	fmt.Fprintln(&b)

	detailPath := fmt.Sprintf("/v1/repos/%s/%s/models/uploads/%s",
		opts.account, opts.name, safeSessionID)
	fmt.Fprintf(&b, "\nInspect it:  %s api %s --jq .state\n", opts.f.Edition.ProgramName(), detailPath)
	switch normalizedState {
	case uploadflow.SessionStateCreated, uploadflow.SessionStateUploading:
		fmt.Fprintf(&b, "Resume it:   %s model upload --resume %s -R %s\n", opts.f.Edition.ProgramName(), safeSessionID, opts.repo)
		fmt.Fprintf(&b, "Cancel it:   %s model upload --cancel %s -R %s --yes\n", opts.f.Edition.ProgramName(), safeSessionID, opts.repo)
	case uploadflow.SessionStateVerifying, uploadflow.SessionStateDispatchPending:
		fmt.Fprintln(&b, "The files are server-owned; resume completion without local artifacts:")
		fmt.Fprintf(&b, "  %s model upload --resume %s -R %s --wait\n", opts.f.Edition.ProgramName(), safeSessionID, opts.repo)
	default:
		fmt.Fprintln(&b, "Inspect the session state before deciding whether to resume, cancel, wait, or retry.")
	}
	_, _ = fmt.Fprint(errOut, text.SanitizeTerminal(b.String()))
}

// completeReport reports a finished completion state machine. Completion is
// itself asynchronous: VERIFYING, DISPATCH_PENDING, and even CONVERTING may
// temporarily carry no model reference; those responses keep local recovery
// state (the flow already replayed complete when --wait asked for it).
func completeReport(ctx context.Context, opts *uploadOptions, g *gen.ClientWithResponses,
	res *uploadflow.Result,
) error {
	ios := opts.f.IOStreams
	sessionID := res.SessionID
	lease := res.Lease
	out := res.Response

	if strings.EqualFold(string(out.State), "FAILED") {
		fmt.Fprintf(ios.ErrOut, "✗ Upload verification failed: %s (session %s)\n",
			text.SanitizeTerminalInline(deref(out.FailureCode)), text.SanitizeTerminalInline(out.Id))
		fmt.Fprintf(ios.ErrOut, "Fix the reported file and upload again.\n")
		// FAILED is terminal: the session can never be resumed, so keeping
		// the state file (and its session URIs) would only mislead --resume.
		warnRemoveUploadState(ios, sessionID)
		if err := lease.Close(); err != nil {
			return err
		}
		if opts.exporter != nil {
			_ = opts.exporter.Write(ios, json.RawMessage(res.Completion))
		}
		return cmdutil.ErrSilent
	}

	if uploadflow.TerminalCompletionWithoutModel(out) {
		warnRemoveUploadState(ios, sessionID)
		if err := lease.Close(); err != nil {
			return err
		}
		return fmt.Errorf("upload session %s is %s; start a new upload",
			text.SanitizeTerminalInline(sessionID), strings.ToLower(string(out.State)))
	}

	if out.Model != nil {
		fmt.Fprintf(ios.ErrOut, "✓ Upload complete: model %s version %d (state %s)\n",
			text.SanitizeTerminalInline(out.Model.Key), out.Model.Version,
			text.SanitizeTerminalInline(strings.ToLower(string(out.State))))
	} else {
		fmt.Fprintf(ios.ErrOut, "✓ Upload complete: session %s (state %s)\n",
			text.SanitizeTerminalInline(out.Id),
			text.SanitizeTerminalInline(strings.ToLower(string(out.State))))
		fmt.Fprintf(ios.ErrOut, "Completion is still in progress; the session is preserved.\n")
		fmt.Fprintf(ios.ErrOut, "Resume with: %s model upload --resume %s -R %s\n",
			opts.f.Edition.ProgramName(), text.SanitizeTerminalInline(sessionID), text.SanitizeTerminalInline(opts.repo))
		if opts.exporter != nil {
			return opts.exporter.Write(ios, json.RawMessage(res.Completion))
		}
		return nil
	}

	// A model reference is the durable handoff from upload-session recovery to
	// model-status polling. Only now is local session state no longer useful.
	warnRemoveUploadState(ios, sessionID)
	if err := lease.Close(); err != nil {
		return err
	}

	if opts.doWait {
		modelJSON, err := uploadflow.CompletedModelJSON(res.Completion)
		if err != nil {
			return err
		}
		remaining := remainingCompletionBudget(res.WaitStarted, opts.timeout)
		if remaining <= 0 {
			fmt.Fprintf(ios.ErrOut, "Timed out after %s; the model is still processing.\n", opts.timeout)
			fmt.Fprintf(ios.ErrOut, "Check again with: %s model status %s -R %s\n",
				opts.f.Edition.ProgramName(), text.SanitizeTerminalInline(out.Model.Key),
				text.SanitizeTerminalInline(opts.repo))
			return cmdutil.ErrSilent
		}
		// Mirror the flow's completion deadline so model-status polling keeps
		// the original shared --timeout budget semantics.
		waitCtx, cancelWait := context.WithTimeoutCause(ctx, remaining, wait.ErrTimeout)
		defer cancelWait()
		return waitForModelWithResultWithin(waitCtx, opts.f, g, opts.account, opts.name,
			out.Model.Key, remaining, opts.timeout, opts.exporter, modelJSON)
	}
	if opts.exporter != nil {
		return opts.exporter.Write(ios, json.RawMessage(res.Completion))
	}
	return nil
}

func completionClockNow() time.Time {
	if pollNow != nil {
		return pollNow()
	}
	return time.Now()
}

func remainingCompletionBudget(start time.Time, timeout time.Duration) time.Duration {
	return timeout - completionClockNow().Sub(start)
}

func completionTimeout(opts *uploadOptions, sessionID string) error {
	fmt.Fprintf(opts.f.IOStreams.ErrOut,
		"Timed out after %s waiting for upload completion; the session is preserved.\n", opts.timeout)
	fmt.Fprintf(opts.f.IOStreams.ErrOut,
		"Resume with: %s model upload --resume %s -R %s --wait\n",
		opts.f.Edition.ProgramName(), text.SanitizeTerminalInline(sessionID), text.SanitizeTerminalInline(opts.repo))
	return cmdutil.ErrSilent
}

func completionRecoveryError(opts *uploadOptions, sessionID string, err error) error {
	recovery := fmt.Errorf("%w\nThe session is preserved; resume with: %s model upload --resume %s -R %s",
		err, opts.f.Edition.ProgramName(), text.SanitizeTerminalInline(sessionID), text.SanitizeTerminalInline(opts.repo))
	// A billing refusal (e.g. a 402 credit_balance_exhausted) parks the
	// session rather than consuming it: replaying complete after remediation
	// resumes it, so the hint APPENDS to the resume guidance.
	return withBillingHint(opts.f.Edition.ProgramName(), recovery)
}

func printCompletionResumeHint(opts *uploadOptions, sessionID string) {
	fmt.Fprintf(opts.f.IOStreams.ErrOut,
		"\nInterrupted. The upload session is preserved.\nResume with: %s model upload --resume %s -R %s\n",
		opts.f.Edition.ProgramName(), text.SanitizeTerminalInline(sessionID), text.SanitizeTerminalInline(opts.repo))
}

// ---------------------------------------------------------------------------
// resume
// ---------------------------------------------------------------------------

func runResume(ctx context.Context, opts *uploadOptions, args []string) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}
	ro := uploadflow.ResumeOptions{
		Account: opts.account,
		Name:    opts.name,
		Repo:    opts.repo,
		Wait:    opts.doWait,
		Timeout: opts.timeout,
	}
	if len(args) > 0 || opts.inputManifest != "" {
		// Digest lazily: hashing happens only when the local state file is
		// actually missing and the session must be rebuilt from the server.
		ro.BuildSpecs = func() ([]upload.FileSpec, error) {
			return buildSpecs(opts, args)
		}
	}
	res, err := uploadOrchestrator(opts, g).Resume(ctx, opts.resumeID, ro)
	return reportUploadOutcome(ctx, opts, g, res, err)
}

// ---------------------------------------------------------------------------
// cancel / sessions
// ---------------------------------------------------------------------------

func runCancel(ctx context.Context, opts *uploadOptions) error {
	if err := confirmUploadCancellation(ctx, opts); err != nil {
		return err
	}
	lease, err := upload.AcquireSession(ctx, opts.cancelID)
	if err != nil {
		return fmt.Errorf("locking upload session %s: %w", opts.cancelID, err)
	}
	defer lease.Close() //nolint:errcheck

	g, err := genClient(opts.f)
	if err != nil {
		return err
	}
	resp, err := g.CancelModelUploadWithResponse(ctx, opts.account, opts.name, opts.cancelID,
		&gen.CancelModelUploadParams{IdempotencyKey: api.NewIdempotencyKeyParam()})
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return aerr
	}
	warnRemoveUploadState(opts.f.IOStreams, opts.cancelID)
	if err := lease.Close(); err != nil {
		return err
	}

	ios := opts.f.IOStreams
	fmt.Fprintf(ios.ErrOut, "✓ Canceled upload session %s\n",
		text.SanitizeTerminalInline(opts.cancelID))
	if opts.exporter != nil {
		return opts.exporter.Write(ios, json.RawMessage(resp.Body))
	}
	return nil
}

func confirmUploadCancellation(ctx context.Context, opts *uploadOptions) error {
	if opts.yes {
		return nil
	}
	ios := opts.f.IOStreams
	if !ios.IsStdinTTY() || opts.f.NoInput {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"canceling upload session %s requires confirmation; re-run with --yes", opts.cancelID)}
	}
	safeCancelID := text.SanitizeTerminalInline(opts.cancelID)
	fmt.Fprintf(ios.ErrOut, "Canceling upload session %s discards its resumable state.\nType %s to confirm: ",
		safeCancelID, safeCancelID)
	line, err := ios.ReadLine(ctx)
	if err != nil && line == "" {
		return fmt.Errorf("reading cancellation confirmation: %w", err)
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line != opts.cancelID {
		return fmt.Errorf("confirmation did not match %s; upload session not canceled", opts.cancelID)
	}
	return nil
}

func warnRemoveUploadState(ios *iostreams.IOStreams, sessionID string) {
	if err := upload.RemoveState(sessionID); err != nil {
		fmt.Fprintf(ios.ErrOut, "! Server operation succeeded, but local upload state cleanup failed: %s\n",
			text.SanitizeTerminalInline(err.Error()))
	}
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
		if ios.HumanOutput() {
			fmt.Fprintln(ios.ErrOut, "No upload sessions found")
		}
		return nil
	}

	human := ios.HumanOutput()
	now := time.Now()
	tp := tableprinter.New(ios)
	tp.HeaderRow("id", "state", "created", "expires", "files")
	for _, s := range sessions {
		tp.AddField(s.Id)
		tp.AddField(string(s.State))
		if human {
			tp.AddField(text.RelativeTime(s.CreatedAt, now))
		} else {
			tp.AddField(s.CreatedAt.Format(time.RFC3339))
		}
		tp.AddField(s.ExpiresAt.Format(time.RFC3339))
		tp.AddField(strconv.Itoa(s.FileCount))
		tp.EndRow()
	}
	tp.Caption(text.Pluralize(len(sessions), "upload session", "upload sessions"))
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
	arrow string // direction glyph: ↑ uploads, ↓ downloads
	verb  string // completion verb: uploaded / downloaded
}

func newProgress(f *cmdutil.Factory, name string, total int64) *progress {
	return &progress{f: f, tty: f.IOStreams.IsStderrTTY(),
		name: text.SanitizeTerminalInline(name), total: total,
		arrow: "↑", verb: "uploaded"}
}

// newDownloadProgress is the ↓ variant; total may be 0 when the size is
// unknown until the stream ends (percentages are then skipped).
func newDownloadProgress(f *cmdutil.Factory, name string, total int64) *progress {
	return &progress{f: f, tty: f.IOStreams.IsStderrTTY(),
		name: text.SanitizeTerminalInline(name), total: total,
		arrow: "↓", verb: "downloaded"}
}

func (p *progress) update(committed int64) {
	if !p.tty || p.total <= 0 {
		return
	}
	pct := committed * 100 / p.total
	fmt.Fprintf(p.f.IOStreams.ErrOut, "\r%s %s  %d%% (%s/%s)   ",
		p.arrow, p.name, pct, text.FormatBytes(committed), text.FormatBytes(p.total))
}

func (p *progress) done() {
	if p.tty {
		fmt.Fprintf(p.f.IOStreams.ErrOut, "\r✓ %s %s (%s)          \n", p.name, p.verb, text.FormatBytes(p.total))
		return
	}
	fmt.Fprintf(p.f.IOStreams.ErrOut, "✓ %s %s (%s)\n", p.name, p.verb, text.FormatBytes(p.total))
}

// doneAs reports completion with the actual byte count — used by downloads,
// where the total may be unknown until the stream ends.
func (p *progress) doneAs(n int64) {
	p.total = n
	p.done()
}

// waitForModel polls conversion status until a terminal state, then prints
// the final status. Failed models exit 1; a timeout prints how to keep
// checking and exits 1.
func waitForModel(ctx context.Context, f *cmdutil.Factory, g *gen.ClientWithResponses, account, name, key string, timeout time.Duration, exporter *cmdutil.Exporter) error {
	return waitForModelWithResult(ctx, f, g, account, name, key, timeout, exporter, nil)
}

type waitedModelResult struct {
	Model  json.RawMessage `json:"model"`
	Status json.RawMessage `json:"status"`
}

// waitForModelWithResult preserves the model-producing response alongside the
// terminal status for structured upload/import output. A nil model keeps the
// model status command's existing raw-status contract.
func waitForModelWithResult(ctx context.Context, f *cmdutil.Factory, g *gen.ClientWithResponses, account, name, key string, timeout time.Duration, exporter *cmdutil.Exporter, model json.RawMessage) error {
	return waitForModelWithResultWithin(ctx, f, g, account, name, key, timeout, timeout, exporter, model)
}

// waitForModelWithResultWithin separates the remaining polling budget from
// the user-facing total. Upload completion recovery may consume part of a
// shared --timeout before model-status polling begins.
func waitForModelWithResultWithin(ctx context.Context, f *cmdutil.Factory, g *gen.ClientWithResponses,
	account, name, key string, timeout, displayTimeout time.Duration,
	exporter *cmdutil.Exporter, model json.RawMessage,
) error {
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
		fmt.Fprintf(ios.ErrOut, "Timed out after %s; the model is still processing.\n", displayTimeout)
		fmt.Fprintf(ios.ErrOut, "Check again with: %s model status %s -R %s/%s\n",
			f.Edition.ProgramName(), text.SanitizeTerminalInline(key), text.SanitizeTerminalInline(account),
			text.SanitizeTerminalInline(name))
		return cmdutil.ErrSilent
	}
	if err != nil {
		return err
	}

	var perr error
	if exporter != nil && model != nil {
		perr = exporter.Write(ios, waitedModelResult{Model: model, Status: json.RawMessage(raw)})
	} else {
		perr = printStatus(f, exporter, last, raw, key, account+"/"+name)
	}
	if perr != nil {
		return perr
	}
	if strings.EqualFold(string(last.State), string(gen.ModelStatusResponseStateFailed)) {
		fmt.Fprintf(ios.ErrOut, "✗ Model processing failed: %s\n",
			text.SanitizeTerminalInline(deref(last.FailureCode)))
		return cmdutil.ErrSilent
	}
	return nil
}
