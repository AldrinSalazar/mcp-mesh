package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

func TestParseOffers(t *testing.T) {
	tool := json.RawMessage(`[{"name":"echo","inputSchema":{"type":"object"}}]`)
	h := &proto.Hello{Node: "machine", MCPs: []proto.MCP{
		{Name: "zeta", Transport: proto.TransportStdio},
		{Name: "lazy-cached", Start: proto.StartLazy, Tools: tool},
		{Name: "lazy-bad-cache", Start: proto.StartLazy, Tools: json.RawMessage(`[null]`)},
		{Name: "lazy-no-cache", Start: proto.StartLazy},
		{Name: "eager-with-cache", Start: proto.StartEager, Tools: tool},
	}}
	infos, cached := parseOffers(h, slog.New(slog.DiscardHandler))
	want := []struct {
		name  string
		start proto.Start
		state MCPState
	}{
		{"eager-with-cache", proto.StartEager, MCPOpening},
		{"lazy-bad-cache", proto.StartLazy, MCPOpening},
		{"lazy-cached", proto.StartLazy, MCPIdle},
		{"lazy-no-cache", proto.StartLazy, MCPOpening},
		{"zeta", proto.StartEager, MCPOpening},
	}
	if len(infos) != len(want) {
		t.Fatalf("got %d infos, want %d", len(infos), len(want))
	}
	for i, w := range want {
		if got := infos[i]; got.Name != w.name || got.Start != w.start || got.State != w.state || got.Sharing != proto.SharingShared {
			t.Errorf("info %d = %+v, want %+v", i, got, w)
		}
	}
	if len(cached) != 1 || len(cached["lazy-cached"]) != 1 || cached["lazy-cached"][0].Name != "echo" {
		t.Fatalf("cached = %v, want only lazy-cached's echo (eager MCPs list their own tools)", cached)
	}
}

func TestCheckHello(t *testing.T) {
	ok := func() *proto.Envelope { return &proto.Envelope{T: proto.THello, Hello: hello("machine")} }
	for _, tt := range []struct {
		name string
		e    *proto.Envelope
		code int // 0 = accepted
	}{
		{"valid", ok(), 0},
		{"no frame", nil, 1008},
		{"not hello", &proto.Envelope{T: proto.TOpen}, 1008},
		{"hello without body", &proto.Envelope{T: proto.THello}, 1008},
		{"old version", func() *proto.Envelope { e := ok(); e.Hello.Version = proto.MinVersion - 1; return e }(), proto.CloseUnsupportedVersion},
		{"blank node", func() *proto.Envelope { e := ok(); e.Hello.Node = " "; return e }(), 1008},
		{"blank MCP", func() *proto.Envelope { e := ok(); e.Hello.MCPs[0].Name = ""; return e }(), 1008},
		{"duplicate MCP", func() *proto.Envelope { e := ok(); e.Hello.MCPs = append(e.Hello.MCPs, e.Hello.MCPs[0]); return e }(), 1008},
	} {
		t.Run(tt.name, func(t *testing.T) {
			refused := checkHello(tt.e)
			switch {
			case tt.code == 0 && refused != nil:
				t.Fatalf("refused %+v", refused)
			case tt.code != 0 && (refused == nil || int(refused.code) != tt.code):
				t.Fatalf("refusal %+v, want code %d", refused, tt.code)
			}
		})
	}
}

// register and unregister are driven directly: they're the bookkeeping
// between connections, independent of the WebSocket.
func TestRegisterTakesOverPreviousConnection(t *testing.T) {
	cat := catalog.New()
	r := NewRegistry(Options{Catalog: cat, Logger: slog.New(slog.DiscardHandler), OfflineGrace: time.Hour})
	connect := func(mcps ...string) *node {
		h := &proto.Hello{Version: proto.Version, Node: "machine"}
		for _, name := range mcps {
			h.MCPs = append(h.MCPs, proto.MCP{Name: name, Start: proto.StartLazy,
				Tools: json.RawMessage(`[{"name":"t","inputSchema":{"type":"object"}}]`)})
		}
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		return newNode(r, ctx, cancel, nil, h, "192.0.2.1:1", "")
	}
	first := connect("kept", "dropped")
	if r.register(first) != nil {
		t.Fatal("first registration refused")
	}
	if len(cat.List()) != 2 {
		t.Fatalf("catalog has %d tools, want the 2 cached ones", len(cat.List()))
	}
	r.mu.Lock()
	first.info.MCPs[1].Server = "kept-server 1.0"
	r.mu.Unlock()
	if r.register(connect("kept")) == nil {
		t.Fatal("registered a second connection while the name is online")
	}
	r.unregister(first)
	if info := r.Nodes()[0]; info.Online || info.MCPs[0].State != MCPClosed {
		t.Fatalf("after unregister: %+v", info)
	}
	second := connect("kept")
	if r.register(second) != nil {
		t.Fatal("reconnect within grace refused")
	}
	if second.handles["kept"] != first.handles["kept"] {
		t.Fatal("reconnect replaced the MCP handle catalog callers hold")
	}
	if info, _ := second.mcpInfo("kept"); info.Server != "kept-server 1.0" {
		t.Fatalf("reconnect lost the last known server: %+v", info)
	}
	if _, ok := cat.Lookup("machine__dropped__t"); ok {
		t.Fatal("an MCP the node no longer offers stayed in the catalog")
	}
	if _, ok := cat.Lookup("machine__kept__t"); !ok {
		t.Fatal("kept MCP left the catalog")
	}
	r.unregister(second)
	r.mu.Lock()
	second.offlineTimer.Stop()
	r.mu.Unlock()
}

func TestRegistryAuthorize(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond, Authorize: func(req *http.Request, node string) error {
		if node != "allowed" {
			return errors.New("key is bound to another node")
		}
		return nil
	}})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := wire.Dial(ctx, url, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: hello("other")}); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Recv(ctx); wire.CloseStatus(err) != proto.CloseInvalidKey {
		t.Fatalf("unauthorized node: %v, want close %d", err, proto.CloseInvalidKey)
	}
	if len(r.Nodes()) != 0 {
		t.Fatal("refused node was registered")
	}
	startFake(t, url, hello("allowed"), fakeOptions{})
	await(t, func() bool { return len(cat.List()) == 2 })
}
