package mcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zetic-ai/melange-cli/internal/cmdutil"
	"github.com/zetic-ai/melange-cli/internal/config"
)

// TestResolveHostFollowsCLIPrecedence pins that the HTTP transport fronts the
// same API the rest of the CLI talks to, chosen the same way. The host is the
// one piece of local configuration HTTP mode is allowed to read — it says
// which backend this deployment serves — so it must not invent its own
// precedence rules alongside --host/MELANGE_HOST/config.
func TestResolveHostFollowsCLIPrecedence(t *testing.T) {
	configured := &config.Config{Host: "https://config.example"}

	tests := []struct {
		name     string
		flag     string
		env      string
		cfg      *config.Config
		want     string
		loadFail bool
	}{
		{name: "flag wins", flag: "https://flag.example", env: "https://env.example", cfg: configured, want: "https://flag.example"},
		{name: "env beats config", env: "https://env.example", cfg: configured, want: "https://env.example"},
		{name: "config beats default", cfg: configured, want: "https://config.example"},
		{name: "default", want: config.DefaultHost},
		{name: "no config loader still resolves", env: "https://env.example", want: "https://env.example"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(config.EnvHost, tt.env)
			f := &cmdutil.Factory{HostOverride: tt.flag}
			if tt.cfg != nil {
				f.Config = func() (*config.Config, error) { return tt.cfg, nil }
			}

			got, err := resolveHost(f)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestResolveHostSurfacesConfigErrors: an unreadable config is an operator
// problem, not something to paper over with the default host.
func TestResolveHostSurfacesConfigErrors(t *testing.T) {
	f := &cmdutil.Factory{Config: func() (*config.Config, error) { return nil, assert.AnError }}

	_, err := resolveHost(f)
	assert.ErrorIs(t, err, assert.AnError)
}
