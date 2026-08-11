package model

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"hash/crc32"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/downloadstate"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// redactedURL replaces artifact URLs in --json output: signed URLs are
// short-lived credentials and must never land in logs or agent transcripts.
const redactedURL = "<redacted>"

const (
	artifactMaxAttempts          = 4
	artifactMaxAuthorizationRuns = 2 // initial authorization plus one URL refresh
	artifactMaxRetryDelay        = 30 * time.Second
)

// Retry hooks are variables so white-box tests can verify Retry-After and
// bounded backoff without waiting in real time.
var (
	artifactRetrySleep                = sleepArtifactRetry
	artifactRetryNow                  = time.Now
	artifactTransferInactivityTimeout = 30 * time.Second
	artifactRetryJitter               = func(max time.Duration) time.Duration {
		if max <= 0 {
			return 0
		}
		var b [8]byte
		if _, err := cryptorand.Read(b[:]); err != nil {
			return 0
		}
		return time.Duration(binary.BigEndian.Uint64(b[:]) % uint64(max))
	}
)

type downloadOptions struct {
	f *cmdutil.Factory

	repo     string
	target   string
	output   string
	yes      bool
	force    bool
	exporter *cmdutil.Exporter

	account, name, key string
}

func newCmdDownload(f *cmdutil.Factory) *cobra.Command {
	opts := &downloadOptions{f: f}

	cmd := &cobra.Command{
		Use:   "download MODEL_KEY --target TARGET_ID",
		Short: "Download a converted target's artifacts (billable)",
		Long: `Download the artifacts of a converted target. This is BILLABLE: the
target's download size counts against your account's bandwidth quota —
also for public models owned by others.

On a terminal the command previews the target and its size and asks for
confirmation before anything is charged; non-interactive runs (and
--no-input) require --yes instead. The authorization request carries an
Idempotency-Key that is persisted in per-user application state and reused
by later CLI processes for the same host, account/repo, model, and target. Local output
is tracked separately so a post-authorization correction from file or
stdout to a directory keeps the charged key. A cross-process lock
serializes transfers; durable completion/recovery state prevents a
waiting or failed follower from rotating the key after another process
succeeds. The private state stores no signed URLs or access tokens.
Exceeding the quota is an error with nothing charged.

Files are written to --output (default: the current directory; an
existing directory receives one file per artifact, any other path names
the destination file for single-artifact targets). Each file is
downloaded to a temporary file, verified against the artifact's
checksum when one is available, and atomically committed into place —
interrupted downloads never leave partial files. Existing files are
never overwritten without --force. Connection resets, timeouts, 429
(honoring Retry-After), and HTTP 502–504 artifact failures are retried
with bounded backoff. An expired 403/404 signed URL is refreshed once
with the persisted authorization key. A transfer with no byte progress
for 30 seconds is canceled and retried; every received chunk resets that
inactivity timer.

The CLI validates the output path and known file collisions before the
billable request. Artifact names are only disclosed by that response, so
an existing same-named file inside an output directory is necessarily
detected afterward; replay state is kept and the error tells you to
re-run the same command with --force without another charge.

Set --output - for one artifact to write verified binary bytes to stdout.
The artifact is fully staged and verified before stdout is touched; this
mode cannot be combined with --json, --jq, or --template.

With --json the authorization response is written to stdout with every
artifact url replaced by "<redacted>" (the only documented deviation
from the API response). Output ends with exactly one trailing newline.
Use this command to download, or melange api if you genuinely need raw
signed URLs.

Exit codes: 0 success, 1 API/download/verification error (including
quota exhaustion), 2 usage error or missing confirmation, 4 not
authenticated, 130 interrupted.`,
		Example: `  # Resolve a model and one of its converted targets
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')
  target_id=$(melange model targets "$model_key" -R zetic/whisper-tiny --jq '.results[0].target_id')

  # Download the target into a directory
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id" --output ./models

  # Agent pattern: non-interactive download (the billable step needs --yes)
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id" --yes

  # Agent pattern: capture the authorization id (URLs are redacted)
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id" --yes --json --jq .authorization_id`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoFlag(opts.repo)
			if err != nil {
				return err
			}
			opts.account, opts.name, opts.key = account, name, args[0]
			if opts.target == "" {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"--target TARGET_ID is required; list targets with: %s model targets %s -R %s",
					f.Edition.ProgramName(), opts.key, opts.repo)}
			}
			if opts.output == "-" && opts.exporter != nil {
				return cmdutil.FlagError{Err: errors.New(
					"--output - cannot be combined with --json, --jq, or --template")}
			}
			// A missing --yes is a flag-contract error, so it is decided here —
			// before the API client is built. Authenticating first would mask it
			// behind an auth error and send the caller chasing credentials for a
			// mistake in the invocation itself.
			if err := requireConfirmable(opts); err != nil {
				return err
			}
			// Validate the writable destination BEFORE anything is charged: a
			// bad --output must never cost quota.
			if err := validOutput(opts.output, opts.force); err != nil {
				return err
			}
			return runDownload(cmd.Context(), opts)
		},
	}

	fl := cmd.Flags()
	fl.StringVarP(&opts.repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	fl.StringVar(&opts.target, "target", "", "Target to download as `TARGET_ID` (see `melange model targets`)")
	fl.StringVarP(&opts.output, "output", "o", ".", "Destination `directory`, single-artifact file, or - for binary stdout")
	fl.BoolVar(&opts.yes, "yes", false, "Skip the billable-download confirmation")
	fl.BoolVar(&opts.force, "force", false, "Overwrite existing files")
	cmdutil.AddJSONFlags(cmd, &opts.exporter)

	return cmd
}

