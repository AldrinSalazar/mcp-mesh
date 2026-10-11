package nodes

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

const keyHeader = "X-Test-Key"

func keyedOptions() Options {
	return Options{OfflineGrace: 500 * time.Millisecond, KeyID: func(r *http.Request) string {
		return r.Header.Get(keyHeader)
	}}
}

func keyHeaderFor(id string) http.Header { return http.Header{keyHeader: {id}} }

// dialRaw connects a node that only reads, so the test sees the close code.
func dialRaw(t *testing.T, url, key, name string) (*wire.Conn, context.Context) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	conn, err := wire.Dial(ctx, url, keyHeaderFor(key), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: hello(name)}); err != nil {
		t.Fatal(err)
	}
	if e, err := conn.Recv(ctx); err != nil || e.T != proto.TWelcome {
		t.Fatalf("welcome: %v, %v", e, err)
	}
	return conn, ctx
}

func TestRegistryKeyIDInNodeInfo(t *testing.T) {
	r, _, url := newTestRegistryOptions(t, keyedOptions())
	startFakeHeader(t, url, keyHeaderFor("k1"), hello("node1"), fakeOptions{})
	startFake(t, url, hello("node2"), fakeOptions{})
	await(t, func() bool { return len(r.Nodes()) == 2 })
	got := map[string]string{}
	for _, n := range r.Nodes() {
		got[n.Name] = n.KeyID
	}
	if got["node1"] != "k1" || got["node2"] != "" {
		t.Fatalf("key IDs: %v", got)
	}
}

func TestRegistryDisconnectKey(t *testing.T) {
	r, _, url := newTestRegistryOptions(t, keyedOptions())
	conn1, ctx := dialRaw(t, url, "k1", "node1")
	startFakeHeader(t, url, keyHeaderFor("k2"), hello("node2"), fakeOptions{})
	await(t, func() bool { return len(r.Nodes()) == 2 })

	for _, id := range []string{"", "unknown"} {
		if got := r.DisconnectKey(id); len(got) != 0 {
			t.Fatalf("DisconnectKey(%q) = %v, want none", id, got)
		}
	}
	for _, n := range r.Nodes() {
		if !n.Online {
			t.Fatalf("%s went offline", n.Name)
		}
	}

	if got := r.DisconnectKey("k1"); !slices.Equal(got, []string{"node1"}) {
		t.Fatalf("DisconnectKey(k1) = %v", got)
	}
	if err := recvUntilClosed(ctx, conn1); wire.CloseStatus(err) != proto.CloseInvalidKey {
		t.Fatalf("node1 close: %v, want code %d", err, proto.CloseInvalidKey)
	}
	await(t, func() bool {
		for _, n := range r.Nodes() {
			if n.Name == "node1" && n.Online {
				return false
			}
		}
		return true
	})
	for _, n := range r.Nodes() {
		if n.Name == "node2" && !n.Online {
			t.Fatal("node2 was disconnected")
		}
	}
}

func TestRegistryDisconnectKeyAfterReconnectWithOtherKey(t *testing.T) {
	r, _, url := newTestRegistryOptions(t, keyedOptions())
	conn1, ctx := dialRaw(t, url, "old", "machine")
	await(t, func() bool { return len(r.Nodes()) == 1 && r.Nodes()[0].Online })
	if got := r.DisconnectKey("old"); !slices.Equal(got, []string{"machine"}) {
		t.Fatalf("DisconnectKey(old) = %v", got)
	}
	recvUntilClosed(ctx, conn1)
	await(t, func() bool { return !r.Nodes()[0].Online })

	startFakeHeader(t, url, keyHeaderFor("new"), hello("machine"), fakeOptions{})
	await(t, func() bool { return r.Nodes()[0].Online && r.Nodes()[0].KeyID == "new" })
	if got := r.DisconnectKey("old"); len(got) != 0 {
		t.Fatalf("DisconnectKey(old) hit the new connection: %v", got)
	}
	if !r.Nodes()[0].Online {
		t.Fatal("replacement connection was closed")
	}
}

