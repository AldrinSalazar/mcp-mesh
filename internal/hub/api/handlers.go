package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/trust"
)

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"version":        s.opts.Version,
		"auth":           s.opts.Sessions != nil,
		"node_url":       s.opts.NodeURL,
		"client_url":     s.opts.ClientURL,
		"discovery_name": s.opts.DiscoveryName,
		"oauth":          s.opts.OAuthIssuer != "",
	})
}

// --- session ---

type sessionView struct {
	Username string    `json:"username"`
	CSRF     string    `json:"csrf"`
	Expires  time.Time `json:"expires"`
}

func (s *Server) sessionGet(w http.ResponseWriter, r *http.Request) {
	if s.opts.Sessions == nil {
		writeJSON(w, http.StatusOK, map[string]bool{"auth": false})
		return
	}
	st := state(r)
	if !st.signed {
		writeError(w, http.StatusUnauthorized, "sign in required")
		return
	}
	writeJSON(w, http.StatusOK, sessionView{st.sess.Username, st.sess.CSRF, st.sess.Expires})
}

func (s *Server) sessionLogin(w http.ResponseWriter, r *http.Request) {
	if s.opts.Sessions == nil {
		writeError(w, http.StatusNotFound, "auth is off")
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	sess, err := s.opts.Sessions.Login(w, r, req.Username, req.Password)
	var rl *auth.RateLimitError
	if s.opts.AdminLog != nil && err != nil && !errors.As(err, &rl) {
		s.opts.AdminLog.Record(r.Context(), adminlog.Entry{Action: adminlog.SignInFailed, Target: req.Username, IP: auth.ClientIP(r)})
	}
	switch {
	case errors.Is(err, auth.ErrBadCredentials):
		writeError(w, http.StatusUnauthorized, auth.ErrBadCredentials.Error())
	case errors.As(err, &rl):
		secs := int((rl.RetryAfter + time.Second - 1) / time.Second)
		w.Header().Set("Retry-After", strconv.Itoa(max(secs, 1)))
		writeError(w, http.StatusTooManyRequests, rl.Error())
	case err != nil:
		s.internal(w, "login", err)
	default:
		if s.opts.AdminLog != nil {
			s.opts.AdminLog.Record(r.Context(), adminlog.Entry{Actor: sess.Username, Action: adminlog.SignIn, Target: sess.Username, IP: auth.ClientIP(r)})
		}
		writeJSON(w, http.StatusOK, sessionView{sess.Username, sess.CSRF, sess.Expires})
	}
}

func (s *Server) sessionLogout(w http.ResponseWriter, r *http.Request) {
	if s.opts.Sessions != nil {
		if st := state(r); st.signed {
			s.record(r, adminlog.SignOut, st.sess.Username, "")
		}
		s.opts.Sessions.Logout(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- read-only views ---

func (s *Server) nodesList(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.nodeViews()) }
func (s *Server) toolsList(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.toolViews()) }
func (s *Server) callsList(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, s.callViews()) }

func (s *Server) statsGet(w http.ResponseWriter, r *http.Request) {
	v, err := s.statsView(r.Context())
	if err != nil {
		s.internal(w, "stats", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

const (
	auditDefaultLimit = 100
	auditMaxLimit     = 1000
)

func (s *Server) auditList(w http.ResponseWriter, r *http.Request) {
	if s.opts.Audit == nil {
		writeError(w, http.StatusNotFound, "audit log is not available")
		return
	}
	v := r.URL.Query()
	q := activity.AuditQuery{
		ClientID: v.Get("client_id"), Node: v.Get("node"), MCP: v.Get("mcp"),
		Tool: v.Get("tool"), Outcome: v.Get("outcome"),
	}
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{{"since", &q.Since}, {"until", &q.Until}} {
		if raw := v.Get(p.name); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be an RFC 3339 time", p.name))
				return
			}
			*p.dst = t
		}
	}
	if raw := v.Get("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "before_id must be a non-negative integer")
			return
		}
		q.BeforeID = n
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		q.Limit = n
	}
	limit := auditDefaultLimit
	if q.Limit > 0 {
		limit = min(q.Limit, auditMaxLimit)
	}
	entries, err := s.opts.Audit.Query(r.Context(), q)
	if err != nil {
		s.internal(w, "audit query", err)
		return
	}
	out := make([]entryView, 0, len(entries))
	for _, e := range entries {
		out = append(out, entryView{e.ID, newCallView(e.Call)})
	}
	var next int64
	if len(entries) >= limit {
		next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out, "next_before_id": next})
}

