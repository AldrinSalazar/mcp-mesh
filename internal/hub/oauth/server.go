package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const (
	scopeMCP         = "mcp"
	pathAssetsCSS    = "/oauth/assets/authorize.css"
	cleanupEvery     = 5 * time.Minute
	maxBody          = 64 << 10
	unusedClientKeep = 30 * 24 * time.Hour
)

// Server is the authorization server.
type Server struct {
	opts   Options
	log    *slog.Logger
	alog   adminlog.Recorder
	client *http.Client
	now    func() time.Time // replaced in tests

	registerLimit *bucketSet // per IP, POST /oauth/register
	fetchLimit    *bucketSet // per IP, client ID metadata document fetches
}

// New returns a Server.
func New(opts Options) *Server {
	if opts.AccessTTL <= 0 {
		opts.AccessTTL = time.Hour
	}
	if opts.RefreshTTL <= 0 {
		opts.RefreshTTL = 30 * 24 * time.Hour
	}
	if opts.CodeTTL <= 0 {
		opts.CodeTTL = 60 * time.Second
	}
	if opts.HubName == "" {
		opts.HubName = "mcp-mesh"
	}
	opts.Issuer = strings.TrimRight(opts.Issuer, "/")
	s := &Server{opts: opts, log: opts.Logger, alog: opts.Log, client: opts.HTTPClient, now: time.Now}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.alog == nil {
		s.alog = adminlog.Nop{}
	}
	if s.client == nil {
		s.client = safeClient()
	}
	s.registerLimit = newBucketSet(10, 6*time.Second, func() time.Time { return s.now() })
	s.fetchLimit = newBucketSet(20, 3*time.Second, func() time.Time { return s.now() })
	return s
}

// Handler serves the metadata, registration, authorize, token and revoke
// endpoints (see the Path constants).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+PathResourceMeta, s.handleResourceMeta)
	mux.HandleFunc("GET "+PathResourceMeta+"/{path...}", s.handleResourceMeta)
	mux.HandleFunc("GET "+PathServerMeta, s.handleServerMeta)
	mux.HandleFunc("POST "+PathRegister, s.handleRegister)
	mux.HandleFunc("GET "+PathAuthorize, s.handleAuthorizeGet)
	mux.HandleFunc("POST "+PathAuthorize, s.handleAuthorizePost)
	mux.HandleFunc("GET "+pathAssetsCSS, s.handleCSS)
	mux.HandleFunc("GET /oauth/assets/fonts/{name}", s.handleFont)
	mux.HandleFunc("POST "+PathToken, s.handleToken)
	mux.HandleFunc("POST "+PathRevoke, s.handleRevoke)
	return cors(mux)
}

// cors lets browser-based MCP clients use the discovery, registration and
// token endpoints. None of them use cookies, so any origin is safe; the
// authorize page is a navigation and gets no CORS headers.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case PathAuthorize, pathAssetsCSS:
		default:
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", "*")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
			h.Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleResourceMeta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"resource":                 s.opts.Resource,
		"authorization_servers":    []string{s.opts.Issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{scopeMCP},
		"resource_name":            s.opts.HubName,
	})
}

func (s *Server) handleServerMeta(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                                s.opts.Issuer,
		"authorization_endpoint":                s.opts.Issuer + PathAuthorize,
		"token_endpoint":                        s.opts.Issuer + PathToken,
		"registration_endpoint":                 s.opts.Issuer + PathRegister,
		"revocation_endpoint":                   s.opts.Issuer + PathRevoke,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{scopeMCP},
		"client_id_metadata_document_supported": true,
		// RFC 9207: the authorization response carries iss.
		"authorization_response_iss_parameter_supported": true,
	})
}

