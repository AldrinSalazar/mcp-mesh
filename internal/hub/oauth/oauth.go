// Package oauth makes the hub an OAuth 2.1 authorization server for its MCP
// endpoint, so hosted apps and phones (claude.ai, mobile clients) can add the
// hub by URL instead of pasting a client key.
//
// It follows the MCP authorization spec: protected resource metadata
// (RFC 9728) and authorization server metadata (RFC 8414) under
// /.well-known, dynamic client registration (RFC 7591) and client ID
// metadata documents, the authorization code flow with PKCE (S256 only),
// resource indicators (RFC 8707), rotating refresh tokens and token
// revocation (RFC 7009).
//
// Approving an authorization creates a client key (auth.Store.CreateOAuth)
// in the groups the admin picked; access tokens map to that key, so the ACL,
// revocation, last-used and the audit log see an ordinary client key.
// Revoking the key (dashboard or CLI) deletes its tokens with it.
//
// The authorize page is served on the MCP listener, since that is the
// address the app knows. Whoever approves must be a hub admin: an existing
// dashboard session (cookies are per host, not per port, so a session from
// the dashboard port counts) or the admin username and password on the
// page, rate limited like the dashboard's sign-in.
package oauth

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

// Paths served by Handler (the client listener mounts "/.well-known/" and
// "/oauth/" on it).
const (
	PathResourceMeta = "/.well-known/oauth-protected-resource" // also "<this>/mcp"
	PathServerMeta   = "/.well-known/oauth-authorization-server"
	PathRegister     = "/oauth/register"
	PathAuthorize    = "/oauth/authorize"
	PathToken        = "/oauth/token"
	PathRevoke       = "/oauth/revoke"
)

// Sessions is the dashboard session mechanism, to recognise a signed-in
// admin on the authorize page or sign one in there.
type Sessions interface {
	Login(w http.ResponseWriter, r *http.Request, username, password string) (auth.Session, error)
	Current(r *http.Request) (auth.Session, bool)
	CheckCSRF(r *http.Request, s auth.Session) bool
}

// Groups lists groups for the consent page and puts the new key in the
// chosen ones.
type Groups interface {
	List(ctx context.Context) ([]acl.Group, error)
	SetKeyGroups(ctx context.Context, keyID string, groupIDs []string) error
}

// Options configure a Server.
type Options struct {
	DB       *store.DB
	Keys     *auth.Store
	Sessions Sessions
	Groups   Groups
	Log      adminlog.Recorder // nil = adminlog.Nop
	Logger   *slog.Logger      // nil = slog.Default()

	// Issuer is the authorization server's identifier and base URL: the
	// client URL's origin, e.g. "https://hub.example:7002".
	Issuer string
	// Resource is the protected MCP endpoint, the hub's client URL, e.g.
	// "https://hub.example:7002/mcp". Tokens are bound to it.
	Resource string
	// HubName is shown on the authorize page.
	HubName string

	AccessTTL  time.Duration // 0 = 1h
	RefreshTTL time.Duration // 0 = 30 days, rotated on every use
	CodeTTL    time.Duration // 0 = 60s

	// HTTPClient fetches client ID metadata documents. Nil = a client with a
	// 5 s timeout, 64 KiB body limit, that refuses non-HTTPS URLs and
	// private, loopback and link-local addresses (no SSRF into the LAN).
	HTTPClient *http.Client
}
