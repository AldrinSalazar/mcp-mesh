package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/server"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

func TestServeSingleListener(t *testing.T) {
	dir := t.TempDir()
	out, _ := runKey(t, dir, "create", "client", "c1")
	clientSecret := secretIn(t, out, "client")
	out, _ = runKey(t, dir, "create", "node", "n1")
	nodeSecret := secretIn(t, out, "node")

	ready := make(chan addrs, 1)
	done := make(chan error, 1)
	go func() {
		done <- serve(t.Context(), config{Config: server.Config{
			// The per-role addresses would collide if they were used.
			AdminListen: "127.0.0.1:1", NodeListen: "127.0.0.1:1", ClientListen: "127.0.0.1:1",
			Listen:     "127.0.0.1:0",
			PublicHost: "localhost", CallTimeout: time.Minute, DataDir: dir, Auth: true,
			OnListen: func(a, n, c string) { ready <- addrs{a, n, c} },
		}, logLevel: "error", logFormat: "text"})
	}()
	t.Cleanup(func() {
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
	var a addrs
	select {
	case a = <-ready:
	case err := <-done:
		t.Fatalf("serve returned early: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("hub did not start")
	}
	if a.admin != a.node || a.node != a.client {
		t.Fatalf("roles on different addresses: %+v", a)
	}
	base := a.admin

	resp, err := http.Get("http://" + base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz: %d", resp.StatusCode)
	}

	resp, err = http.Post("http://"+base+"/mcp", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("/mcp without a key: %d", resp.StatusCode)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(t.Context(),
		&mcp.StreamableClientTransport{Endpoint: "http://" + base + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{clientSecret}}}, nil)
	if err != nil {
		t.Fatalf("client on the single listener: %v", err)
	}
	cs.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn, err := wire.Dial(ctx, "ws://"+base+proto.NodePath, http.Header{"Authorization": {"Bearer " + nodeSecret}}, 1<<20)
	if err != nil {
		t.Fatalf("node on the single listener: %v", err)
	}
	defer conn.CloseNow()
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: &proto.Hello{Version: proto.Version, Node: "n1"}}); err != nil {
		t.Fatal(err)
	}
	if e, err := conn.Recv(ctx); err != nil || e.T != proto.TWelcome {
		t.Fatalf("welcome: %v, %v", e, err)
	}
}
