//go:build !windows

package nodectl

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
)

type fakeBackend struct {
	cancel    context.CancelFunc
	mu        sync.Mutex
	reloads   int
	reloadErr error
}

func (b *fakeBackend) Status() control.Status {
	return control.Status{Node: "fake", PID: os.Getpid(), Version: "test"}
}
func (b *fakeBackend) Logs(string, int) ([]byte, error) { return []byte("fake logs"), nil }
func (b *fakeBackend) Tools(string) ([]control.Tool, error) {
	return []control.Tool{{Name: "fake-tool"}}, nil
}
func (b *fakeBackend) Restart(context.Context, string) error { return nil }
func (b *fakeBackend) Reload(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reloads++
	return b.reloadErr
}
func (b *fakeBackend) Shutdown() { b.cancel() }
func (b *fakeBackend) Subscribe(ctx context.Context) <-chan control.Event {
	ch := make(chan control.Event, 1)
	ch <- control.Event{Kind: control.EventStatus}
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}

func TestMain(m *testing.M) {
	mode := os.Getenv("NODECTL_FAKE_DAEMON")
	if mode == "" {
		os.Exit(m.Run())
	}
	if mode == "fail" {
		fmt.Fprintln(os.Stderr, "fake daemon: cannot open configured socket")
		os.Exit(1)
	}
	socket := ""
	for i, arg := range os.Args {
		if arg == "--socket" && i+1 < len(os.Args) {
			socket = os.Args[i+1]
		}
	}
	if socket == "" {
		os.Exit(2)
	}
	if err := os.WriteFile(socket+".pid", []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		os.Exit(2)
	}
	if mode == "hang" {
		listener, err := net.Listen("unix", socket)
		if err != nil {
			os.Exit(2)
		}
		defer listener.Close()
		for {
			conn, err := listener.Accept()
			if err != nil {
				os.Exit(2)
			}
			go func() {
				defer conn.Close()
				buffer := make([]byte, 4096)
				for {
					if _, err := conn.Read(buffer); err != nil {
						return
					}
				}
			}()
		}
	}
	listener, err := control.Listen(socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := control.Serve(ctx, listener, &fakeBackend{cancel: cancel}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

type fakeManager struct {
	mu        sync.Mutex
	state     service.State
	statusErr error
	calls     []string
	spec      service.Spec
	start     func() error
	stop      func() error
	status    func(context.Context) (service.State, error)
}

func (m *fakeManager) Install(_ context.Context, s service.Spec) error {
	m.mu.Lock()
	m.calls = append(m.calls, "install")
	m.spec = s
	m.state.Installed = true
	m.state.Running = true
	start := m.start
	m.mu.Unlock()
	if start != nil {
		return start()
	}
	return nil
}
func (m *fakeManager) Uninstall(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, "uninstall")
	m.state = service.State{}
	return nil
}
func (m *fakeManager) Start(context.Context) error {
	m.mu.Lock()
	m.calls = append(m.calls, "start")
	m.state.Running = true
	start := m.start
	m.mu.Unlock()
	if start != nil {
		return start()
	}
	return nil
}
func (m *fakeManager) Stop(context.Context) error {
	m.mu.Lock()
	m.calls = append(m.calls, "stop")
	m.state.Running = false
	stop := m.stop
	m.mu.Unlock()
	if stop != nil {
		return stop()
	}
	return nil
}
func (m *fakeManager) Status(ctx context.Context) (service.State, error) {
	m.mu.Lock()
	state, err, status := m.state, m.statusErr, m.status
	m.mu.Unlock()
	if status != nil {
		return status(ctx)
	}
	return state, err
}

func newTestNode(t *testing.T, manager *fakeManager) *Node {
	t.Helper()
	dir, err := os.MkdirTemp("", "nc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if manager == nil {
		manager = &fakeManager{}
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	n, err := New(Options{ConfigPath: filepath.Join(dir, "node.yaml"), SocketPath: filepath.Join(dir, "n.sock"), StateDir: filepath.Join(dir, "state"), Executable: exe, Service: manager, Version: "vtest"})
	if err != nil {
		t.Fatal(err)
	}
	// Register before any spawn so even a failed startup cannot leak a child.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = n.client.Shutdown(ctx)
		data, err := os.ReadFile(n.opts.SocketPath + ".pid")
		if err != nil {
			return
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil {
			t.Error(err)
			return
		}
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				return
			}
			select {
			case <-ctx.Done():
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("daemon %d did not exit before cleanup deadline", pid)
				return
			case <-ticker.C:
			}
		}
	})
	return n
}

