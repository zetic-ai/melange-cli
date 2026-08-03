package mcp

import (
	"fmt"
	"strings"
)

// splitRepo parses the `repo` tool argument into its account and repository
// halves. Tools always require the fully qualified ACCOUNT/NAME form: like
// the CLI's -R flag on `melange model`, no account default is applied, so an
// agent can never mistake which account it is reading from.
//
// The returned error is an ordinary error: handlers surface it as an IsError
// tool result so the model can correct the argument and retry.
func splitRepo(s string) (account, name string, err error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf(
			"invalid repo %q: expected ACCOUNT/NAME (for example zetic/whisper-tiny); "+
				"call list_repos to discover repositories", s)
	}
	return parts[0], parts[1], nil
}
