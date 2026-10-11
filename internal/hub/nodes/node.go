package nodes

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/logx"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

// node is one connection from a node, from register to unregister. Its
// MCP handles may have come from the previous connection with the same name.
type node struct {
	r            *Registry
	conn         *wire.Conn
	ctx          context.Context
	cancel       context.CancelFunc
	keyID        string                   // key that authenticated this connection; immutable
	info         NodeInfo                 // protected by Registry.mu
	handles      map[string]*mcpHandle    // immutable after registration
	demands      map[string]chan struct{} // immutable after registration
	offlineTimer *time.Timer              // protected by Registry.mu
	mcpTimers    map[string]*time.Timer   // protected by Registry.mu
	cached       map[string][]*mcp.Tool   // tools the node cached for lazy MCPs; read by register
	timeouts     map[string]time.Duration // per-MCP call timeouts from the offer; immutable

	mu       sync.Mutex
	channels map[string]*channel
	// clientWG tracks the goroutines that watch per-client sessions; serve
	// waits for them, and they end when the node's context does. Its Go
	// calls happen under mu while closing is false; serve sets closing
	// before it waits, so no watcher can be added once Wait has started.
	clientWG sync.WaitGroup
	closing  bool
}

func newNode(r *Registry, ctx context.Context, cancel context.CancelFunc, conn *wire.Conn, h *proto.Hello, addr, keyID string) *node {
	infos, cached := parseOffers(h, r.opts.Logger)
	timeouts := make(map[string]time.Duration, len(h.MCPs))
	for _, offered := range h.MCPs {
		if offered.CallTimeoutMs > 0 {
			timeouts[offered.Name] = time.Duration(offered.CallTimeoutMs) * time.Millisecond
		}
	}
	return &node{
		r: r, conn: conn, ctx: ctx, cancel: cancel, keyID: keyID, cached: cached, timeouts: timeouts,
		channels: make(map[string]*channel), handles: make(map[string]*mcpHandle),
		demands: make(map[string]chan struct{}), mcpTimers: make(map[string]*time.Timer),
		info: NodeInfo{
			Name: h.Node, NodeVersion: h.NodeVersion, OS: h.OS, MCPs: infos,
			RemoteAddr: addr, KeyID: keyID, ConnectedAt: time.Now(), Online: true,
		},
	}
}

// parseOffers turns hello's MCPs into their first snapshots, sorted by
// name. A lazy MCP with a usable tool cache starts idle with those tools;
// a bad cache is logged and ignored, so the MCP opens to list its tools.
func parseOffers(h *proto.Hello, logger *slog.Logger) ([]MCPInfo, map[string][]*mcp.Tool) {
	infos := make([]MCPInfo, 0, len(h.MCPs))
	cached := make(map[string][]*mcp.Tool)
	for _, offered := range h.MCPs {
		start, state := offered.Start, MCPOpening
		if start == "" {
			start = proto.StartEager
		}
		if start == proto.StartLazy && len(offered.Tools) > 0 {
			var tools []*mcp.Tool
			err := json.Unmarshal(offered.Tools, &tools)
			// null and null array entries are not tool arrays usable by the catalog.
			if err == nil && (tools == nil || slices.Contains(tools, nil)) {
				err = fmt.Errorf("cache must be an array of tool objects")
			}
			if err != nil {
				logger.Warn("invalid cached MCP tools", "node", h.Node, "mcp", offered.Name, "error", err)
			} else {
				cached[offered.Name], state = tools, MCPIdle
			}
		}
		sharing := offered.Sharing
		if sharing == "" {
			sharing = proto.SharingShared
		}
		infos = append(infos, MCPInfo{
			Name: offered.Name, Transport: offered.Transport, Sharing: sharing, Start: start, State: state,
			CallTimeout: time.Duration(offered.CallTimeoutMs) * time.Millisecond,
		})
	}
	slices.SortFunc(infos, func(a, b MCPInfo) int { return strings.Compare(a.Name, b.Name) })
	return infos, cached
}

// mcpInfo returns the named MCP's snapshot. Caller holds Registry.mu.
func (n *node) mcpInfo(name string) (MCPInfo, bool) {
	for _, info := range n.info.MCPs {
		if info.Name == name {
			return info, true
		}
	}
	return MCPInfo{}, false
}

