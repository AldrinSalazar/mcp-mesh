//go:build linux || darwin

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/gateway"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/supervisor"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const helperEnv = "MCP_MESH_DAEMON_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "server" {
		_, _ = io.WriteString(os.Stderr, "daemon helper started\n")
		s := mcp.NewServer(&mcp.Implementation{Name: "daemon-helper", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
		for _, name := range []string{"echo", "pid", "hidden"} {
			s.AddTool(&mcp.Tool{Name: name, Description: "Helper tool", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				_, _ = io.WriteString(os.Stderr, "called "+r.Params.Name+"\n")
				value := r.Params.Name
				if value == "pid" {
					value = strconv.Itoa(os.Getpid())
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: value}}}, nil
			})
		}
		if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ctx.Done():
			t.Fatal("condition timed out")
		case <-ticker.C:
		}
	}
}
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mesh-d-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
func startTestDaemon(t *testing.T, opts Options) (*control.Client, func()) {
	t.Helper()
	c, stop, _ := startTestDaemonWithDone(t, opts)
	return c, stop
}
func startTestDaemonWithDone(t *testing.T, opts Options) (*control.Client, func(), <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var runErr error
	go func() { defer close(done); runErr = Run(ctx, opts) }()
	stop := func() {
		t.Helper()
		cancel()
		select {
		case <-done:
			if runErr != nil {
				t.Errorf("daemon: %v", runErr)
			}
		case <-time.After(20 * time.Second):
			t.Error("daemon did not stop")
		}
	}
	t.Cleanup(stop)
	c := control.NewClient(opts.SocketPath)
	await(t, func() bool {
		select {
		case <-done:
			t.Fatalf("daemon exited: %v", runErr)
		default:
		}
		_, err := c.Status(t.Context())
		return err == nil
	})
	return c, stop, done
}
func status(t *testing.T, c *control.Client) control.Status {
	t.Helper()
	s, err := c.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func mcpStatus(t *testing.T, c *control.Client, name string) control.MCPStatus {
	t.Helper()
	for _, m := range status(t, c).MCPs {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("missing MCP %s", name)
	return control.MCPStatus{}
}
func noProcess(t *testing.T, pid int) {
	t.Helper()
	if pid > 0 && !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		t.Errorf("process %d still exists", pid)
	}
}
func call(t *testing.T, cs *mcp.ClientSession, name string) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("call error: %+v", res)
	}
	return res.Content[0].(*mcp.TextContent).Text
}