func writeConfig(t *testing.T, n *Node, data string) {
	t.Helper()
	if err := os.WriteFile(n.opts.ConfigPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

const validConfig = "hub:\n  url: ws://localhost:7001/node\nnode:\n  name: test-node\nmcps: {}\n"

func serveFake(t *testing.T, socket string) (*fakeBackend, <-chan error) {
	t.Helper()
	listener, err := control.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	backend := &fakeBackend{cancel: cancel}
	done := make(chan error, 1)
	go func() { done <- control.Serve(ctx, listener, backend) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("fake server did not stop")
		}
	})
	return backend, done
}

func TestNew(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MCPMESH_CONFIG", filepath.Join(dir, "default.yaml"))
	t.Setenv("MCPMESH_SOCKET", filepath.Join(dir, "default.sock"))
	t.Setenv("XDG_STATE_HOME", dir)
	n, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	exe, _ = filepath.EvalSymlinks(exe)
	if n.opts.Executable != exe || n.opts.ConfigPath != filepath.Join(dir, "default.yaml") || n.opts.StateDir != filepath.Join(dir, "mcp-mesh") {
		t.Fatalf("defaults: %#v", n.opts)
	}
	n, err = New(Options{ConfigPath: "node.yaml", SocketPath: "node.sock", StateDir: "state", Service: &fakeManager{}})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(n.opts.ConfigPath) || !filepath.IsAbs(n.opts.SocketPath) {
		t.Fatal("relative paths")
	}
}

func TestInspect(t *testing.T) {
	tests := []struct {
		name, data      string
		up, unsupported bool
		exists          bool
		invalid         bool
		entries         int
	}{
		{name: "missing"},
		{name: "invalid", data: "hub: {url: wrong}\nmcps:\n  weather: {command: weather}\n", exists: true, invalid: true, entries: 1},
		{name: "valid down", data: validConfig, exists: true},
		{name: "valid up", data: validConfig, exists: true, up: true},
		{name: "unsupported", unsupported: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n := newTestNode(t, nil)
			if tt.unsupported {
				n.opts.Service = nil
			}
			if tt.data != "" {
				writeConfig(t, n, tt.data)
			}
			if tt.up {
				serveFake(t, n.opts.SocketPath)
			}
			snapshot := n.Inspect(t.Context())
			if snapshot.Config.Exists != tt.exists || (snapshot.Config.Err != "") != tt.invalid || len(snapshot.Config.Entries) != tt.entries || snapshot.Config.Node == "" {
				t.Fatalf("config: %#v", snapshot.Config)
			}
			if snapshot.Daemon.Running != tt.up || snapshot.Daemon.Err != "" {
				t.Fatalf("daemon: %#v", snapshot.Daemon)
			}
			if snapshot.Service.Supported == tt.unsupported {
				t.Fatalf("service: %#v", snapshot.Service)
			}
			if snapshot.Version != "vtest" || snapshot.LogFile != n.logFile() {
				t.Fatalf("metadata: %#v", snapshot)
			}
		})
	}
}

func TestInspectErrorsAndTimeouts(t *testing.T) {
	manager := &fakeManager{statusErr: errors.New("service unavailable")}
	n := newTestNode(t, manager)
	if err := os.Symlink(n.opts.SocketPath, n.opts.SocketPath); err != nil {
		t.Fatal(err)
	}
	// A cyclic symlink produces stat and dial errors distinct from ENOENT.
	n.opts.ConfigPath = filepath.Join(n.opts.SocketPath, "config")
	snapshot := n.Inspect(t.Context())
	if snapshot.Config.Err == "" || snapshot.Daemon.Err == "" || snapshot.Service.Err == "" {
		t.Fatalf("errors: %#v", snapshot)
	}
	manager.status = func(ctx context.Context) (service.State, error) { <-ctx.Done(); return service.State{}, ctx.Err() }
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	snapshot = n.Inspect(ctx)
	if snapshot.Service.Err == "" {
		t.Fatal("expected service timeout")
	}
}

