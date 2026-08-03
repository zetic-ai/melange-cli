package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/upload"
	"github.com/zetic-ai/melange-cli/internal/uploadflow"
	"github.com/zetic-ai/melange-cli/internal/wait"
)

// uploadStallTimeout is the per-chunk inactivity budget during transfers,
// matching the CLI's --inactivity-timeout default.
const uploadStallTimeout = 2 * time.Minute

// registerUpload registers the model upload tool. It is local-only
// (Options.EnableLocalTools): the flow reads files from the machine the
// server process runs on, which is only the caller's machine over stdio.
func registerUpload(s *mcp.Server, d Deps) {
	mcp.AddTool(s, &mcp.Tool{
		Name: "upload_model",
		Description: "Upload a local model file (with optional sample inputs and external data) " +
			"into a repository through a resumable upload session, then register it and start " +
			"conversion. Not billable. File paths are read by the MCP SERVER process, so they " +
			"must be absolute or relative to the server process's working directory — not the " +
			"user's. Success returns the envelope {\"session\": <upload-complete response>, " +
			"\"model\": <model reference>}; \"model\" appears once registration produced one, " +
			"and with wait_seconds a \"status\" key carries the latest conversion status the " +
			"wait observed. If the upload is interrupted or fails, the error names the session " +
			"id: fix the issue, then call upload_model again with resume_session_id to continue " +
			"— already-acknowledged bytes are never re-sent, and once the session reaches " +
			"VERIFYING, DISPATCH_PENDING, or CONVERTING the resume replays completion without " +
			"needing the local files. Conversion usually outlives one call: follow up with " +
			"get_conversion_status on the returned model key rather than blocking on a long wait. " +
			"Bucketed .pt2 uploads and manifest files are CLI-only for now " +
			"('melange model upload').",
		InputSchema:  inputSchemaFor[uploadModelArgs](withWaitBounds),
		OutputSchema: outputSchema("upload_model"),
		Annotations: &mcp.ToolAnnotations{
			IdempotentHint:  false,
			DestructiveHint: falsePtr(),
			OpenWorldHint:   falsePtr(),
		},
	}, uploadModelHandler(d))
}

// uploadModelArgs are the arguments of upload_model, mirroring the CLI's
// upload file-spec vocabulary (MODEL_FILE, --input, --external-data) exactly
// as uploadflow.BuildSpecs accepts it.
type uploadModelArgs struct {
	Repo            string   `json:"repo" jsonschema:"Target repository in ACCOUNT/NAME form (example: zetic/whisper-tiny). No default account is applied."`
	ModelFile       string   `json:"model_file,omitempty" jsonschema:"Path to the model file, readable by the server process (absolute, or relative to the server's working directory). Required unless resume_session_id is set."`
	Inputs          []string `json:"inputs,omitempty" jsonschema:"Sample input file paths; order defines each input's input_index."`
	External        []string `json:"external,omitempty" jsonschema:"External data file paths, e.g. ONNX external weights."`
	ResumeSessionID string   `json:"resume_session_id,omitempty" jsonschema:"Session id from an earlier failed or interrupted upload_model call: continue that session instead of starting a new upload. Pass the same model_file/inputs/external when bytes are still missing; a session that is already VERIFYING, DISPATCH_PENDING, or CONVERTING needs no files."`
	WaitSeconds     int      `json:"wait_seconds,omitempty" jsonschema:"Seconds to wait for registration and conversion after the transfer, 0-120. The budget is shared: completion is replayed and then conversion status is polled until it runs out. 0 returns as soon as the session completes."`
}

// uploadEnvelope is upload_model's success payload. Session is the raw
// upload-complete response byte-exact; Model repeats its model reference when
// registration produced one; Status is the latest raw conversion status a
// wait_seconds poll observed.
type uploadEnvelope struct {
	Session json.RawMessage `json:"session"`
	Model   json.RawMessage `json:"model,omitempty"`
	Status  json.RawMessage `json:"status,omitempty"`
}

// slogEvents adapts uploadflow.Events to the server log: flow notes land at
// info level. Per-chunk progress is deliberately dropped — a tool call has no
// progress stream, and logging every committed offset would flood the log.
type slogEvents struct{ log *slog.Logger }

func (slogEvents) Progress(string, int64, int64) {}

func (e slogEvents) Note(msg string) { e.log.Info("upload_model", "note", msg) }