func TestDaemonIntegration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cat := catalog.New()
	registry := nodes.NewRegistry(nodes.Options{Catalog: cat, Logger: logger, OfflineGrace: 10 * time.Second})
	nodeServer := httptest.NewServer(registry)
	t.Cleanup(nodeServer.Close)
	gw := gateway.New(gateway.Options{Catalog: cat, Logger: logger, CallTimeout: 10 * time.Second})
	gwCtx, gwCancel := context.WithCancel(t.Context())
	gwDone := make(chan struct{})
	go func() { defer close(gwDone); gw.Run(gwCtx) }()
	t.Cleanup(func() { gwCancel(); <-gwDone })
	mcpServer := httptest.NewServer(gw.Handler())
	t.Cleanup(mcpServer.Close)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: mcpServer.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := shortDir(t)
	opts := Options{ConfigPath: filepath.Join(dir, "node.yaml"), SocketPath: filepath.Join(dir, "s"), ToolCacheDir: filepath.Join(dir, "tools"), Version: "test-version", Logger: logger}
	hubURL := "ws" + strings.TrimPrefix(nodeServer.URL, "http") + proto.NodePath
	definition := func(name, start string) string {
		idle := ""
		if start == "lazy" {
			idle = "    idle_timeout: 750ms\n"
		}
		return fmt.Sprintf("  %s:\n    command: %q\n    env: {%s: server}\n    start: %s\n%s    allow_tools: [echo, pid]\n", name, exe, helperEnv, start, idle)
	}
	data := fmt.Sprintf("hub: {url: %q}\nnode: {name: desk}\nlog_level: info\nmcps:\n", hubURL) + definition("eager", "eager") + definition("lazy", "lazy")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(opts.ConfigPath, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(data)
	c, stop := startTestDaemon(t, opts)
	awaitTools := func(count int) {
		t.Helper()
		await(t, func() bool { res, err := cs.ListTools(t.Context(), nil); return err == nil && len(res.Tools) == count })
	}
	awaitTools(4)
	wantTools := []control.Tool{{Name: "echo", Description: "Helper tool"}, {Name: "pid", Description: "Helper tool"}}
	checkTools := func(name string) {
		t.Helper()
		got, err := c.Tools(t.Context(), name)
		if err != nil || !reflect.DeepEqual(got, wantTools) {
			t.Fatalf("Tools(%s) = %+v, %v; want %+v", name, got, err, wantTools)
		}
	}
	checkTools("eager")
	checkTools("lazy")
	firstStatus := status(t, c)
	if firstStatus.Node != "desk" || firstStatus.Version != "test-version" || firstStatus.PID != os.Getpid() || firstStatus.ConfigPath != opts.ConfigPath || firstStatus.StartedAt.IsZero() || !firstStatus.Hub.Connected || len(firstStatus.MCPs) != 2 || firstStatus.MCPs[0].Name != "eager" {
		t.Fatalf("status: %+v", firstStatus)
	}
	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range res.Tools {
		if strings.Contains(tool.Name, "hidden") {
			t.Fatalf("hidden tool listed: %s", tool.Name)
		}
	}
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "desk__eager__hidden"})
	if err == nil && !result.IsError {
		t.Fatal("hidden call succeeded")
	}
	if got := call(t, cs, "desk__eager__echo"); got != "echo" {
		t.Fatalf("echo: %s", got)
	}
	await(t, func() bool {
		logs, err := c.Logs(t.Context(), "eager", 1024)
		return err == nil && strings.Contains(string(logs), "called echo")
	})
	tail, err := c.Logs(t.Context(), "eager", 4)
	if err != nil || string(tail) != "cho\n" {
		t.Fatalf("tail=%q err=%v", tail, err)
	}
	for _, err := range []error{c.Restart(t.Context(), "missing"), func() error { _, err := c.Logs(t.Context(), "missing", 10); return err }()} {
		if !errors.Is(err, control.ErrUnknownMCP) {
			t.Fatalf("unknown error: %v", err)
		}
	}
	await(t, func() bool { return mcpStatus(t, c, "lazy").State == control.MCPStopped })
	awaitTools(4)
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, def := range cfg.MCPs {
		cache := toolCache{dir: opts.ToolCacheDir, definition: def}
		raw := cache.read()
		if toolCount(raw) != 2 {
			t.Fatalf("cache %s: %s", def.Name, raw)
		}
		info, err := os.Stat(cache.path())
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("cache permissions: %v, %v", info, err)
		}
	}
	// A level-only reload preserves the process and its existing hub channel.
	pid := mcpStatus(t, c, "eager").PID
	write(strings.Replace(data, "log_level: info", "log_level: debug", 1))
	if err := c.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := mcpStatus(t, c, "eager").PID; got != pid {
		t.Fatalf("level reload replaced PID %d with %d", pid, got)
	}
	write("hub: [invalid\n")
	if err := c.Reload(t.Context()); !errors.Is(err, control.ErrInvalidConfig) {
		t.Fatalf("invalid reload=%v", err)
	}
	if got := mcpStatus(t, c, "eager").PID; got != pid {
		t.Fatal("invalid reload stopped old config")
	}
	if call(t, cs, "desk__eager__echo") != "echo" {
		t.Fatal("old config no longer works")
	}
	write(data)
	stop()
	noProcess(t, pid)
	if _, err := os.Stat(opts.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
	// The matching cache lets the hub list a lazy MCP without any discovery process.
	c, stop = startTestDaemon(t, opts)
	awaitTools(4)
	await(t, func() bool { return status(t, c).Hub.Connected })
	lazy := mcpStatus(t, c, "lazy")
	checkTools("lazy")
	if lazy.PID != 0 || lazy.State != control.MCPStopped || lazy.Tools != 2 {
		t.Fatalf("cached lazy status: %+v", lazy)
	}
	logs, err := c.Logs(t.Context(), "lazy", 1024)
	if err != nil || len(logs) != 0 {
		t.Fatalf("cached lazy started: %q, %v", logs, err)
	}
	// Watch the real event stream while a demand call starts and writes stderr.
	eventsCtx, cancelEvents := context.WithCancel(t.Context())
	defer cancelEvents()
	events, err := c.Events(eventsCtx)
	if err != nil {
		t.Fatal(err)
	}
	lazyPID, err := strconv.Atoi(call(t, cs, "desk__lazy__pid"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	seenStatus, seenLog := false, false
	for !seenStatus || !seenLog {
		select {
		case e := <-events:
			seenStatus = seenStatus || e.Kind == control.EventStatus
			seenLog = seenLog || (e.Kind == control.EventLog && e.MCP == "lazy" && strings.Contains(e.Data, "called pid"))
		case <-deadline.C:
			t.Fatal("missing status/log events")
		}
	}
	await(t, func() bool { return mcpStatus(t, c, "lazy").State == control.MCPStopped })
	noProcess(t, lazyPID)
	awaitTools(4)
	pid = mcpStatus(t, c, "eager").PID
	if err := c.Restart(t.Context(), "eager"); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool {
		s := mcpStatus(t, c, "eager")
		return s.PID > 0 && s.PID != pid && s.Restarts >= 1 && s.Attached
	})
	noProcess(t, pid)
	await(t, func() bool {
		for _, node := range registry.Nodes() {
			for _, m := range node.MCPs {
				if m.Name == "eager" && m.State == nodes.MCPReady {
					return true
				}
			}
		}
		return false
	})
	if call(t, cs, "desk__eager__echo") != "echo" {
		t.Fatal("restart failed")
	}
	oldPID := mcpStatus(t, c, "eager").PID
	write(data + definition("added", "eager"))
	if err := c.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	awaitTools(6)
	checkTools("added")
	noProcess(t, oldPID)
	if call(t, cs, "desk__added__echo") != "echo" {
		t.Fatal("added MCP unavailable")
	}
	pids := []int{mcpStatus(t, c, "eager").PID, mcpStatus(t, c, "added").PID}
	stop()
	for _, pid := range pids {
		noProcess(t, pid)
	}
	if _, err := os.Stat(opts.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remains: %v", err)
	}
	select {
	case _, ok := <-events:
		if ok {
			for range events {
			}
		}
	case <-time.After(10 * time.Second):
		t.Fatal("events did not close")
	}
}

// Bypass the hub's catalog lookup to prove the node itself rejects hidden calls.
func TestFilteredSupervisorRejectsCalls(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sup := supervisor.New(supervisor.Options{Name: "filtered", Start: proto.StartLazy, New: func(stderr io.Writer) upstream.Upstream {
		return upstream.NewStdio(upstream.StdioConfig{Command: exe, Env: []string{helperEnv + "=server"}, Stderr: stderr})
	}})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); sup.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("supervisor failed to stop")
		}
	})
	u := &filteredUpstream{sup: sup, allow: []string{"echo"}}
	conn, err := u.Open(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "filter-test", Version: "1"}, nil).Connect(t.Context(), connectionTransport{conn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	_, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "hidden"})
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != jsonrpc.CodeInvalidParams {
		t.Fatalf("hidden call error: %v", err)
	}
	if strings.Contains(string(sup.Logs(1024)), "called hidden") {
		t.Fatal("hidden call reached process")
	}
}