func runDownload(ctx context.Context, opts *downloadOptions) (retErr error) {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}

	if !opts.yes {
		if err := confirmBillableDownload(ctx, opts, g); err != nil {
			return err
		}
	}

	identity, output, err := downloadReplayScope(opts, g)
	if err != nil {
		return err
	}
	lease, err := downloadstate.Acquire(ctx, identity, output, api.NewIdempotencyKey)
	if err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			if preserveErr := lease.PreserveRecovery(); preserveErr != nil {
				retErr = errors.Join(retErr, preserveErr)
			}
		}
		if closeErr := lease.Close(); closeErr != nil {
			retErr = errors.Join(retErr, closeErr)
		}
	}()
	replay := lease.State()

	auth, rawAuthorization, err := authorizeDownload(ctx, g, opts, replay.IdempotencyKey)
	if err != nil {
		return err
	}

	paths, err := planArtifactPaths(opts, auth.Artifacts)
	if err != nil {
		return err
	}

	var total int64
	authorizationRuns := 1
	for i := 0; i < len(auth.Artifacts); {
		n, err := downloadArtifact(ctx, opts, auth.Artifacts[i], paths[i])
		if err != nil {
			var expired *expiredArtifactURLError
			if errors.As(err, &expired) {
				if authorizationRuns < artifactMaxAuthorizationRuns {
					refreshed, raw, refreshErr := authorizeDownload(ctx, g, opts, replay.IdempotencyKey)
					if refreshErr != nil {
						return refreshErr
					}
					merged, mergeErr := mergeRefreshedArtifacts(auth, refreshed)
					if mergeErr != nil {
						return mergeErr
					}
					auth = merged
					rawAuthorization = raw
					authorizationRuns++
					continue
				}
				return fmt.Errorf("%w; authorization URLs were refreshed once and replay state was kept — re-run the same command", err)
			}
			return err
		}
		total += n
		i++
	}

	ios := opts.f.IOStreams
	dest := opts.output
	if len(paths) == 1 {
		dest = paths[0]
	}
	if dest == "-" {
		dest = "stdout"
	}
	fmt.Fprintf(ios.ErrOut, "✓ Downloaded %d artifact(s) (%s) to %s\n",
		len(auth.Artifacts), text.FormatBytes(total), text.SanitizeTerminalInline(dest))

	if opts.exporter != nil {
		redacted, err := redactAuthorization(rawAuthorization)
		if err != nil {
			return err
		}
		if err := opts.exporter.Write(ios, redacted); err != nil {
			return err
		}
	}
	return lease.Complete()
}

