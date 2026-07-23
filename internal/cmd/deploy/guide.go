package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/text"
)

var supportedLanguages = map[string]gen.GetDeploymentGuideParamsLanguage{
	"android-kotlin": gen.GetDeploymentGuideParamsLanguageAndroidKotlin,
	"android-java":   gen.GetDeploymentGuideParamsLanguageAndroidJava,
	"ios-swift":      gen.GetDeploymentGuideParamsLanguageIosSwift,
	"flutter":        gen.GetDeploymentGuideParamsLanguageFlutter,
}

var supportedModes = map[string]gen.GetDeploymentGuideParamsInferenceMode{
	"auto":     gen.GetDeploymentGuideParamsInferenceModeAuto,
	"speed":    gen.GetDeploymentGuideParamsInferenceModeSpeed,
	"accuracy": gen.GetDeploymentGuideParamsInferenceModeAccuracy,
}

func newCmdGuide(f *cmdutil.Factory) *cobra.Command {
	var (
		repo     string
		language string
		mode     string
		exporter *cmdutil.Exporter
	)
	cmd := &cobra.Command{
		Use:   "guide MODEL_KEY",
		Short: "Print exact SDK deployment code for a model",
		Long: `Render the SDK install and inference code for one contained model
version. Select a language and inference mode explicitly, or use the dashboard
defaults (android-kotlin and auto).

The guide always contains YOUR_PERSONAL_KEY. For credential safety this command
does not interpolate, print, or persist the active PAT. General-model tensor
construction remains an explicit TODO because tensor shapes and preprocessing
are model-specific.`,
		Example: `  # Android Kotlin, automatic target selection
  melange deploy guide MODEL_KEY -R ACCOUNT/REPO

  # iOS Swift, prefer speed
  melange deploy guide MODEL_KEY -R ACCOUNT/REPO --language ios-swift --mode speed

  # Structured guide for an agent
  melange deploy guide MODEL_KEY -R ACCOUNT/REPO --language flutter --mode accuracy --json`,
		Args: cmdutil.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			account, name, err := splitRepoFlag(repo)
			if err != nil {
				return err
			}
			languageValue, ok := supportedLanguages[language]
			if !ok {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --language %q; expected android-kotlin, android-java, ios-swift, or flutter", language)}
			}
			modeValue, ok := supportedModes[mode]
			if !ok {
				return cmdutil.FlagError{Err: fmt.Errorf(
					"invalid --mode %q; expected auto, speed, or accuracy", mode)}
			}
			g, err := genClient(f)
			if err != nil {
				return err
			}
			params := &gen.GetDeploymentGuideParams{
				Language:      &languageValue,
				InferenceMode: &modeValue,
			}
			resp, err := g.GetDeploymentGuideWithResponse(
				cmd.Context(), account, name, args[0], params,
			)
			if err != nil {
				return err
			}
			if aerr := api.GenError(resp.StatusCode(), resp.HTTPResponse, resp.Body); aerr != nil {
				return aerr
			}
			if resp.JSON200 == nil {
				return fmt.Errorf("unexpected response fetching deployment guide (HTTP %d)", resp.StatusCode())
			}
			if resp.JSON200.CredentialPlaceholder != "YOUR_PERSONAL_KEY" {
				return errors.New("invalid deployment guide: credential_placeholder must be YOUR_PERSONAL_KEY")
			}
			if exporter != nil {
				return exporter.Write(f.IOStreams, json.RawMessage(resp.Body))
			}
			return printGuide(f, resp.JSON200)
		},
	}
	cmd.Flags().StringVarP(&repo, "repo", "R", "", "Repository as `ACCOUNT/REPO` (required)")
	cmd.Flags().StringVar(&language, "language", "android-kotlin", "SDK language: android-kotlin, android-java, ios-swift, or flutter")
	cmd.Flags().StringVar(&mode, "mode", "auto", "Inference mode: auto, speed, or accuracy")
	cmdutil.AddJSONFlags(cmd, &exporter)
	return cmd
}

func printGuide(f *cmdutil.Factory, guide *gen.DeploymentGuideResponse) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# Deployment guide: %s\n\n", guide.Model.Repository)
	fmt.Fprintf(&b, "Model: %s (version %d, %s, %s)\n", guide.Model.Key, guide.Model.Version, guide.Model.Type, guide.Model.State)
	fmt.Fprintf(&b, "Language: %s\nInference mode: %s\nSDK: %s %s\n", guide.Language, guide.InferenceMode, guide.Sdk.Name, guide.Sdk.Version)
	if !guide.Model.DownloadReady {
		fmt.Fprintln(&b, "Status: model artifacts are not download-ready yet")
	}
	fmt.Fprintf(&b, "Credential: replace %s with a personal key at implementation time.\n", guide.CredentialPlaceholder)
	for i, step := range guide.Steps {
		fmt.Fprintf(&b, "\n## %d. %s\n\n```%s\n%s\n```\n", i+1, step.Title, step.CodeLanguage, strings.TrimRight(step.Code, "\n"))
	}
	_, err := fmt.Fprint(f.IOStreams.Out, text.SanitizeTerminal(b.String()))
	return err
}