// randomToken returns 32 random bytes as base64url. Tokens and codes carry
// no prefix: they are opaque, and only their hash is stored.
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Verify maps an access token to its client key. It fails for unknown,
// expired or revoked tokens and for tokens whose key was revoked.
func (s *Server) Verify(ctx context.Context, token string) (auth.Key, bool) {
	if token == "" || len(token) > 512 {
		return auth.Key{}, false
	}
	var keyID string
	err := s.opts.DB.QueryRowContext(ctx,
		"SELECT key_id FROM oauth_tokens WHERE hash = ? AND kind = 'access' AND expires_at > ?",
		auth.Hash(token), store.Millis(s.now())).Scan(&keyID)
	if err != nil {
		return auth.Key{}, false
	}
	key, ok := s.opts.Keys.Get(keyID)
	if !ok || key.Kind != auth.KindClient {
		return auth.Key{}, false
	}
	return key, true
}

func bearerToken(r *http.Request) (string, bool) {
	const scheme = "bearer "
	h := r.Header.Get("Authorization")
	if len(h) <= len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	t := strings.TrimSpace(h[len(scheme):])
	return t, t != ""
}

// RequireClient admits requests to the MCP endpoint that carry either a
// client key or an OAuth access token as a bearer token, marks the key used
// (auth.Store.Touch) and attaches it with auth.WithKey. Anything else gets
// 401 with "WWW-Authenticate: Bearer resource_metadata=..." pointing at the
// protected resource metadata (and error="invalid_token" when a token was
// presented), which is how MCP clients discover the OAuth flow.
func (s *Server) RequireClient(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, presented := bearerToken(r)
		var key auth.Key
		var ok bool
		if presented {
			if strings.HasPrefix(token, "mm_client_") {
				key, ok = s.opts.Keys.Lookup(token)
				ok = ok && key.Kind == auth.KindClient
			} else {
				key, ok = s.Verify(r.Context(), token)
			}
		}
		if !ok {
			challenge := `Bearer resource_metadata="` + s.resourceMetaURL() + `"`
			msg := "missing credentials: send Authorization: Bearer <client key or access token>"
			if presented {
				challenge += `, error="invalid_token"`
				msg = "unknown, expired or revoked credentials"
			}
			w.Header().Set("WWW-Authenticate", challenge)
			http.Error(w, msg, http.StatusUnauthorized)
			return
		}
		s.opts.Keys.Touch(key, auth.ClientIP(r))
		next.ServeHTTP(w, r.WithContext(auth.WithKey(r.Context(), key)))
	})
}

// resourceMetaURL is where the protected resource metadata for the MCP
// endpoint lives (RFC 9728 section 3.1: the well-known path with the
// resource's path appended).
func (s *Server) resourceMetaURL() string {
	p := ""
	if u, err := url.Parse(s.opts.Resource); err == nil {
		p = strings.TrimRight(u.Path, "/")
	}
	return s.opts.Issuer + PathResourceMeta + p
}

// Run deletes expired codes and tokens periodically until ctx ends.
func (s *Server) Run(ctx context.Context) {
	s.cleanup(ctx)
	t := time.NewTicker(cleanupEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.cleanup(ctx)
		}
	}
}

func (s *Server) cleanup(ctx context.Context) {
	now := store.Millis(s.now())
	// A used code stays (as a negative expiry) until it would have expired,
	// so a replay can be told from an unknown code.
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{"DELETE FROM oauth_codes WHERE abs(expires_at) <= ?", []any{now}},
		{"DELETE FROM oauth_tokens WHERE expires_at <= ?", []any{now}},
		{`DELETE FROM oauth_clients WHERE created_at < ?
			AND NOT EXISTS (SELECT 1 FROM keys WHERE keys.oauth_client = oauth_clients.id)`,
			[]any{store.Millis(s.now().Add(-unusedClientKeep))}},
	} {
		if _, err := s.opts.DB.ExecContext(ctx, q.sql, q.args...); err != nil && ctx.Err() == nil {
			s.log.Warn("oauth cleanup failed", "error", err)
		}
	}
}