type connectionTransport struct{ conn mcp.Connection }

func (t connectionTransport) Connect(context.Context) (mcp.Connection, error) { return t.conn, nil }

func TestEnvAppliedBeforeConfigAndRunningGuard(t *testing.T) {
	dir := shortDir(t)
	t.Setenv("MCP_MESH_DAEMON_NODE", "before")
	envPath := filepath.Join(dir, "env")
	if err := service.WriteEnvFile(envPath, map[string]string{"MCP_MESH_DAEMON_NODE": "captured"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(path, []byte("hub: {url: ws://localhost:1/node}\nnode: {name: '${env:MCP_MESH_DAEMON_NODE}'}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ConfigPath: path, SocketPath: filepath.Join(dir, "s"), EnvFile: envPath, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c, stop := startTestDaemon(t, opts)
	if status(t, c).Node != "captured" {
		t.Fatal("environment not applied before config")
	}
	if err := Run(t.Context(), opts); !errors.Is(err, control.ErrDaemonRunning) {
		t.Fatalf("second daemon: %v", err)
	}
	stop()
}

func TestSubscriptionsDropAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	d := &backend{ctx: ctx, subscribers: make(map[chan control.Event]struct{})}
	slow := d.Subscribe(t.Context())
	subscriberCtx, subscriberCancel := context.WithCancel(t.Context())
	fast := d.Subscribe(subscriberCtx)
	for range 1000 {
		d.publish(control.Event{Kind: control.EventStatus})
	}
	if len(slow) != 64 || len(fast) != 64 {
		t.Fatal("buffer not bounded")
	}
	for range 64 {
		<-fast
	}
	d.publish(control.Event{Kind: control.EventLog, Data: "latest"})
	if (<-fast).Data != "latest" {
		t.Fatal("slow subscriber blocked fast")
	}
	subscriberCancel()
	for range fast {
	}
	cancel()
	for range slow {
	}
}

func TestReloadSIGHUP(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "node.yaml")
	data := "hub: {url: ws://localhost:1/node}\nnode: {name: before}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ConfigPath: path, SocketPath: filepath.Join(dir, "s"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c, _ := startTestDaemon(t, opts)
	if err := os.WriteFile(path, []byte(strings.Replace(data, "before", "after", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return status(t, c).Node == "after" })
}

// Status says when node.yaml changed after the daemon loaded it (MCP edits
// are saved, not applied), until a reload applies it.
func TestStatusConfigChanged(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "node.yaml")
	data := "hub: {url: ws://localhost:1/node}\nnode: {name: before}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	opts := Options{ConfigPath: path, SocketPath: filepath.Join(dir, "s"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	c, _ := startTestDaemon(t, opts)
	if status(t, c).ConfigChanged {
		t.Fatal("ConfigChanged before any change")
	}
	if err := os.WriteFile(path, []byte(strings.Replace(data, "before", "after", 1)), 0600); err != nil {
		t.Fatal(err)
	}
	if st := status(t, c); !st.ConfigChanged || st.Node != "before" {
		t.Fatalf("after saving: changed=%v node=%q", st.ConfigChanged, st.Node)
	}
	if err := c.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if st := status(t, c); st.ConfigChanged || st.Node != "after" {
		t.Fatalf("after reload: changed=%v node=%q", st.ConfigChanged, st.Node)
	}
}

var _ control.Backend = (*backend)(nil)

func TestDaemonShutdown(t *testing.T) {
	for _, method := range []string{"client", "HTTP"} {
		t.Run(method, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			dir := shortDir(t)
			opts := Options{ConfigPath: filepath.Join(dir, "node.yaml"), SocketPath: filepath.Join(dir, "s"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			data := fmt.Sprintf("hub: {url: ws://localhost:1/node}\nnode: {name: shutdown}\nmcps:\n  fake:\n    command: %q\n    env: {%s: server}\n    start: eager\n", exe, helperEnv)
			if err := os.WriteFile(opts.ConfigPath, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			c, stop, done := startTestDaemonWithDone(t, opts)
			await(t, func() bool { return mcpStatus(t, c, "fake").State == control.MCPRunning })
			pid := mcpStatus(t, c, "fake").PID
			if pid <= 0 {
				t.Fatal("fake MCP did not start")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			if method == "client" {
				if err := c.Shutdown(ctx); err != nil {
					t.Fatal(err)
				}
			} else {
				transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", opts.SocketPath)
				}}
				defer transport.CloseIdleConnections()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon/v1/shutdown", nil)
				if err != nil {
					t.Fatal(err)
				}
				resp, err := (&http.Client{Transport: transport}).Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if resp.StatusCode != http.StatusAccepted || err != nil || len(body) != 0 {
					t.Fatalf("shutdown response: %s, body=%q, err=%v", resp.Status, body, err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("daemon did not return after shutdown")
			}
			// Join and check Run's result only after it has exited on its own.
			stop()
			noProcess(t, pid)
			if _, err := os.Stat(opts.SocketPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("socket after shutdown: %v", err)
			}
		})
	}
}

func TestDaemonToolsCache(t *testing.T) {
	dir := shortDir(t)
	opts := Options{ConfigPath: filepath.Join(dir, "node.yaml"), SocketPath: filepath.Join(dir, "s"), ToolCacheDir: filepath.Join(dir, "tools"), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	data := "hub: {url: ws://localhost:1/node}\nnode: {name: tools}\nmcps:\n  cached:\n    command: unused\n    start: lazy\n  empty:\n    command: unused\n    start: lazy\n"
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(opts.ConfigPath, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(data)
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var cache toolCache
	for _, def := range cfg.MCPs {
		if def.Name == "cached" {
			cache = toolCache{dir: opts.ToolCacheDir, definition: def}
		}
	}
	if err := cache.write(json.RawMessage(`[{"name":"echo","description":"Cached description","inputSchema":{"type":"object"}},{"name":"pid"}]`)); err != nil {
		t.Fatal(err)
	}
	c, _ := startTestDaemon(t, opts)
	want := []control.Tool{{Name: "echo", Description: "Cached description"}, {Name: "pid"}}
	check := func(name string, want []control.Tool) {
		t.Helper()
		got, err := c.Tools(t.Context(), name)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Tools(%s) = %+v, %v; want %+v", name, got, err, want)
		}
	}
	check("cached", want)
	check("empty", []control.Tool{})
	if _, err := c.Tools(t.Context(), "missing"); !errors.Is(err, control.ErrUnknownMCP) {
		t.Fatalf("unknown tools: %v", err)
	}
	// Disk changes are picked up by a replacement runtime, not by API reads.
	if err := cache.write(json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	check("cached", want)
	write(strings.Replace(data, "name: tools", "name: reloaded", 1))
	if err := c.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	check("cached", []control.Tool{})
	check("empty", []control.Tool{})
	write("hub: {url: ws://localhost:1/node}\nnode: {name: reloaded}\n")
	if err := c.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tools(t.Context(), "cached"); !errors.Is(err, control.ErrUnknownMCP) {
		t.Fatalf("removed MCP tools: %v", err)
	}
}

func toolCount(raw json.RawMessage) int {
	var tools []json.RawMessage
	_ = json.Unmarshal(raw, &tools)
	return len(tools)
}
