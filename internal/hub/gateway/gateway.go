// Package gateway exposes the merged catalog as a Streamable HTTP MCP server.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/progress"
	"github.com/AldrinSalazar/mcp-mesh/internal/logx"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	Catalog     *catalog.Catalog
	Activity    activity.Recorder // may be nil
	Logger      *slog.Logger
	CallTimeout time.Duration // Zero defaults to 120 seconds.
	Version     string
	// Identify maps a request to its client. Auth middleware (for example the
	// SDK's bearer middleware, which stores auth.TokenInfo in the request
	// context) runs before the handler, so Identify can read it from
	// r.Context() or from headers. Nil means every request is
	// identity.Anonymous.
	Identify func(*http.Request) identity.Client
	// NewClient, if set, is called before the gateway builds a client's
	// server, once per client while the gateway keeps that server: the hub
	// reloads group memberships there, so a key made by another process a
	// moment ago sees its tools on its first request.
	NewClient func(context.Context, identity.Client)
	// Visible reports whether a client may see and call a tool. The ACL plugs
	// in here. Nil means every tool is visible to every client.
	Visible func(identity.Client, *catalog.Tool) bool
	// VisibleMCP reports whether a client may see an MCP's prompts,
	// resources and resource templates; those are granted per MCP, not per
	// tool. Nil means all are visible.
	VisibleMCP func(c identity.Client, node, mcp string) bool
	// Progress routes upstream progress notifications back to the client
	// that asked for them. Nil means progress is not forwarded.
	Progress *progress.Router
	// SessionTimeout and MaxSessionsPerClient limit session-based (MCP
	// 2025-11-25) sessions, MaxRequestsPerClient the stateless (2026-07-28)
	// requests and streams one key has open at once; zero means the package
	// defaults.
	SessionTimeout       time.Duration
	MaxSessionsPerClient int
	MaxRequestsPerClient int
	// MaxRequestBytes bounds one request body from a client: the same limit
	// as one MCP message between hub and node (the SDK's own default is
	// 4 MiB). Zero means proto.DefaultMaxMessageSize.
	MaxRequestBytes int64
}

type Gateway struct {
	opts   Options
	sdkLog *slog.Logger
	// mirrors holds one view per client ID, created on first sight and kept
	// until DisconnectClient. Their number is bounded by the number of client
	// identities (one per client key), so there is no other eviction.
	mu      sync.Mutex
	mirrors map[string]*serverMirror
	// revoked holds the IDs DisconnectClient was given. A request
	// authenticated just before the key was revoked can reach mirrorFor
	// after DisconnectClient dropped the mirror; it gets an empty, cancelled
	// server instead of a fresh view. Key IDs are never reused.
	revoked map[string]bool
	// stopped is set when Run ends: every mirror was closed, and requests
	// still arriving get an empty, cancelled server.
	stopped bool
	// opening counts each client's session-creating requests in progress,
	// so concurrent ones can't all pass the session cap; active counts its
	// stateless requests and streams. Both under mu, by client ID.
	opening, active map[string]int
	// refused counts new sessions turned away by MaxSessionsPerClient,
	// refusedRequests stateless requests by MaxRequestsPerClient.
	refused, refusedRequests atomic.Int64
	handler                  http.Handler
	// refresh asks Run for a resync when visibility changed without the
	// catalog changing (ACL grants, approvals).
	refresh chan struct{}
}

// serverMirror is one client's view of the catalog: its server plus the sync
// state of the tools currently registered on it.
type serverMirror struct {
	client identity.Client
	// ctx ends when the client is disconnected; every call it makes derives
	// from it so a revoked key's in-flight work stops.
	ctx         context.Context
	cancel      context.CancelCauseFunc // the cause is what cancelled calls answer
	mu          sync.Mutex
	server      *mcp.Server
	definitions map[string]string
	// Serialized definitions of the prompts (by exposed name), resources (by
	// exposed URI) and templates (by exposed template) on the server.
	prompts, resources, templates map[string]string
	// inflight counts tool calls inside forward, so a disconnect can let
	// them answer "key revoked" before their sessions close.
	inflight atomic.Int64
}