// uploadModelHandler drives the shared upload session state machine
// (internal/uploadflow) end to end: create → transfer → complete, or a resume
// of any of those stages when resume_session_id is set.
func uploadModelHandler(d Deps) mcp.ToolHandlerFor[uploadModelArgs, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in uploadModelArgs) (*mcp.CallToolResult, any, error) {
		account, name, err := splitRepo(in.Repo)
		if err != nil {
			return d.toolError(err), nil, nil
		}
		repo := account + "/" + name
		if in.ResumeSessionID == "" && in.ModelFile == "" {
			return d.toolError(errors.New(
				"model_file is required to start an upload; pass resume_session_id instead " +
					"to continue an interrupted session")), nil, nil
		}
		if in.ResumeSessionID != "" {
			if err := upload.ValidateSessionID(in.ResumeSessionID); err != nil {
				return d.toolError(fmt.Errorf(
					"invalid resume_session_id: %w; pass the session id exactly as an earlier "+
						"upload_model error reported it", err)), nil, nil
			}
		}
		g, err := d.Clients.Client(ctx)
		if err != nil {
			return d.toolError(err), nil, nil
		}

		events := slogEvents{log: d.logger()}
		orch := &uploadflow.Orchestrator{
			Gen:          g,
			Events:       events,
			Bare:         d.bareClient(),
			StallTimeout: uploadStallTimeout,
			Jitter:       pollJitter,
			Sleep:        pollSleep,
			Now:          pollNow,
		}
		buildSpecs := func() ([]upload.FileSpec, error) {
			specs, _, err := uploadflow.BuildSpecs(uploadflow.ManifestInputs{
				ModelFile: in.ModelFile,
				Inputs:    in.Inputs,
				External:  in.External,
			}, events)
			return specs, err
		}
		doWait := in.WaitSeconds > 0
		budget := time.Duration(in.WaitSeconds) * time.Second

		var res *uploadflow.Result
		var runErr error
		if in.ResumeSessionID != "" {
			opts := uploadflow.ResumeOptions{
				Account: account,
				Name:    name,
				Repo:    repo,
				Wait:    doWait,
				Timeout: budget,
			}
			if in.ModelFile != "" {
				opts.BuildSpecs = buildSpecs
			}
			res, runErr = orch.Resume(ctx, in.ResumeSessionID, opts)
		} else {
			specs, err := buildSpecs()
			if err != nil {
				return d.toolError(err), nil, nil
			}
			res, runErr = orch.Run(ctx, uploadflow.Request{
				Account: account,
				Name:    name,
				Repo:    repo,
				Specs:   specs,
				Wait:    doWait,
				Timeout: budget,
			})
		}
		// The flow hands the held session lock back on every non-nil Result
		// (also partial ones next to an error); this handler owns releasing it.
		defer func() { _ = res.CloseLease() }()

		if runErr != nil {
			return d.toolError(d.uploadFlowError(runErr, events)), nil, nil
		}
		return d.uploadSuccess(ctx, g, account, name, res, in.WaitSeconds)
	}
}

// uploadSuccess turns a completed flow into the tool result: the terminal
// verdict (FAILED / canceled / expired sessions are expected failures the
// caller can act on), local state cleanup on terminal outcomes, and the
// optional shared-budget conversion wait.
func (d Deps) uploadSuccess(ctx context.Context, g *gen.ClientWithResponses,
	account, name string, res *uploadflow.Result, waitSeconds int,
) (*mcp.CallToolResult, any, error) {
	out := res.Response

	if strings.EqualFold(string(out.State), "FAILED") {
		// FAILED is terminal: the session can never be resumed, so local
		// resume state would only mislead a later resume_session_id call.
		removeUploadState(d.logger(), res.SessionID)
		failure := "unknown"
		if out.FailureCode != nil {
			failure = *out.FailureCode
		}
		return d.toolError(fmt.Errorf(
			"upload verification failed: %s (session %s). The session is not resumable; "+
				"fix the reported file and call upload_model again to start a new upload",
			failure, res.SessionID)), nil, nil
	}

	if uploadflow.TerminalCompletionWithoutModel(out) {
		removeUploadState(d.logger(), res.SessionID)
		return d.toolError(fmt.Errorf(
			"upload session %s is %s and can never produce a model; call upload_model "+
				"again without resume_session_id to start a new upload",
			res.SessionID, strings.ToLower(string(out.State)))), nil, nil
	}

	envelope := uploadEnvelope{Session: res.Completion, Model: res.Model}
	if res.Model == nil {
		// Completion is still in progress server-side (VERIFYING or
		// DISPATCH_PENDING without a model reference yet). The session — and
		// its local resume state — stays; the caller re-calls with
		// resume_session_id (optionally wait_seconds) to finish.
		return marshalUploadEnvelope(envelope)
	}

	// A model reference is the durable handoff from session recovery to model
	// status polling; local session state has served its purpose.
	removeUploadState(d.logger(), res.SessionID)

	if waitSeconds > 0 && out.Model != nil {
		// The wait budget is shared with the completion replay the flow
		// already ran; WaitStarted anchors it (mirroring the CLI's --timeout).
		remaining := time.Duration(waitSeconds)*time.Second - pollClockNow().Sub(res.WaitStarted)
		if remaining > 0 {
			envelope.Status = d.pollStatusAfterUpload(ctx, g, account, name,
				res.Response.Model.Key, remaining)
		}
	}
	return marshalUploadEnvelope(envelope)
}

// pollClockNow reads the shared poll clock seam (real time unless a test
// injected pollNow), matching the clock the orchestrator's completion wait
// ran on so the shared budget subtracts consistently.
func pollClockNow() time.Time {
	if pollNow != nil {
		return pollNow()
	}
	return time.Now()
}

