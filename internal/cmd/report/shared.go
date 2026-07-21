package report

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// genClient returns the generated API client over the authenticated transport.
func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, err
	}
	return client.Gen()
}

// splitRepoFlag parses the required -R/--repo value as ACCOUNT/REPO.
func splitRepoFlag(value string) (account, name string, err error) {
	if value == "" {
		return "", "", cmdutil.FlagError{Err: errors.New(
			"-R/--repo is required for report commands; pass -R ACCOUNT/REPO")}
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", cmdutil.FlagError{Err: fmt.Errorf(
			"invalid --repo %q; expected ACCOUNT/REPO", value)}
	}
	return parts[0], parts[1], nil
}

// deref returns "" for nil string pointers.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// formatFloat renders a metric value with one decimal place.
func formatFloat(v float32) string {
	return strconv.FormatFloat(float64(v), 'f', 1, 32)
}
