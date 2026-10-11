package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func newUpstream() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{Name: "fake-upstream", Version: "1.2.3"}, nil)
}

// rewriteToolList lets a server announce whatever a hostile MCP might,
// including entries the SDK would never produce itself.
func rewriteToolList(server *mcp.Server, rewrite func(*mcp.ListToolsResult)) {
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
				rewrite(list)
			}
			return res, err
		}
	})
}

func TestBadDefinitionsAreDroppedAndReported(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	bad := newUpstream()
	addTool(bad, "good")
	rewriteToolList(bad, func(l *mcp.ListToolsResult) {
		l.NextCursor = ""
		l.Tools = append(l.Tools,
			&mcp.Tool{Name: "noschema"},
			&mcp.Tool{Name: "badschema", InputSchema: 5},
		)
	})
	h := hello("machine")
	h.MCPs = append(h.MCPs, proto.MCP{Name: "other", Transport: proto.TransportStdio, Sharing: proto.SharingShared})
	startFake(t, url, h, fakeOptions{serverFor: func(e *proto.Envelope) *mcp.Server {
		if e.MCP == "tools" {
			return bad
		}
		return nil
	}})
	await(t, func() bool {
		n := infoOf(r, "machine")
		return len(n.MCPs) == 2 && n.MCPs[0].State == MCPReady && n.MCPs[1].State == MCPReady
	})
	var tools, other MCPInfo
	for _, m := range infoOf(r, "machine").MCPs {
		if m.Name == "tools" {
			tools = m
		} else {
			other = m
		}
	}
	if tools.Tools != 1 || len(tools.Problems) != 2 {
		t.Fatalf("tools MCP: tools=%d problems=%q", tools.Tools, tools.Problems)
	}
	joined := strings.Join(tools.Problems, "\n")
	for _, want := range []string{"noschema", "badschema"} {
		if !strings.Contains(joined, want) {
			t.Errorf("problems %q lack %q", tools.Problems, want)
		}
	}
	if len(other.Problems) != 0 || other.Tools != 2 {
		t.Errorf("other MCP: %+v", other)
	}
	good, ok := cat.Lookup("machine__tools__good")
	if !ok {
		t.Fatal("good tool missing from the catalog")
	}
	for _, name := range []string{"noschema", "badschema"} {
		if _, ok := cat.Lookup("machine__tools__" + name); ok {
			t.Errorf("%s is in the catalog", name)
		}
	}
	echo, ok := cat.Lookup("machine__other__echo")
	if !ok {
		t.Fatal("other MCP's tool missing")
	}
	if _, err := echo.Call(t.Context(), map[string]any{}); err != nil {
		t.Fatalf("other MCP's call: %v", err)
	}
	if _, err := good.Call(t.Context(), map[string]any{}); err != nil {
		t.Fatalf("good tool call: %v", err)
	}
}

func TestBadCachedDefinitions(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	h := hello("machine")
	h.MCPs[0].Start = proto.StartLazy
	h.MCPs[0].Tools = json.RawMessage(`[{"name":"bad","inputSchema":5},{"name":"ok","inputSchema":{"type":"object"}}]`)
	startFake(t, url, h, fakeOptions{})
	await(t, func() bool {
		n := infoOf(r, "machine")
		return len(n.MCPs) == 1 && n.MCPs[0].State == MCPIdle
	})
	info := infoOf(r, "machine").MCPs[0]
	if info.Tools != 1 || len(info.Problems) != 1 || !strings.Contains(info.Problems[0], "bad") {
		t.Fatalf("cached snapshot: tools=%d problems=%q", info.Tools, info.Problems)
	}
	if _, ok := cat.Lookup("machine__tools__ok"); !ok {
		t.Error("good cached tool missing")
	}
	if _, ok := cat.Lookup("machine__tools__bad"); ok {
		t.Error("bad cached tool listed")
	}
}

