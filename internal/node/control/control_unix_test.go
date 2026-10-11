//go:build !windows

package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	status     Status
	logs       func(string, int) ([]byte, error)
	tools      func(string) ([]Tool, error)
	restart    func(context.Context, string) error
	reload     func(context.Context) error
	shutdown   func()
	events     <-chan Event
	subscribed chan context.Context
}

func (b *fakeBackend) Status() Status { return b.status }
func (b *fakeBackend) Tools(name string) ([]Tool, error) {
	if b.tools != nil {
		return b.tools(name)
	}
	return []Tool{}, nil
}
func (b *fakeBackend) Shutdown() {
	if b.shutdown != nil {
		b.shutdown()
	}
}
func (b *fakeBackend) Logs(name string, maxBytes int) ([]byte, error) {
	if b.logs != nil {
		return b.logs(name, maxBytes)
	}
	return nil, nil
}
func (b *fakeBackend) Restart(ctx context.Context, name string) error {
	if b.restart != nil {
		return b.restart(ctx, name)
	}
	return nil
}
func (b *fakeBackend) Reload(ctx context.Context) error {
	if b.reload != nil {
		return b.reload(ctx)
	}
	return nil
}
func (b *fakeBackend) Subscribe(ctx context.Context) <-chan Event {
	if b.subscribed != nil {
		select {
		case b.subscribed <- ctx:
		case <-ctx.Done():
		}
	}
	return b.events
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func testSocketPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// macOS limits sockaddr_un paths to 104 bytes, including the terminator.
	if len(filepath.Join(dir, "s")) >= 100 {
		var err error
		dir, err = os.MkdirTemp(os.TempDir(), "mc-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(dir); err != nil {
				t.Errorf("remove temporary socket directory: %v", err)
			}
		})
	}
	return filepath.Join(dir, "s")
}

func startDaemon(t *testing.T, b Backend) (*Client, string, func()) {
	t.Helper()
	path := testSocketPath(t)
	l, err := Listen(path)
	if err != nil {
		t.Fatal(err)
	}
	return serveDaemon(t, path, l, b)
}

func serveDaemon(t *testing.T, path string, l net.Listener, b Backend) (*Client, string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var serveErr error
	go func() {
		defer close(done)
		serveErr = Serve(ctx, l, b)
	}()
	stop := func() {
		t.Helper()
		cancel()
		select {
		case <-done:
			if serveErr != nil {
				t.Errorf("Serve: %v", serveErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not return after cancellation")
		}
	}
	t.Cleanup(stop)
	return NewClient(path), path, stop
}

func TestClientEndpoints(t *testing.T) {
	ctx := testContext(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	want := Status{
		Node: "workstation", Version: "test", PID: 123, StartedAt: now, ConfigPath: "/node.yaml",
		Hub: HubStatus{URL: "https://hub", Connected: true, Since: now},
		MCPs: []MCPStatus{{Name: "example", Transport: "stdio", Start: "eager", State: MCPRunning,
			Attached: true, PID: 456, StartedAt: now, LastActivity: now, Restarts: 2,
			Tools: 3, AllowTools: []string{"echo"}}},
	}
	name := "mcp with /?# unicode-é"
	tail := []byte{'s', 't', 'd', 'e', 'r', 'r', '\n', 0xff}
	type logRequest struct {
		name string
		max  int
	}
	logs := make(chan logRequest, 2)
	restarts := make(chan string, 1)
	reloads := make(chan struct{}, 1)
	shutdowns := make(chan struct{}, 1)
	toolRequests := make(chan string, 2)
	wantTools := []Tool{{Name: "echo", Description: "Echo input"}, {Name: "pid"}}
	b := &fakeBackend{
		status: want,
		tools: func(name string) ([]Tool, error) {
			toolRequests <- name
			if name == "empty" {
				return []Tool{}, nil
			}
			return wantTools, nil
		},
		shutdown: func() { shutdowns <- struct{}{} },
		logs: func(name string, max int) ([]byte, error) {
			logs <- logRequest{name, max}
			return tail, nil
		},
		restart: func(ctx context.Context, name string) error {
			restarts <- name
			return ctx.Err()
		},
		reload: func(ctx context.Context) error {
			reloads <- struct{}{}
			return ctx.Err()
		},
	}
	c, _, _ := startDaemon(t, b)
	got, err := c.Status(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Status = %+v, %v; want %+v", got, err, want)
	}
	for _, max := range []int{7, 0} {
		got, err := c.Logs(ctx, name, max)
		if err != nil || !bytes.Equal(got, tail) {
			t.Fatalf("Logs = %q, %v; want %q", got, err, tail)
		}
		request := <-logs
		wantMax := max
		if max == 0 {
			wantMax = 64 * 1024
		}
		if request.name != name || request.max != wantMax {
			t.Errorf("Logs backend args = %+v; want %q, %d", request, name, wantMax)
		}
	}
	if err := c.Restart(ctx, name); err != nil {
		t.Fatal(err)
	}
	if got := <-restarts; got != name {
		t.Errorf("Restart name = %q; want %q", got, name)
	}
	if err := c.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-reloads:
	default:
		t.Error("Reload did not call backend")
	}
	for _, tc := range []struct {
		name string
		want []Tool
	}{{name, wantTools}, {"empty", []Tool{}}} {
		got, err := c.Tools(ctx, tc.name)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("Tools = %+v, %v; want %+v", got, err, tc.want)
		}
		if got := <-toolRequests; got != tc.name {
			t.Errorf("Tools name = %q; want %q", got, tc.name)
		}
	}
	if err := c.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-shutdowns:
	case <-ctx.Done():
		t.Fatal("Shutdown did not call backend")
	}
}

