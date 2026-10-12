// Package api is the hub's JSON API under /api/v1, the only thing the
// dashboard SPA talks to. It is read-mostly: views are built from the node
// registry, the catalog and the activity logs; key and admin changes go
// through the interfaces below, so this package holds no state of its own
// except the events feed.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/known"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/update"
)

const (
	maxBody     = 64 << 10
	defaultPoll = time.Second
	pingEvery   = 25 * time.Second
	prefix      = "/api/v1"
)

// Sessions is the dashboard session mechanism (cookie + CSRF token).
type Sessions interface {
	Login(w http.ResponseWriter, r *http.Request, username, password string) (auth.Session, error)
	Current(r *http.Request) (auth.Session, bool)
	Logout(w http.ResponseWriter, r *http.Request)
	CheckCSRF(r *http.Request, s auth.Session) bool
}

// Admins manages admin users.
type Admins interface {
	List(ctx context.Context) ([]auth.User, error)
	Create(ctx context.Context, username, password string) (auth.User, error)
	SetPassword(ctx context.Context, username, password string) error
	Delete(ctx context.Context, username string) error
	Verify(ctx context.Context, username, password string) (auth.User, error)
}

// Keys manages node and client keys.
type Keys interface {
	List() ([]auth.Key, error)
	Get(id string) (auth.Key, bool)
	Create(kind auth.Kind, name string) (string, auth.Key, error)
	Revoke(idOrName string) (auth.Key, error)
}

// Audit queries the persistent call log.
type Audit interface {
	Query(ctx context.Context, q activity.AuditQuery) ([]activity.Entry, error)
	Count(ctx context.Context, since time.Time) (activity.Counts, error)
}

// Options configure a Server.
type Options struct {
	Nodes interface{ Nodes() []nodes.NodeInfo }
	// Known lists the nodes that connected before, so offline ones stay
	// listed; nil lists only the registry's.
	Known    Known
	Catalog  *catalog.Catalog
	Recent   *activity.Log
	Audit    Audit    // nil: /audit answers 404
	Keys     Keys     // nil when auth is off
	Admins   Admins   // nil when auth is off
	Sessions Sessions // nil when auth is off: no sign-in, everything open
	Groups   Groups   // nil when auth is off (everyone sees every tool)
	Trust    Trust    // nil: every tool is approved, no trust endpoints
	AdminLog AdminLog // nil: actions aren't logged, /admin-log answers 404
	// Overrides is nil in tests: /overrides answers 404.
	Overrides Overrides
	// Updates knows the newest release; nil reports the check as off.
	Updates Updates
	// OAuthName names an OAuth client key's app (keys.oauth_client); nil
	// when OAuth is off.
	OAuthName func(clientID string) string
	// OAuthIssuer is the authorization server's URL; empty when OAuth is off.
	OAuthIssuer string
	// Pairing is nil when the hub doesn't accept pairing requests (auth
	// off, or turned off): the list is empty and approving answers 404.
	Pairing Pairing
	// DisconnectKey closes the nodes connected with a key at once (the
	// registry's), for a key replaced by a pairing; nil leaves it to the
	// key watcher.
	DisconnectKey func(keyID string)

	Version, NodeURL, ClientURL, DiscoveryName string
	// StartedAt is when the hub started: for a while after, its nodes are
	// reconnecting rather than offline.
	StartedAt time.Time

	// Health reports readiness problems and metrics from the rest of the
	// hub (GET /health, and /readyz on the admin listener). Nil: only the
	// API's own checks.
	Health func(ctx context.Context) Health

	Logger *slog.Logger  // nil = slog.Default()
	Poll   time.Duration // event snapshot interval; 0 = 1s
}

// Updates is the check for newer releases.
type Updates interface {
	Enabled() bool
	Status() update.Status
}

// Known remembers nodes that have connected.
type Known interface {
	List() []known.Node
	Forget(ctx context.Context, name string) (bool, error)
}

// Server is the API handler plus the events feed.
type Server struct {
	opts Options
	log  *slog.Logger
	feed *feed
	ping time.Duration
}