func TestProblemsClearOnCleanListing(t *testing.T) {
	r, _, url := newTestRegistry(t)
	srv := newUpstream()
	addTool(srv, "good")
	var poisoned atomic.Bool
	poisoned.Store(true)
	rewriteToolList(srv, func(l *mcp.ListToolsResult) {
		if poisoned.Load() {
			l.NextCursor, l.Tools = "", append(l.Tools, &mcp.Tool{Name: "noschema"})
		}
	})
	startFake(t, url, hello("machine"), fakeOptions{serverFor: func(*proto.Envelope) *mcp.Server { return srv }})
	await(t, func() bool {
		return len(infoOf(r, "machine").MCPs) == 1 && len(infoOf(r, "machine").MCPs[0].Problems) == 1
	})
	poisoned.Store(false)
	addTool(srv, "another") // list_changed: the hub lists again
	await(t, func() bool {
		m := infoOf(r, "machine").MCPs[0]
		return m.Tools == 2 && len(m.Problems) == 0
	})
}

// blockingFake offers a "block" tool that holds its slot until released.
func blockingFake(entered chan<- struct{}, release <-chan struct{}) func(*mcp.Server) {
	return func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "block", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				entered <- struct{}{}
				select {
				case <-release:
					return &mcp.CallToolResult{}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			})
	}
}

func TestCheckRunsAfterSlotAcquired(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond, MaxInFlight: 1})
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	startFake(t, url, hello("machine"), fakeOptions{configure: blockingFake(entered, release)})
	await(t, func() bool { return len(cat.List()) == 3 })
	block, _ := cat.Lookup("machine__tools__block")
	echo, _ := cat.Lookup("machine__tools__echo")

	var workers sync.WaitGroup
	t.Cleanup(workers.Wait)
	first := make(chan error, 1)
	workers.Go(func() {
		_, err := block.Call(t.Context(), map[string]any{})
		first <- err
	})
	select {
	case <-entered:
	case <-t.Context().Done():
		t.Fatal("first call did not reach upstream")
	}

	errDenied := &jsonrpc.Error{Code: -32001, Message: "approval revoked"}
	var ran atomic.Int32
	inFlightAtCheck := make(chan int, 1)
	ctx := catalog.WithCheck(t.Context(), func(catalog.Listed) error {
		ran.Add(1)
		inFlightAtCheck <- infoOf(r, "machine").MCPs[0].InFlight
		return errDenied
	})
	second := make(chan error, 1)
	workers.Go(func() {
		_, err := block.Call(ctx, map[string]any{})
		second <- err
	})
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].Queued == 1 })
	if ran.Load() != 0 {
		t.Fatal("check ran while the call was still queued")
	}
	close(release)
	select {
	case err := <-second:
		if err != errDenied {
			t.Fatalf("second call: %v, want the check's error unchanged", err)
		}
	case <-t.Context().Done():
		t.Fatal("second call did not finish")
	}
	if err := <-first; err != nil {
		t.Fatalf("first call: %v", err)
	}
	if got := <-inFlightAtCheck; got != 1 {
		t.Errorf("in flight while checking = %d, want 1 (the check holds the slot)", got)
	}
	if len(entered) != 0 {
		t.Error("the denied call reached the upstream")
	}
	// The slot went back.
	if _, err := echo.Call(t.Context(), map[string]any{}); err != nil {
		t.Fatalf("call after denied one: %v", err)
	}
	await(t, func() bool { m := infoOf(r, "machine").MCPs[0]; return m.InFlight == 0 && m.Queued == 0 })
}

// A call waiting for a lazy start must be checked against the definition
// the started process lists, so the catalog is refreshed before the handle
// is marked ready. afterReady holds the worker right after "ready" until the
// waiting call's check has run: if publishing came after "ready", the check
// would see the cached definition every time.
func TestLazyStartPublishesBeforeReady(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	checked := make(chan struct{})
	r.afterReady = func(string, string) {
		select {
		case <-checked:
		case <-t.Context().Done():
		}
	}
	h := lazyHello(t, true) // cached echo: description "cached"
	fresh := newUpstream()
	fresh.AddTool(&mcp.Tool{Name: "echo", Description: "fresh", InputSchema: map[string]any{"type": "object"}},
		func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{}, nil
		})
	startFake(t, url, h, fakeOptions{serverFor: func(*proto.Envelope) *mcp.Server { return fresh }})
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].State == MCPIdle })
	tool, _ := cat.Lookup("machine__tools__echo")
	if tool.Upstream.Description != "cached" {
		t.Fatalf("precondition: %q", tool.Upstream.Description)
	}
	var seen string
	ctx := catalog.WithCheck(t.Context(), func(catalog.Listed) error {
		defer close(checked)
		cur, ok := cat.ToolByKey(catalog.ToolKey{Node: "machine", MCP: "tools", Tool: "echo"})
		if !ok {
			seen = "<missing>"
			return nil
		}
		seen = cur.Upstream.Description
		return nil
	})
	if _, err := tool.Call(ctx, map[string]any{}); err != nil {
		t.Fatalf("call: %v", err)
	}
	if seen != "fresh" {
		t.Errorf("check saw definition %q, want the refreshed one", seen)
	}
}

