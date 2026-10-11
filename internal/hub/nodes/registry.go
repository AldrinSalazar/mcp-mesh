package nodes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/progress"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

type Options struct {
	Catalog        *catalog.Catalog
	Logger         *slog.Logger
	MaxMessageSize int64         // 0 = proto.DefaultMaxMessageSize
	OfflineGrace   time.Duration // 0 = 30s
	MaxInFlight    int           // 0 = 8 per MCP
	// Authorize decides whether the request (its credentials were checked
	// by middleware before the upgrade) may register as node. An error
	// closes the connection with proto.CloseInvalidKey. Nil allows every
	// node (auth off).
	Authorize func(r *http.Request, node string) error
	// KeyID returns the ID of the credential that authenticated the node's
	// request (the auth key ID). Nil, or "", means none (auth off).
	KeyID func(*http.Request) string
	// Progress receives the progress notifications MCPs send for calls the
	// gateway registered there. Nil drops them.
	Progress *progress.Router
	// Version is the hub's version, which its MCP client gives MCPs.
	Version string
}

// Registry owns connected nodes and their upstream MCP sessions.
type Registry struct {
	mu    sync.RWMutex
	nodes map[string]*node
	opts  Options
	// revoked holds the key IDs DisconnectKey and CloseClient were given.
	// A node or a client request authenticated just before its key was
	// revoked can finish registering, or opening a per-client session,
	// after the revocation swept what existed; this is what turns those
	// away. Key IDs are never reused, and the set only grows by one ID per
	// revoked key.
	revoked map[string]bool

	counters counters

	helloTimeout     time.Duration
	reopenBackoff    time.Duration
	maxReopenBackoff time.Duration
	maxDemandBackoff time.Duration
	// afterReady, when set (tests), runs right after an MCP's handle is
	// marked ready, with Registry.mu held.
	afterReady func(node, mcp string)
}

func NewRegistry(opts Options) *Registry {
	if opts.Catalog == nil {
		opts.Catalog = catalog.New()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.MaxMessageSize <= 0 {
		opts.MaxMessageSize = proto.DefaultMaxMessageSize
	}
	if opts.OfflineGrace <= 0 {
		opts.OfflineGrace = 30 * time.Second
	}
	if opts.MaxInFlight <= 0 {
		opts.MaxInFlight = 8
	}
	return &Registry{
		nodes: make(map[string]*node), opts: opts, revoked: make(map[string]bool),
		helloTimeout: 10 * time.Second, reopenBackoff: time.Second,
		maxReopenBackoff: 60 * time.Second,
		maxDemandBackoff: 5 * time.Second,
	}
}

// ServeHTTP is one node's connection, from handshake to disconnect.
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	conn, err := wire.Accept(w, req, r.opts.MaxMessageSize)
	if err != nil {
		r.opts.Logger.Debug("accept node", "error", err)
		return
	}
	defer conn.CloseNow()
	h, ok := r.handshake(req, conn)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	// Taken now and kept for the connection's lifetime: revocation matches
	// on what authenticated this connection.
	var keyID string
	if r.opts.KeyID != nil {
		keyID = r.opts.KeyID(req)
	}
	n := newNode(r, ctx, cancel, conn, h, req.RemoteAddr, keyID)
	if refused := r.register(n); refused != nil {
		r.counters.nodesRefused.Add(1)
		conn.Close(refused.code, refused.reason)
		return
	}
	// Runs after serve has stopped the node's workers, so none of them can
	// publish state for an offline node.
	defer r.unregister(n)
	n.serve(min(h.Version, proto.Version))
}

// refusal is why a hello is turned away, as a WebSocket close.
type refusal struct {
	code   websocket.StatusCode
	reason string
}

// checkHello validates the first frame before anything is registered.
func checkHello(e *proto.Envelope) *refusal {
	if e == nil || e.T != proto.THello || e.Hello == nil {
		return &refusal{websocket.StatusPolicyViolation, "first frame must be hello"}
	}
	h := e.Hello
	if h.Version < proto.MinVersion {
		return &refusal{proto.CloseUnsupportedVersion, "unsupported protocol version"}
	}
	if strings.TrimSpace(h.Node) == "" {
		return &refusal{websocket.StatusPolicyViolation, "empty or duplicate name"}
	}
	seen := make(map[string]bool)
	for _, offered := range h.MCPs {
		if strings.TrimSpace(offered.Name) == "" || seen[offered.Name] {
			return &refusal{websocket.StatusPolicyViolation, "empty or duplicate name"}
		}
		seen[offered.Name] = true
	}
	return nil
}

