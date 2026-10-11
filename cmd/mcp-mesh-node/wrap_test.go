package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type testUpstream struct{ conn mcp.Connection }

func (u testUpstream) Open(context.Context) (mcp.Connection, error) { return u.conn, nil }
func (u testUpstream) Transport() proto.Transport                   { return proto.TransportStdio }

func TestForegroundAllowTools(t *testing.T) {
	path, _, _ := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	agentRunner = func(ctx context.Context, cfg agent.Config) error {
		filtered, ok := cfg.MCPs[0].Upstream.(*filteredUpstream)
		if !ok {
			t.Fatalf("upstream=%T", cfg.MCPs[0].Upstream)
		}
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		client, err := clientTransport.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		server, err := serverTransport.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		filtered.Upstream = testUpstream{client}
		conn, err := filtered.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		id, err := jsonrpc.MakeID("1")
		if err != nil {
			t.Fatal(err)
		}
		// Denied calls are answered locally: no upstream process needs to run.
		if err := conn.Write(ctx, &jsonrpc.Request{ID: id, Method: "tools/call", Params: json.RawMessage(`{"name":"write"}`)}); err != nil {
			t.Fatal(err)
		}
		denied, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		response, ok := denied.(*jsonrpc.Response)
		if !ok || response.Error == nil {
			t.Fatalf("denied=%#v", denied)
		}
		done := make(chan error, 1)
		go func() {
			request, err := server.Read(ctx)
			if err != nil {
				done <- err
				return
			}
			done <- server.Write(ctx, &jsonrpc.Response{ID: request.(*jsonrpc.Request).ID, Result: json.RawMessage(`{"tools":[{"name":"read"},{"name":"write"}]}`)})
		}()
		defer func() {
			_ = conn.Close()
			if err := <-done; err != nil {
				t.Error(err)
			}
		}()
		if err := conn.Write(ctx, &jsonrpc.Request{ID: id, Method: "tools/list"}); err != nil {
			t.Fatal(err)
		}
		listed, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		response, ok = listed.(*jsonrpc.Response)
		if !ok || response.Error != nil || string(response.Result) != `{"tools":[{"name":"read"}]}` {
			t.Fatalf("listed=%#v", listed)
		}
		return nil
	}
	if _, _, err := runCLI(t, "", "wrap", "--allow-tools", "read", "--", "weather"); err != nil {
		t.Fatal(err)
	}
}

func TestWrapCredentialsOnlyForConfiguredHub(t *testing.T) {
	path, _, _ := setupCLI(t, false)
	t.Setenv("WRAP_TEST_KEY", "secret")
	writeCLIConfig(t, path, "hub: {url: 'ws://localhost:7001/node', key_env: WRAP_TEST_KEY}\nmcps: {}\n")
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"wrap", "--foreground", "--", "weather"}, "Bearer secret"},
		{[]string{"wrap", "--foreground", "--hub", "ws://elsewhere:7001/node", "--", "weather"}, ""},
	} {
		var got string
		agentRunner = func(_ context.Context, cfg agent.Config) error {
			got = cfg.Header.Get("Authorization")
			return nil
		}
		if _, _, err := runCLI(t, "", test.args...); err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Errorf("%v: Authorization=%q, want %q", test.args, got, test.want)
		}
	}
}
