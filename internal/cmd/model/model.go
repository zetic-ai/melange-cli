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

  # List models and inspect one
  melange model list -R zetic/whisper-tiny
  melange model view m_ab12cd -R zetic/whisper-tiny

  # Download a converted target (billable)
  melange model targets m_ab12cd -R zetic/whisper-tiny
  melange model download m_ab12cd -R zetic/whisper-tiny --target tm_71

  # Check conversion status
  melange model status m_ab12cd -R zetic/whisper-tiny`,
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