// New returns a Server. Call Run to drive the events feed.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Poll <= 0 {
		opts.Poll = defaultPoll
	}
	s := &Server{opts: opts, log: opts.Logger, ping: pingEvery}
	s.feed = newFeed(s.snapshot, s.eventTypes())
	return s
}

// Run polls state and publishes changes to /events subscribers until ctx
// ends. The caller owns the goroutine.
func (s *Server) Run(ctx context.Context) { s.feed.run(ctx, s.opts.Poll) }

// Handler serves the API; mount it at "/api/v1/" without stripping the prefix.
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serve) }

type route struct {
	methods map[string]http.HandlerFunc // GET also serves HEAD
	public  bool                        // reachable without a session
	// needsAuth routes answer 404 when auth is off.
	needsAuth bool
}

func (s *Server) route(path string) (route, map[string]string, bool) {
	seg := strings.Split(strings.Trim(path, "/"), "/")
	m := func(kv ...any) map[string]http.HandlerFunc {
		out := map[string]http.HandlerFunc{}
		for i := 0; i < len(kv); i += 2 {
			out[kv[i].(string)] = kv[i+1].(func(http.ResponseWriter, *http.Request))
		}
		return out
	}
	switch {
	case len(seg) == 1 && seg[0] == "info":
		return route{methods: m("GET", s.info), public: true}, nil, true
	case len(seg) == 1 && seg[0] == "session":
		return route{methods: m("GET", s.sessionGet, "POST", s.sessionLogin, "DELETE", s.sessionLogout), public: true}, nil, true
	case len(seg) == 1 && seg[0] == "nodes":
		return route{methods: m("GET", s.nodesList)}, nil, true
	case len(seg) == 1 && seg[0] == "tools":
		return route{methods: m("GET", s.toolsList)}, nil, true
	case len(seg) == 1 && seg[0] == "prompts":
		return route{methods: m("GET", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.promptViews()) })}, nil, true
	case len(seg) == 1 && seg[0] == "resources":
		return route{methods: m("GET", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.resourceViews()) })}, nil, true
	case len(seg) == 1 && seg[0] == "calls":
		return route{methods: m("GET", s.callsList)}, nil, true
	case len(seg) == 1 && seg[0] == "audit":
		return route{methods: m("GET", s.auditList)}, nil, true
	case len(seg) == 1 && seg[0] == "stats":
		return route{methods: m("GET", s.statsGet)}, nil, true
	case len(seg) == 1 && seg[0] == "update":
		return route{methods: m("GET", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.updateView()) })}, nil, true
	case len(seg) == 1 && seg[0] == "health":
		return route{methods: m("GET", s.healthGet)}, nil, true
	case len(seg) == 1 && seg[0] == "approvals":
		return route{methods: m("GET", s.approvalsList, "POST", s.approvalsApprove)}, nil, true
	case len(seg) == 2 && seg[0] == "nodes":
		return route{methods: m("DELETE", s.nodeForget)}, map[string]string{"name": seg[1]}, true
	case len(seg) == 3 && seg[0] == "nodes" && seg[2] == "trust":
		return route{methods: m("PUT", s.nodeTrustSet)}, map[string]string{"name": seg[1]}, true
	case len(seg) == 3 && seg[0] == "nodes" && seg[2] == "approve":
		return route{methods: m("POST", s.nodeApprove)}, map[string]string{"name": seg[1]}, true
	case len(seg) == 1 && seg[0] == "settings":
		return route{methods: m("GET", s.settingsGet, "PUT", s.settingsPut)}, nil, true
	case len(seg) == 1 && seg[0] == "admin-log":
		return route{methods: m("GET", s.adminLogList), needsAuth: true}, nil, true
	case len(seg) == 1 && seg[0] == "groups":
		return route{methods: m("GET", s.groupsList, "POST", s.groupsCreate), needsAuth: true}, nil, true
	case len(seg) == 2 && seg[0] == "groups":
		return route{methods: m("PUT", s.groupsUpdate, "DELETE", s.groupsDelete), needsAuth: true}, map[string]string{"id": seg[1]}, true
	case len(seg) == 3 && seg[0] == "keys" && seg[2] == "groups":
		return route{methods: m("PUT", s.keyGroupsSet), needsAuth: true}, map[string]string{"id": seg[1]}, true
	case len(seg) == 3 && seg[0] == "keys" && seg[2] == "tools":
		return route{methods: m("GET", s.keyTools), needsAuth: true}, map[string]string{"id": seg[1]}, true
	case len(seg) == 1 && seg[0] == "overrides":
		return route{methods: m("GET", s.overridesList)}, nil, true
	case len(seg) >= 4 && seg[0] == "overrides":
		return route{methods: m("PUT", s.overridesSet, "DELETE", s.overridesDelete)},
			map[string]string{"node": seg[1], "mcp": seg[2], "tool": strings.Join(seg[3:], "/")}, true
	case len(seg) == 1 && seg[0] == "events":
		return route{methods: m("GET", s.events)}, nil, true
	case len(seg) == 1 && seg[0] == "keys":
		return route{methods: m("GET", s.keysList, "POST", s.keysCreate), needsAuth: true}, nil, true
	case len(seg) == 2 && seg[0] == "keys":
		return route{methods: m("DELETE", s.keysDelete), needsAuth: true}, map[string]string{"id": seg[1]}, true
	case len(seg) == 1 && seg[0] == "pairings":
		return route{methods: m("GET", s.pairingsList), needsAuth: true}, nil, true
	case len(seg) == 2 && seg[0] == "pairings":
		return route{methods: m("DELETE", s.pairingDeny), needsAuth: true}, map[string]string{"id": seg[1]}, true
	case len(seg) == 3 && seg[0] == "pairings" && seg[2] == "approve":
		return route{methods: m("POST", s.pairingApprove), needsAuth: true}, map[string]string{"id": seg[1]}, true
	case len(seg) == 1 && seg[0] == "admins":
		return route{methods: m("GET", s.adminsList, "POST", s.adminsCreate), needsAuth: true}, nil, true
	case len(seg) == 2 && seg[0] == "admins":
		return route{methods: m("DELETE", s.adminsDelete), needsAuth: true}, map[string]string{"username": seg[1]}, true
	case len(seg) == 3 && seg[0] == "admins" && seg[2] == "password":
		return route{methods: m("PUT", s.adminsPassword), needsAuth: true}, map[string]string{"username": seg[1]}, true
	}
	return route{}, nil, false
}