func New(opts Options) *Gateway {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.CallTimeout == 0 {
		opts.CallTimeout = 120 * time.Second
	}
	if opts.Identify == nil {
		opts.Identify = func(*http.Request) identity.Client { return identity.Anonymous }
	}
	if opts.VisibleMCP == nil {
		opts.VisibleMCP = func(identity.Client, string, string) bool { return true }
	}
	if opts.SessionTimeout <= 0 {
		opts.SessionTimeout = SessionTimeout
	}
	if opts.MaxSessionsPerClient <= 0 {
		opts.MaxSessionsPerClient = MaxSessionsPerClient
	}
	if opts.MaxRequestsPerClient <= 0 {
		opts.MaxRequestsPerClient = MaxRequestsPerClient
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = proto.DefaultMaxMessageSize
	}
	if opts.Visible == nil {
		opts.Visible = func(identity.Client, *catalog.Tool) bool { return true }
	}
	g := &Gateway{
		opts:    opts,
		sdkLog:  logx.AtLeast(opts.Logger.With("component", "mcp-sdk"), slog.LevelWarn),
		mirrors: make(map[string]*serverMirror),
		revoked: make(map[string]bool),
		opening: make(map[string]int),
		active:  make(map[string]int),
		refresh: make(chan struct{}, 1),
	}
	getServer := func(r *http.Request) *mcp.Server {
		c := opts.Identify(r)
		if opts.NewClient != nil && !g.hasMirror(c.ID) {
			opts.NewClient(r.Context(), c)
		}
		return g.mirrorFor(c).server
	}
	sessionBased := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Logger: g.sdkLog,
		// A client that goes away without DELETE would otherwise leave its
		// session (and the server-side state behind it) forever. Only POSTs
		// count as activity; a client told 404 starts a new session.
		SessionTimeout:      opts.SessionTimeout,
		MaxRequestBodyBytes: opts.MaxRequestBytes,
	})
	// A session-based client's Mcp-Session-Id must not work with another
	// client's key: the session leads to its owner's server (grants,
	// per-client processes, audit identity). The SDK refuses a session to a
	// request whose auth.TokenInfo.UserID differs from the one it was created
	// with, and only RequireBearerToken can put TokenInfo in the context; the
	// key or access token was already verified, so the verifier just names
	// the client.
	ownedSessions := auth.RequireBearerToken(func(_ context.Context, _ string, r *http.Request) (*auth.TokenInfo, error) {
		return &auth.TokenInfo{UserID: opts.Identify(r).ID}, nil
	}, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(sessionBased)
	current := mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		PropagateRequestCancellation: true,
		Logger:                       g.sdkLog,
		MaxRequestBodyBytes:          opts.MaxRequestBytes,
	})
	g.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The SDK requires stateless HTTP for 2026-07-28 clients, while
		// session-based clients (MCP 2025-11-25) need stateful HTTP and their
		// standalone notification stream.
		switch {
		case r.Header.Get("MCP-Protocol-Version") >= "2026-07-28":
			// Each request is its own temporary session, and a
			// subscriptions/listen stream lasts until the client ends it.
			release, ok := g.reserve(g.active, opts.Identify(r).ID, opts.MaxRequestsPerClient, nil)
			if !ok {
				g.refusedRequests.Add(1)
				http.Error(w, fmt.Sprintf("too many requests and streams open for this key (%d): end some or try again later", opts.MaxRequestsPerClient), http.StatusTooManyRequests)
				return
			}
			defer release()
			current.ServeHTTP(w, r)
		case hasBearer(r):
			if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
				// The request may create a session: it holds a place until it
				// has (the session then counts itself) or hasn't.
				id := opts.Identify(r).ID
				release, ok := g.reserve(g.opening, id, opts.MaxSessionsPerClient, func() int { return g.sessionCountLocked(id) })
				if !ok {
					g.refused.Add(1)
					http.Error(w, fmt.Sprintf("too many open sessions for this key (%d): end unused ones (DELETE) or wait for them to expire", opts.MaxSessionsPerClient), http.StatusTooManyRequests)
					return
				}
				defer release()
			}
			ownedSessions.ServeHTTP(w, r)
		default:
			// Only with auth off (tests, --auth=false): every client is
			// anonymous, so there is no owner to check.
			sessionBased.ServeHTTP(w, r)
		}
	})
	return g
}

