package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/pairing"
)

// Pairing holds the nodes asking to join without a key.
type Pairing interface {
	Pending() []pairing.Request
	Get(id string) (pairing.Request, bool)
	Deliver(id string, g pairing.Grant) error
	Deny(id string) (pairing.Request, bool)
}

// pairingView is a waiting request as the dashboard shows it.
type pairingView struct {
	pairing.Request
	// NameTaken: a node key is already bound to the name the node asked
	// for. Approving under that name replaces it (and disconnects its node).
	NameTaken bool `json:"name_taken"`
}

// noPairing answers 404 when the hub doesn't accept pairing requests.
func (s *Server) noPairing(w http.ResponseWriter) bool {
	if s.opts.Pairing == nil {
		writeError(w, http.StatusNotFound, "pairing is turned off on this hub")
		return true
	}
	return false
}

func (s *Server) pairingViews() ([]pairingView, error) {
	out := []pairingView{}
	if s.opts.Pairing == nil {
		return out, nil
	}
	pending := s.opts.Pairing.Pending()
	if len(pending) == 0 {
		return out, nil
	}
	keys, err := s.opts.Keys.List()
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for _, k := range keys {
		if k.Kind == auth.KindNode {
			taken[k.Name] = true
		}
	}
	for _, p := range pending {
		out = append(out, pairingView{Request: p, NameTaken: taken[p.Node]})
	}
	return out, nil
}

func (s *Server) pairingsList(w http.ResponseWriter, r *http.Request) {
	v, err := s.pairingViews()
	if err != nil {
		s.internal(w, "list pairing requests", err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

// nodeKeysNamed are the node keys bound to name.
func (s *Server) nodeKeysNamed(name string) ([]auth.Key, error) {
	keys, err := s.opts.Keys.List()
	if err != nil {
		return nil, err
	}
	var out []auth.Key
	for _, k := range keys {
		if k.Kind == auth.KindNode && k.Name == name {
			out = append(out, k)
		}
	}
	return out, nil
}

// pairingApprove creates a node key for the request and hands it to the
// waiting node. The admin may give the node another name; a name another
// node key is bound to needs replace, which revokes that key first.
func (s *Server) pairingApprove(w http.ResponseWriter, r *http.Request) {
	if s.noPairing(w) {
		return
	}
	id := state(r).params["id"]
	var req struct {
		Name    string `json:"name"`
		Replace bool   `json:"replace"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	p, ok := s.opts.Pairing.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, pairing.ErrNotPending.Error())
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = p.Node
	}
	if err := auth.ValidName(name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	existing, err := s.nodeKeysNamed(name)
	if err != nil {
		s.internal(w, "list keys", err)
		return
	}
	if len(existing) > 0 && !req.Replace {
		writeError(w, http.StatusConflict, fmt.Sprintf("a node key for %q already exists; replace it (its node disconnects) or choose another name", name))
		return
	}
	// The old key goes first: the paired node connects as soon as it has
	// its key, and the hub refuses a second node under a name that is online.
	for _, k := range existing {
		if _, err := s.opts.Keys.Revoke(k.ID); err != nil {
			s.internal(w, "revoke replaced key", err)
			return
		}
		if s.opts.DisconnectKey != nil {
			s.opts.DisconnectKey(k.ID)
		}
		s.record(r, adminlog.KeyRevoke, k.ID, string(k.Kind)+" "+k.Name)
	}
	secret, key, err := s.opts.Keys.Create(auth.KindNode, name)
	if err != nil {
		s.internal(w, "create key", err)
		return
	}
	if err := s.opts.Pairing.Deliver(id, pairing.Grant{Node: name, Key: secret}); err != nil {
		// The node never got the key: nobody holds it.
		if _, rerr := s.opts.Keys.Revoke(key.ID); rerr != nil {
			s.log.Error("api: revoke undelivered pairing key", "key", key.ID, "error", rerr)
		}
		switch {
		case errors.Is(err, pairing.ErrNotPending):
			writeError(w, http.StatusNotFound, err.Error())
		case errors.Is(err, pairing.ErrNotDelivered):
			writeError(w, http.StatusGone, err.Error()+"; it asks again when it reconnects")
		default:
			s.internal(w, "deliver pairing key", err)
		}
		return
	}
	s.record(r, adminlog.PairingApprove, key.ID, fmt.Sprintf("node %s from %s", name, p.IP))
	v, err := s.keyView(r.Context(), key)
	if err != nil {
		s.internal(w, "key view", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": v})
}

func (s *Server) pairingDeny(w http.ResponseWriter, r *http.Request) {
	if s.noPairing(w) {
		return
	}
	p, ok := s.opts.Pairing.Deny(state(r).params["id"])
	if !ok {
		writeError(w, http.StatusNotFound, pairing.ErrNotPending.Error())
		return
	}
	s.record(r, adminlog.PairingDeny, p.Node, p.IP)
	w.WriteHeader(http.StatusNoContent)
}
