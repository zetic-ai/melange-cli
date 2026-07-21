// Package cmdutil provides shared utilities for CLI commands: the Factory,
// error types, and exit-code mapping.
package cmdutil

import (
	"context"
	"errors"
)

// SilentError is a sentinel that signals the command has already printed its
// error and the runner should exit 1 without printing anything further.
var SilentError = errors.New("silent error")

// FlagError wraps a flag/argument parse error. The runner maps it to exit 2
// and prints usage.
type FlagError struct {
	Err error
}

func (e FlagError) Error() string { return e.Err.Error() }
func (e FlagError) Unwrap() error { return e.Err }

// AuthError signals that the user is not authenticated. The runner maps it to
// exit 4.
type AuthError struct {
	Err error
}

func (e AuthError) Error() string { return e.Err.Error() }
func (e AuthError) Unwrap() error { return e.Err }

// ExitCode maps err to the appropriate process exit code following the
// melange exit-code contract:
//
//	0   nil (success)
//	1   generic error / SilentError
//	2   FlagError (usage error)
//	4   AuthError
//	130 context.Canceled (SIGINT)
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	var fe FlagError
	if errors.As(err, &fe) {
		return 2
	}
	var ae AuthError
	if errors.As(err, &ae) {
		return 4
	}
	return 1
}