// authorizeDownload obtains signed artifact URLs with the persisted key. The
// API transport may retry this call in-process, and a later process or URL
// refresh uses the exact same key. A bandwidth-quota 429 is deliberately not
// retried by the API transport because it is not transient at that timescale.
func authorizeDownload(ctx context.Context, g *gen.ClientWithResponses, opts *downloadOptions, key string) (*gen.DownloadAuthorizationResponse, []byte, error) {
	idempotencyKey := gen.IdempotencyKey(key)
	resp, err := g.CreateDownloadAuthorizationWithResponse(api.WithNoRetryOn429(ctx),
		opts.account, opts.name, opts.key, opts.target,
		&gen.CreateDownloadAuthorizationParams{IdempotencyKey: &idempotencyKey})
	if err != nil {
		return nil, nil, err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		var apiErr *api.Error
		if errors.As(aerr, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			hint := fmt.Sprintf("The download was not authorized and nothing was charged; check `%s usage quotas`.", opts.f.Edition.ProgramName())
			if apiErr.RetryAfter > 0 {
				hint += fmt.Sprintf(" Retry after %s.", apiErr.RetryAfter)
			}
			return nil, nil, fmt.Errorf("%w\n%s", aerr, hint)
		}
		return nil, nil, aerr
	}
	auth := resp.JSON201
	if auth == nil {
		auth = resp.JSON200
	}
	if auth == nil {
		return nil, nil, fmt.Errorf("unexpected response authorizing download (HTTP %d)", resp.StatusCode())
	}
	return auth, resp.Body, nil
}

func downloadReplayScope(opts *downloadOptions, g *gen.ClientWithResponses) (downloadstate.Identity, downloadstate.Output, error) {
	client, ok := g.ClientInterface.(*gen.Client)
	if !ok {
		return downloadstate.Identity{}, downloadstate.Output{}, errors.New("determining API host for download replay state")
	}
	u, err := url.Parse(client.Server)
	if err != nil {
		return downloadstate.Identity{}, downloadstate.Output{}, fmt.Errorf("determining API host for download replay state: %w", err)
	}
	// Credentials and query parameters never belong in replay state, even if
	// a nonstandard client was constructed with them in its base URL.
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	host := strings.TrimRight(u.String(), "/")

	mode := "file"
	outputPath := opts.output
	if opts.output == "-" {
		mode = "stdout"
	} else {
		outputPath, err = filepath.Abs(opts.output)
		if err != nil {
			return downloadstate.Identity{}, downloadstate.Output{}, fmt.Errorf("resolving --output for download replay state: %w", err)
		}
		outputPath = filepath.Clean(outputPath)
		if info, statErr := os.Stat(opts.output); statErr == nil && info.IsDir() {
			mode = "directory"
		}
	}
	identity := downloadstate.Identity{
		Host:    host,
		Account: opts.account,
		Repo:    opts.name,
		Model:   opts.key,
		Target:  opts.target,
	}
	return identity, downloadstate.Output{Mode: mode, Path: outputPath}, nil
}

// mergeRefreshedArtifacts accepts only a replay of the same authorization.
// Names and verification metadata remain bound to the original path plan;
// only fresh signed URLs and expiry are adopted.
func mergeRefreshedArtifacts(original, refreshed *gen.DownloadAuthorizationResponse) (*gen.DownloadAuthorizationResponse, error) {
	if refreshed.AuthorizationId != original.AuthorizationId || len(refreshed.Artifacts) != len(original.Artifacts) {
		return nil, errors.New("refreshed download authorization did not match the original; replay state was kept")
	}
	byName := make(map[string]gen.DownloadArtifact, len(refreshed.Artifacts))
	for _, art := range refreshed.Artifacts {
		byName[art.Name] = art
	}
	merged := *original
	merged.ExpiresAt = refreshed.ExpiresAt
	merged.Artifacts = append([]gen.DownloadArtifact(nil), original.Artifacts...)
	for i, art := range merged.Artifacts {
		fresh, ok := byName[art.Name]
		if !ok || !sameOptionalInt(art.Size, fresh.Size) || deref(art.Checksum) != deref(fresh.Checksum) {
			return nil, errors.New("refreshed download authorization changed artifact metadata; replay state was kept")
		}
		merged.Artifacts[i].Url = fresh.Url
	}
	return &merged, nil
}