func TestRegistryDisconnectKeyFailsInFlightCalls(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, keyedOptions())
	entered := make(chan struct{}, 1)
	startFakeHeader(t, url, keyHeaderFor("k1"), hello("node1"), fakeOptions{configure: func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "hang", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			})
	}})
	await(t, func() bool { return len(cat.List()) == 3 })
	tool, _ := cat.Lookup("node1__tools__hang")
	// Far longer than the bounded wait below: only revocation can end the call.
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := tool.Call(ctx, nil)
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("call never reached the node")
	}
	if got := r.DisconnectKey("k1"); !slices.Equal(got, []string{"node1"}) {
		t.Fatalf("DisconnectKey = %v", got)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("in-flight call succeeded after its key was revoked")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight call still waiting after DisconnectKey")
	}
}

// A node whose upgrade was authenticated but whose hello arrives after its
// key was revoked found nothing to disconnect at revocation time; it must
// not register afterwards.
func TestRegistryRefusesHelloAfterRevocation(t *testing.T) {
	r, _, url := newTestRegistryOptions(t, keyedOptions())
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	conn, err := wire.Dial(ctx, url, keyHeaderFor("k1"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	// The upgrade is done (the key was checked); the hello is late.
	if got := r.DisconnectKey("k1"); len(got) != 0 {
		t.Fatalf("DisconnectKey = %v, want nothing online yet", got)
	}
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: hello("node1")}); err != nil {
		t.Fatal(err)
	}
	if err := recvUntilClosed(ctx, conn); wire.CloseStatus(err) != proto.CloseInvalidKey {
		t.Fatalf("close: %v, want code %d", err, proto.CloseInvalidKey)
	}
	if n := r.Nodes(); len(n) != 0 {
		t.Fatalf("revoked node registered: %+v", n)
	}
	// Other keys are unaffected.
	startFakeHeader(t, url, keyHeaderFor("k2"), hello("node1"), fakeOptions{})
	await(t, func() bool { return len(r.Nodes()) == 1 && r.Nodes()[0].Online })
}

// A per-client process that is still starting when its client's key is
// revoked is not in the handle's sessions yet, so the revocation can't close
// it; the open must not publish it once it finishes.
func TestPerClientOpenFinishingAfterRevocation(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	permits := make(chan struct{})
	tagger := &sessionTagger{tags: map[*mcp.ServerSession]int{}}
	f := startFake(t, url, perClientHello(t, true), fakeOptions{configure: tagger.configure, permits: permits})
	var tool *catalog.Tool
	await(t, func() bool {
		var ok bool
		tool, ok = cat.Lookup("machine__tools__whoami")
		return ok
	})
	alice := identity.With(t.Context(), identity.Client{ID: "id-alice", Name: "alice"})
	result := make(chan error, 1)
	go func() {
		_, err := tool.Call(alice, map[string]any{})
		result <- err
	}()
	if open := nextOpen(t, f); open.Client != "alice" {
		t.Fatalf("open for %q, want alice", open.Client)
	}
	// The node holds the open (no permit yet) while the key is revoked.
	if n := r.CloseClient("id-alice"); n != 0 {
		t.Fatalf("CloseClient closed %d sessions, want 0 (still opening)", n)
	}
	permits <- struct{}{}
	if err := <-result; !errors.Is(err, errClientRevoked) {
		t.Fatalf("call = %v, want %v", err, errClientRevoked)
	}
	// The process the open started is stopped, and none is published.
	await(t, func() bool {
		f.mu.Lock()
		defer f.mu.Unlock()
		return len(f.active) == 0
	})
	if c := infoOf(r, "machine").MCPs[0].Clients; c != 0 {
		t.Fatalf("clients = %d, want 0", c)
	}
	// Later calls with the key are refused without starting a process.
	opens := f.countOpens()
	if _, err := tool.Call(alice, map[string]any{}); !errors.Is(err, errClientRevoked) {
		t.Fatalf("later call = %v, want %v", err, errClientRevoked)
	}
	if f.countOpens() != opens {
		t.Fatal("a revoked client started a process")
	}
	// Other clients are unaffected.
	go func() { permits <- struct{}{} }()
	if got := whoami(t, tool, "bob"); got == "" {
		t.Fatal("bob got no session")
	}
}

// recvUntilClosed reads past the hub's open frames to the connection's end.
func recvUntilClosed(ctx context.Context, conn *wire.Conn) error {
	for {
		if _, err := conn.Recv(ctx); err != nil {
			return err
		}
	}
}
