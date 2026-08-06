package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitRepo(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		account string
		repo    string
		wantErr bool
	}{
		{name: "account and name", in: "zetic/whisper-tiny", account: "zetic", repo: "whisper-tiny"},
		{name: "dots and dashes survive", in: "acme-co/my.model_v2", account: "acme-co", repo: "my.model_v2"},
		{name: "empty is rejected", in: "", wantErr: true},
		{name: "bare name has no account default", in: "whisper-tiny", wantErr: true},
		{name: "missing account", in: "/whisper-tiny", wantErr: true},
		{name: "missing name", in: "zetic/", wantErr: true},
		{name: "trailing slash is not a third segment", in: "zetic/whisper/", wantErr: true},
		{name: "three segments", in: "a/b/c", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account, repo, err := splitRepo(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "ACCOUNT/NAME", "the error must teach the expected form")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.account, account)
			assert.Equal(t, tt.repo, repo)
		})
	}
}
