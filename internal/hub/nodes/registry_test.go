package nodes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

type fakeOptions struct {
	failOpens      int
	openError      string
	closeOnOpen    bool
	closeOnMethod  string
	failAfterFirst bool
	permits        <-chan struct{}
	sessionBased   bool
	configure      func(*mcp.Server)
	// serverFor picks the MCP server that answers an open; nil or a nil
	// result means the node's shared one.
	serverFor func(*proto.Envelope) *mcp.Server
}

type fakeNode struct {
	conn      *wire.Conn
	server    *mcp.Server
	welcome   *proto.Welcome
	opens     chan proto.Envelope
	errors    chan error
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	mu        sync.Mutex
	active    map[string]mcp.Connection
	openCount int
}

func newTestRegistry(t *testing.T) (*Registry, *catalog.Catalog, string) {
	return newTestRegistryOptions(t, Options{OfflineGrace: 500 * time.Millisecond})
}

func newTestRegistryOptions(t *testing.T, opts Options) (*Registry, *catalog.Catalog, string) {
	t.Helper()
	cat := catalog.New()
	opts.Catalog = cat
	r := NewRegistry(opts)
	r.reopenBackoff = 150 * time.Millisecond
	r.maxReopenBackoff = 300 * time.Millisecond
	r.maxDemandBackoff = 300 * time.Millisecond
	httpServer := httptest.NewServer(r)
	t.Cleanup(func() {
		httpServer.Close()
		// Cleanup follows fake disconnects; wait for their grace timers to finish.
		awaitContext(t, context.WithoutCancel(t.Context()), func() bool { return len(r.Nodes()) == 0 })
	})
	return r, cat, "ws" + strings.TrimPrefix(httpServer.URL, "http")
}

func hello(name string) *proto.Hello {
	return &proto.Hello{Version: proto.Version, Node: name, NodeVersion: "test-node", OS: "test-os", MCPs: []proto.MCP{
		{Name: "tools", Transport: proto.TransportStdio, Sharing: proto.SharingShared},
	}}
}

func addTool(server *mcp.Server, name string) {
	server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "result from " + name}}}, nil
		})
}

func startFake(t *testing.T, url string, h *proto.Hello, opts fakeOptions) *fakeNode {
	t.Helper()
	return startFakeHeader(t, url, nil, h, opts)
}

func startFakeHeader(t *testing.T, url string, header http.Header, h *proto.Hello, opts fakeOptions) *fakeNode {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	conn, err := wire.Dial(ctx, url, header, 0)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	f := &fakeNode{
		conn: conn, ctx: ctx, cancel: cancel, done: make(chan struct{}), active: make(map[string]mcp.Connection),
		opens: make(chan proto.Envelope, 20), errors: make(chan error, 1),
		server: mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "1.2.3"}, &mcp.ServerOptions{PageSize: 1}),
	}
	addTool(f.server, "echo")
	addTool(f.server, "second")
	if opts.configure != nil {
		opts.configure(f.server)
	}
	if opts.sessionBased {
		// Exercise SDK negotiation and the initialize fallback, without making
		// the hub select a protocol version itself.
		f.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "server/discover" {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "2025-11-25 server"}
				}
				return next(ctx, method, req)
			}
		})
	}
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: h}); err != nil {
		conn.CloseNow()
		cancel()
		t.Fatal(err)
	}
	e, err := conn.Recv(ctx)
	if err != nil || e.T != proto.TWelcome || e.Welcome == nil {
		conn.CloseNow()
		cancel()
		t.Fatalf("welcome: %v, %v", e, err)
	}
	f.welcome = e.Welcome
	go f.run(opts)
	t.Cleanup(func() {
		f.disconnect()
		select {
		case err := <-f.errors:
			t.Errorf("fake node: %v", err)
		default:
		}
	})
	return f
}

func (f *fakeNode) disconnect() {
	f.cancel()
	f.conn.CloseNow()
	f.mu.Lock()
	for _, conn := range f.active {
		conn.Close()
	}
	f.mu.Unlock()
	<-f.done
}

