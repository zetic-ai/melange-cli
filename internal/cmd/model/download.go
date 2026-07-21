package model

import (
	"bufio"
	"bytes"
	"context"
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
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

// redactedURL replaces artifact URLs in --json output: signed URLs are
// short-lived credentials and must never land in logs or agent transcripts.
const redactedURL = "<redacted>"

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
Idempotency-Key, so transient failures are retried without a double
charge. Exceeding the quota is an error with nothing charged.

Files are written to --output (default: the current directory; an
existing directory receives one file per artifact, any other path names
the destination file for single-artifact targets). Each file is
downloaded to a temporary file, verified against the artifact's
checksum when one is available, and atomically renamed into place —
interrupted downloads never leave partial files. Existing files are
never overwritten without --force.

With --json the authorization response is written to stdout with every
artifact url replaced by "<redacted>" (the only documented deviation
from byte-exact --json): use this command to download, or melange api
if you genuinely need raw signed URLs.

Exit codes: 0 success, 1 API/download/verification error (including
quota exhaustion), 2 usage error or missing confirmation, 4 not
authenticated, 130 interrupted.`,
		Example: `  # Pick a target, then download it into a directory
  melange model targets m_ab12cd -R zetic/whisper-tiny
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71 --output ./models

  # Agent pattern: non-interactive download (the billable step needs --yes)
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71 --yes

  # Agent pattern: capture the authorization id (URLs are redacted)
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71 --yes --json --jq .authorization_id`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoFlag(opts.repo)
			if err != nil {
				return err
			}
			opts.account, opts.name, opts.key = account, name, args[0]
			if opts.target == "" {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"--target TARGET_ID is required; list targets with: melange model targets %s -R %s",
					opts.key, opts.repo)}
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
	fl.StringVarP(&opts.output, "output", "o", ".", "Destination `directory` (or file for single-artifact targets)")
	fl.BoolVar(&opts.yes, "yes", false, "Skip the billable-download confirmation")
	fl.BoolVar(&opts.force, "force", false, "Overwrite existing files")
	cmdutil.AddJSONFlags(cmd, &opts.exporter)

	return cmd
}

func runDownload(ctx context.Context, opts *downloadOptions) error {
	g, err := genClient(opts.f)
	if err != nil {
		return err
	}

	if !opts.yes {
		if err := confirmBillableDownload(ctx, opts, g); err != nil {
			return err
		}
	}

	// One Idempotency-Key per logical download: the retry transport replays
	// the SAME key on transient 5xx, so the server never charges twice. 429
	// retries are exempted — a bandwidth-quota 429 is not transient at retry
	// timescales, so it must surface immediately with the quota message.
	resp, err := g.CreateDownloadAuthorizationWithResponse(api.WithNoRetryOn429(ctx),
		opts.account, opts.name, opts.key,
		opts.target, &gen.CreateDownloadAuthorizationParams{IdempotencyKey: newIdempotencyKeyParam()})
	if err != nil {
		return err
	}
	if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
		var apiErr *api.Error
		if errors.As(aerr, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
			hint := "The download was not authorized and nothing was charged; check `melange usage quotas`."
			if apiErr.RetryAfter > 0 {
				hint += fmt.Sprintf(" Retry after %s.", apiErr.RetryAfter)
			}
			return fmt.Errorf("%w\n%s", aerr, hint)
		}
		return aerr
	}
	auth := resp.JSON201
	if auth == nil {
		auth = resp.JSON200 // Idempotency-Key replay: fresh URLs, no new charge
	}
	if auth == nil {
		return fmt.Errorf("unexpected response authorizing download (HTTP %d)", resp.StatusCode())
	}

	paths, err := planArtifactPaths(opts, auth.Artifacts)
	if err != nil {
		return err
	}

	var total int64
	for i, art := range auth.Artifacts {
		n, err := downloadArtifact(ctx, opts, art, paths[i])
		if err != nil {
			return err
		}
		total += n
	}

	ios := opts.f.IOStreams
	dest := opts.output
	if len(paths) == 1 {
		dest = paths[0]
	}
	fmt.Fprintf(ios.ErrOut, "✓ Downloaded %d artifact(s) (%s) to %s\n",
		len(auth.Artifacts), text.FormatBytes(total), dest)

	if opts.exporter != nil {
		redacted, err := redactAuthorization(resp.Body)
		if err != nil {
			return err
		}
		return opts.exporter.Write(ios, redacted)
	}
	return nil
}

// validOutput rejects --output values that could only fail after the
// billable authorization: the path must be an existing directory, or a file
// path whose parent directory exists and (without --force) no existing file.
// Dir-mode collisions cannot be checked here — artifact names only exist
// after the billable POST — so planArtifactPaths re-checks post-charge.
func validOutput(output string, force bool) error {
	if info, err := os.Stat(output); err == nil {
		if info.IsDir() {
			return nil
		}
		if !force {
			return cmdutil.FlagError{Err: fmt.Errorf(
				"--output %q already exists; pass --force to overwrite", output)}
		}
		return nil
	}
	if strings.HasSuffix(output, string(os.PathSeparator)) {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"--output %q is not an existing directory", output)}
	}
	if info, err := os.Stat(filepath.Dir(output)); err != nil || !info.IsDir() {
		return cmdutil.FlagError{Err: fmt.Errorf(
			"--output %q: parent directory does not exist", output)}
	}
	return nil
}

// confirmBillableDownload gates the charge. The preview reads the FREE
// targets listing (artifact names only exist after the billable POST, so the
// preview shows the target identity and its aggregate size instead) and asks
// for an explicit yes. Non-interactive runs must pass --yes.
func confirmBillableDownload(ctx context.Context, opts *downloadOptions, g *gen.ClientWithResponses) error {
	ios := opts.f.IOStreams
	if !ios.IsStdinTTY() || opts.f.NoInput {
		return cmdutil.FlagError{Err: errors.New(
			"downloading is billable and requires confirmation; re-run with --yes to confirm non-interactively")}
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
		return fmt.Errorf("target %s not found for model %s in %s; list targets with: melange model targets %s -R %s",
			opts.target, opts.key, opts.repo, opts.key, opts.repo)
	}

	desc := string(target.Kind) + "/" + target.Target
	if quant := deref(target.QuantType); quant != "" {
		desc += ", " + quant
	}
	fmt.Fprintf(ios.ErrOut, "Target %s (%s): %s\n", target.TargetId, desc,
		text.FormatBytes(int64(target.DownloadSize)))
	fmt.Fprintf(ios.ErrOut, "This download counts against your bandwidth quota.\n")
	fmt.Fprintf(ios.ErrOut, "Proceed? [y/N] ")

	line, err := bufio.NewReader(ios.In).ReadString('\n')
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
	for i, art := range artifacts {
		if err := validArtifactName(art.Name); err != nil {
			return nil, err
		}
		if seen[art.Name] {
			return nil, fmt.Errorf("duplicate artifact name %q in authorization", art.Name)
		}
		seen[art.Name] = true
		if dirMode {
			paths[i] = filepath.Join(opts.output, art.Name)
		} else {
			paths[i] = opts.output
		}
		if _, err := os.Stat(paths[i]); err == nil && !opts.force {
			return nil, fmt.Errorf("%s already exists; pass --force to overwrite", paths[i])
		}
	}
	return paths, nil
}

// validArtifactName rejects server-supplied names that could escape the
// output directory. The API contract is plain basenames; anything else is
// treated as hostile.
func validArtifactName(name string) error {
	if name == "" || name == "." || name == ".." ||
		strings.ContainsAny(name, `/\`) || filepath.IsAbs(name) {
		return fmt.Errorf("unsafe artifact name %q in authorization", name)
	}
	return nil
}

// downloadArtifact streams one signed URL to its destination: temp file in
// the same directory, checksum verification, then an atomic rename. On any
// failure the temp file is removed and the destination is left untouched.
func downloadArtifact(ctx context.Context, opts *downloadOptions, art gen.DownloadArtifact, dest string) (written int64, err error) {
	client := bareHTTPClient(opts.f)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, art.Url, nil)
	if err != nil {
		// URL parse errors arrive as *url.Error embedding the full URL.
		return 0, fmt.Errorf("building download request for %s: %w", art.Name, stripSignedURL(err))
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, downloadFailure(art.Name, err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("downloading %s: HTTP %d (the signed URL may have expired; re-run the download)",
			art.Name, resp.StatusCode)
	}

	total := int64(0)
	if art.Size != nil {
		total = int64(*art.Size)
	} else if resp.ContentLength > 0 {
		total = resp.ContentLength
	}
	prog := newDownloadProgress(opts.f, art.Name, total)

	tmp, err := os.CreateTemp(filepath.Dir(dest), ".melange-download-*")
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()

	crc := crc32.New(crc32.MakeTable(crc32.Castagnoli))
	sha := sha256.New()
	pw := &progressWriter{prog: prog}
	written, err = io.Copy(io.MultiWriter(tmp, crc, sha, pw), resp.Body)
	if err != nil {
		return 0, downloadFailure(art.Name, err)
	}
	if err = verifyArtifactChecksum(opts.f, art, crc, sha); err != nil {
		return 0, err
	}
	if err = tmp.Close(); err != nil {
		return 0, err
	}
	// Downloads are shareable data files, not secrets: world-readable like
	// any normally-created file (CreateTemp defaults to 0600).
	if err = os.Chmod(tmp.Name(), 0o644); err != nil {
		return 0, err
	}
	if err = os.Rename(tmp.Name(), dest); err != nil {
		return 0, err
	}
	prog.doneAs(written)
	return written, nil
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

func checksumMismatch(name, expected, got string) error {
	return fmt.Errorf("checksum mismatch for %s (expected %s, got %s); the partial file was discarded — re-run the download",
		name, expected, got)
}

func warnUnverified(f *cmdutil.Factory, name string) {
	fmt.Fprintf(f.IOStreams.ErrOut,
		"! %s: unrecognized checksum format; integrity not verified\n", name)
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