func sameOptionalInt(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

// validOutput rejects --output values that could only fail after the
// billable authorization: the path must be an existing directory, or a file
// path whose parent directory exists and (without --force) no existing file.
// Dir-mode collisions cannot be checked here — artifact names only exist
// after the billable POST — so planArtifactPaths re-checks post-charge.
func validOutput(output string, force bool) error {
	if output == "-" {
		return nil
	}
	if info, err := os.Stat(output); err == nil {
		if info.IsDir() {
			return preflightWritableDirectory(output)
		}
		if !force {
			return cmdutil.FlagError{Err: fmt.Errorf(
				"--output %q already exists; pass --force to overwrite", output)}
		}
		return preflightWritableDirectory(filepath.Dir(output))
	}
	if strings.HasSuffix(output, string(os.PathSeparator)) {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"--output %q is not an existing directory", output)}
	}
	parent := filepath.Dir(output)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"--output %q: parent directory does not exist", output)}
	}
	return preflightWritableDirectory(parent)
}

func preflightWritableDirectory(dir string) error {
	probe, err := os.CreateTemp(dir, ".melange-download-write-test-*")
	if err != nil {
		return cmdutil.FlagError{Err: fmt.Errorf("destination directory %q is not writable: %w", dir, err)}
	}
	path := probe.Name()
	closeErr := probe.Close()
	removeErr := os.Remove(path)
	if closeErr != nil {
		return cmdutil.FlagError{Err: fmt.Errorf("destination directory %q is not writable: %w", dir, closeErr)}
	}
	if removeErr != nil {
		return cmdutil.FlagError{Err: fmt.Errorf("cleaning destination writability probe in %q: %w", dir, removeErr)}
	}
	return nil
}

// requireConfirmable rejects a billable download that can be neither confirmed
// interactively nor waived with --yes. It is the single source of that rule:
// RunE calls it before any client or filesystem work so the usage error is not
// masked by an auth failure, and confirmBillableDownload calls it again at the
// charge site so no future caller can reach the prompt without the guard.
func requireConfirmable(opts *downloadOptions) error {
	if opts.yes {
		return nil
	}
	ios := opts.f.IOStreams
	if !ios.IsStdinTTY() || opts.f.NoInput {
		return cmdutil.FlagError{Err: errors.New(
			"downloading is billable and requires confirmation; re-run with --yes to confirm non-interactively")}
	}
	return nil
}