// perClientServers returns, for an open, the listing server when no client
// is named and client(n) for the n-th client open.
func perClientServers(listing *mcp.Server, client func(n int) *mcp.Server) func(*proto.Envelope) *mcp.Server {
	var mu sync.Mutex
	opens := 0
	return func(e *proto.Envelope) *mcp.Server {
		if e.Client == "" {
			return listing
		}
		mu.Lock()
		defer mu.Unlock()
		opens++
		return client(opens)
	}
}

func callAs(t *testing.T, tool *catalog.Tool, client string) error {
	t.Helper()
	ctx := identity.With(t.Context(), identity.Client{ID: "id-" + client, Name: client})
	_, err := tool.Call(ctx, map[string]any{})
	return err
}

// A per-client process whose tools differ from the listing (or from the
// node's cache, when the MCP was never listed) has its tools published
// before the call goes on, so the admission check judges the call against
// what this process offers: a changed definition is refused, an unchanged
// one runs.
func TestPerClientProcessToolsArePublished(t *testing.T) {
	changedT1 := func(s *mcp.Server) {
		s.AddTool(&mcp.Tool{Name: "t1", Description: "different", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{}, nil
			})
	}
	errChanged := errors.New("definition is not the approved one")
	tests := []struct {
		name   string
		cached bool // the MCP starts from the node's cache and is never listed
		client func(*mcp.Server)
		want   error    // from the call
		tools  []string // in the catalog afterwards
	}{
		{"same tools", false, func(*mcp.Server) {}, nil, []string{"t1"}},
		{"extra tool", false, func(s *mcp.Server) { addTool(s, "t2") }, nil, []string{"t1", "t2"}},
		{"changed tool", false, changedT1, errChanged, []string{"t1"}},
		{"changed tool, cached MCP", true, changedT1, errChanged, []string{"t1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cat, url := newTestRegistry(t)
			listing := newUpstream()
			addTool(listing, "t1")
			var upstreamCalls atomic.Int32
			h := perClientHello(t, false)
			if tt.cached {
				data, _ := json.Marshal([]*mcp.Tool{{Name: "t1", InputSchema: map[string]any{"type": "object"}}})
				h.MCPs[0].Tools = data
			}
			startFake(t, url, h, fakeOptions{
				serverFor: perClientServers(listing, func(int) *mcp.Server {
					s := newUpstream()
					addTool(s, "t1")
					tt.client(s)
					s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
						return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
							if method == "tools/call" {
								upstreamCalls.Add(1)
							}
							return next(ctx, method, req)
						}
					})
					return s
				}),
			})
			await(t, func() bool { _, ok := cat.Lookup("machine__tools__t1"); return ok })
			tool, _ := cat.Lookup("machine__tools__t1")
			key := catalog.ToolKey{Node: "machine", MCP: "tools", Tool: "t1"}
			// The check plays the gateway with t1's first definition approved.
			ctx := identity.With(t.Context(), identity.Client{ID: "id-alice", Name: "alice"})
			ctx = catalog.WithCheck(ctx, func(catalog.Listed) error {
				if cur, ok := cat.ToolByKey(key); !ok || cur.Upstream.Description != "" {
					return errChanged
				}
				return nil
			})
			_, err := tool.Call(ctx, map[string]any{})
			if !errors.Is(err, tt.want) {
				t.Fatalf("call: %v, want %v", err, tt.want)
			}
			if got := upstreamCalls.Load(); (tt.want == nil) != (got == 1) {
				t.Errorf("upstream calls = %d", got)
			}
			var names []string
			for _, tl := range cat.List() {
				names = append(names, tl.Upstream.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.tools, ",") {
				t.Errorf("catalog tools = %v, want %v", names, tt.tools)
			}
		})
	}
}

