package api

import (
	"context"
	"net/http"
)

// NOTE: this file is hand-written and will be replaced by the generated
// OpenAPI client in a later task — keep everything /v1/me-specific here.

// Me is the response of GET /v1/me.
type Me struct {
	User    MeUser    `json:"user"`
	Account MeAccount `json:"account"`
	Token   MeToken   `json:"token"`
}

// MeUser identifies the authenticated user.
type MeUser struct {
	Email    string `json:"email"`
	Nickname string `json:"nickname"`
}

// MeAccount identifies the account the token belongs to.
type MeAccount struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// MeToken describes the personal access token in use.
type MeToken struct {
	Name       string   `json:"name"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  string   `json:"expires_at"`
	LastUsedAt string   `json:"last_used_at"`
}

// GetMe fetches the identity behind the client's token.
func (c *Client) GetMe(ctx context.Context) (*Me, error) {
	var me Me
	if err := c.JSON(ctx, http.MethodGet, "/v1/me", nil, &me); err != nil {
		return nil, err
	}
	return &me, nil
}
