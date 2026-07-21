// Package model implements `melange model` — uploading models through the
// ingestion v2 protocol (manifest, resumable GCS uploads, verify/complete)
// and tracking their conversion status.
package model

import (
	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdModel builds the `melange model` command group.
func NewCmdModel(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "model <command>",
		Short: "Upload models and track their conversion",
		Long: `Upload models to a Melange repository and follow their conversion.

An upload is a session: the CLI declares a manifest (model file, optional
inputs and external data, each with size and CRC32C), streams the bytes to
signed storage URLs with resumable uploads, then completes the session so
the server verifies integrity and starts conversion. Interrupted uploads
resume without re-sending acknowledged bytes.

Uploads always require an explicit -R ACCOUNT/REPO. Data is written to
stdout; progress and messages go to stderr.`,
		Example: `  # Upload a model and wait until it is ready
  melange model upload -R zetic/whisper-tiny model.onnx --wait

  # Upload with sample inputs (order defines input_index)
  melange model upload -R zetic/whisper-tiny model.onnx --input audio.bin --input mask.bin

  # Preview the manifest without creating anything
  melange model upload -R zetic/whisper-tiny model.onnx --dry-run

  # Check conversion status
  melange model status m_ab12cd -R zetic/whisper-tiny`,
	}

	cmd.AddCommand(newCmdUpload(f))
	cmd.AddCommand(newCmdStatus(f))

	return cmd
}
