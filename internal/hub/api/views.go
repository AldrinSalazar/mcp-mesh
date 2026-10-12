package api

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/trust"
)

type mcpView struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Sharing   string `json:"sharing"`
	Start     string `json:"start"`
	State     string `json:"state"`
	Error     string `json:"error"`
	Tools     int    `json:"tools"`
	InFlight  int    `json:"in_flight"`
	Queued    int    `json:"queued"`
	Server    string `json:"server"`
	Prompts   int    `json:"prompts"`
	Resources int    `json:"resources"` // resources and templates
	// Clients is how many clients have their own process (per-client MCPs).
	Clients int `json:"clients"`
	// CallTimeoutMs is the MCP's own call timeout; 0 means the hub default.
	CallTimeoutMs int64 `json:"call_timeout_ms"`
	// Problems lists definitions the hub dropped from the MCP's listing, and why.
	Problems []string `json:"problems"`
}

type promptView struct {
	Name        string `json:"name"`
	Node        string `json:"node"`
	MCP         string `json:"mcp"`
	Original    string `json:"original_name"`
	Description string `json:"description"`
	Arguments   any    `json:"arguments"`
}

type resourceView struct {
	URI         string `json:"uri"` // exposed (mcp-mesh://node/mcp/...)
	Template    bool   `json:"template"`
	Node        string `json:"node"`
	MCP         string `json:"mcp"`
	Original    string `json:"original_uri"`
	Name        string `json:"name"`
	Description string `json:"description"`
	MIMEType    string `json:"mime_type,omitempty"`
}

type nodeView struct {
	Name           string     `json:"name"`
	NodeVersion    string     `json:"node_version"`
	OS             string     `json:"os"`
	RemoteAddr     string     `json:"remote_addr"`
	KeyID          string     `json:"key_id"`
	ConnectedAt    time.Time  `json:"connected_at"`
	Online         bool       `json:"online"`
	DisconnectedAt *time.Time `json:"disconnected_at,omitempty"`
	MCPs           []mcpView  `json:"mcps"`
	Trust          trustView  `json:"trust"`
	PendingTools   int        `json:"pending_tools"`
}

type trustView struct {
	Level    trust.Level `json:"level"`
	Explicit bool        `json:"explicit"`
	// Approved is whether the node's tools can be visible at all: always
	// for trust and review, after approval for approve.
	Approved bool `json:"approved"`
}

func newTrustView(p trust.NodePolicy) trustView {
	return trustView{Level: p.Level, Explicit: p.Explicit, Approved: p.Level != trust.Approve || p.Approved}
}

type toolView struct {
	Name         string `json:"name"`
	Node         string `json:"node"`
	MCP          string `json:"mcp"`
	OriginalName string `json:"original_name"`
	Description  string `json:"description"`
	InputSchema  any    `json:"input_schema"`
	Approval     string `json:"approval"`
	// Alias is whether Name is an admin-set alias; GeneratedName is the
	// <node>__<mcp>__<tool> name it would have otherwise. Description stays
	// the MCP's own; DescriptionOverride is what models see instead.
	Alias               bool   `json:"alias"`
	GeneratedName       string `json:"generated_name"`
	DescriptionOverride string `json:"description_override"`
}

type callView struct {
	Time       time.Time `json:"time"`
	ClientID   string    `json:"client_id"`
	Client     string    `json:"client"`
	Kind       string    `json:"kind"` // tool, prompt or resource
	Tool       string    `json:"tool"` // tool or prompt name, or resource URI
	Node       string    `json:"node"`
	MCP        string    `json:"mcp"`
	DurationMS int64     `json:"duration_ms"`
	Outcome    string    `json:"outcome"`
	Error      string    `json:"error"`
}

type entryView struct {
	ID int64 `json:"id"`
	callView
}

type keyView struct {
	ID       string     `json:"id"`
	Kind     string     `json:"kind"`
	Name     string     `json:"name"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"last_used,omitempty"`
	LastIP   string     `json:"last_ip,omitempty"`
	// Groups is set for client keys (possibly empty: sees nothing).
	Groups      []string        `json:"groups,omitzero"`
	OAuthClient *oauthClientRef `json:"oauth_client,omitempty"`
}

type oauthClientRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type statsView struct {
	CallsLastHour  int `json:"calls_last_hour"`
	ErrorsLastHour int `json:"errors_last_hour"`
}

