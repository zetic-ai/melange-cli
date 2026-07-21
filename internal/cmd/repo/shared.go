package repo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zetic-ai/melange-cli/internal/api"
	"github.com/zetic-ai/melange-cli/internal/api/gen"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// genClient returns the generated API client (over the authenticated wrapper's
// transport chain) plus the wrapper itself for /v1/me resolution.
func genClient(f *cmdutil.Factory) (*gen.ClientWithResponses, *api.Client, error) {
	client, err := f.ApiClient()
	if err != nil {
		return nil, nil, err
	}
	g, err := client.Gen()
	if err != nil {
		return nil, nil, err
	}
	return g, client, nil
}

// splitRepoArg parses a "[ACCOUNT/]NAME" argument. account is "" when omitted.
func splitRepoArg(arg string) (account, name string, err error) {
	parts := strings.Split(arg, "/")
	switch len(parts) {
	case 1:
		account, name = "", parts[0]
	case 2:
		account, name = parts[0], parts[1]
	default:
		return "", "", cmdutil.FlagError{Err: fmt.Errorf(
			"invalid repository %q; expected NAME or ACCOUNT/NAME", arg)}
	}
	if name == "" || (len(parts) == 2 && account == "") {
		return "", "", cmdutil.FlagError{Err: fmt.Errorf(
			"invalid repository %q; expected NAME or ACCOUNT/NAME", arg)}
	}
	return account, name, nil
}

// resolveAccount returns the caller's account name via GET /v1/me. Only called
// when the ACCOUNT/ prefix was omitted.
func resolveAccount(ctx context.Context, client *api.Client) (string, error) {
	me, err := client.GetMe(ctx)
	if err != nil {
		return "", fmt.Errorf("resolving your account: %w", err)
	}
	if me.Account.Name == "" {
		return "", errors.New("resolving your account: /v1/me returned no account name")
	}
	return me.Account.Name, nil
}

// visibility renders is_private for humans and tab output alike.
func visibility(isPrivate bool) string {
	if isPrivate {
		return "private"
	}
	return "public"
}

// deref returns the empty string for nil string pointers.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