// handshake reads and checks hello, then asks Authorize. It closes the
// connection with the matching code when the node is turned away.
func (r *Registry) handshake(req *http.Request, conn *wire.Conn) (*proto.Hello, bool) {
	ctx, cancel := context.WithTimeout(req.Context(), r.helloTimeout)
	e, err := conn.Recv(ctx)
	cancel()
	if err != nil {
		e = nil
	}
	if refused := checkHello(e); refused != nil {
		r.counters.nodesRefused.Add(1)
		conn.Close(refused.code, refused.reason)
		return nil, false
	}
	if r.opts.Authorize != nil {
		if err := r.opts.Authorize(req, e.Hello.Node); err != nil {
			r.counters.nodesRefused.Add(1)
			r.opts.Logger.Warn("node refused", "node", e.Hello.Node, "addr", req.RemoteAddr, "error", err)
			conn.Close(proto.CloseInvalidKey, "invalid node key")
			return nil, false
		}
	}
	return e.Hello, true
}

// register makes n the node for its name. A node that reconnects within
// the offline grace period takes over the previous connection's MCP
// handles, so catalog callers keep working, and its last known tools. It
// fails if a node with that name is online, or if n's key was revoked
// after its upgrade was authenticated (DisconnectKey runs either before
// this, and is seen here, or after, and finds n).
func (r *Registry) register(n *node) *refusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	name := n.info.Name
	if n.keyID != "" && r.revoked[n.keyID] {
		r.opts.Logger.Warn("node refused: key revoked", "node", name, "key", n.keyID)
		return &refusal{proto.CloseInvalidKey, "key revoked"}
	}
	old := r.nodes[name]
	if old != nil && old.info.Online {
		return &refusal{proto.CloseNodeNameTaken, "node name already connected"}
	}
	if old != nil {
		old.offlineTimer.Stop()
		old.offlineTimer = nil
	}
	for i := range n.info.MCPs {
		info := &n.info.MCPs[i]
		var handle *mcpHandle
		if old != nil {
			handle = old.handles[info.Name]
			if previous, ok := old.mcpInfo(info.Name); ok {
				info.Tools, info.Server, info.Problems = previous.Tools, previous.Server, previous.Problems
			}
		}
		reused := handle != nil
		if !reused {
			handle = newMCPHandle(name, info.Name, r.opts.MaxInFlight)
			handle.isRevoked = r.isRevoked
			handle.counters = &r.counters
		}
		n.handles[info.Name] = handle
		n.demands[info.Name] = make(chan struct{}, 1)
		handle.setSession(nil, nil, "starting")
		handle.configure(n.ctx, n.demands[info.Name], info.Start == proto.StartLazy, info.State == MCPOpening)
		if info.Sharing == proto.SharingPerClient {
			mcpName := info.Name
			handle.configureClients(func(ctx context.Context, c identity.Client) (*clientSession, error) {
				return n.openClient(ctx, mcpName, c)
			})
		} else {
			handle.configureClients(nil)
		}
		if tools, ok := n.cached[info.Name]; ok {
			handle.setSession(nil, nil, "idle")
			// The node only caches tools. Prompts and resources listed by an
			// earlier connection stay until the MCP is opened again.
			content := handle.lastContent()
			content.Tools, content.Timeout = tools, n.timeouts[info.Name]
			// The cache is what the catalog shows, so it is also what
			// per-client processes are compared with (openClient).
			handle.setContent(content)
			n.publishLocked(i, content, handle)
		}
		// A reconnect starts a fresh grace period while its sessions initialize.
		if reused && info.Start != proto.StartLazy {
			n.expireMCPLocked(i)
		}
	}
	if old != nil {
		for mcpName := range old.handles {
			if n.handles[mcpName] == nil {
				r.opts.Catalog.RemoveMCP(name, mcpName)
			}
		}
	}
	r.nodes[name] = n
	return nil
}

