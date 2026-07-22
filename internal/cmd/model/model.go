// Package model implements `melange model` — uploading models through the
// ingestion v2 protocol (manifest, resumable GCS uploads, verify/complete),
// tracking their conversion status, browsing models and their converted
// targets, and downloading target artifacts.
package model

import (
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdModel builds the `melange model` command group.
func NewCmdModel(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model <command>",
		Short: "Upload, browse, and download models",
		Args:  cmdutil.CommandGroupArgs,
		RunE:  cmdutil.ShowCommandGroupHelp,
		Long: `Work with the models of a Melange repository: upload new models,
follow their conversion, list and inspect them, browse their converted
targets, download target artifacts, import LLMs from HuggingFace, and
set the repository default.

An upload is a session: the CLI declares a manifest (model file, optional
inputs and external data, each with size and CRC32C), streams the bytes to
signed storage URLs with resumable uploads, then completes the session so
the server verifies integrity and starts conversion. Interrupted uploads
resume without re-sending acknowledged bytes.

Model commands always require an explicit -R ACCOUNT/REPO. Data is
written to stdout; progress and messages go to stderr.`,
		Example: `  # Upload a model and wait until it is ready
  melange model upload -R zetic/whisper-tiny model.onnx --wait

  # Resolve the default model key, then inspect it
  model_key=$(melange model list -R zetic/whisper-tiny --jq '.results[] | select(.is_default) | .key')
  melange model view "$model_key" -R zetic/whisper-tiny

  # Download a converted target (billable)
  target_id=$(melange model targets "$model_key" -R zetic/whisper-tiny --jq '.results[0].target_id')
  melange model download "$model_key" -R zetic/whisper-tiny --target "$target_id"

  # Check conversion status
  melange model status "$model_key" -R zetic/whisper-tiny`,
	}

	cmd.AddCommand(newCmdUpload(f))
	cmd.AddCommand(newCmdStatus(f))
	cmd.AddCommand(newCmdList(f))
	cmd.AddCommand(newCmdView(f))
	cmd.AddCommand(newCmdTargets(f))
	cmd.AddCommand(newCmdSetDefault(f))
	cmd.AddCommand(newCmdImport(f))
	cmd.AddCommand(newCmdDownload(f))

	return cmd
}