// Session-based (MCP 2025-11-25) session limits, the defaults of
// Options.SessionTimeout and Options.MaxSessionsPerClient.
const (
	// SessionTimeout closes a session that has had no request for this long.
	SessionTimeout = 30 * time.Minute
	// MaxSessionsPerClient bounds the sessions one key keeps open; a new one
	// past it is refused (429) until others end or expire.
	MaxSessionsPerClient = 64
	// MaxRequestsPerClient bounds the stateless (2026-07-28) requests and
	// streams one key has open at once; past it they are refused (429).
	MaxRequestsPerClient = 128
)

// reserve takes one of the client's places in counts if, with what extra
// reports (nil: nothing), fewer than limit are taken. release gives it back.
func (g *Gateway) reserve(counts map[string]int, id string, limit int, extra func() int) (release func(), ok bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := counts[id]
	if extra != nil {
		n += extra()
	}
	if n >= limit {
		return nil, false
	}
	counts[id]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if counts[id]--; counts[id] <= 0 {
			delete(counts, id)
		}
	}, true
}

// sessionCount is sessionCountLocked for a caller without g.mu.
func (g *Gateway) sessionCount(c identity.Client) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.sessionCountLocked(c.ID)
}

// sessionCountLocked is how many addressable sessions the client has open;
// the temporary sessions of stateless (2026-07-28) requests have no ID and
// don't count. Neither do sessions not initialized yet: the SDK adds one as
// its initialize request starts (closing it if that fails), and that
// request already holds a place (opening). Caller holds g.mu.
func (g *Gateway) sessionCountLocked(id string) int {
	m := g.mirrors[id]
	if m == nil {
		return 0
	}
	n := 0
	for ss := range m.server.Sessions() {
		if ss.ID() != "" && ss.InitializeParams() != nil {
			n++
		}
	}
	return n
}

// Stats is a point-in-time summary for health and metrics.
type Stats struct {
	Clients         int   `json:"clients"`          // client views (one per key seen)
	Sessions        int   `json:"sessions"`         // open session-based sessions
	SessionsRefused int64 `json:"sessions_refused"` // since start, over MaxSessionsPerClient
	Requests        int   `json:"requests"`         // stateless requests and streams open
	RequestsRefused int64 `json:"requests_refused"` // since start, over MaxRequestsPerClient
}

func (g *Gateway) Stats() Stats {
	g.mu.Lock()
	mirrors := make([]*serverMirror, 0, len(g.mirrors))
	for _, m := range g.mirrors {
		mirrors = append(mirrors, m)
	}
	requests := 0
	for _, n := range g.active {
		requests += n
	}
	g.mu.Unlock()
	s := Stats{Clients: len(mirrors), SessionsRefused: g.refused.Load(), Requests: requests, RequestsRefused: g.refusedRequests.Load()}
	for _, m := range mirrors {
		for ss := range m.server.Sessions() {
			if ss.ID() != "" {
				s.Sessions++
			}
		}
	}
	return s
}

// hasBearer reports whether the request carries a bearer token; with auth on
// every request that reaches the gateway does (auth.RequireKey,
// oauth.RequireClient).
func hasBearer(r *http.Request) bool {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	return ok && strings.EqualFold(scheme, "bearer") && strings.TrimSpace(token) != ""
}

// hasMirror reports whether the client's server exists already.
func (g *Gateway) hasMirror(id string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.mirrors[id]
	return ok
}