// confirmBillableDownload gates the charge. The preview reads the FREE
// targets listing (artifact names only exist after the billable POST, so the
// preview shows the target identity and its aggregate size instead) and asks
// for an explicit yes. Non-interactive runs must pass --yes.
func confirmBillableDownload(ctx context.Context, opts *downloadOptions, g *gen.ClientWithResponses) error {
	ios := opts.f.IOStreams
	if err := requireConfirmable(opts); err != nil {
		return err
	}

	resp, err := g.ListModelTargetsWithResponse(ctx, opts.account, opts.name, opts.key)
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		return aerr
	}
	if resp.JSON200 == nil {
		return fmt.Errorf("unexpected response listing targets (HTTP %d)", resp.StatusCode())
	}
	var target *gen.ModelTargetItem
	for i := range resp.JSON200.Results {
		if resp.JSON200.Results[i].TargetId == opts.target {
			target = &resp.JSON200.Results[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("target %s not found for model %s in %s; list targets with: %s model targets %s -R %s",
			opts.target, opts.key, opts.repo, opts.f.Edition.ProgramName(), opts.key, opts.repo)
	}

	desc := string(target.Kind)
	if target.Precision != nil && *target.Precision != "" {
		desc += "/" + string(*target.Precision)
	}
	if quant := deref(target.QuantType); quant != "" {
		desc += ", " + quant
	}
	fmt.Fprintf(ios.ErrOut, "Target %s (%s): %s\n",
		text.SanitizeTerminalInline(target.TargetId), text.SanitizeTerminalInline(desc),
		text.FormatBytes(int64(target.DownloadSize)))
	fmt.Fprintf(ios.ErrOut, "This download counts against your bandwidth quota.\n")
	fmt.Fprintf(ios.ErrOut, "Proceed? [y/N] ")

	line, err := ios.ReadLine(ctx)
	if err != nil && strings.TrimSpace(line) == "" {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return nil
	}
	fmt.Fprintln(ios.ErrOut, "Canceled; nothing was charged.")
	return cmdutil.ErrSilent
}

// planArtifactPaths maps artifacts to destination paths and applies the
// overwrite policy BEFORE any bytes move (the authorization charge has
// already happened; failing here at least fails before partial downloads).
func planArtifactPaths(opts *downloadOptions, artifacts []gen.DownloadArtifact) ([]string, error) {
	if len(artifacts) == 0 {
		return nil, errors.New("the authorization carried no artifacts; try again or contact support")
	}

	stdoutMode := opts.output == "-"
	if stdoutMode && len(artifacts) != 1 {
		return nil, fmt.Errorf("target has %d artifacts; --output - requires exactly one artifact", len(artifacts))
	}

	dirMode := false
	if info, err := os.Stat(opts.output); err == nil && info.IsDir() {
		dirMode = true
	}
	if !dirMode && len(artifacts) > 1 {
		return nil, fmt.Errorf(
			"target has %d artifacts; --output must name an existing directory", len(artifacts))
	}

	paths := make([]string, len(artifacts))
	seen := map[string]bool{}
	seenNames := make([]string, 0, len(artifacts))
	for i, art := range artifacts {
		if err := validArtifactName(art.Name); err != nil {
			return nil, err
		}
		if seen[art.Name] {
			return nil, fmt.Errorf("duplicate artifact name %q in authorization", art.Name)
		}
		for _, prior := range seenNames {
			if strings.EqualFold(prior, art.Name) {
				return nil, fmt.Errorf("artifact names %q and %q collide on case-insensitive filesystems", prior, art.Name)
			}
		}
		seen[art.Name] = true
		seenNames = append(seenNames, art.Name)
		if stdoutMode {
			paths[i] = "-"
		} else if dirMode {
			paths[i] = filepath.Join(opts.output, art.Name)
		} else {
			paths[i] = opts.output
		}
		if paths[i] != "-" {
			if _, err := os.Stat(paths[i]); err == nil && !opts.force {
				return nil, postAuthorizationCollision(paths[i])
			}
		}
	}
	return paths, nil
}

// validArtifactName rejects server-supplied names that could escape the
// output directory or alias a special Windows device. The API contract is
// portable plain basenames; anything else is treated as hostile.
func validArtifactName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\<>:"|?*`) || filepath.IsAbs(name) ||
		strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("unsafe artifact name %q in authorization", name)
	}
	for _, r := range name {
		if r < 0x20 {
			return fmt.Errorf("unsafe artifact name %q in authorization", name)
		}
	}
	stem, _, _ := strings.Cut(name, ".")
	switch strings.ToUpper(stem) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return fmt.Errorf("unsafe artifact name %q in authorization (reserved device name)", name)
	}
	return nil
}

type expiredArtifactURLError struct {
	name   string
	status int
}

func (e *expiredArtifactURLError) Error() string {
	return fmt.Sprintf("downloading %s: HTTP %d from an expired or unavailable signed URL", e.name, e.status)
}

type artifactStatusError struct {
	name          string
	status        int
	retryAfter    time.Duration
	hasRetryAfter bool
}

func (e *artifactStatusError) Error() string {
	return fmt.Sprintf("downloading %s: HTTP %d", e.name, e.status)
}

// downloadArtifact retries only failures that can safely succeed with a new
// GET. Every attempt creates its own temporary file, so a reset or timeout
// never appends to partial bytes from the preceding attempt.
func downloadArtifact(ctx context.Context, opts *downloadOptions, art gen.DownloadArtifact, dest string) (int64, error) {
	client := bareHTTPClient(opts.f)
	for attempt := 0; attempt < artifactMaxAttempts; attempt++ {
		written, err := downloadArtifactOnce(ctx, client, opts, art, dest)
		if err == nil {
			return written, nil
		}
		if _, expired := err.(*expiredArtifactURLError); expired {
			return 0, err
		}
		delay, transient := artifactRetryDelay(ctx, err, attempt)
		if !transient || attempt == artifactMaxAttempts-1 {
			return 0, err
		}
		if err := artifactRetrySleep(ctx, delay); err != nil {
			if errors.Is(err, context.Canceled) {
				return 0, canceledSilently{}
			}
			return 0, downloadFailure(art.Name, err)
		}
	}
	return 0, errors.New("download retry attempts exhausted")
}

