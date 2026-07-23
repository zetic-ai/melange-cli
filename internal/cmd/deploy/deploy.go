// Package deploy exposes deterministic SDK deployment guides from public-v1.
package deploy

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// NewCmdDeploy builds the `melange deploy` command group.
func NewCmdDeploy(f *cmdutil.Factory) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "deploy <command>",
		Short: "Get SDK deployment code for a model",
		Long: `Inspect the supported SDK stacks and render deterministic deployment
code for a specific model version. Guides use the public credential placeholder
YOUR_PERSONAL_KEY; melange never writes the active PAT into code or output.`,
		Args: cmdutil.CommandGroupArgs,
		RunE: cmdutil.ShowCommandGroupHelp,
	}
	cmd.AddCommand(newCmdOptions(f))
	cmd.AddCommand(newCmdGuide(f))
	return cmd
}

func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, err
	}
	return client.Gen()
}

func splitRepoFlag(value string) (account, name string, err error) {
	if value == "" {
		return "", "", cmdutil.FlagError{Err: errors.New(
			"-R/--repo is required for deploy guide; pass -R ACCOUNT/REPO")}
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", cmdutil.FlagError{Err: fmt.Errorf(
			"invalid --repo %q; expected ACCOUNT/REPO", value)}
	}
	return parts[0], parts[1], nil
}