// mirrorFor returns the client's mirror, creating and syncing it on first use.
// A disconnected (revoked) client gets a server with nothing on it, not kept.
func (g *Gateway) mirrorFor(c identity.Client) *serverMirror {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.mirrors[c.ID]; ok {
		return m
	}
	if g.revoked[c.ID] || g.stopped {
		m := g.buildServer(c, snapshot{})
		cause := errCallCancelled
		if g.stopped {
			cause = errHubStopping
		}
		m.cancel(cause)
		return m
	}
	m := g.buildServer(c, g.snapshot())
	g.mirrors[c.ID] = m
	return m
}

// snapshot is what one sync copies into the mirrors.
type snapshot struct {
	tools     []*catalog.Tool
	prompts   []*catalog.Prompt
	resources []*catalog.Resource
	templates []*catalog.ResourceTemplate
}

func (g *Gateway) snapshot() snapshot {
	c := g.opts.Catalog
	return snapshot{c.List(), c.Prompts(), c.Resources(), c.ResourceTemplates()}
}

func (g *Gateway) buildServer(c identity.Client, snap snapshot) *serverMirror {
	s := mcp.NewServer(&mcp.Implementation{Name: "mcp-mesh", Version: g.opts.Version}, &mcp.ServerOptions{
		Logger: g.sdkLog,
		// What a client sees depends on its key (grants, per-client
		// processes), so nothing may be cached for anyone else; an unset scope
		// would mean public. Lists keep TTL 0 (list_changed tells clients when
		// to refetch); a resource read keeps its upstream's TTL.
		SetCacheable: func(_ context.Context, _ mcp.Request, c *mcp.Cacheable) { c.CacheScope = "private" },
		// Keep tools/list available even when the catalog is initially empty.
		Capabilities: &mcp.ServerCapabilities{
			Tools:     &mcp.ToolCapabilities{ListChanged: true},
			Prompts:   &mcp.PromptCapabilities{ListChanged: true},
			Resources: &mcp.ResourceCapabilities{ListChanged: true},
		},
	})
	m := &serverMirror{
		client: c, server: s, definitions: make(map[string]string),
		prompts: make(map[string]string), resources: make(map[string]string), templates: make(map[string]string),
	}
	// Not derived from a request: the mirror outlives them. DisconnectClient
	// and Run's end cancel it.
	m.ctx, m.cancel = context.WithCancelCause(context.Background())
	// The server is the client's view, so identity comes from the mirror. It
	// runs first (after containing panics) so recording and routing see the
	// client.
	s.AddReceivingMiddleware(g.containPanics, func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			return next(identity.With(ctx, c), method, req)
		}
	}, g.recordCalls)
	g.syncMirror(m, snap)
	return m
}

// containPanics turns a panic in a request handler into an internal error.
// Handlers work on what nodes send, and the SDK recovers nothing: a panic in
// its request goroutine would end the whole hub.
func (g *Gateway) containPanics(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (result mcp.Result, err error) {
		defer func() {
			if p := recover(); p != nil {
				g.opts.Logger.Error("request handler panicked", "method", method, "panic", p, "stack", string(debug.Stack()))
				result, err = nil, &jsonrpc.Error{Code: jsonrpc.CodeInternalError, Message: "mcp-mesh: internal error"}
			}
		}()
		return next(ctx, method, req)
	}
}

// syncAll brings every mirror in line with one catalog snapshot.
func (g *Gateway) syncAll() {
	g.mu.Lock()
	// Snapshot under the lock: a mirror created after it was synced from a
	// newer snapshot and isn't in this list, so it can't be set back.
	snap := g.snapshot()
	mirrors := make([]*serverMirror, 0, len(g.mirrors))
	for _, m := range g.mirrors {
		mirrors = append(mirrors, m)
	}
	g.mu.Unlock()
	for _, m := range mirrors {
		g.syncMirror(m, snap)
	}
}