func (f *fakeNode) run(opts fakeOptions) {
	var workers sync.WaitGroup
	var sessions []*mcp.ServerSession
	defer close(f.done)
	defer func() {
		f.cancel()
		f.conn.CloseNow()
		f.mu.Lock()
		for _, conn := range f.active {
			conn.Close()
		}
		f.mu.Unlock()
		for _, session := range sessions {
			session.Close()
		}
		workers.Wait()
	}()
	report := func(err error) {
		select {
		case f.errors <- err:
		default:
		}
	}
	for {
		e, err := f.conn.Recv(f.ctx)
		if err != nil {
			return
		}
		switch e.T {
		case proto.TOpen:
			f.mu.Lock()
			f.openCount++
			f.mu.Unlock()
			if opts.failAfterFirst && len(sessions) > 0 {
				opts.failOpens++
			}
			select {
			case f.opens <- *e:
			case <-f.ctx.Done():
				return
			}
			if opts.permits != nil {
				select {
				case <-opts.permits:
				case <-f.ctx.Done():
					return
				}
			}
			if opts.failOpens > 0 {
				opts.failOpens--
				message := opts.openError
				if message == "" {
					message = "process failed to start"
				}
				typ := proto.TOpened
				if opts.closeOnOpen {
					typ = proto.TClose
				}
				if err := f.conn.Send(f.ctx, &proto.Envelope{T: typ, Ch: e.Ch, Error: message}); err != nil {
					return
				}
				continue
			}
			a, b := mcp.NewInMemoryTransports()
			server := f.server
			if opts.serverFor != nil {
				if custom := opts.serverFor(e); custom != nil {
					server = custom
				}
			}
			session, err := server.Connect(f.ctx, a, nil)
			if err != nil {
				report(err)
				return
			}
			sessions = append(sessions, session)
			upstream, err := b.Connect(f.ctx)
			if err != nil {
				report(err)
				return
			}
			f.mu.Lock()
			f.active[e.Ch] = upstream
			f.mu.Unlock()
			if err := f.conn.Send(f.ctx, &proto.Envelope{T: proto.TOpened, Ch: e.Ch}); err != nil {
				return
			}
			id := e.Ch
			workers.Go(func() { f.pump(id, upstream) })
		case proto.TMCP:
			f.mu.Lock()
			upstream := f.active[e.Ch]
			f.mu.Unlock()
			if upstream != nil {
				msg, err := jsonrpc.DecodeMessage(e.Msg)
				if err != nil {
					report(err)
					return
				}
				if req, ok := msg.(*jsonrpc.Request); ok && opts.closeOnMethod != "" && req.Method == opts.closeOnMethod {
					f.mu.Lock()
					delete(f.active, e.Ch)
					f.mu.Unlock()
					if err := f.conn.Send(f.ctx, &proto.Envelope{T: proto.TClose, Ch: e.Ch, Error: opts.openError}); err != nil {
						return
					}
					upstream.Close()
					continue
				}
				// A close racing an already relayed frame is expected.
				upstream.Write(f.ctx, msg)
			}
		case proto.TClose:
			f.mu.Lock()
			if upstream := f.active[e.Ch]; upstream != nil {
				upstream.Close()
				delete(f.active, e.Ch)
			}
			f.mu.Unlock()
		}
	}
}

func (f *fakeNode) pump(id string, upstream mcp.Connection) {
	for {
		msg, err := upstream.Read(f.ctx)
		if err != nil {
			f.mu.Lock()
			_, active := f.active[id]
			delete(f.active, id)
			f.mu.Unlock()
			if active && f.ctx.Err() == nil {
				f.conn.Send(f.ctx, &proto.Envelope{T: proto.TClose, Ch: id, Error: "upstream exited"})
			}
			return
		}
		raw, err := jsonrpc.EncodeMessage(msg)
		if err != nil {
			return
		}
		if err := f.conn.Send(f.ctx, &proto.Envelope{T: proto.TMCP, Ch: id, Msg: raw}); err != nil {
			return
		}
	}
}

func (f *fakeNode) countOpens() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.openCount
}

func (f *fakeNode) closeChannel(t *testing.T, id string, reason proto.CloseReason) {
	t.Helper()
	f.mu.Lock()
	upstream := f.active[id]
	delete(f.active, id)
	f.mu.Unlock()
	if err := f.conn.Send(t.Context(), &proto.Envelope{T: proto.TClose, Ch: id, Reason: reason}); err != nil {
		t.Fatal(err)
	}
	if upstream != nil {
		upstream.Close()
	}
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	awaitContext(t, t.Context(), condition)
}

func awaitContext(t *testing.T, ctx context.Context, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("condition did not become true before deadline")
		case <-ticker.C:
		}
	}
}

func nextOpen(t *testing.T, f *fakeNode) proto.Envelope {
	t.Helper()
	select {
	case open := <-f.opens:
		if !strings.HasPrefix(open.Ch, "c_") {
			t.Fatalf("invalid channel ID %q", open.Ch)
		}
		return open
	case <-time.After(5 * time.Second):
		t.Fatal("no open received")
		return proto.Envelope{}
	}
}