func TestErrorMapping(t *testing.T) {
	unknown := fmt.Errorf("MCP missing: %w", ErrUnknownMCP)
	invalid := fmt.Errorf("config node.yaml: %w: missing hub", ErrInvalidConfig)
	internal := errors.New("cannot stop process")
	for _, tt := range []struct {
		name, method, path string
		backendErr         error
		status             int
		sentinel           error
	}{
		{"logs unknown", "GET", "/v1/mcps/missing/logs", unknown, 404, ErrUnknownMCP},
		{"tools unknown", "GET", "/v1/mcps/missing/tools", unknown, 404, ErrUnknownMCP},
		{"tools internal", "GET", "/v1/mcps/missing/tools", internal, 500, nil},
		{"restart unknown", "POST", "/v1/mcps/missing/restart", unknown, 404, ErrUnknownMCP},
		{"reload unknown", "POST", "/v1/reload", unknown, 404, ErrUnknownMCP},
		{"reload invalid", "POST", "/v1/reload", invalid, 400, ErrInvalidConfig},
		{"logs internal", "GET", "/v1/mcps/missing/logs", internal, 500, nil},
		{"restart internal", "POST", "/v1/mcps/missing/restart", internal, 500, nil},
		{"reload internal", "POST", "/v1/reload", internal, 500, nil},
		{"logs invalid config", "GET", "/v1/mcps/missing/logs", invalid, 500, nil},
		{"restart invalid config", "POST", "/v1/mcps/missing/restart", invalid, 500, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testContext(t)
			b := &fakeBackend{
				logs:    func(string, int) ([]byte, error) { return nil, tt.backendErr },
				tools:   func(string) ([]Tool, error) { return nil, tt.backendErr },
				restart: func(context.Context, string) error { return tt.backendErr },
				reload:  func(context.Context) error { return tt.backendErr },
			}
			c, _, _ := startDaemon(t, b)
			var err error
			switch {
			case strings.HasSuffix(tt.path, "/tools"):
				_, err = c.Tools(ctx, "missing")
			case tt.method == "GET":
				_, err = c.Logs(ctx, "missing", 1)
			case tt.path == "/v1/reload":
				err = c.Reload(ctx)
			default:
				err = c.Restart(ctx, "missing")
			}
			if err == nil || err.Error() != tt.backendErr.Error() {
				t.Fatalf("client error = %v; want %v", err, tt.backendErr)
			}
			for _, sentinel := range []error{ErrUnknownMCP, ErrInvalidConfig, ErrNotRunning} {
				if got, want := errors.Is(err, sentinel), sentinel == tt.sentinel; got != want {
					t.Errorf("errors.Is(%v, %v) = %t; want %t", err, sentinel, got, want)
				}
			}
			resp := rawRequest(t, c, ctx, tt.method, tt.path)
			defer resp.Body.Close()
			if resp.StatusCode != tt.status || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("HTTP error = %s, %q; want %d, JSON", resp.Status, resp.Header.Get("Content-Type"), tt.status)
			}
			var body map[string]string
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != tt.backendErr.Error() {
				t.Errorf("error body = %v; want %q", body, tt.backendErr.Error())
			}
		})
	}
}