func TestBackgroundLifecycle(t *testing.T) {
	t.Setenv("NODECTL_FAKE_DAEMON", "serve")
	n := newTestNode(t, nil)
	writeConfig(t, n, validConfig)
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := n.Inspect(t.Context())
	if !first.Daemon.Running || first.Daemon.Status.PID == os.Getpid() {
		t.Fatalf("not spawned: %#v", first.Daemon)
	}
	for range 3 {
		if err := n.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if got := n.Inspect(t.Context()).Daemon.Status.PID; got != first.Daemon.Status.PID {
		t.Fatalf("second daemon: %d vs %d", got, first.Daemon.Status.PID)
	}
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, uintptr(first.Daemon.Status.PID), 0, 0)
	if errno != 0 || int(sid) != first.Daemon.Status.PID {
		t.Fatalf("daemon session: %d, %v", sid, errno)
	}
	data, err := os.ReadFile(n.logFile())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "--- mcp-mesh-node started ") != 1 {
		t.Fatalf("markers: %s", data)
	}
	for path, want := range map[string]os.FileMode{n.opts.StateDir: 0700, n.logFile(): 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode %o", path, info.Mode().Perm())
		}
	}
	if err := n.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if n.Inspect(t.Context()).Daemon.Running {
		t.Fatal("still running")
	}
	if err := n.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundEarlyExit(t *testing.T) {
	t.Setenv("NODECTL_FAKE_DAEMON", "fail")
	n := newTestNode(t, nil)
	writeConfig(t, n, validConfig)
	if err := os.MkdirAll(n.opts.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(n.logFile(), []byte("old startup error\n"), 0600); err != nil {
		t.Fatal(err)
	}
	err := n.Start(t.Context())
	if err == nil || !strings.Contains(err.Error(), "cannot open configured socket") || strings.Contains(err.Error(), "old startup error") {
		t.Fatalf("error: %v", err)
	}
}

func TestStartValidation(t *testing.T) {
	n := newTestNode(t, nil)
	if err := n.Start(t.Context()); err == nil {
		t.Fatal("started with missing config")
	}
	writeConfig(t, n, "hub: {url: wrong}")
	if err := n.Start(t.Context()); err == nil {
		t.Fatal("started with invalid config")
	}
	serveFake(t, n.opts.SocketPath)
	if err := n.Start(t.Context()); err != nil {
		t.Fatalf("already running: %v", err)
	}
}

func TestServiceLifecycle(t *testing.T) {
	manager := &fakeManager{state: service.State{Installed: true}}
	n := newTestNode(t, manager)
	writeConfig(t, n, validConfig)
	var backend *fakeBackend
	manager.start = func() error { backend, _ = serveFake(t, n.opts.SocketPath); return nil }
	manager.stop = func() error { backend.Shutdown(); return nil }
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := n.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := n.Uninstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.calls, []string{"start", "stop", "uninstall"}) {
		t.Fatalf("calls: %v", manager.calls)
	}
}