// downloadArtifactOnce streams one signed URL to its destination: temp file
// in the same directory, checksum verification, then an atomic commit. On any
// failure the temp file is removed and the destination is left untouched.
func downloadArtifactOnce(ctx context.Context, client *http.Client, opts *downloadOptions, art gen.DownloadArtifact, dest string) (written int64, err error) {
	transferCtx, touch, stop := withArtifactInactivity(ctx, artifactTransferInactivityTimeout)
	defer stop()
	req, err := http.NewRequestWithContext(transferCtx, http.MethodGet, art.Url, nil)
	if err != nil {
		// URL parse errors arrive as *url.Error embedding the full URL.
		return 0, fmt.Errorf("building download request for %s: %w", art.Name, stripSignedURL(err))
	}
	resp, err := client.Do(req)
	if err != nil {
		if cause := context.Cause(transferCtx); isArtifactInactivity(cause) {
			return 0, downloadFailure(art.Name, cause)
		}
		return 0, downloadFailure(art.Name, err)
	}
	touch()
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound {
			return 0, &expiredArtifactURLError{name: art.Name, status: resp.StatusCode}
		}
		retryAfter, hasRetryAfter := parseArtifactRetryAfter(resp.Header.Get("Retry-After"))
		return 0, &artifactStatusError{
			name:          art.Name,
			status:        resp.StatusCode,
			retryAfter:    retryAfter,
			hasRetryAfter: hasRetryAfter,
		}
	}

	total := int64(-1)
	if art.Size != nil {
		if *art.Size < 0 {
			return 0, fmt.Errorf("downloading %s: authorization carried invalid size %d", art.Name, *art.Size)
		}
		total = int64(*art.Size)
		if resp.ContentLength >= 0 && resp.ContentLength != total {
			return 0, fmt.Errorf("downloading %s: authorized size is %d bytes but storage reports %d bytes",
				art.Name, total, resp.ContentLength)
		}
	} else if resp.ContentLength >= 0 {
		total = resp.ContentLength
	}
	if total < 0 && !recognizedArtifactChecksum(art) {
		return 0, fmt.Errorf("downloading %s: cannot verify completeness because both artifact size and a recognized checksum are unavailable", art.Name)
	}
	prog := newDownloadProgress(opts.f, art.Name, total)

	tmpDir := filepath.Dir(dest)
	if dest == "-" {
		tmpDir = os.TempDir()
	}
	tmp, err := os.CreateTemp(tmpDir, ".melange-download-*")
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	crc := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	sha := sha256.New()
	pw := &progressWriter{prog: prog}
	reader := io.Reader(&activityReader{reader: resp.Body, touch: touch})
	if total >= 0 {
		// Read one byte beyond the expected size to detect overruns, without
		// overflowing for a malicious MaxInt64 size in the authorization.
		limit := total
		if limit < math.MaxInt64 {
			limit++
		}
		reader = io.LimitReader(reader, limit)
	}
	written, err = io.Copy(io.MultiWriter(tmp, crc, sha, pw), reader)
	if err != nil {
		if cause := context.Cause(transferCtx); isArtifactInactivity(cause) {
			return 0, downloadFailure(art.Name, cause)
		}
		return 0, downloadFailure(art.Name, err)
	}
	if total >= 0 && written != total {
		return 0, fmt.Errorf("downloading %s: authorized size is %d bytes but received %d bytes",
			art.Name, total, written)
	}
	if err = verifyArtifactChecksum(opts.f, art, crc, sha); err != nil {
		return 0, err
	}
	if err = tmp.Sync(); err != nil {
		return 0, err
	}
	if dest == "-" {
		if _, err = tmp.Seek(0, io.SeekStart); err != nil {
			return 0, err
		}
		if _, err = io.Copy(opts.f.IOStreams.Out, tmp); err != nil {
			return 0, fmt.Errorf("writing %s to stdout: %w", art.Name, err)
		}
		prog.doneAs(written)
		return written, nil
	}
	if err = tmp.Close(); err != nil {
		return 0, err
	}
	// Downloads are shareable data files, not secrets: world-readable like
	// any normally-created file (CreateTemp defaults to 0600).
	if err = os.Chmod(tmp.Name(), 0o644); err != nil {
		return 0, err
	}
	if err = commitDownloadedFile(tmp.Name(), dest, opts.force); err != nil {
		return 0, err
	}
	prog.doneAs(written)
	return written, nil
}