type ctxKey struct{}

type reqState struct {
	sess   auth.Session
	signed bool
	params map[string]string
}

func state(r *http.Request) *reqState {
	st, _ := r.Context().Value(ctxKey{}).(*reqState)
	if st == nil {
		st = &reqState{}
	}
	return st
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	path, ok := strings.CutPrefix(r.URL.Path, prefix)
	if !ok {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	rt, params, found := s.route(path)
	if !found {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	h, ok := rt.methods[method]
	if !ok {
		allow := make([]string, 0, len(rt.methods))
		for k := range rt.methods {
			allow = append(allow, k)
		}
		w.Header().Set("Allow", strings.Join(allow, ", "))
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	safe := method == http.MethodGet
	if !safe && !sameOrigin(r) {
		writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if rt.needsAuth && (s.opts.Sessions == nil || s.opts.Keys == nil || s.opts.Admins == nil) {
		writeError(w, http.StatusNotFound, "auth is off")
		return
	}
	st := &reqState{params: params}
	if s.opts.Sessions != nil {
		st.sess, st.signed = s.opts.Sessions.Current(r)
		if !st.signed && !rt.public {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		// Login has no session to bind a token to; the origin check above
		// covers it.
		isLogin := path == "/session" && method == http.MethodPost
		if st.signed && !safe && !isLogin && !s.opts.Sessions.CheckCSRF(r, st.sess) {
			writeError(w, http.StatusForbidden, "missing or wrong CSRF token")
			return
		}
	}
	h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, st)))
}

// sameOrigin accepts requests without an Origin header (non-browser clients)
// and those whose Origin host is this host.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(b, '\n'))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.log.Error("api: "+what, "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// decode reads a JSON request body into v, rejecting other content types,
// unknown fields, oversized bodies and trailing data. It writes the error
// response itself and reports whether v is usable.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "invalid JSON: trailing data")
		return false
	}
	return true
}
