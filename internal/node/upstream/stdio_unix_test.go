//go:build !windows

package upstream

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func runIgnoringHelper() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	_, _ = os.Stdout.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"ready\"}\n")
	for range signals {
	}
}
func runTreeHelper() {
	child := exec.Command("sleep", "3600")
	if child.Start() != nil {
		os.Exit(1)
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM)
	s := testServer(nil)
	s.AddTool(&mcp.Tool{Name: "pid", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strconv.Itoa(child.Process.Pid)}}}, nil
	})
	_ = s.Run(context.Background(), &mcp.StdioTransport{})
	// Stay alive after EOF to reap the grandchild when the group receives TERM.
	<-signals
	_ = child.Wait()
	signal.Stop(signals)
}
func TestStdioCloseKillsGrandchild(t *testing.T) {
	ctx := testContext(t)
	conn, err := helperUpstream(t, "tree").Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*stdioConn)
	c.exitWait, c.termWait = 100*time.Millisecond, time.Second
	session := connectClient(t, ctx, conn, nil)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "pid"})
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(result.Content[0].(*mcp.TextContent).Text)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Fatalf("grandchild %d still exists: %v", pid, err)
	}
	if processGroupAlive(c.cmd) {
		t.Fatal("process group still exists")
	}
}
func TestStdioCloseEscalatesToKill(t *testing.T) {
	ctx := testContext(t)
	conn, err := helperUpstream(t, "ignore").Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := conn.(*stdioConn)
	c.exitWait, c.termWait = 20*time.Millisecond, 20*time.Millisecond
	if _, err := conn.Read(ctx); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if processGroupAlive(c.cmd) {
		t.Fatal("process group still exists")
	}
	status := c.cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("process exit = %v, want SIGKILL", status)
	}
}