// Refresh resyncs every client's tool list soon, for when what Visible
// answers changed (groups, grants, approvals). Calls are coalesced; it never
// blocks.
func (g *Gateway) Refresh() {
	select {
	case g.refresh <- struct{}{}:
	default:
	}
}

// Run mirrors the catalog until ctx ends. The caller owns its goroutine.
func (g *Gateway) Run(ctx context.Context) {
	changes, cancel := g.opts.Catalog.Changes()
	defer cancel()
	// Subscribe first so a change racing the initial snapshot is not missed.
	g.syncAll()
	var timer *time.Timer
	var tick <-chan time.Time
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	// debounce (re)starts the timer: bursts of changes cause one resync.
	debounce := func() {
		if timer == nil {
			timer = time.NewTimer(100 * time.Millisecond)
		} else {
			timer.Reset(100 * time.Millisecond)
		}
		tick = timer.C
	}
	for {
		select {
		case <-ctx.Done():
			g.stop()
			return
		case <-changes:
			debounce()
		case <-g.refresh:
			debounce()
		case <-tick:
			g.syncAll()
			tick = nil
		}
	}
}

// stop cancels every client's calls and closes its sessions, which ends
// session-based clients' notification streams (an HTTP server's Shutdown
// would wait for them). It returns once the sessions are closed.
func (g *Gateway) stop() {
	g.mu.Lock()
	g.stopped = true
	mirrors := g.mirrors
	g.mirrors = make(map[string]*serverMirror)
	g.mu.Unlock()
	var closing sync.WaitGroup
	for id, m := range mirrors {
		m.cancel(errHubStopping)
		closing.Go(func() { g.closeSessions(id, m) })
	}
	closing.Wait()
}

// Handler serves MCP Streamable HTTP, intended to be mounted at /mcp.
func (g *Gateway) Handler() http.Handler { return g.handler }

func (g *Gateway) syncMirror(m *serverMirror, snap snapshot) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g.syncTools(m, snap.tools)
	g.syncPrompts(m, snap.prompts)
	g.syncResources(m, snap.resources, snap.templates)
}

func (g *Gateway) syncTools(m *serverMirror, tools []*catalog.Tool) {
	current := make(map[string]string, len(tools))
	for _, tool := range tools {
		if !g.opts.Visible(m.client, tool) {
			continue
		}
		exposed := tool.Exposed()
		// A serialized snapshot compares definitions without comparing callers
		// or retaining pointers to mutable schema values.
		data, err := json.Marshal(exposed)
		if err != nil {
			g.opts.Logger.Error("serialize exposed tool", "tool", tool.Name, "error", err)
			continue
		}
		definition := string(data)
		current[tool.Name] = definition
		if old, ok := m.definitions[tool.Name]; !ok || old != definition {
			if !g.addTool(m, exposed) {
				delete(current, tool.Name)
			}
		}
	}
	var removed []string
	for name := range m.definitions {
		if _, ok := current[name]; !ok {
			removed = append(removed, name)
		}
	}
	if len(removed) != 0 {
		m.server.RemoveTools(removed...)
	}
	m.definitions = current
}

// addTool registers a tool, containing the SDK's panic on a definition it
// refuses. The catalog already drops those (catalog.Sanitize); this is the
// last line, since a panic here would end the whole hub.
func (g *Gateway) addTool(m *serverMirror, t *mcp.Tool) (ok bool) {
	defer func() {
		if p := recover(); p != nil {
			g.opts.Logger.Error("tool skipped", "tool", t.Name, "error", p)
			ok = false
		}
	}()
	m.server.AddTool(t, g.callTool(m))
	return true
}

func unknownTool(name string) error {
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: fmt.Sprintf("unknown tool %q", name)}
}

// callTool re-checks visibility at call time: the catalog or ACL may have
// changed since the client listed tools. A hidden tool looks like an unknown
// one so its existence does not leak.
func (g *Gateway) callTool(m *serverMirror) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return g.forward(ctx, m, req)
	}
}