// serve welcomes the node, runs its MCP workers and routes its frames to
// channels until the connection ends. Every worker has stopped when it
// returns.
func (n *node) serve(version int) {
	var workers sync.WaitGroup
	defer func() {
		n.cancel()
		n.conn.CloseNow()
		workers.Wait()
		n.mu.Lock()
		n.closing = true
		n.mu.Unlock()
		n.clientWG.Wait()
	}()
	n.r.opts.Logger.Info("node connected", "node", n.info.Name, "addr", n.info.RemoteAddr, "version", n.info.NodeVersion, "protocol", version, "os", n.info.OS, "mcps", len(n.info.MCPs))
	if err := n.conn.Send(n.ctx, &proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{
		Version: version, NodeID: randomID("n_"), MaxMessageSize: n.r.opts.MaxMessageSize,
	}}); err != nil {
		return
	}
	workers.Go(func() { n.conn.Heartbeat(n.ctx) })
	for i := range n.info.MCPs {
		workers.Go(func() { n.runMCP(i) })
	}
	for {
		e, err := n.conn.Recv(n.ctx)
		if err != nil {
			return
		}
		n.mu.Lock()
		ch := n.channels[e.Ch]
		n.mu.Unlock()
		if ch != nil {
			ch.dispatch(e)
		}
	}
}

func (n *node) update(i int, state MCPState, err error, server string) {
	n.r.mu.Lock()
	info := &n.info.MCPs[i]
	info.State, info.Error = state, ""
	if server != "" {
		info.Server = server
	}
	if err != nil {
		info.Error = err.Error()
	}
	n.r.mu.Unlock()
}

// Caller holds Registry.mu. Timer identity keeps an expired callback from
// removing tools after a successful reopen or a replacement node connection.
func (n *node) expireMCPLocked(i int) {
	name := n.info.MCPs[i].Name
	if n.mcpTimers[name] != nil || n.ctx.Err() != nil {
		return
	}
	var timer *time.Timer
	timer = time.AfterFunc(n.r.opts.OfflineGrace, func() {
		n.r.mu.Lock()
		defer n.r.mu.Unlock()
		if n.r.nodes[n.info.Name] != n || n.mcpTimers[name] != timer || n.ctx.Err() != nil {
			return
		}
		delete(n.mcpTimers, name)
		n.r.opts.Catalog.RemoveMCP(n.info.Name, name)
		n.info.MCPs[i].Tools, n.info.MCPs[i].Prompts, n.info.MCPs[i].Resources = 0, 0, 0
	})
	n.mcpTimers[name] = timer
}

// unavailable takes the MCP's session away from callers as soon as its
// channel ends, and starts or cancels the tools' grace period.
func (n *node) unavailable(i int, ch *channel, lc *lifecycle) {
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	info := &n.info.MCPs[i]
	n.handles[info.Name].setSession(nil, nil, "offline")
	if lc.expiresTools(ch.closeReason()) {
		n.expireMCPLocked(i)
		return
	}
	if timer := n.mcpTimers[info.Name]; timer != nil {
		timer.Stop()
		delete(n.mcpTimers, info.Name)
	}
}

func (n *node) policy() reopenPolicy {
	return reopenPolicy{initial: n.r.reopenBackoff, maxAuto: n.r.maxReopenBackoff, maxDemand: n.r.maxDemandBackoff}
}

