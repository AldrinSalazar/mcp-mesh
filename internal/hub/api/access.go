package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/trust"
)

// Groups is the ACL: groups, their grants and key memberships.
type Groups interface {
	List(ctx context.Context) ([]acl.Group, error)
	Get(ctx context.Context, id string) (acl.Group, error)
	Create(ctx context.Context, in acl.GroupInput) (acl.Group, error)
	Update(ctx context.Context, id string, in acl.GroupInput) (acl.Group, error)
	Delete(ctx context.Context, id string) error
	Memberships(ctx context.Context) (map[string][]string, error)
	SetKeyGroups(ctx context.Context, keyID string, groupIDs []string) error
	Allows(keyID, node, mcp, tool string) bool
}

// Trust is node trust levels and tool approval.
type Trust interface {
	State(t *catalog.Tool) trust.State
	Policy(node string) trust.NodePolicy
	Default() trust.Level
	SetDefault(ctx context.Context, l trust.Level) error
	SetLevel(ctx context.Context, node string, l trust.Level) error
	ClearLevel(ctx context.Context, node string) error
	Pending() []trust.Pending
	ApproveNode(ctx context.Context, node, by string, tools []trust.Expected) error
	Approve(ctx context.Context, by string, tools []trust.Expected) (int, error)
}

// AdminLog records and lists admin actions.
type AdminLog interface {
	adminlog.Recorder
	Query(ctx context.Context, beforeID int64, limit int) ([]adminlog.Entry, error)
}

// record logs an admin action taken through this request.
func (s *Server) record(r *http.Request, action, target, detail string) {
	if s.opts.AdminLog == nil {
		return
	}
	s.opts.AdminLog.Record(r.Context(), adminlog.Entry{
		Actor: state(r).sess.Username, Action: action, Target: target, Detail: detail, IP: auth.ClientIP(r),
	})
}

// --- groups ---

// noGroups answers 404 when the hub has no ACL (tests).
func (s *Server) noGroups(w http.ResponseWriter) bool {
	if s.opts.Groups == nil {
		writeError(w, http.StatusNotFound, "groups are not available")
		return true
	}
	return false
}