func (g *Gateway) forward(ctx context.Context, m *serverMirror, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	m.inflight.Add(1)
	defer m.inflight.Add(-1)
	client := m.client
	tool, ok := g.opts.Catalog.Lookup(req.Params.Name)
	if !ok || !g.opts.Visible(client, tool) {
		return nil, unknownTool(req.Params.Name)
	}
	if call, ok := ctx.Value(callKey{}).(*activity.Call); ok {
		call.Node, call.MCP = tool.Node, tool.MCP
	}
	// Starting a lazy MCP or waiting for a slot can take a while; the tool's
	// definition (and with it its approval) or the client's grants can change
	// meanwhile, so the handle checks again right before sending.
	key := catalog.ToolKey{Node: tool.Node, MCP: tool.MCP, Tool: tool.Upstream.Name}
	ctx = catalog.WithCheck(ctx, func(listed catalog.Listed) error {
		t, ok := g.opts.Catalog.ToolByKey(key)
		if !ok || !g.opts.Visible(client, t) {
			return unknownTool(req.Params.Name)
		}
		// A per-client process may list another definition than the one the
		// catalog holds (and Visible judged).
		return listed.Check(t)
	})
	ctx, cancel := context.WithTimeout(ctx, g.timeout(tool.Timeout))
	defer cancel()
	// Also end the call when the client is disconnected. stop releases the
	// AfterFunc registration when the call returns, so nothing outlives it.
	stop := context.AfterFunc(m.ctx, cancel)
	defer stop()
	params := &mcp.CallToolParams{Arguments: req.Params.Arguments}
	// The client's own _meta (its progress token above all) is not sent
	// upstream: tokens only mean something on the session that chose them.
	if token := req.Params.GetProgressToken(); token != nil && g.opts.Progress != nil {
		// The notification is sent with the call's context: the transport
		// uses it to put the message on this request's stream.
		hubToken, release := g.opts.Progress.Register(token, func(_ context.Context, p *mcp.ProgressNotificationParams) error {
			return req.Session.NotifyProgress(ctx, p)
		})
		defer release()
		params.SetProgressToken(hubToken)
		defer g.opts.Progress.Settle(ctx, hubToken) // runs before release
	}
	result, err := tool.CallWith(ctx, params)
	if m.ctx.Err() != nil {
		// Whatever the upstream made of the cancellation, the client was
		// disconnected; say so instead of "unavailable" or a bare ctx error.
		return nil, context.Cause(m.ctx)
	}
	if err == nil {
		return result, nil
	}
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) {
		// Returning the unwrapped wire error preserves its data as well as its
		// code and message; the SDK discards data on wrapped handler errors.
		return nil, rpcErr
	}
	if call, ok := ctx.Value(callKey{}).(*activity.Call); ok {
		call.Outcome, call.Error = "error", err.Error()
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf("mcp-mesh: %s/%s is unavailable: %v", tool.Node, tool.MCP, err),
		}},
	}, nil
}

// timeout is the bound for one call: the MCP's own, else the hub default.
func (g *Gateway) timeout(perMCP time.Duration) time.Duration {
	if perMCP > 0 {
		return perMCP
	}
	return g.opts.CallTimeout
}

var (
	errCallCancelled = errors.New("mcp-mesh: call cancelled: client disconnected (key revoked)")
	errHubStopping   = errors.New("mcp-mesh: call cancelled: the hub is shutting down")
)

// DisconnectClient ends everything the client with this identity ID has open
// (its key was revoked): it cancels its in-flight tool calls, closes its MCP
// sessions (including a session-based client's notification stream) and
// drops its server; later requests with the ID see an empty server. It
// reports whether the client had a server.
func (g *Gateway) DisconnectClient(id string) bool {
	g.mu.Lock()
	m, ok := g.mirrors[id]
	delete(g.mirrors, id)
	g.revoked[id] = true
	g.mu.Unlock()
	if !ok {
		return false
	}
	// Cancel first so calls stop before their sessions go away. A syncAll that
	// still holds m only touches the dropped server.
	m.cancel(errCallCancelled)
	// The cancelled calls answer "key revoked" on their sessions; closing them
	// at once would cut that answer off, so wait (briefly) for the calls to
	// return. The goroutine ends within closeGrace.
	go g.closeSessions(id, m)
	return true
}

