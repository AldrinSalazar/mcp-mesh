package supervisor

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const helperEnv = "MCP_MESH_SUPERVISOR_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "server" {
		_, _ = io.WriteString(os.Stderr, "supervisor child stderr\n")
		server := mcp.NewServer(&mcp.Implementation{Name: "stdio-helper", Version: "1"}, nil)
		server.AddTool(&mcp.Tool{Name: "pid", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strconv.Itoa(os.Getpid())}}}, nil
		})
		if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			_, _ = io.WriteString(os.Stderr, err.Error())
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestStdioPIDAndStderr(t *testing.T) {
	ctx := testContext(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	chunks := make(chan string, 16)
	s := New(Options{Name: "stdio-helper", Start: proto.StartLazy,
		New: func(stderr io.Writer) upstream.Upstream {
			return upstream.NewStdio(upstream.StdioConfig{Command: exe, Env: []string{helperEnv + "=server"}, Stderr: stderr})
		}, OnLog: func(p []byte) { chunks <- string(p) },
	})
	cancel, done := runSupervisor(t, s)
	c := openConn(t, ctx, s)
	status := s.Status()
	if status.PID <= 0 || status.PID == os.Getpid() {
		t.Fatalf("child PID = %d", status.PID)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil).Connect(ctx, connTransport{c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "pid"})
	if err != nil {
		t.Fatal(err)
	}
	if pid := res.Content[0].(*mcp.TextContent).Text; pid != strconv.Itoa(status.PID) {
		t.Fatalf("server PID = %s, status = %d", pid, status.PID)
	}
	if chunk := receive(t, ctx, chunks); !strings.Contains(chunk, "supervisor child stderr") {
		t.Fatalf("OnLog = %q", chunk)
	}
	if got := string(s.Logs(1024)); !strings.Contains(got, "supervisor child stderr") {
		t.Fatalf("stderr = %q", got)
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	waitState(t, ctx, s, control.MCPStopped)
	if s.Status().PID != 0 {
		t.Fatal("PID retained after process exit")
	}
	cancel()
	receive(t, ctx, done)
}