func TestRegistryCatalog(t *testing.T) {
	for _, sessionBased := range []bool{false, true} {
		t.Run(fmt.Sprintf("sessionBased=%v", sessionBased), func(t *testing.T) {
			r, cat, url := newTestRegistry(t)
			f := startFake(t, url, hello("machine"), fakeOptions{sessionBased: sessionBased})
			if f.welcome.NodeID == "" || f.welcome.MaxMessageSize != proto.DefaultMaxMessageSize {
				t.Fatalf("welcome: %+v", f.welcome)
			}
			await(t, func() bool {
				nodes := r.Nodes()
				return len(cat.List()) == 2 && len(nodes) == 1 && nodes[0].MCPs[0].State == MCPReady
			})
			nodes := r.Nodes()
			info := nodes[0]
			if info.Name != "machine" || info.NodeVersion != "test-node" || info.OS != "test-os" || info.RemoteAddr == "" || info.ConnectedAt.IsZero() || !info.Online || !info.DisconnectedAt.IsZero() {
				t.Fatalf("node info: %+v", info)
			}
			if got := info.MCPs[0]; got.Tools != 2 || got.Server != "fake-upstream 1.2.3" || got.Error != "" || got.Sharing != proto.SharingShared {
				t.Fatalf("MCP info: %+v", got)
			}
			// Snapshots must not expose the registry's mutable MCP slice.
			nodes[0].MCPs[0].Name = "changed"
			if r.Nodes()[0].MCPs[0].Name != "tools" {
				t.Fatal("snapshot changed registry")
			}
			tool, ok := cat.Lookup("machine__tools__echo")
			if !ok || tool.Exposed().Name != "machine__tools__echo" {
				t.Fatal("missing exposed tool")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			result, err := tool.Call(ctx, map[string]any{})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "result from echo" {
				t.Fatalf("tool result: %+v", result)
			}
			for _, name := range []string{"third", "fourth", "fifth"} {
				addTool(f.server, name)
			}
			await(t, func() bool { return len(cat.List()) == 5 && r.Nodes()[0].MCPs[0].Tools == 5 })
			f.disconnect()
			await(t, func() bool { return len(r.Nodes()) == 1 && !r.Nodes()[0].Online })
			info = r.Nodes()[0]
			if info.DisconnectedAt.IsZero() || info.MCPs[0].State != MCPClosed || info.MCPs[0].Tools != 5 || len(cat.List()) != 5 {
				t.Fatalf("offline snapshot: %+v; catalog: %+v", info, cat.List())
			}
			if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("offline call: %v, want ErrUnavailable", err)
			}
			await(t, func() bool { return len(cat.List()) == 0 && len(r.Nodes()) == 0 })
		})
	}
}

func TestRegistryRejectedHello(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	startFake(t, url, hello("taken"), fakeOptions{})
	await(t, func() bool { return len(cat.List()) == 2 })
	tests := []struct {
		name string
		edit func(*proto.Hello)
		code websocket.StatusCode
	}{
		{"duplicate node", func(h *proto.Hello) { h.Node = "taken" }, proto.CloseNodeNameTaken},
		{"version", func(h *proto.Hello) { h.Version = proto.MinVersion - 1 }, proto.CloseUnsupportedVersion},
		{"empty node", func(h *proto.Hello) { h.Node = "" }, websocket.StatusPolicyViolation},
		{"empty MCP", func(h *proto.Hello) { h.MCPs[0].Name = "" }, websocket.StatusPolicyViolation},
		{"duplicate MCP", func(h *proto.Hello) { h.MCPs = append(h.MCPs, h.MCPs[0]) }, websocket.StatusPolicyViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, err := wire.Dial(ctx, url, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			h := hello("new")
			tt.edit(h)
			if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: h}); err != nil {
				t.Fatal(err)
			}
			_, err = conn.Recv(ctx)
			if got := wire.CloseStatus(err); got != tt.code {
				t.Fatalf("close status %d, want %d: %v", got, tt.code, err)
			}
			if len(r.Nodes()) != 1 || len(cat.List()) != 2 {
				t.Fatal("rejected connection disturbed existing node")
			}
		})
	}
}

