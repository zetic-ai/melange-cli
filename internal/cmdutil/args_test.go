package cmdutil_test

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
)

// The positional-arg validators must wrap cobra's errors in FlagError so the
// exit-code contract (2 for usage errors) holds for arg-count mistakes, not
// just flag-parse mistakes.
func TestArgsValidatorsReturnFlagError(t *testing.T) {
	cmd := &cobra.Command{Use: "stub"}

	tests := []struct {
		name      string
		validator cobra.PositionalArgs
		args      []string
		wantErr   bool
	}{
		{"ExactArgs too many", cmdutil.ExactArgs(1), []string{"a", "b"}, true},
		{"ExactArgs too few", cmdutil.ExactArgs(1), []string{}, true},
		{"ExactArgs ok", cmdutil.ExactArgs(1), []string{"a"}, false},
		{"MaximumNArgs too many", cmdutil.MaximumNArgs(1), []string{"a", "b"}, true},
		{"MaximumNArgs ok", cmdutil.MaximumNArgs(1), []string{}, false},
		{"NoArgs extra", cmdutil.NoArgs, []string{"a"}, true},
		{"NoArgs ok", cmdutil.NoArgs, []string{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.validator(cmd, tt.args)
			if !tt.wantErr {
				assert.NoError(t, err)
				return
			}
			assert.Error(t, err)
			assert.Equal(t, 2, cmdutil.ExitCode(err),
				"positional-arg errors must map to exit 2")
		})
	}
}