func rawRequest(t *testing.T, c *Client, ctx context.Context, method, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, method, "http://daemon"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestHTTPContract(t *testing.T) {
	ctx := testContext(t)
	c, _, _ := startDaemon(t, &fakeBackend{})
	for _, tt := range []struct {
		method, path string
		status       int
		contentType  string
	}{
		{"POST", "/v1/mcps/example/restart", 204, ""},
		{"POST", "/v1/reload", 204, ""},
		{"POST", "/v1/shutdown", 202, ""},
		{"GET", "/v1/mcps/example/tools", 200, "application/json"},
		{"POST", "/v1/mcps/example/tools", 405, "application/json"},
		{"GET", "/v1/shutdown", 405, "application/json"},
		{"GET", "/v1/mcps/example/logs", 200, "text/plain; charset=utf-8"},
		{"GET", "/v1/status", 200, "application/json"},
		{"GET", "/v1/mcps/example/logs?max=bad", 400, "application/json"},
		{"GET", "/v1/mcps/example/logs?max=-1", 400, "application/json"},
		{"GET", "/v1/mcps/example/logs?max=", 400, "application/json"},
		{"GET", "/v1/reload", 405, "application/json"},
		{"POST", "/v1/status", 405, "application/json"},
		{"GET", "/v1/unknown", 404, "application/json"},
	} {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			resp := rawRequest(t, c, ctx, tt.method, tt.path)
			defer resp.Body.Close()
			if resp.StatusCode != tt.status {
				t.Errorf("status = %s; want %d", resp.Status, tt.status)
			}
			if tt.contentType != "" && resp.Header.Get("Content-Type") != tt.contentType {
				t.Errorf("Content-Type = %q; want %q", resp.Header.Get("Content-Type"), tt.contentType)
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if tt.status == 204 && len(body) != 0 {
				t.Errorf("204 body = %q", body)
			}
			if tt.path == "/v1/mcps/example/tools" && tt.status == 200 && string(body) != "[]\n" {
				t.Errorf("empty tools body = %q; want []", body)
			}
		})
	}
	if _, err := c.Logs(ctx, "example", -1); err == nil || errors.Is(err, ErrInvalidConfig) {
		t.Errorf("invalid max error = %v; want ordinary error", err)
	}
}

func TestNotRunning(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale=%t", stale), func(t *testing.T) {
			ctx := testContext(t)
			path := testSocketPath(t)
			if stale {
				makeStaleSocket(t, path)
			}
			c := NewClient(path)
			for _, call := range []struct {
				name string
				fn   func() error
			}{
				{"Status", func() error { _, err := c.Status(ctx); return err }},
				{"Logs", func() error { _, err := c.Logs(ctx, "example", 1); return err }},
				{"Tools", func() error { _, err := c.Tools(ctx, "example"); return err }},
				{"Shutdown", func() error { return c.Shutdown(ctx) }},
				{"Restart", func() error { return c.Restart(ctx, "example") }},
				{"Reload", func() error { return c.Reload(ctx) }},
				{"Events", func() error { _, err := c.Events(ctx); return err }},
			} {
				if err := call.fn(); !errors.Is(err, ErrNotRunning) {
					t.Errorf("%s error = %v; want ErrNotRunning", call.name, err)
				}
			}
		})
	}
}

func TestShutdownFlushesBeforeBackend(t *testing.T) {
	w := httptest.NewRecorder()
	called := false
	b := &fakeBackend{shutdown: func() {
		called = true
		if !w.Flushed || w.Code != http.StatusAccepted {
			t.Errorf("Shutdown called before 202 flushed: code=%d flushed=%t", w.Code, w.Flushed)
		}
	}}
	Handler(b).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/shutdown", nil))
	if !called {
		t.Fatal("Shutdown did not call backend")
	}
}