type adminView struct {
	Username        string     `json:"username"`
	Created         time.Time  `json:"created"`
	PasswordChanged time.Time  `json:"password_changed"`
	LastLogin       *time.Time `json:"last_login,omitempty"`
}

func newCallView(c activity.Call) callView {
	return callView{c.Time, c.ClientID, c.Client, c.KindOrTool(), c.Tool, c.Node, c.MCP, c.Duration.Milliseconds(), c.Outcome, c.Error}
}

func newKeyView(k auth.Key) keyView {
	v := keyView{ID: k.ID, Kind: string(k.Kind), Name: k.Name, Created: k.Created, LastIP: k.LastIP}
	if !k.LastUsed.IsZero() {
		t := k.LastUsed
		v.LastUsed = &t
	}
	return v
}

func newAdminView(u auth.User) adminView {
	v := adminView{Username: u.Username, Created: u.Created, PasswordChanged: u.PasswordChanged}
	if !u.LastLogin.IsZero() {
		t := u.LastLogin
		v.LastLogin = &t
	}
	return v
}

func (s *Server) nodeViews() []nodeView {
	out := make([]nodeView, 0)
	if s.opts.Nodes == nil {
		return out
	}
	pending := map[string]int{}
	for _, p := range s.approvalViews() {
		pending[p.Node]++
	}
	listed := map[string]bool{}
	for _, n := range s.opts.Nodes.Nodes() {
		mcps := make([]mcpView, 0, len(n.MCPs))
		for _, m := range n.MCPs {
			mcps = append(mcps, mcpView{m.Name, string(m.Transport), string(m.Sharing), string(m.Start),
				string(m.State), m.Error, m.Tools, m.InFlight, m.Queued, m.Server,
				m.Prompts, m.Resources, m.Clients, m.CallTimeout.Milliseconds(),
				append([]string{}, m.Problems...)})
		}
		v := nodeView{Name: n.Name, NodeVersion: n.NodeVersion, OS: n.OS, RemoteAddr: n.RemoteAddr,
			KeyID: n.KeyID, ConnectedAt: n.ConnectedAt, Online: n.Online, MCPs: mcps,
			Trust: trustView{Level: trust.Trust, Approved: true}, PendingTools: pending[n.Name]}
		if s.opts.Trust != nil {
			v.Trust = newTrustView(s.opts.Trust.Policy(n.Name))
		}
		if !n.Online && !n.DisconnectedAt.IsZero() {
			at := n.DisconnectedAt
			v.DisconnectedAt = &at
		}
		out = append(out, v)
		listed[n.Name] = true
	}
	// Nodes the registry no longer holds are offline, as last seen.
	if s.opts.Known != nil {
		for _, k := range s.opts.Known.List() {
			if listed[k.Name] {
				continue
			}
			mcps := make([]mcpView, 0, len(k.MCPs))
			for _, m := range k.MCPs {
				mcps = append(mcps, mcpView{Name: m.Name, Transport: m.Transport, Sharing: m.Sharing, Start: m.Start,
					State: string(nodes.MCPClosed), Tools: m.Tools, Server: m.Server, Prompts: m.Prompts, Resources: m.Resources, Problems: []string{}})
			}
			at := k.LastSeen
			v := nodeView{Name: k.Name, NodeVersion: k.NodeVersion, OS: k.OS, RemoteAddr: k.RemoteAddr, KeyID: k.KeyID,
				DisconnectedAt: &at, MCPs: mcps, Trust: trustView{Level: trust.Trust, Approved: true}, PendingTools: pending[k.Name]}
			if s.opts.Trust != nil {
				v.Trust = newTrustView(s.opts.Trust.Policy(k.Name))
			}
			out = append(out, v)
		}
		slices.SortFunc(out, func(a, b nodeView) int { return strings.Compare(a.Name, b.Name) })
	}
	return out
}

func (s *Server) toolViews() []toolView {
	out := make([]toolView, 0)
	if s.opts.Catalog == nil {
		return out
	}
	for _, t := range s.opts.Catalog.List() {
		out = append(out, s.toolView(t))
	}
	return out
}

