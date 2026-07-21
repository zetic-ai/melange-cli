package cmdutil_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "nil → 0",
			err:  nil,
			want: 0,
		},
		{
			name: "generic error → 1",
			err:  errors.New("some error"),
			want: 1,
		},
		{
			name: "SilentError → 1",
			err:  cmdutil.SilentError,
			want: 1,
		},
		{
			name: "FlagError → 2",
			err:  cmdutil.FlagError{Err: errors.New("bad flag")},
			want: 2,
		},
		{
			name: "wrapped FlagError → 2",
			err:  fmt.Errorf("outer: %w", cmdutil.FlagError{Err: errors.New("bad flag")}),
			want: 2,
		},
		{
			name: "AuthError → 4",
			err:  cmdutil.AuthError{Err: errors.New("not authed")},
			want: 4,
		},
		{
			name: "wrapped AuthError → 4",
			err:  fmt.Errorf("outer: %w", cmdutil.AuthError{Err: errors.New("not authed")}),
			want: 4,
		},
		{
			name: "context.Canceled → 130",
			err:  context.Canceled,
			want: 130,
		},
		{
			name: "wrapped context.Canceled → 130",
			err:  fmt.Errorf("outer: %w", context.Canceled),
			want: 130,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cmdutil.ExitCode(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestFlagError(t *testing.T) {
	inner := errors.New("flag was bad")
	fe := cmdutil.FlagError{Err: inner}
	assert.Equal(t, "flag was bad", fe.Error())
	assert.Equal(t, inner, fe.Unwrap())
}

func TestAuthError(t *testing.T) {
	inner := errors.New("not authenticated")
	ae := cmdutil.AuthError{Err: inner}
	assert.Equal(t, "not authenticated", ae.Error())
	assert.Equal(t, inner, ae.Unwrap())
}

func TestSilentError(t *testing.T) {
	assert.Error(t, cmdutil.SilentError)
	// SilentError is a sentinel — it equals itself
	assert.True(t, errors.Is(cmdutil.SilentError, cmdutil.SilentError))
}