// --- keys ---

func (s *Server) keysList(w http.ResponseWriter, r *http.Request) {
	v, err := s.keyViews(r.Context())
	if err != nil {
		s.internal(w, "list keys", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) keysCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind   string    `json:"kind"`
		Name   string    `json:"name"`
		Groups *[]string `json:"groups"`
		Trust  string    `json:"trust"`
	}
	if !decode(w, r, &req) {
		return
	}
	kind := auth.Kind(req.Kind)
	if kind != auth.KindNode && kind != auth.KindClient {
		writeError(w, http.StatusBadRequest, `kind must be "node" or "client"`)
		return
	}
	if kind == auth.KindNode && req.Groups != nil {
		writeError(w, http.StatusBadRequest, "node keys have no groups")
		return
	}
	if kind == auth.KindClient && req.Trust != "" {
		writeError(w, http.StatusBadRequest, "client keys have no trust level")
		return
	}
	level := trust.Level(req.Trust)
	if req.Trust != "" && (!level.Valid() || s.opts.Trust == nil) {
		writeError(w, http.StatusBadRequest, `trust must be "trust", "review" or "approve"`)
		return
	}
	groups, ok := s.newKeyGroups(w, r, kind, req.Groups)
	if !ok {
		return
	}
	secret, key, err := s.opts.Keys.Create(kind, req.Name)
	if err != nil {
		// The store's errors are validation messages (name rules); a
		// database failure would be misreported as 400 but is also logged.
		s.log.Warn("api: create key", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if kind == auth.KindClient && s.opts.Groups != nil {
		if err := s.opts.Groups.SetKeyGroups(r.Context(), key.ID, groups); err != nil {
			// The groups were checked above; a failure here is the database.
			s.internal(w, "set new key's groups", err)
			return
		}
	}
	if req.Trust != "" {
		if err := s.opts.Trust.SetLevel(r.Context(), key.Name, level); err != nil {
			s.internal(w, "set node trust", err)
			return
		}
	}
	s.record(r, adminlog.KeyCreate, key.ID, string(kind)+" "+key.Name)
	var setup map[string]string
	if kind == auth.KindNode {
		setup = map[string]string{
			"hub_url":      s.opts.NodeURL,
			"init_command": NodeInitCommand(s.opts.NodeURL, key.Name, secret),
			"command":      "mcp-mesh-node key set " + secret,
		}
	} else {
		setup = map[string]string{
			"mcp_url":     s.opts.ClientURL,
			"header":      "Authorization: Bearer " + secret,
			"claude_code": fmt.Sprintf("claude mcp add --transport http mcp-mesh %s --header \"Authorization: Bearer %s\"", s.opts.ClientURL, secret),
		}
	}
	v, err := s.keyView(r.Context(), key)
	if err != nil {
		s.internal(w, "key view", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"key": v, "secret": secret, "setup": setup})
}

// newKeyGroups resolves the groups a new client key joins: the requested
// ones (all must exist), or by default the Everything group if it exists.
func (s *Server) newKeyGroups(w http.ResponseWriter, r *http.Request, kind auth.Kind, req *[]string) ([]string, bool) {
	if kind != auth.KindClient || s.opts.Groups == nil {
		return nil, true
	}
	all, err := s.opts.Groups.List(r.Context())
	if err != nil {
		s.internal(w, "list groups", err)
		return nil, false
	}
	exists := make(map[string]bool, len(all))
	for _, g := range all {
		exists[g.ID] = true
	}
	if req == nil {
		if exists[acl.Everything] {
			return []string{acl.Everything}, true
		}
		return []string{}, true
	}
	for _, id := range *req {
		if !exists[id] {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("no group %q", id))
			return nil, false
		}
	}
	return *req, true
}

func (s *Server) keysDelete(w http.ResponseWriter, r *http.Request) {
	id := state(r).params["id"]
	// Exact ID only: Revoke also accepts names, which must not be reachable
	// from a URL that looks like an ID.
	if _, ok := s.opts.Keys.Get(id); !ok {
		writeError(w, http.StatusNotFound, "no key with that ID")
		return
	}
	key, err := s.opts.Keys.Revoke(id)
	if err != nil {
		s.internal(w, "revoke key", err)
		return
	}
	s.record(r, adminlog.KeyRevoke, key.ID, string(key.Kind)+" "+key.Name)
	writeJSON(w, http.StatusOK, map[string]any{"key": newKeyView(key)})
}

// --- admins ---

func (s *Server) adminsList(w http.ResponseWriter, r *http.Request) {
	v, err := s.adminViews(r.Context())
	if err != nil {
		s.internal(w, "list admins", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) adminsCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	u, err := s.opts.Admins.Create(r.Context(), req.Username, req.Password)
	switch {
	case errors.Is(err, auth.ErrUserExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		// Other errors from Create are username validation messages.
		s.log.Warn("api: create admin", "error", err)
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.record(r, adminlog.AdminCreate, u.Username, "")
		writeJSON(w, http.StatusCreated, newAdminView(u))
	}
}

func (s *Server) adminsPassword(w http.ResponseWriter, r *http.Request) {
	username := state(r).params["username"]
	var req struct {
		Password        string `json:"password"`
		CurrentPassword string `json:"current_password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if st := state(r); strings.EqualFold(st.sess.Username, username) {
		// Someone at an unlocked browser shouldn't be able to take over the
		// account, so changing your own password proves the current one.
		_, err := s.opts.Admins.Verify(r.Context(), username, req.CurrentPassword)
		var rl *auth.RateLimitError
		switch {
		case errors.Is(err, auth.ErrBadCredentials):
			writeError(w, http.StatusForbidden, "current password is wrong")
			return
		case errors.As(err, &rl):
			w.Header().Set("Retry-After", strconv.Itoa(max(int((rl.RetryAfter+time.Second-1)/time.Second), 1)))
			writeError(w, http.StatusTooManyRequests, rl.Error())
			return
		case err != nil:
			s.internal(w, "verify password", err)
			return
		}
	}
	err := s.opts.Admins.SetPassword(r.Context(), username, req.Password)
	switch {
	case errors.Is(err, auth.ErrNoUser):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, err.Error())
	case err != nil:
		s.internal(w, "set password", err)
	default:
		s.record(r, adminlog.AdminPassword, username, "")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) adminsDelete(w http.ResponseWriter, r *http.Request) {
	username := state(r).params["username"]
	err := s.opts.Admins.Delete(r.Context(), username)
	switch {
	case errors.Is(err, auth.ErrNoUser):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, auth.ErrLastAdmin):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		s.internal(w, "delete admin", err)
	default:
		s.record(r, adminlog.AdminDelete, username, "")
		w.WriteHeader(http.StatusNoContent)
	}
}

// NodeInitCommand sets up a new machine as the node a key was made for: a
// config pointing at the hub, the node's name (the key is bound to it) and
// the key. The hub's CLI prints the same command.
func NodeInitCommand(nodeURL, name, secret string) string {
	return fmt.Sprintf("mcp-mesh-node init --hub %s --name %s --key %s", shellWord(nodeURL), shellWord(name), secret)
}

// shellWord quotes s for a POSIX shell when it needs it.
func shellWord(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.:/@%+=,") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