func (s *Server) toolView(t *catalog.Tool) toolView {
	v := toolView{Name: t.Name, Node: t.Node, MCP: t.MCP, Approval: string(trust.Approved)}
	if t.Upstream != nil {
		v.OriginalName = t.Upstream.Name
		v.Description = t.Upstream.Description
		v.InputSchema = t.Upstream.InputSchema
		v.GeneratedName = catalog.ExposedName(t.Node, t.MCP, t.Upstream.Name)
	}
	v.DescriptionOverride = t.Description
	v.Alias = t.Aliased
	if s.opts.Trust != nil {
		v.Approval = string(s.opts.Trust.State(t))
	}
	return v
}

func (s *Server) callViews() []callView {
	out := make([]callView, 0)
	if s.opts.Recent != nil {
		for _, c := range s.opts.Recent.Recent() {
			out = append(out, newCallView(c))
		}
	}
	return out
}

func (s *Server) keyViews(ctx context.Context) ([]keyView, error) {
	keys, err := s.opts.Keys.List()
	if err != nil {
		return nil, err
	}
	var members map[string][]string
	if s.opts.Groups != nil {
		if members, err = s.opts.Groups.Memberships(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]keyView, 0, len(keys))
	for _, k := range keys {
		out = append(out, s.decorate(newKeyView(k), k, members))
	}
	return out, nil
}

// keyView is one key with its groups and OAuth client.
func (s *Server) keyView(ctx context.Context, k auth.Key) (keyView, error) {
	var members map[string][]string
	if s.opts.Groups != nil {
		var err error
		if members, err = s.opts.Groups.Memberships(ctx); err != nil {
			return keyView{}, err
		}
	}
	return s.decorate(newKeyView(k), k, members), nil
}

func (s *Server) decorate(v keyView, k auth.Key, members map[string][]string) keyView {
	if k.Kind == auth.KindClient && s.opts.Groups != nil {
		v.Groups = members[k.ID]
		if v.Groups == nil {
			v.Groups = []string{}
		}
	}
	if k.OAuthClient != "" {
		name := k.OAuthClient
		if s.opts.OAuthName != nil {
			if n := s.opts.OAuthName(k.OAuthClient); n != "" {
				name = n
			}
		}
		v.OAuthClient = &oauthClientRef{ID: k.OAuthClient, Name: name}
	}
	return v
}

func (s *Server) adminViews(ctx context.Context) ([]adminView, error) {
	users, err := s.opts.Admins.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]adminView, 0, len(users))
	for _, u := range users {
		out = append(out, newAdminView(u))
	}
	return out, nil
}

// statsWindow is the period the dashboard's call counts cover.
const statsWindow = time.Hour

// statsView counts recent calls from the audit log, or from the in-memory
// ring when there is none (which then caps the count at the ring's size).
func (s *Server) statsView(ctx context.Context) (statsView, error) {
	since := time.Now().Add(-statsWindow)
	if s.opts.Audit != nil {
		c, err := s.opts.Audit.Count(ctx, since)
		if err != nil {
			return statsView{}, err
		}
		return statsView{c.Calls, c.Errors}, nil
	}
	var v statsView
	if s.opts.Recent != nil {
		for _, c := range s.opts.Recent.Recent() {
			if c.Time.Before(since) {
				break // newest first
			}
			v.CallsLastHour++
			if c.Outcome != "ok" {
				v.ErrorsLastHour++
			}
		}
	}
	return v, nil
}

func (s *Server) promptViews() []promptView {
	out := make([]promptView, 0)
	if s.opts.Catalog == nil {
		return out
	}
	for _, p := range s.opts.Catalog.Prompts() {
		args := any([]any{})
		if p.Upstream.Arguments != nil {
			args = p.Upstream.Arguments
		}
		out = append(out, promptView{p.Name, p.Node, p.MCP, p.Upstream.Name, p.Upstream.Description, args})
	}
	return out
}

func (s *Server) resourceViews() []resourceView {
	out := make([]resourceView, 0)
	if s.opts.Catalog == nil {
		return out
	}
	for _, r := range s.opts.Catalog.Resources() {
		out = append(out, resourceView{r.URI, false, r.Node, r.MCP, r.Upstream.URI, r.Upstream.Name, r.Upstream.Description, r.Upstream.MIMEType})
	}
	for _, t := range s.opts.Catalog.ResourceTemplates() {
		out = append(out, resourceView{t.URITemplate, true, t.Node, t.MCP, t.Upstream.URITemplate, t.Upstream.Name, t.Upstream.Description, t.Upstream.MIMEType})
	}
	return out
}