// closeGrace bounds how long a disconnect waits for cancelled calls to
// answer before closing the client's sessions.
const closeGrace = 2 * time.Second

func (g *Gateway) closeSessions(id string, m *serverMirror) {
	deadline := time.Now().Add(closeGrace)
	for m.inflight.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	// The SDK writes a handler's result after the handler returns.
	time.Sleep(100 * time.Millisecond)
	for ss := range m.server.Sessions() {
		if err := ss.Close(); err != nil {
			g.opts.Logger.Debug("close client session", "client", id, "error", err)
		}
	}
}

type callKey struct{}

const maxErrorLen = 500

// truncate cuts s to at most n bytes on a rune boundary, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// recordCalls records tool calls, prompt fetches and resource reads (the
// requests that reach an MCP) in the activity log and the audit log. The
// log keeps no arguments or results, only an error's first maxErrorLen
// bytes, which for a tool error is the start of the tool's own error text.
func (g *Gateway) recordCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if g.opts.Activity == nil {
			return next(ctx, method, req)
		}
		client := identity.From(ctx)
		call := activity.Call{Time: time.Now(), ClientID: client.ID, Client: client.Name}
		switch r := req.(type) {
		case *mcp.CallToolRequest:
			if method != "tools/call" || r.Params == nil {
				return next(ctx, method, req)
			}
			call.Kind, call.Tool = activity.KindTool, r.Params.Name
			// The SDK rejects unregistered tools before invoking the tool
			// handler. Middleware records those calls too, including stale
			// catalog entries.
			if tool, ok := g.opts.Catalog.Lookup(call.Tool); ok && g.opts.Visible(client, tool) {
				call.Node, call.MCP = tool.Node, tool.MCP
			}
		case *mcp.GetPromptRequest:
			if r.Params == nil {
				return next(ctx, method, req)
			}
			call.Kind, call.Tool = activity.KindPrompt, r.Params.Name
			if p, ok := g.opts.Catalog.LookupPrompt(call.Tool); ok && g.opts.VisibleMCP(client, p.Node, p.MCP) {
				call.Node, call.MCP = p.Node, p.MCP
			}
		case *mcp.ReadResourceRequest:
			if r.Params == nil {
				return next(ctx, method, req)
			}
			call.Kind, call.Tool = activity.KindResource, r.Params.URI
			if route, ok := g.opts.Catalog.ResolveResource(call.Tool); ok && g.opts.VisibleMCP(client, route.Node, route.MCP) {
				call.Node, call.MCP = route.Node, route.MCP
			}
		default:
			return next(ctx, method, req)
		}
		result, err := next(context.WithValue(ctx, callKey{}, &call), method, req)
		call.Duration = time.Since(call.Time)
		switch {
		case err != nil:
			call.Outcome, call.Error = "error", err.Error()
		case call.Outcome == "error":
			// A reachability failure was converted to a model-visible result.
		default:
			call.Outcome = "ok"
			if res, ok := result.(*mcp.CallToolResult); ok && res != nil && res.IsError {
				call.Outcome = "tool_error"
				var messages []string
				for _, content := range res.Content {
					if text, ok := content.(*mcp.TextContent); ok {
						messages = append(messages, text.Text)
					}
				}
				call.Error = strings.Join(messages, "\n")
			}
		}
		// Tool output can be megabytes, and may hold data the log shouldn't
		// keep in full; the log only needs a hint.
		call.Error = truncate(call.Error, maxErrorLen)
		g.opts.Activity.Add(call)
		return result, err
	}
}