// unregister marks n offline and keeps its tools listed for the grace
// period, unless the node reconnects first.
func (r *Registry) unregister(n *node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n.info.Online, n.info.DisconnectedAt = false, time.Now()
	for i := range n.info.MCPs {
		info := &n.info.MCPs[i]
		info.State = MCPClosed
		n.handles[info.Name].setSession(nil, nil, "offline")
	}
	for name, timer := range n.mcpTimers {
		timer.Stop()
		delete(n.mcpTimers, name)
	}
	n.offlineTimer = time.AfterFunc(r.opts.OfflineGrace, func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.nodes[n.info.Name] != n || n.info.Online {
			return
		}
		r.opts.Catalog.RemoveNode(n.info.Name)
		delete(r.nodes, n.info.Name)
		n.offlineTimer = nil
	})
	r.opts.Logger.Info("node disconnected", "node", n.info.Name)
}

// CloseClient ends a client's sessions on every per-client MCP (its key was
// revoked), which stops its processes on the nodes, and refuses the client
// new ones. It reports how many sessions it closed.
func (r *Registry) CloseClient(clientID string) int {
	if clientID == "" {
		return 0
	}
	// Marked before the sweep: an open that publishes its session after the
	// sweep looked at its handle checks the mark afterwards (clientSession).
	r.mu.Lock()
	r.revoked[clientID] = true
	var handles []*mcpHandle
	for _, n := range r.nodes {
		for _, h := range n.handles {
			handles = append(handles, h)
		}
	}
	r.mu.Unlock()
	closed := 0
	for _, h := range handles {
		if h.closeClient(clientID) {
			closed++
		}
	}
	return closed
}

// DisconnectKey closes every online node connection that authenticated with
// keyID (its key was revoked) and cancels its in-flight calls, and refuses
// the key from now on. Their tools stay listed for the offline grace
// period, like any disconnect. It returns the names of the nodes it
// disconnected. An empty keyID matches nothing.
func (r *Registry) DisconnectKey(keyID string) []string {
	if keyID == "" {
		return nil
	}
	r.mu.Lock()
	r.revoked[keyID] = true
	r.mu.Unlock()
	names := r.closeOnline("key revoked", func(n *node) bool { return n.keyID == keyID })
	for _, name := range names {
		r.opts.Logger.Info("node disconnected: key revoked", "node", name, "key", keyID)
	}
	return names
}

func (r *Registry) isRevoked(keyID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.revoked[keyID]
}

// closeOnline closes the online nodes match accepts (match runs under the
// registry lock). The closes happen outside the lock, in parallel, because
// a close handshake with an unresponsive peer can block. Ending the node's
// context fails calls in flight on its channels. It returns the node names,
// sorted.
func (r *Registry) closeOnline(reason string, match func(*node) bool) []string {
	r.mu.RLock()
	var targets []*node
	for _, n := range r.nodes {
		if n.info.Online && match(n) {
			targets = append(targets, n)
		}
	}
	r.mu.RUnlock()
	var closing sync.WaitGroup
	names := make([]string, 0, len(targets))
	for _, n := range targets {
		names = append(names, n.info.Name)
		closing.Go(func() {
			n.conn.Close(proto.CloseInvalidKey, reason)
			n.cancel()
		})
	}
	closing.Wait()
	slices.Sort(names)
	return names
}

// Nodes returns independent snapshots sorted by node and MCP name.
func (r *Registry) Nodes() []NodeInfo {
	r.mu.RLock()
	out := make([]NodeInfo, 0, len(r.nodes))
	for _, n := range r.nodes {
		info := n.info
		info.MCPs = slices.Clone(info.MCPs)
		for i := range info.MCPs {
			mcpInfo := &info.MCPs[i]
			mcpInfo.InFlight, mcpInfo.Queued, mcpInfo.Starting = n.handles[mcpInfo.Name].counts()
			mcpInfo.Clients = n.handles[mcpInfo.Name].clientCount()
		}
		out = append(out, info)
	}
	r.mu.RUnlock()
	slices.SortFunc(out, func(a, b NodeInfo) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func randomID(prefix string) string {
	var b [16]byte
	rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}