func TestPerClientToolChangeEndsSession(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	listing := newUpstream()
	addTool(listing, "t1")
	var mu sync.Mutex
	var clientServers []*mcp.Server
	f := startFake(t, url, perClientHello(t, false), fakeOptions{
		serverFor: perClientServers(listing, func(int) *mcp.Server {
			s := newUpstream()
			addTool(s, "t1")
			mu.Lock()
			clientServers = append(clientServers, s)
			mu.Unlock()
			return s
		}),
	})
	await(t, func() bool { _, ok := cat.Lookup("machine__tools__t1"); return ok })
	tool, _ := cat.Lookup("machine__tools__t1")
	if err := callAs(t, tool, "alice"); err != nil {
		t.Fatal(err)
	}
	opens := f.countOpens() // listing + alice
	if opens != 2 {
		t.Fatalf("opens = %d, want 2", opens)
	}
	// Re-adding an identical tool announces a change without altering the set,
	// so the reopened process can pass verification again.
	mu.Lock()
	first := clientServers[0]
	mu.Unlock()
	addTool(first, "t1")
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].Clients == 0 })
	if err := callAs(t, tool, "alice"); err != nil {
		t.Fatalf("call after change: %v", err)
	}
	if got := f.countOpens(); got != opens+1 {
		t.Errorf("opens = %d, want %d (a new session after the change)", got, opens+1)
	}
	if n := infoOf(r, "machine").MCPs[0].Clients; n != 1 {
		t.Errorf("clients = %d, want 1", n)
	}
}

// Two clients' processes list different definitions of t1 and take turns
// being the catalog's. A call must be judged against what its own process
// lists: alice's process lists an unapproved t1, so her retry stays refused
// even after bob's process has republished the approved one.
func TestPerClientProcessMustMatchCatalog(t *testing.T) {
	_, cat, url := newTestRegistry(t)
	listing := newUpstream()
	addTool(listing, "t1")
	var aliceCalls, bobCalls atomic.Int32
	counted := func(s *mcp.Server, calls *atomic.Int32) *mcp.Server {
		s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method == "tools/call" {
					calls.Add(1)
				}
				return next(ctx, method, req)
			}
		})
		return s
	}
	startFake(t, url, perClientHello(t, false), fakeOptions{
		serverFor: func(e *proto.Envelope) *mcp.Server {
			switch e.Client {
			case "alice":
				s := newUpstream()
				s.AddTool(&mcp.Tool{Name: "t1", Description: "different", InputSchema: map[string]any{"type": "object"}},
					func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						return &mcp.CallToolResult{}, nil
					})
				return counted(s, &aliceCalls)
			case "bob":
				s := newUpstream()
				addTool(s, "t1")
				return counted(s, &bobCalls)
			}
			return listing
		},
	})
	await(t, func() bool { _, ok := cat.Lookup("machine__tools__t1"); return ok })
	tool, _ := cat.Lookup("machine__tools__t1")
	key := catalog.ToolKey{Node: "machine", MCP: "tools", Tool: "t1"}
	errChanged := errors.New("definition is not the approved one")
	call := func(client string) error {
		ctx := identity.With(t.Context(), identity.Client{ID: "id-" + client, Name: client})
		// Plays the gateway with t1's first definition approved.
		ctx = catalog.WithCheck(ctx, func(listed catalog.Listed) error {
			cur, ok := cat.ToolByKey(key)
			if !ok || cur.Upstream.Description != "" {
				return errChanged
			}
			return listed.Check(cur)
		})
		_, err := tool.Call(ctx, map[string]any{})
		return err
	}
	if err := call("alice"); !errors.Is(err, errChanged) {
		t.Fatalf("alice's first call: %v, want %v", err, errChanged)
	}
	if err := call("bob"); err != nil {
		t.Fatalf("bob's call: %v", err)
	}
	if cur, _ := cat.ToolByKey(key); cur.Upstream.Description != "" {
		t.Fatalf("precondition: bob's process republished %q", cur.Upstream.Description)
	}
	if err := call("alice"); !errors.Is(err, errChanged) {
		t.Fatalf("alice's retry: %v, want %v", err, errChanged)
	}
	if n := aliceCalls.Load(); n != 0 {
		t.Errorf("alice's process got %d calls", n)
	}
	if n := bobCalls.Load(); n != 1 {
		t.Errorf("bob's process got %d calls, want 1", n)
	}
}
