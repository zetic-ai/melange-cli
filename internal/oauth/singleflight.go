package oauth

import "sync"

// RefreshMu provides per-host single-flight for OAuth refresh.
// It is shared by internal/cmd/auth and cmd/melange to prevent
// concurrent refreshes on the same host from double-revoking a family.
var RefreshMu sync.Map