func TestInstall(t *testing.T) {
	t.Setenv("NODECTL_FAKE_DAEMON", "serve")
	oldCapture := captureLoginEnv
	captureLoginEnv = func(context.Context) (map[string]string, error) {
		return map[string]string{"PATH": "/test/bin", "LANG": "en_US.UTF-8"}, nil
	}
	t.Cleanup(func() { captureLoginEnv = oldCapture })
	manager := &fakeManager{}
	n := newTestNode(t, manager)
	writeConfig(t, n, validConfig)
	if err := n.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	pid := n.Inspect(t.Context()).Daemon.Status.PID
	manager.start = func() error {
		if _, err := n.daemonStatus(t.Context()); !errors.Is(err, control.ErrNotRunning) {
			return fmt.Errorf("background daemon still answering: %v", err)
		}
		serveFake(t, n.opts.SocketPath)
		return nil
	}
	state, err := n.Install(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || !state.Running {
		t.Fatalf("state: %#v", state)
	}
	if n.Inspect(t.Context()).Daemon.Status.PID == pid {
		t.Fatal("background daemon still owns socket")
	}
	if !reflect.DeepEqual(manager.spec, service.Spec{Executable: n.opts.Executable, Args: n.runArgs(), LogFile: n.logFile()}) {
		t.Fatalf("spec: %#v", manager.spec)
	}
	env, err := service.ReadEnvFile(filepath.Join(n.opts.StateDir, "login.env"))
	if err != nil || env["PATH"] != "/test/bin" {
		t.Fatalf("env: %v, %v", env, err)
	}
	info, err := os.Stat(filepath.Join(n.opts.StateDir, "login.env"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("env mode: %o", info.Mode().Perm())
	}
	if !reflect.DeepEqual(manager.calls, []string{"install"}) {
		t.Fatalf("calls: %v", manager.calls)
	}
}

func TestUnsupportedService(t *testing.T) {
	n := newTestNode(t, nil)
	n.opts.Service = nil
	if _, err := n.Install(t.Context()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("install: %v", err)
	}
	if err := n.Uninstall(t.Context()); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatalf("uninstall: %v", err)
	}
	if hint := n.LogsHint(); hint != "tail -f "+n.logFile() {
		t.Fatalf("hint: %s", hint)
	}
}

func TestConfigEditsAndForwarding(t *testing.T) {
	n := newTestNode(t, nil)
	if err := n.InitConfig("ws://localhost:7001/node", false); err != nil {
		t.Fatal(err)
	}
	if err := n.InitConfig("", false); !errors.Is(err, config.ErrConfigExists) {
		t.Fatalf("create: %v", err)
	}
	if reloaded, err := n.AddMCP(t.Context(), config.Entry{Name: "weather", Command: "weather"}); err != nil || reloaded {
		t.Fatalf("offline add: %v, %v", reloaded, err)
	}
	backend, _ := serveFake(t, n.opts.SocketPath)
	for _, action := range []func() (bool, error){
		func() (bool, error) { return n.AddMCP(t.Context(), config.Entry{Name: "fetch", Command: "fetch"}) },
		func() (bool, error) { return n.SetDisabled(t.Context(), "weather", true) },
		func() (bool, error) { return n.RemoveMCP(t.Context(), "weather") },
	} {
		// Saved, not applied: the running node keeps its MCPs until reload.
		if pending, err := action(); err != nil || !pending {
			t.Fatalf("edit: %v, %v", pending, err)
		}
	}
	backend.mu.Lock()
	count := backend.reloads
	backend.reloadErr = fmt.Errorf("missing credential: %w", control.ErrInvalidConfig)
	backend.mu.Unlock()
	if count != 0 {
		t.Fatalf("MCP edits reloaded the node %d times", count)
	}
	// A reload problem can't fail a save that doesn't reload.
	pending, err := n.AddMCP(t.Context(), config.Entry{Name: "saved", Command: "saved"})
	if !pending || err != nil {
		t.Fatalf("save: %v, %v", pending, err)
	}
	if err := n.Reload(t.Context()); err == nil || !errors.Is(err, control.ErrInvalidConfig) {
		t.Fatalf("reload error: %v", err)
	}
	entries, err := config.Entries(n.opts.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range entries {
		if entry.Name == "saved" {
			found = true
		}
	}
	if !found {
		t.Fatal("edit was not saved")
	}
	backend.mu.Lock()
	backend.reloadErr = nil
	backend.mu.Unlock()
	if err := n.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := n.Restart(t.Context(), "fetch"); err != nil {
		t.Fatal(err)
	}
	if logs, err := n.Logs(t.Context(), "fetch", 8); err != nil || string(logs) != "fake logs" {
		t.Fatalf("logs: %s, %v", logs, err)
	}
	if tools, err := n.Tools(t.Context(), "fetch"); err != nil || len(tools) != 1 || tools[0].Name != "fake-tool" {
		t.Fatalf("tools: %v, %v", tools, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	events, err := n.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Kind != control.EventStatus {
			t.Fatalf("event: %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("missing event")
	}
	cancel()
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("events still open")
		}
	case <-time.After(time.Second):
		t.Fatal("events did not close")
	}
}

func TestInspectConcurrentActions(t *testing.T) {
	n := newTestNode(t, nil)
	writeConfig(t, n, validConfig)
	serveFake(t, n.opts.SocketPath)
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Go(func() {
			for range 10 {
				n.Inspect(t.Context())
			}
		})
		wg.Go(func() {
			if _, err := n.AddMCP(t.Context(), config.Entry{Name: fmt.Sprintf("mcp%d", i), Command: "mcp"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, err := config.Entries(n.opts.ConfigPath)
	if err != nil || len(entries) != 10 {
		t.Fatalf("entries: %d, %v", len(entries), err)
	}
}

func TestServiceWaitFailure(t *testing.T) {
	manager := &fakeManager{state: service.State{Installed: true}}
	n := newTestNode(t, manager)
	writeConfig(t, n, validConfig)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := n.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "logs:") {
		t.Fatalf("error: %v", err)
	}
}

func TestInspectDaemonDeadline(t *testing.T) {
	n := newTestNode(t, nil)
	listener, err := net.Listen("unix", n.opts.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buffer := make([]byte, 4096)
		for {
			if _, err := conn.Read(buffer); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done })
	snapshot := n.Inspect(t.Context())
	if snapshot.Daemon.Running || snapshot.Daemon.Err == "" {
		t.Fatalf("daemon: %#v", snapshot.Daemon)
	}
}

func TestBackgroundTimeoutCleanup(t *testing.T) {
	t.Setenv("NODECTL_FAKE_DAEMON", "hang")
	n := newTestNode(t, nil)
	writeConfig(t, n, validConfig)
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	err := n.Start(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error: %v", err)
	}
	data, err := os.ReadFile(n.opts.SocketPath + ".pid")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("failed startup still alive: %v", err)
	}
}

func TestForwardingWhenStopped(t *testing.T) {
	n := newTestNode(t, nil)
	calls := []func() error{
		func() error { return n.Reload(t.Context()) },
		func() error { return n.Restart(t.Context(), "mcp") },
		func() error { _, err := n.Logs(t.Context(), "mcp", 0); return err },
		func() error { _, err := n.Tools(t.Context(), "mcp"); return err },
		func() error { _, err := n.Events(t.Context()); return err },
	}
	for i, call := range calls {
		if err := call(); !errors.Is(err, control.ErrNotRunning) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
}