func TestRegistryReopen(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	permits := make(chan struct{}, 3)
	permits <- struct{}{}
	f := startFake(t, url, hello("machine"), fakeOptions{failOpens: 1, permits: permits})
	first := nextOpen(t, f)
	await(t, func() bool {
		info := r.Nodes()[0].MCPs[0]
		return info.State == MCPFailed && strings.Contains(info.Error, "process failed to start") && info.Tools == 0
	})
	second := nextOpen(t, f)
	if second.Ch == first.Ch {
		t.Fatal("reused failed channel ID")
	}
	permits <- struct{}{}
	await(t, func() bool { return len(cat.List()) == 2 && r.Nodes()[0].MCPs[0].State == MCPReady })
	tool, _ := cat.Lookup("machine__tools__echo")
	if err := f.conn.Send(t.Context(), &proto.Envelope{T: proto.TClose, Ch: second.Ch, Error: "process crashed"}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		info := r.Nodes()[0].MCPs[0]
		return len(cat.List()) == 2 && info.Tools == 2 && info.State == MCPClosed && strings.Contains(info.Error, "process crashed")
	})
	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("crashed MCP call: %v, want ErrUnavailable", err)
	}
	third := nextOpen(t, f)
	if third.Ch == second.Ch {
		t.Fatal("reused closed channel ID")
	}
	permits <- struct{}{}
	await(t, func() bool { return len(cat.List()) == 2 && r.Nodes()[0].MCPs[0].State == MCPReady })
	fresh, _ := cat.Lookup(tool.Name)
	if fresh.Caller != tool.Caller {
		t.Fatal("MCP reopen replaced routing handle")
	}
	if _, err := tool.Call(t.Context(), nil); err != nil {
		t.Fatalf("old caller after MCP reopen: %v", err)
	}
}

func TestRegistrySortedNodesAndMCPs(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	h := hello("zeta")
	h.MCPs = append([]proto.MCP{{Name: "zulu", Transport: proto.TransportHTTP}}, h.MCPs...)
	startFake(t, url, h, fakeOptions{})
	startFake(t, url, hello("alpha"), fakeOptions{})
	await(t, func() bool { return len(cat.List()) == 6 })
	infos := r.Nodes()
	if len(infos) != 2 || infos[0].Name != "alpha" || infos[1].Name != "zeta" || infos[1].MCPs[0].Name != "tools" || infos[1].MCPs[1].Name != "zulu" {
		t.Fatalf("unsorted snapshots: %+v", infos)
	}
}

func TestRegistryFirstFrame(t *testing.T) {
	for _, frame := range []*proto.Envelope{{T: proto.TOpened}, {T: proto.THello}} {
		t.Run(fmt.Sprintf("%s/hello=%v", frame.T, frame.Hello), func(t *testing.T) {
			r, _, url := newTestRegistry(t)
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			conn, err := wire.Dial(ctx, url, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Send(ctx, frame); err != nil {
				t.Fatal(err)
			}
			_, err = conn.Recv(ctx)
			if wire.CloseStatus(err) != websocket.StatusPolicyViolation || len(r.Nodes()) != 0 {
				t.Fatalf("invalid first frame: %v; nodes=%+v", err, r.Nodes())
			}
		})
	}
}

func TestRegistryHelloTimeout(t *testing.T) {
	r := NewRegistry(Options{})
	r.helloTimeout = 20 * time.Millisecond
	server := httptest.NewServer(r)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := wire.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if _, err := conn.Recv(ctx); err == nil || ctx.Err() != nil {
		t.Fatalf("hello timeout failed to close promptly: %v", err)
	}
	if len(r.Nodes()) != 0 {
		t.Fatal("node registered without hello")
	}
}

func TestRegistryDisconnectWhileOpening(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	f := startFake(t, url, hello("machine"), fakeOptions{permits: make(chan struct{})})
	nextOpen(t, f)
	if info := r.Nodes()[0].MCPs[0]; info.State != MCPOpening || info.Tools != 0 {
		t.Fatalf("opening snapshot: %+v", info)
	}
	f.disconnect()
	await(t, func() bool { return len(r.Nodes()) == 1 && !r.Nodes()[0].Online && len(cat.List()) == 0 })
	// An offline name can be taken over even while its grace timer is pending.
	startFake(t, url, hello("machine"), fakeOptions{})
	await(t, func() bool { return len(cat.List()) == 2 })
}

func TestRegistryNegotiatesVersion(t *testing.T) {
	_, _, url := newTestRegistryOptions(t, Options{OfflineGrace: 500 * time.Millisecond, Version: "v1.4.0"})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := wire.Dial(ctx, url, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// A newer node is accepted and told to speak the hub's version.
	h := hello("future")
	h.Version = proto.Version + 1
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: h}); err != nil {
		t.Fatal(err)
	}
	e, err := conn.Recv(ctx)
	if err != nil || e.T != proto.TWelcome || e.Welcome == nil {
		t.Fatalf("got %+v, %v; want welcome", e, err)
	}
	if e.Welcome.Version != proto.Version {
		t.Fatalf("welcome version = %d, want %d", e.Welcome.Version, proto.Version)
	}
	// The hub's release version, for the node's notices.
	if e.Welcome.HubVersion != "v1.4.0" {
		t.Fatalf("welcome hub version = %q", e.Welcome.HubVersion)
	}
}