type artifactInactivityError struct{ timeout time.Duration }

func (e *artifactInactivityError) Error() string {
	return fmt.Sprintf("artifact transfer inactive for %s", e.timeout)
}

func (*artifactInactivityError) Timeout() bool   { return true }
func (*artifactInactivityError) Temporary() bool { return true }

func isArtifactInactivity(err error) bool {
	var inactive *artifactInactivityError
	return errors.As(err, &inactive)
}

type activityReader struct {
	reader io.Reader
	touch  func()
}

func (r *activityReader) Read(buffer []byte) (int, error) {
	n, err := r.reader.Read(buffer)
	if n > 0 {
		r.touch()
	}
	return n, err
}

func withArtifactInactivity(parent context.Context, timeout time.Duration) (context.Context, func(), func()) {
	ctx, cancel := context.WithCancelCause(parent)
	activity := make(chan struct{}, 1)
	done := make(chan struct{})
	timer := time.NewTimer(timeout)
	go func() {
		defer close(done)
		defer timer.Stop()
		for {
			select {
			case <-activity:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(timeout)
			case <-timer.C:
				cancel(&artifactInactivityError{timeout: timeout})
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	touch := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}
	stop := func() {
		cancel(context.Canceled)
		<-done
	}
	return ctx, touch, stop
}

func artifactRetryDelay(ctx context.Context, err error, attempt int) (time.Duration, bool) {
	if ctx.Err() != nil {
		return 0, false
	}
	var statusErr *artifactStatusError
	if errors.As(err, &statusErr) {
		switch statusErr.status {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			if statusErr.status == http.StatusTooManyRequests && statusErr.hasRetryAfter {
				return statusErr.retryAfter, true
			}
		default:
			return 0, false
		}
	} else if !transientArtifactNetworkError(err) {
		return 0, false
	}

	base := 100 * time.Millisecond
	for i := 0; i < attempt && base < 2*time.Second; i++ {
		base *= 2
	}
	if base > 2*time.Second {
		base = 2 * time.Second
	}
	return base + artifactRetryJitter(base/2), true
}

func transientArtifactNetworkError(err error) bool {
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func parseArtifactRetryAfter(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, false
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && seconds >= 0 {
		if seconds >= int(artifactMaxRetryDelay/time.Second) {
			return artifactMaxRetryDelay, true
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(raw)
	if err != nil {
		return 0, false
	}
	delay := when.Sub(artifactRetryNow())
	if delay < 0 {
		delay = 0
	}
	if delay > artifactMaxRetryDelay {
		delay = artifactMaxRetryDelay
	}
	return delay, true
}

func sleepArtifactRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// commitDownloadedFile publishes a verified temporary file with platform-
// specific atomic replace/no-replace primitives. The temp lives beside the
// destination, so publication never crosses filesystems and closes the
// collision TOCTOU window between preflight and commit.
func commitDownloadedFile(tmp, dest string, force bool) error {
	if err := publishDownloadedFile(tmp, dest, force); err != nil {
		if errors.Is(err, os.ErrExist) {
			return postAuthorizationCollision(dest)
		}
		return fmt.Errorf("atomically committing download to %s: %w", dest, err)
	}
	return nil
}

func postAuthorizationCollision(dest string) error {
	return fmt.Errorf("%s already exists; re-run the same command with --force to overwrite and resume using the kept authorization replay state without another charge", dest)
}

// downloadFailure maps a transfer error to a safe, actionable error. A
// canceled context (SIGINT) becomes canceledSilently, which maps to exit 130
// with no further output. Everything else is wrapped with the artifact name
// and the *unwrapped* cause: url.Error.Error() embeds the full signed URL,
// which is a credential and must never reach stderr.
func downloadFailure(name string, err error) error {
	if errors.Is(err, context.Canceled) {
		return canceledSilently{}
	}
	return fmt.Errorf("downloading %s: %w", name, stripSignedURL(err))
}

// stripSignedURL unwraps a *url.Error (whose Error() embeds the full URL,
// query included) down to its underlying cause. Non-url.Error values pass
// through unchanged.
func stripSignedURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

// verifyArtifactChecksum checks the streamed digests against the artifact's
// recorded checksum ("algo:value"). Unknown formats warn and continue; a
// recognized mismatch is fatal.
func verifyArtifactChecksum(f *cmdutil.Factory, art gen.DownloadArtifact, crc hash.Hash32, sha hash.Hash) error {
	expected := deref(art.Checksum)
	if expected == "" {
		return nil
	}
	algo, value, found := strings.Cut(expected, ":")
	if !found {
		warnUnverified(f, art.Name)
		return nil
	}
	switch strings.ToLower(algo) {
	case "sha256":
		got := hex.EncodeToString(sha.Sum(nil))
		if !strings.EqualFold(value, got) {
			return checksumMismatch(art.Name, expected, "sha256:"+got)
		}
	case "crc32c":
		// GCS convention: base64 of the big-endian 4-byte sum. Hex is
		// accepted too in case the pipeline recorded it that way.
		var be [4]byte
		binary.BigEndian.PutUint32(be[:], crc.Sum32())
		gotB64 := base64.StdEncoding.EncodeToString(be[:])
		gotHex := hex.EncodeToString(be[:])
		if value != gotB64 && !strings.EqualFold(value, gotHex) {
			return checksumMismatch(art.Name, expected, "crc32c:"+gotB64)
		}
	default:
		warnUnverified(f, art.Name)
	}
	return nil
}

func recognizedArtifactChecksum(art gen.DownloadArtifact) bool {
	expected := deref(art.Checksum)
	algo, _, found := strings.Cut(expected, ":")
	if !found {
		return false
	}
	switch strings.ToLower(algo) {
	case "sha256", "crc32c":
		return true
	}
	return false
}

func checksumMismatch(name, expected, got string) error {
	return fmt.Errorf("checksum mismatch for %s (expected %s, got %s); the partial file was discarded — re-run the download",
		name, expected, got)
}

func warnUnverified(f *cmdutil.Factory, name string) {
	fmt.Fprintf(f.IOStreams.ErrOut,
		"! %s: unrecognized checksum format; integrity not verified\n",
		text.SanitizeTerminalInline(name))
}

// progressWriter throttles progress updates to once per MiB so TTY redraws
// never dominate large downloads.
type progressWriter struct {
	prog *progress
	n    int64
	last int64
}

func (pw *progressWriter) Write(b []byte) (int, error) {
	pw.n += int64(len(b))
	if pw.n-pw.last >= 1<<20 {
		pw.prog.update(pw.n)
		pw.last = pw.n
	}
	return len(b), nil
}

// redactAuthorization replaces every artifact url in the raw authorization
// body with a placeholder. The result is re-marshaled (sorted keys) — the
// documented exception to byte-exact --json output.
func redactAuthorization(raw []byte) (json.RawMessage, error) {
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("decoding authorization for --json: %w", err)
	}
	if artifacts, ok := body["artifacts"].([]any); ok {
		for _, a := range artifacts {
			if artifact, ok := a.(map[string]any); ok {
				if _, ok := artifact["url"]; ok {
					artifact["url"] = redactedURL
				}
			}
		}
	}
	// A plain json.Marshal would HTML-escape "<redacted>" to <...;
	// disable HTML escaping so the documented placeholder survives verbatim.
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return nil, err
	}
	return json.RawMessage(bytes.TrimRight(buf.Bytes(), "\n")), nil
}
