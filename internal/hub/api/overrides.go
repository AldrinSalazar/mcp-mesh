package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/overrides"
)

// Overrides stores tool aliases and description overrides.
type Overrides interface {
	List(ctx context.Context) ([]overrides.Entry, error)
	Set(ctx context.Context, key catalog.ToolKey, alias, description, by string) (overrides.Entry, error)
	Delete(ctx context.Context, key catalog.ToolKey) error
}

func (s *Server) noOverrides(w http.ResponseWriter) bool {
	if s.opts.Overrides == nil {
		writeError(w, http.StatusNotFound, "tool overrides are not available")
		return true
	}
	return false
}

func (s *Server) overridesList(w http.ResponseWriter, r *http.Request) {
	if s.noOverrides(w) {
		return
	}
	l, err := s.opts.Overrides.List(r.Context())
	if err != nil {
		s.internal(w, "list overrides", err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func overrideKey(r *http.Request) catalog.ToolKey {
	p := state(r).params
	return catalog.ToolKey{Node: p["node"], MCP: p["mcp"], Tool: p["tool"]}
}

func (s *Server) overrideError(w http.ResponseWriter, what string, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, overrides.ErrAliasTaken):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, overrides.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		s.internal(w, what, err)
	}
	return true
}

func (s *Server) overridesSet(w http.ResponseWriter, r *http.Request) {
	if s.noOverrides(w) {
		return
	}
	var in struct {
		Alias       string `json:"alias"`
		Description string `json:"description"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := overrideKey(r)
	e, err := s.opts.Overrides.Set(r.Context(), key, in.Alias, in.Description, state(r).sess.Username)
	if s.overrideError(w, "set override", err) {
		return
	}
	s.record(r, adminlog.ToolOverride, key.Node+"/"+key.MCP+"/"+key.Tool, in.Alias)
	if in.Alias == "" && in.Description == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) overridesDelete(w http.ResponseWriter, r *http.Request) {
	if s.noOverrides(w) {
		return
	}
	key := overrideKey(r)
	if s.overrideError(w, "delete override", s.opts.Overrides.Delete(r.Context(), key)) {
		return
	}
	s.record(r, adminlog.ToolOverride, key.Node+"/"+key.MCP+"/"+key.Tool, "")
	w.WriteHeader(http.StatusNoContent)
}