func (s *Server) groupsList(w http.ResponseWriter, r *http.Request) {
	if s.noGroups(w) {
		return
	}
	g, err := s.opts.Groups.List(r.Context())
	if err != nil {
		s.internal(w, "list groups", err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// groupError answers a Groups error, reporting whether there was one.
func (s *Server) groupError(w http.ResponseWriter, what string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, acl.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, acl.ErrNameTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, acl.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.internal(w, what, err)
	}
	return true
}

func (s *Server) groupsCreate(w http.ResponseWriter, r *http.Request) {
	if s.noGroups(w) {
		return
	}
	var in acl.GroupInput
	if !decode(w, r, &in) {
		return
	}
	g, err := s.opts.Groups.Create(r.Context(), in)
	if s.groupError(w, "create group", err) {
		return
	}
	s.record(r, adminlog.GroupCreate, g.ID, g.Name)
	writeJSON(w, http.StatusCreated, g)
}

func (s *Server) groupsUpdate(w http.ResponseWriter, r *http.Request) {
	if s.noGroups(w) {
		return
	}
	var in acl.GroupInput
	if !decode(w, r, &in) {
		return
	}
	g, err := s.opts.Groups.Update(r.Context(), state(r).params["id"], in)
	if s.groupError(w, "update group", err) {
		return
	}
	s.record(r, adminlog.GroupUpdate, g.ID, g.Name)
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) groupsDelete(w http.ResponseWriter, r *http.Request) {
	if s.noGroups(w) {
		return
	}
	id := state(r).params["id"]
	g, err := s.opts.Groups.Get(r.Context(), id)
	if s.groupError(w, "delete group", err) {
		return
	}
	if s.groupError(w, "delete group", s.opts.Groups.Delete(r.Context(), id)) {
		return
	}
	s.record(r, adminlog.GroupDelete, id, g.Name)
	w.WriteHeader(http.StatusNoContent)
}

// clientKey returns the client key named by the URL, answering 404 or 400
// itself when there is none.
func (s *Server) clientKey(w http.ResponseWriter, r *http.Request) (auth.Key, bool) {
	k, ok := s.opts.Keys.Get(state(r).params["id"])
	if !ok {
		writeError(w, http.StatusNotFound, "no key with that ID")
		return auth.Key{}, false
	}
	if k.Kind != auth.KindClient {
		writeError(w, http.StatusBadRequest, "only client keys have groups and tools")
		return auth.Key{}, false
	}
	return k, true
}

func (s *Server) keyGroupsSet(w http.ResponseWriter, r *http.Request) {
	if s.noGroups(w) {
		return
	}
	var req struct {
		Groups []string `json:"groups"`
	}
	if !decode(w, r, &req) {
		return
	}
	k, ok := s.clientKey(w, r)
	if !ok {
		return
	}
	if req.Groups == nil {
		req.Groups = []string{}
	}
	if err := s.opts.Groups.SetKeyGroups(r.Context(), k.ID, req.Groups); err != nil {
		if errors.Is(err, acl.ErrNotFound) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.internal(w, "set key groups", err)
		return
	}
	s.record(r, adminlog.KeyGroups, k.ID, s.groupNames(r.Context(), req.Groups))
	v, err := s.keyView(r.Context(), k)
	if err != nil {
		s.internal(w, "key view", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": v})
}

// groupNames is "Everything, Read only" for the admin log.
func (s *Server) groupNames(ctx context.Context, ids []string) string {
	all, err := s.opts.Groups.List(ctx)
	if err != nil {
		return strings.Join(ids, ", ")
	}
	names := make(map[string]string, len(all))
	for _, g := range all {
		names[g.ID] = g.Name
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if n, ok := names[id]; ok {
			out = append(out, n)
		} else {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return "(none)"
	}
	return strings.Join(out, ", ")
}

func (s *Server) keyTools(w http.ResponseWriter, r *http.Request) {
	if s.noGroups(w) {
		return
	}
	k, ok := s.clientKey(w, r)
	if !ok {
		return
	}
	out := make([]toolView, 0)
	for _, t := range s.catalogTools() {
		if t.Upstream == nil || !s.opts.Groups.Allows(k.ID, t.Node, t.MCP, t.Upstream.Name) {
			continue
		}
		if s.opts.Trust != nil && s.opts.Trust.State(t) != trust.Approved {
			continue
		}
		out = append(out, s.toolView(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) catalogTools() []*catalog.Tool {
	if s.opts.Catalog == nil {
		return nil
	}
	return s.opts.Catalog.List()
}

// --- trust and approvals ---

// noTrust answers 404 when the hub runs without approvals (tests).
func (s *Server) noTrust(w http.ResponseWriter) bool {
	if s.opts.Trust == nil {
		writeError(w, http.StatusNotFound, "approvals are not available")
		return true
	}
	return false
}

func (s *Server) nodeTrustSet(w http.ResponseWriter, r *http.Request) {
	if s.noTrust(w) {
		return
	}
	var req struct {
		Level *string `json:"level"`
	}
	if !decode(w, r, &req) {
		return
	}
	node := state(r).params["name"]
	var err error
	detail := "default"
	if req.Level == nil {
		err = s.opts.Trust.ClearLevel(r.Context(), node)
	} else {
		l := trust.Level(*req.Level)
		if !l.Valid() {
			writeError(w, http.StatusBadRequest, `level must be "trust", "review", "approve" or null`)
			return
		}
		err = s.opts.Trust.SetLevel(r.Context(), node, l)
		detail = string(l)
	}
	if err != nil {
		s.internal(w, "set trust level", err)
		return
	}
	s.record(r, adminlog.TrustLevel, node, detail)
	writeJSON(w, http.StatusOK, newTrustView(s.opts.Trust.Policy(node)))
}

// staleResponse answers 409 with the tools whose definitions changed (and
// 400 for a malformed request).
func staleResponse(w http.ResponseWriter, err error) bool {
	if errors.Is(err, trust.ErrBadRequest) {
		writeError(w, http.StatusBadRequest, err.Error())
		return true
	}
	var se *trust.StaleError
	if !errors.As(err, &se) {
		return false
	}
	writeJSON(w, http.StatusConflict, map[string]any{
		"error": "a tool changed since it was reviewed; review it again",
		"stale": se.Tools,
	})
	return true
}

func (s *Server) nodeApprove(w http.ResponseWriter, r *http.Request) {
	if s.noTrust(w) {
		return
	}
	node := state(r).params["name"]
	var req struct {
		Tools []struct {
			MCP  string `json:"mcp"`
			Tool string `json:"tool"`
			Hash string `json:"hash"`
		} `json:"tools"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	tools := make([]trust.Expected, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.MCP == "" || t.Tool == "" || t.Hash == "" {
			writeError(w, http.StatusBadRequest, "each tool needs mcp, tool and hash")
			return
		}
		tools = append(tools, trust.Expected{Node: node, MCP: t.MCP, Tool: t.Tool, Hash: t.Hash})
	}
	if err := s.opts.Trust.ApproveNode(r.Context(), node, state(r).sess.Username, tools); err != nil {
		if staleResponse(w, err) {
			return
		}
		s.internal(w, "approve node", err)
		return
	}
	s.record(r, adminlog.NodeApprove, node, strconv.Itoa(len(tools)))
	writeJSON(w, http.StatusOK, newTrustView(s.opts.Trust.Policy(node)))
}

func (s *Server) approvalsList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.approvalViews())
}

func (s *Server) approvalViews() []trust.Pending {
	if s.opts.Trust == nil {
		return []trust.Pending{}
	}
	p := s.opts.Trust.Pending()
	if p == nil {
		p = []trust.Pending{}
	}
	return p
}

func (s *Server) approvalsApprove(w http.ResponseWriter, r *http.Request) {
	if s.noTrust(w) {
		return
	}
	var req struct {
		Tools []trust.Expected `json:"tools"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.Tools) == 0 {
		writeError(w, http.StatusBadRequest, "tools must list at least one tool")
		return
	}
	names := make([]string, 0, len(req.Tools))
	for _, t := range req.Tools {
		if t.Node == "" || t.MCP == "" || t.Tool == "" || t.Hash == "" {
			writeError(w, http.StatusBadRequest, "each tool needs node, mcp, tool and hash")
			return
		}
		names = append(names, t.Node+"/"+t.MCP+"/"+t.Tool)
	}
	n, err := s.opts.Trust.Approve(r.Context(), state(r).sess.Username, req.Tools)
	if err != nil {
		if staleResponse(w, err) {
			return
		}
		s.internal(w, "approve", err)
		return
	}
	target := names[0]
	if len(names) > 1 {
		target = req.Tools[0].Node
	}
	s.record(r, adminlog.ToolApprove, target, strconv.Itoa(n))
	writeJSON(w, http.StatusOK, map[string]int{"approved": n})
}

// --- settings ---

type settingsView struct {
	TrustDefault trust.Level `json:"trust_default"`
	OAuth        oauthView   `json:"oauth"`
	// Pairing: nodes without a key may ask to join (--pairing).
	Pairing bool `json:"pairing"`
}

type oauthView struct {
	Enabled  bool   `json:"enabled"`
	Issuer   string `json:"issuer,omitempty"`
	Resource string `json:"resource,omitempty"`
}

func (s *Server) settingsView() settingsView {
	v := settingsView{TrustDefault: trust.Trust, OAuth: oauthView{Enabled: s.opts.OAuthIssuer != ""}, Pairing: s.opts.Pairing != nil}
	if s.opts.Trust != nil {
		v.TrustDefault = s.opts.Trust.Default()
	}
	if v.OAuth.Enabled {
		v.OAuth.Issuer, v.OAuth.Resource = s.opts.OAuthIssuer, s.opts.ClientURL
	}
	return v
}

func (s *Server) settingsGet(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.settingsView())
}

func (s *Server) settingsPut(w http.ResponseWriter, r *http.Request) {
	if s.noTrust(w) {
		return
	}
	var req struct {
		TrustDefault string `json:"trust_default"`
	}
	if !decode(w, r, &req) {
		return
	}
	l := trust.Level(req.TrustDefault)
	if !l.Valid() {
		writeError(w, http.StatusBadRequest, `trust_default must be "trust", "review" or "approve"`)
		return
	}
	if err := s.opts.Trust.SetDefault(r.Context(), l); err != nil {
		s.internal(w, "set default trust", err)
		return
	}
	s.record(r, adminlog.TrustLevel, "", string(l))
	writeJSON(w, http.StatusOK, s.settingsView())
}

// --- admin log ---

func (s *Server) adminLogList(w http.ResponseWriter, r *http.Request) {
	if s.opts.AdminLog == nil {
		writeError(w, http.StatusNotFound, "the admin log is not available")
		return
	}
	v := r.URL.Query()
	var before int64
	limit := auditDefaultLimit
	if raw := v.Get("before_id"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "before_id must be a non-negative integer")
			return
		}
		before = n
	}
	if raw := v.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > auditMaxLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and 1000")
			return
		}
		limit = n
	}
	entries, err := s.opts.AdminLog.Query(r.Context(), before, limit)
	if err != nil {
		s.internal(w, "admin log", err)
		return
	}
	var next int64
	if len(entries) >= limit {
		next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries, "next_before_id": next})
}
