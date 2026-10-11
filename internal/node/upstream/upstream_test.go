package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const helperEnv = "MCP_MESH_UPSTREAM_HELPER"

type connTransport struct{ c mcp.Connection }

func (t connTransport) Connect(context.Context) (mcp.Connection, error) { return t.c, nil }

func testServer(versions []string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "upstream-test", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: versions})
	for _, name := range []string{"echo", "large"} {
		s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			text := "hello"
			if req.Params.Name == "large" {
				text = strings.Repeat("x", 2<<20)
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
		})
	}
	return s
}
func TestMain(m *testing.M) {
	if mode := os.Getenv(helperEnv); mode != "" {
		switch mode {
		case "exit":
			os.Exit(0)
		case "oversize":
			_, _ = io.WriteString(os.Stdout, strings.Repeat("x", 1024)+"\n")
			os.Exit(0)
		case "tree":
			runTreeHelper()
			os.Exit(0)
		case "ignore":
			runIgnoringHelper()
			os.Exit(0)
		case "config":
			dir, _ := os.Getwd()
			raw, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0", "method": "config",
				"params": map[string]any{"env": os.Getenv("MCP_MESH_CONFIG_TEST"), "dir": dir, "args": os.Args[1:]},
			})
			_, _ = os.Stdout.Write(append(raw, '\n'))
			_, _ = io.WriteString(os.Stderr, "child stderr")
			os.Exit(0)
		}
		if err := testServer(nil).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStdioConfiguration(t *testing.T) {
	ctx := testContext(t)
	u := helperUpstream(t, "config").(*stdioUpstream)
	u.cfg.Args = []string{"extra", "argument"}
	u.cfg.Dir = t.TempDir()
	u.cfg.Env = append(u.cfg.Env, "MCP_MESH_CONFIG_TEST=extra-env")
	var stderr bytes.Buffer
	u.cfg.Stderr = &stderr
	conn, err := u.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Env, Dir string
		Args     []string
	}
	if err := json.Unmarshal(msg.(*jsonrpc.Request).Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Env != "extra-env" || params.Dir != u.cfg.Dir || strings.Join(params.Args, " ") != "extra argument" {
		t.Fatalf("child configuration = %+v", params)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if stderr.String() != "child stderr" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func connectClient(t *testing.T, ctx context.Context, conn mcp.Connection, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("close upstream: %v", err)
		}
	})
	session, err := mcp.NewClient(&mcp.Implementation{Name: "relay-client", Version: "1"}, opts).Connect(ctx, connTransport{conn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return session
}
func checkTools(t *testing.T, ctx context.Context, session *mcp.ClientSession, large bool) {
	t.Helper()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) < 2 {
		t.Fatalf("tools = %v", tools.Tools)
	}
	names := []string{"echo"}
	if large {
		names = append(names, "large")
	}
	for _, name := range names {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name})
		if err != nil {
			t.Fatal(err)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		want := "hello"
		if name == "large" {
			want = strings.Repeat("x", 2<<20)
		}
		if result.IsError || text != want {
			t.Fatalf("unexpected %s result, length %d", name, len(text))
		}
	}
}
func helperUpstream(t *testing.T, mode string) Upstream {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return NewStdio(StdioConfig{Command: exe, Env: []string{helperEnv + "=" + mode}, Stderr: os.Stderr})
}
func TestStdioClientAndReopen(t *testing.T) {
	ctx := testContext(t)
	upstream := helperUpstream(t, "server")
	for range 2 {
		conn, err := upstream.Open(ctx)
		if err != nil {
			t.Fatal(err)
		}
		session := connectClient(t, ctx, conn, nil)
		checkTools(t, ctx, session, true)
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestStdioExitAndLimit(t *testing.T) {
	for _, mode := range []string{"exit", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			ctx := testContext(t)
			u := helperUpstream(t, mode).(*stdioUpstream)
			u.cfg.MaxMessageSize = 128
			conn, err := u.Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if _, err := conn.Read(ctx); err == nil {
				t.Fatal("Read succeeded")
			}
		})
	}
}
func TestStdioCloseUnblocksRead(t *testing.T) {
	ctx := testContext(t)
	conn, err := helperUpstream(t, "server").Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	readDone := make(chan error, 1)
	go func() { _, err := conn.Read(ctx); readDone <- err }()
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- conn.Close() }()
	}
	for range 2 {
		select {
		case err := <-closed:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("Read succeeded")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
func TestStdioCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(testContext(t))
	conn, err := helperUpstream(t, "server").Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	c := conn.(*stdioConn)
	select {
	case <-c.ownerDone:
	default:
		t.Fatal("context owner still running")
	}
	select {
	case <-c.readDone:
	default:
		t.Fatal("reader still running")
	}
	select {
	case <-c.waitDone:
	default:
		t.Fatal("process waiter still running")
	}
}
