package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// sessionTagger answers "whoami" with a number per server session, so a test
// can tell which channel (process, on a real node) served a call.
type sessionTagger struct {
	mu   sync.Mutex
	tags map[*mcp.ServerSession]int
}

func (s *sessionTagger) configure(server *mcp.Server) {
	server.AddTool(&mcp.Tool{Name: "whoami", InputSchema: map[string]any{"type": "object"}},
		func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			s.mu.Lock()
			tag, ok := s.tags[req.Session]
			if !ok {
				tag = len(s.tags) + 1
				s.tags[req.Session] = tag
			}
			s.mu.Unlock()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(tag)}}}, nil
		})
}

func perClientHello(t *testing.T, cached bool) *proto.Hello {
	t.Helper()
	h := hello("machine")
	h.MCPs[0].Sharing, h.MCPs[0].Start = proto.SharingPerClient, proto.StartLazy
	if cached {
		tools, err := json.Marshal([]*mcp.Tool{{Name: "whoami", InputSchema: map[string]any{"type": "object"}}})
		if err != nil {
			t.Fatal(err)
		}
		h.MCPs[0].Tools = tools
	}
	return h
}

func whoami(t *testing.T, tool *catalog.Tool, client string) string {
	t.Helper()
	ctx := identity.With(t.Context(), identity.Client{ID: "id-" + client, Name: client})
	res, err := tool.Call(ctx, map[string]any{})
	if err != nil {
		t.Fatalf("%s call: %v", client, err)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestPerClientChannels(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	tagger := &sessionTagger{tags: map[*mcp.ServerSession]int{}}
	f := startFake(t, url, perClientHello(t, true), fakeOptions{configure: tagger.configure})
	var tool *catalog.Tool
	await(t, func() bool {
		var ok bool
		tool, ok = cat.Lookup("machine__tools__whoami")
		return ok
	})

	// Each client gets a channel of its own, opened on its first call and
	// reused after; the node learns which client it is for.
	alice := whoami(t, tool, "alice")
	if open := nextOpen(t, f); open.Client != "alice" {
		t.Fatalf("first open for %q, want alice", open.Client)
	}
	if again := whoami(t, tool, "alice"); again != alice {
		t.Errorf("alice's second call went to session %s, first to %s", again, alice)
	}
	bob := whoami(t, tool, "bob")
	bobOpen := nextOpen(t, f)
	if bobOpen.Client != "bob" || bob == alice {
		t.Fatalf("bob: open for %q, session %s (alice %s)", bobOpen.Client, bob, alice)
	}
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].Clients == 2 })

	// The node idling out one client's process ends only that session; its
	// next call opens a fresh one.
	f.closeChannel(t, bobOpen.Ch, proto.CloseIdle)
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].Clients == 1 })
	if again := whoami(t, tool, "alice"); again != alice {
		t.Errorf("alice moved to session %s after bob's idle-out", again)
	}
	bob2 := whoami(t, tool, "bob")
	if open := nextOpen(t, f); open.Client != "bob" || bob2 == bob || bob2 == alice {
		t.Errorf("bob after idle: open for %q, session %s", open.Client, bob2)
	}

	// Revoking a client closes its channels on the node.
	if n := r.CloseClient("id-alice"); n != 1 {
		t.Fatalf("CloseClient closed %d sessions, want 1", n)
	}
	await(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.active) == 1 // only bob's left
	})
	// The key is gone for good: no new session for it.
	ctx := identity.With(t.Context(), identity.Client{ID: "id-alice", Name: "alice"})
	if _, err := tool.Call(ctx, map[string]any{}); !errors.Is(err, errClientRevoked) {
		t.Errorf("alice after revocation: %v, want %v", err, errClientRevoked)
	}
}

func TestPerClientListingChannel(t *testing.T) {
	// Without cached tools the hub opens one channel with no client to list
	// them, then closes it so it doesn't hold one of the node's client slots.
	r, cat, url := newTestRegistry(t)
	tagger := &sessionTagger{tags: map[*mcp.ServerSession]int{}}
	f := startFake(t, url, perClientHello(t, false), fakeOptions{configure: tagger.configure})
	if open := nextOpen(t, f); open.Client != "" {
		t.Fatalf("listing open for client %q", open.Client)
	}
	await(t, func() bool {
		_, ok := cat.Lookup("machine__tools__whoami")
		return ok
	})
	await(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.active) == 0
	})
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].State == MCPIdle })
	tool, _ := cat.Lookup("machine__tools__whoami")
	whoami(t, tool, "carol")
	if open := nextOpen(t, f); open.Client != "carol" {
		t.Errorf("call opened for %q, want carol", open.Client)
	}
}

func infoOf(r *Registry, name string) NodeInfo {
	for _, n := range r.Nodes() {
		if n.Name == name {
			return n
		}
	}
	return NodeInfo{}
}