// pollStatusAfterUpload runs the shared conversion-status polling core on the
// wait budget left after completion. The upload itself already succeeded, so
// a failed status readout must not discard the completion payload: it is
// logged and the status is simply omitted (the tool description documents
// "status" as what the wait observed and steers callers to
// get_conversion_status). A budget that runs out returns the latest status
// observed, exactly like get_conversion_status.
func (d Deps) pollStatusAfterUpload(ctx context.Context, g *gen.ClientWithResponses,
	account, name, key string, budget time.Duration,
) json.RawMessage {
	status, err := pollModelStatus(ctx, g, account, name, key, budget)
	if err != nil && !errors.Is(err, wait.ErrTimeout) {
		d.logger().Warn("upload_model: conversion status poll failed after a successful upload",
			"model_key", key, "error", err.Error())
		return nil
	}
	return status
}

// marshalUploadEnvelope frames the envelope through marshalEnvelope so the
// raw halves survive byte-exact (no HTML escaping).
func marshalUploadEnvelope(envelope uploadEnvelope) (*mcp.CallToolResult, any, error) {
	body, err := marshalEnvelope(envelope)
	if err != nil {
		// Every half is JSON the flow already decoded; a failure here is a
		// programming fault, not something the caller can act on.
		return nil, nil, fmt.Errorf("building upload_model envelope: %w", err)
	}
	return rawResult(body)
}

// uploadFlowError renders uploadflow's typed errors as actionable tool-error
// text; whenever a preserved session exists, the text carries its id and the
// exact resume instructions.
//
// The underlying cause is rendered through toolErrorText FIRST and the
// guidance appended to the resulting string: toolErrorText prefers an
// *api.Error anywhere in a chain, so guidance attached via %w around an API
// failure would be silently dropped. The returned error is therefore always a
// plain, fully composed message.
func (d Deps) uploadFlowError(err error, events uploadflow.Events) error {
	var uerr *uploadflow.UsageError
	if errors.As(err, &uerr) {
		return uerr.Err
	}

	var cerr *uploadflow.ConflictError
	if errors.As(err, &cerr) {
		cause := d.toolErrorText(cerr.Err)
		if cerr.Stale {
			return fmt.Errorf("%s. The conflicting session is no longer active; "+
				"call upload_model again to retry", cause)
		}
		if cerr.SessionID == "" {
			return fmt.Errorf("%s. Another upload session holds this repository's single "+
				"active slot; inspect sessions with the CLI: melange model upload --sessions", cause)
		}
		return fmt.Errorf("%s. Session %s (%s) holds this repository's single active upload "+
			"slot: continue it by calling upload_model with resume_session_id %q (pass the "+
			"original files while bytes are still missing), or discard it with the CLI: "+
			"melange model upload --cancel %s --yes",
			cause, cerr.SessionID, strings.ToUpper(cerr.State), cerr.SessionID, cerr.SessionID)
	}

	var terr *uploadflow.TerminalStateError
	if errors.As(err, &terr) {
		// Terminal sessions can never be resumed: local state would only
		// mislead a later resume attempt.
		removeUploadStateWithEvents(events, terr.SessionID)
		return fmt.Errorf("%s; call upload_model again without resume_session_id", terr.Error())
	}

	var serr *uploadflow.SessionError
	if errors.As(err, &serr) {
		switch serr.Phase {
		case uploadflow.PhaseTransfer:
			return fmt.Errorf("upload transfer failed: %s. Session %s is preserved: fix the "+
				"issue, then call upload_model again with resume_session_id %q — "+
				"already-acknowledged bytes are never re-sent",
				d.toolErrorText(serr.Err), serr.SessionID, serr.SessionID)
		default: // uploadflow.PhaseComplete
			if errors.Is(serr.Err, wait.ErrTimeout) {
				return fmt.Errorf("the upload finished but completion did not produce a model "+
					"within the wait_seconds budget. Session %s is preserved: call upload_model "+
					"again with resume_session_id %q (no files needed) to keep waiting",
					serr.SessionID, serr.SessionID)
			}
			return fmt.Errorf("completing the upload failed: %s. Session %s is preserved: "+
				"call upload_model again with resume_session_id %q to replay completion",
				d.toolErrorText(serr.Err), serr.SessionID, serr.SessionID)
		}
	}

	return err
}

// removeUploadState discards a terminal session's local resume state.
// Best-effort: the server-side outcome already happened, so a cleanup failure
// is logged, never surfaced as the call's result.
func removeUploadState(log *slog.Logger, sessionID string) {
	if err := upload.RemoveState(sessionID); err != nil {
		log.Warn("upload_model: local upload state cleanup failed",
			"session_id", sessionID, "error", err.Error())
	}
}

// removeUploadStateWithEvents is removeUploadState for call sites that only
// hold the flow's Events sink.
func removeUploadStateWithEvents(events uploadflow.Events, sessionID string) {
	if err := upload.RemoveState(sessionID); err != nil && events != nil {
		events.Note("local upload state cleanup failed: " + err.Error())
	}
}