// runMCP is one MCP's worker: it opens channels and carries out the steps
// its lifecycle decides, until the node disconnects.
func (n *node) runMCP(i int) {
	n.r.mu.RLock()
	name := n.info.MCPs[i].Name
	cached := n.info.MCPs[i].State == MCPIdle
	lazy := n.info.MCPs[i].Start == proto.StartLazy
	n.r.mu.RUnlock()
	handle := n.handles[name]
	log := n.r.opts.Logger.With("node", n.info.Name, "mcp", name)
	lc := newLifecycle(lazy, cached, n.policy())
	s := step{next: lc.first()}
	for {
		switch s.next {
		case waitDemand:
			select {
			case <-n.ctx.Done():
				return
			case <-n.demands[name]:
			}
			log.Info("MCP opened on demand")
		case waitDelay:
			timer := time.NewTimer(s.delay)
			select {
			case <-n.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if n.ctx.Err() != nil {
			return
		}
		handle.setSession(nil, nil, "starting")
		handle.beginOpen(lc.onDemand)
		n.update(i, MCPOpening, nil, "")
		ch := newChannel(n, name)
		n.mu.Lock()
		n.channels[ch.id] = ch
		n.mu.Unlock()
		ready, err := n.serveMCP(i, ch, lc)
		n.unavailable(i, ch, lc)
		ch.Close()
		n.mu.Lock()
		delete(n.channels, ch.id)
		n.mu.Unlock()
		if n.ctx.Err() != nil {
			return
		}
		s = lc.ended(outcome{ready: ready, reason: ch.closeReason(), remote: ch.remoteClosed(), err: err})
		n.r.counters.opens.Add(1)
		if !ready {
			n.r.counters.openFailures.Add(1)
		} else if s.err != nil {
			n.r.counters.failures.Add(1)
		}
		var retryAt time.Time
		if s.next == waitDemand && s.delay > 0 {
			retryAt = time.Now().Add(s.delay)
		}
		handle.endOpen(callerState(s), s.err, retryAt, s.onDemand)
		n.update(i, s.state, s.err, ch.server)
		switch {
		case s.log == logIdle:
			log.Info(s.log)
		case s.log != "":
			level := slog.LevelWarn
			if s.repeat {
				level = slog.LevelDebug
			}
			log.Log(n.ctx, level, s.log, "state", s.state, "error", s.err, "retry_in", s.delay)
		}
	}
}

// callerState is what a call that finds no session is told.
func callerState(s step) string {
	if s.state == MCPOpening {
		return "starting"
	}
	if s.err != nil && !s.onDemand {
		// Demand-driven callers get the error with its retry time instead.
		return s.err.Error()
	}
	return string(s.state)
}

func (n *node) serveMCP(i int, ch *channel, lc *lifecycle) (ready bool, err error) {
	defer func() {
		// A remote close cancels SDK RPCs, which may report only cancellation or
		// a generic connection error instead of the node's explanation.
		if err != nil && ch.remoteClosed() {
			err = ch.failure()
		}
	}()
	refresh := make(chan struct{}, 1)
	// Handlers must return before another RPC can safely complete, so they
	// only signal the loop below.
	requestRefresh := func() {
		select {
		case refresh <- struct{}{}:
		default:
		}
	}
	session, err := n.newClient(requestRefresh).Connect(ch.ctx, ch, nil)
	if err != nil {
		if refused, ok := errors.AsType[*openError](err); ok {
			return false, refused
		}
		return false, fmt.Errorf("initialize: %w", err)
	}
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		err := session.Wait()
		if err == nil {
			err = mcp.ErrConnectionClosed
		}
		ch.fail(err, false)
	}()
	defer func() {
		// Start the grace before closing the SDK session, which may wait on RPCs.
		n.unavailable(i, ch, lc)
		session.Close()
		<-waitDone
	}()
	if result := session.InitializeResult(); result != nil && result.ServerInfo != nil {
		ch.server = strings.TrimSpace(result.ServerInfo.Name + " " + result.ServerInfo.Version)
	}
	for {
		content, err := listContent(ch.ctx, session, func(kind string, err error) {
			n.r.opts.Logger.Warn("list "+kind+" failed", "node", n.info.Name, "mcp", ch.name, "error", err)
		})
		if ch.ctx.Err() != nil {
			return ready, ch.failure()
		}
		if err != nil {
			return ready, fmt.Errorf("%s: %w", ch.name, err)
		}
		content.Timeout = n.timeouts[ch.name]
		n.r.mu.Lock()
		if ch.ctx.Err() != nil {
			n.r.mu.Unlock()
			return ready, ch.failure()
		}
		if timer := n.mcpTimers[ch.name]; timer != nil {
			timer.Stop()
			delete(n.mcpTimers, ch.name)
		}
		handle := n.handles[ch.name]
		// Publish before the handle is ready: a call woken by setSession must
		// already see (and be checked against) the refreshed catalog.
		handle.setContent(content)
		kept := n.publishLocked(i, content, handle)
		handle.setSession(session, ch.ctx, "ready")
		if n.r.afterReady != nil {
			n.r.afterReady(n.info.Name, ch.name)
		}
		info := &n.info.MCPs[i]
		info.State, info.Error, info.Server = MCPReady, "", ch.server
		n.r.mu.Unlock()
		n.r.opts.Logger.Info("MCP ready", "node", n.info.Name, "mcp", ch.name, "server", ch.server, "tools", len(kept.Tools), "prompts", len(kept.Prompts), "resources", len(kept.Resources), "templates", len(kept.ResourceTemplates))
		ready = true
		if n.perClient(i) {
			// This channel only listed what the MCP offers; every client's
			// calls get a channel (and process) of their own. Closing it like
			// an idle-out frees the node's slot and keeps the tools listed.
			ch.failWithReason(errListed, false, proto.CloseIdle)
			return ready, nil
		}
		select {
		case <-ch.ctx.Done():
			return ready, ch.failure()
		case <-refresh:
		}
	}
}

var errListed = errors.New("listing done")

// publishLocked sets what MCP i offers in the catalog and records in its
// snapshot what was kept (counts) and why the rest was dropped (Problems).
// Nothing in content is trusted: Sanitize runs first, so even the counts
// never touch a listed entry directly. Caller holds Registry.mu.
func (n *node) publishLocked(i int, content catalog.Content, handle *mcpHandle) catalog.Content {
	info := &n.info.MCPs[i]
	kept, problems := catalog.Sanitize(n.info.Name, info.Name, content)
	problems = append(problems, n.r.opts.Catalog.Set(n.info.Name, info.Name, kept, handle)...)
	info.Problems = problems
	info.Tools, info.Prompts = len(kept.Tools), len(kept.Prompts)
	info.Resources = len(kept.Resources) + len(kept.ResourceTemplates)
	if len(problems) > 0 {
		n.r.opts.Logger.Warn("MCP listing has unusable definitions; they are left out", "node", n.info.Name, "mcp", info.Name, "problems", problems)
	}
	return kept
}

// errToolsChanged ends a per-client session whose process changed its tools.
var errToolsChanged = errors.New("tools changed")

// perClient reports whether MCP i gives each client its own channel.
func (n *node) perClient(i int) bool {
	n.r.mu.RLock()
	defer n.r.mu.RUnlock()
	return n.info.MCPs[i].Sharing == proto.SharingPerClient
}

// implementation is how the hub's MCP client introduces itself.
func (r *Registry) implementation() *mcp.Implementation {
	v := r.opts.Version
	if v == "" {
		v = "dev"
	}
	return &mcp.Implementation{Name: "mcp-mesh-hub", Version: v}
}

// newClient is the MCP client for one channel. onListChanged runs when the
// MCP's tools, prompts or resources change (nil: ignored, for per-client
// channels, whose lists the MCP's listing channel owns).
func (n *node) newClient(onListChanged func()) *mcp.Client {
	return mcp.NewClient(n.r.implementation(), n.clientOptions(onListChanged))
}

func (n *node) clientOptions(onListChanged func()) *mcp.ClientOptions {
	opts := &mcp.ClientOptions{
		Logger: logx.AtLeast(n.r.opts.Logger, slog.LevelWarn), Capabilities: &mcp.ClientCapabilities{},
		ProgressNotificationHandler: func(ctx context.Context, req *mcp.ProgressNotificationClientRequest) {
			if n.r.opts.Progress != nil {
				n.r.opts.Progress.Dispatch(ctx, req.Params)
			}
		},
	}
	if onListChanged != nil {
		opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { onListChanged() }
		opts.PromptListChangedHandler = func(context.Context, *mcp.PromptListChangedRequest) { onListChanged() }
		opts.ResourceListChangedHandler = func(context.Context, *mcp.ResourceListChangedRequest) { onListChanged() }
	}
	return opts
}

// republishTools makes a per-client process's tools the MCP's catalog entry
// when they differ from the last listing. It does nothing once the node or
// the channel is gone.
func (n *node) republishTools(name string, ch *channel, tools []*mcp.Tool) {
	handle := n.handles[name]
	n.r.mu.Lock()
	defer n.r.mu.Unlock()
	if n.r.nodes[n.info.Name] != n || n.ctx.Err() != nil || ch.ctx.Err() != nil {
		return
	}
	content, listed := handle.listedContent()
	if listed && toolSetKey(tools) == toolSetKey(content.Tools) {
		return
	}
	for i := range n.info.MCPs {
		if n.info.MCPs[i].Name == name {
			n.r.opts.Logger.Warn("per-client process lists different tools than the MCP's listing; publishing its tools", "node", n.info.Name, "mcp", name, "client", ch.client)
			content.Tools, content.Timeout = tools, n.timeouts[name]
			handle.setContent(content)
			n.publishLocked(i, content, handle)
			return
		}
	}
}

// toolSetKey is a canonical form of a tool list: each tool as JSON, sorted
// by name, so equal sets give equal strings whatever the listing order.
func toolSetKey(tools []*mcp.Tool) string {
	type item struct{ name, data string }
	items := make([]item, len(tools))
	for i, t := range tools {
		data, _ := json.Marshal(t)
		items[i].data = string(data)
		if t != nil {
			items[i].name = t.Name
		}
	}
	slices.SortFunc(items, func(a, b item) int {
		return cmp.Or(strings.Compare(a.name, b.name), strings.Compare(a.data, b.data))
	})
	var sb strings.Builder
	for _, it := range items {
		sb.WriteString(it.data)
		sb.WriteByte('\n')
	}
	return sb.String()
}

// openClient opens a per-client MCP's channel for one client and connects
// an MCP session over it. The session lives until the node closes the
// channel (an idle-out, a restart, a failure), the hub closes it (the
// client's key was revoked) or the node disconnects; done closes then.
func (n *node) openClient(ctx context.Context, name string, c identity.Client) (*clientSession, error) {
	if n.ctx.Err() != nil {
		return nil, &unavailableError{reason: "offline"}
	}
	ch := newChannel(n, name)
	ch.client = c.Name
	if ch.client == "" {
		ch.client = c.ID
	}
	n.mu.Lock()
	n.channels[ch.id] = ch
	n.mu.Unlock()
	drop := func() {
		ch.Close()
		n.mu.Lock()
		delete(n.channels, ch.id)
		n.mu.Unlock()
	}
	// ctx bounds the open and initialize only; the session's life is the
	// channel's.
	stop := context.AfterFunc(ctx, func() { ch.fail(ctx.Err(), false) })
	// Per-client processes are expected to offer the same tools as the
	// MCP's listing (or the node's cache), which is what the catalog,
	// approvals and grants were built from. Each process's tools are listed
	// here; if they differ, they become the MCP's catalog entry before the
	// call goes on, so a changed tool waits for approval like any other
	// change. The session keeps its listing: two processes can list
	// different definitions and take turns being the catalog's, so the
	// handle's admission check (catalog.RunCheck) also requires the selected
	// process's definition to be the catalog's, and a process that differs is
	// reopened (which republishes it). If the process later announces a
	// change its session ends (ch.fail only cancels, so the handler returns
	// promptly) and the client's next call reopens and lists again. Prompts
	// and resources of per-client processes are not compared.
	opts := n.clientOptions(nil)
	opts.ToolListChangedHandler = func(context.Context, *mcp.ToolListChangedRequest) { ch.fail(errToolsChanged, false) }
	session, err := mcp.NewClient(n.r.implementation(), opts).Connect(ch.ctx, ch, nil)
	var tools []*mcp.Tool
	if err == nil {
		listCtx, cancel := context.WithTimeoutCause(ch.ctx, maxListTime, fmt.Errorf("%w: not done in %s", errListBudget, maxListTime))
		tools, err = listTools(listCtx, session, newListBudget())
		if err != nil {
			err = fmt.Errorf("list tools: %w", budgetCause(listCtx, err))
		}
		cancel()
	}
	stopped := stop()
	if err == nil && stopped {
		n.republishTools(name, ch, tools)
	}
	if err != nil || !stopped {
		if session != nil {
			session.Close()
		}
		drop()
		if refused, ok := errors.AsType[*openError](err); ok {
			return nil, &unavailableError{reason: refused.reason, cause: refused}
		}
		if err == nil {
			err = ctx.Err()
		}
		return nil, fmt.Errorf("open %s for %s: %w", name, ch.client, err)
	}
	cs := &clientSession{session: session, done: ch.ctx.Done(), tools: make(map[string]*mcp.Tool, len(tools))}
	for _, t := range tools {
		if t == nil {
			continue
		}
		// The first of duplicates, as catalog.Sanitize keeps.
		if _, dup := cs.tools[t.Name]; !dup {
			cs.tools[t.Name] = t
		}
	}
	n.mu.Lock()
	if n.closing {
		n.mu.Unlock()
		session.Close()
		drop()
		return nil, &unavailableError{reason: "offline"}
	}
	n.clientWG.Go(func() {
		err := session.Wait()
		if err == nil {
			err = mcp.ErrConnectionClosed
		}
		ch.fail(err, false)
		session.Close()
		drop()
		n.handles[name].dropClient(c.ID, cs)
	})
	n.mu.Unlock()
	return cs, nil
}