func makeStaleSocket(t *testing.T, path string) {
	t.Helper()
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestListen(t *testing.T) {
	t.Run("permissions", func(t *testing.T) {
		path := filepath.Join(filepath.Dir(testSocketPath(t)), "p", "s")
		l, err := Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		for _, tt := range []struct {
			path string
			mode os.FileMode
		}{
			{path, 0600},
			{filepath.Dir(path), 0700},
		} {
			info, err := os.Stat(tt.path)
			if err != nil {
				t.Fatal(err)
			}
			if got := info.Mode().Perm(); got != tt.mode {
				t.Errorf("%s permissions = %04o; want %04o", tt.path, got, tt.mode)
			}
		}
	})
	t.Run("live listener", func(t *testing.T) {
		path := testSocketPath(t)
		l, err := Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		other, err := Listen(path)
		if other != nil {
			_ = other.Close()
			t.Error("second Listen returned a listener")
		}
		if !errors.Is(err, ErrDaemonRunning) {
			t.Fatalf("second Listen error = %v; want ErrDaemonRunning", err)
		}
		conn, err := net.Dial("unix", path)
		if err != nil {
			t.Fatalf("original socket no longer listens: %v", err)
		}
		_ = conn.Close()
	})
	t.Run("stale socket", func(t *testing.T) {
		path := testSocketPath(t)
		makeStaleSocket(t, path)
		l, err := Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		c, _, _ := serveDaemon(t, path, l, &fakeBackend{})
		if _, err := c.Status(testContext(t)); err != nil {
			t.Fatalf("replaced socket is unusable: %v", err)
		}
	})
	t.Run("preserves regular file", func(t *testing.T) {
		path := testSocketPath(t)
		if err := os.WriteFile(path, []byte("keep me"), 0600); err != nil {
			t.Fatal(err)
		}
		if l, err := Listen(path); err == nil {
			_ = l.Close()
			t.Fatal("Listen accepted a regular file")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "keep me" {
			t.Errorf("existing file = %q, %v", got, err)
		}
	})
	t.Run("leaves existing directory permissions alone", func(t *testing.T) {
		path := testSocketPath(t)
		dir := filepath.Dir(path)
		if err := os.Chmod(dir, 0755); err != nil {
			t.Fatal(err)
		}
		l, err := Listen(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = l.Close() })
		info, err := os.Stat(dir)
		if err != nil || info.Mode().Perm() != 0755 {
			t.Fatalf("existing directory permissions = %v, %v; want unchanged 0755", info, err)
		}
		if sock, err := os.Stat(path); err != nil || sock.Mode().Perm() != 0600 {
			t.Fatalf("socket permissions = %v, %v; want 0600", sock, err)
		}
	})
	t.Run("refuses directory writable by others", func(t *testing.T) {
		path := testSocketPath(t)
		dir := filepath.Dir(path)
		if err := os.Chmod(dir, 0775); err != nil {
			t.Fatal(err)
		}
		if l, err := Listen(path); err == nil {
			l.Close()
			t.Fatal("Listen accepted a group-writable directory")
		}
	})
}

func receiveEvent(t *testing.T, ctx context.Context, events <-chan Event) Event {
	t.Helper()
	select {
	case event, ok := <-events:
		if !ok {
			t.Fatal("event channel closed early")
		}
		return event
	case <-ctx.Done():
		t.Fatal("event did not arrive: ", ctx.Err())
		return Event{}
	}
}

func awaitClosed(t *testing.T, ctx context.Context, events <-chan Event) {
	t.Helper()
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-ctx.Done():
			t.Fatal("event channel did not close: ", ctx.Err())
		}
	}
}

func TestEventsOrderAndShutdown(t *testing.T) {
	ctx := testContext(t)
	source := make(chan Event, 3)
	b := &fakeBackend{events: source}
	c, path, stop := startDaemon(t, b)
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Kind: EventStatus, Time: time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)},
		{Kind: EventLog, MCP: "example", Data: "hello\nsecond line"},
		{Kind: EventLog, MCP: "example", Data: strings.Repeat("x", 128*1024)},
	}
	for _, event := range want {
		source <- event
	}
	for i, want := range want {
		if got := receiveEvent(t, ctx, events); got != want {
			t.Errorf("event %d differs: kind=%s MCP=%s data length=%d", i, got.Kind, got.MCP, len(got.Data))
		}
	}
	stop()
	awaitClosed(t, ctx, events)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket after shutdown: %v; want not exist", err)
	}
}

func TestEventsCancellation(t *testing.T) {
	ctx := testContext(t)
	b := &fakeBackend{subscribed: make(chan context.Context, 1)}
	c, _, _ := startDaemon(t, b)
	streamCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	events, err := c.Events(streamCtx)
	if err != nil {
		t.Fatal(err)
	}
	subscriberCtx := <-b.subscribed
	cancel()
	awaitClosed(t, ctx, events)
	select {
	case <-subscriberCtx.Done():
	case <-ctx.Done():
		t.Fatal("backend subscription was not canceled")
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatalf("canceling client affected daemon: %v", err)
	}
}

func TestEventsBackendCloses(t *testing.T) {
	ctx := testContext(t)
	source := make(chan Event)
	c, _, _ := startDaemon(t, &fakeBackend{events: source})
	events, err := c.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	close(source)
	awaitClosed(t, ctx, events)
}

func TestRequestCancellation(t *testing.T) {
	ctx := testContext(t)
	started := make(chan struct{})
	stopped := make(chan struct{})
	b := &fakeBackend{restart: func(ctx context.Context, name string) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}}
	c, _, _ := startDaemon(t, b)
	callCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() { result <- c.Restart(callCtx, "example") }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("backend did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrNotRunning) {
			t.Errorf("canceled request error = %v; want context.Canceled", err)
		}
	case <-ctx.Done():
		t.Fatal("client request did not cancel")
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("backend request context did not cancel")
	}
}

func TestServeRemovesSocketWithoutAutomaticUnlink(t *testing.T) {
	path := testSocketPath(t)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	c, _, stop := serveDaemon(t, path, l, &fakeBackend{})
	if _, err := c.Status(testContext(t)); err != nil {
		t.Fatal(err)
	}
	stop()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("socket after Serve: %v; want not exist", err)
	}
}
