//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"path/filepath"
	"testing"
)

func TestNotRunningMessage(t *testing.T) {
	setupCLI(t, false)
	dir, err := os.MkdirTemp("/tmp", "mesh-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for _, args := range [][]string{{"logs", "example"}, {"restart", "example"}, {"reload"}} {
		_, err := execute(t, append([]string{"--socket", filepath.Join(dir, "s")}, args...)...)
		if err == nil || err.Error() != notRunning {
			t.Errorf("%v: got %v", args, err)
		}
	}
}
func TestInitPermissions(t *testing.T) {
	setupCLI(t, false)
	path := filepath.Join(t.TempDir(), "config", "node.yaml")
	if _, err := execute(t, "--config", path, "init"); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s permissions=%o", path, info.Mode().Perm())
		}
	}
}

type cliBackend struct {
	cancel    context.CancelFunc
	mu        sync.Mutex
	status    control.Status
	reloads   int
	reloadErr error
	restarts  []string
}

func (b *cliBackend) Status() control.Status               { b.mu.Lock(); defer b.mu.Unlock(); return b.status }
func (b *cliBackend) Logs(string, int) ([]byte, error)     { return []byte("MCP stderr\n"), nil }
func (b *cliBackend) Tools(string) ([]control.Tool, error) { return []control.Tool{}, nil }
func (b *cliBackend) Restart(_ context.Context, name string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.restarts = append(b.restarts, name)
	return nil
}
func (b *cliBackend) Reload(context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.reloads++
	return b.reloadErr
}
func (b *cliBackend) Shutdown() { b.cancel() }
func (b *cliBackend) Subscribe(ctx context.Context) <-chan control.Event {
	ch := make(chan control.Event)
	go func() { <-ctx.Done(); close(ch) }()
	return ch
}
func serveCLI(t *testing.T, socket string) *cliBackend {
	t.Helper()
	listener, err := control.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	b := &cliBackend{cancel: cancel, status: control.Status{Node: "desktop", PID: 1234, StartedAt: time.Now().Add(-time.Minute), Hub: control.HubStatus{URL: "ws://localhost:7001/node", Connected: true}, MCPs: []control.MCPStatus{{Name: "live", State: control.MCPRunning, PID: 42, StartedAt: time.Now().Add(-time.Second), Start: "eager", Tools: 3}}}}
	done := make(chan error, 1)
	go func() { done <- control.Serve(ctx, listener, b) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("control server did not stop")
		}
	})
	return b
}

func TestWrapRunningDaemon(t *testing.T) {
	for _, tt := range []struct {
		name, input, wantNode      string
		interactive, added, prompt bool
		args                       []string
	}{
		{"yes", "yes\n", "", true, true, true, nil},
		{"default yes", "", "", true, true, true, nil},
		{"name then yes", "renamed\ny\n", "", true, true, true, []string{"--name="}},
		{"no", "n\n", "desktop-weather", true, false, true, nil},
		{"foreground", "", "desktop-weather", true, false, false, []string{"--foreground", "--name="}},
		{"add", "", "", true, true, false, []string{"--add", "--name="}},
		{"script", "", "desktop-weather", false, false, false, nil},
		{"script add", "", "", false, true, false, []string{"--add"}},
		{"explicit node", "n\n", "chosen", true, false, true, []string{"--node", "chosen"}},
		{"env node", "n\n", "envnode", true, false, true, nil},
		{"no input", "", "desktop-weather", true, false, false, []string{"--no-input"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, socket, _ := setupCLI(t, tt.interactive)
			writeCLIConfig(t, path, cliConfig)
			b := serveCLI(t, socket)
			if tt.name == "env node" {
				t.Setenv("MCPMESH_NODE", "envnode")
			}
			calls := 0
			agentRunner = func(_ context.Context, cfg agent.Config) error {
				calls++
				if cfg.Node != tt.wantNode {
					t.Fatalf("node %s want %s", cfg.Node, tt.wantNode)
				}
				return nil
			}
			args := append([]string{"wrap", "--name", "weather", "--start", "lazy", "--idle-timeout", "3m", "--allow-tools", "read,write", "--env", "TOKEN=value"}, tt.args...)
			args = append(args, "--", "weather", "--verbose")
			out, notices, err := runCLI(t, tt.input, args...)
			if err != nil {
				t.Fatal(err)
			}
			name := "weather"
			if tt.name == "name then yes" {
				name = "renamed"
			}
			e := entries(t, path)
			b.mu.Lock()
			reloads := b.reloads
			b.mu.Unlock()
			if tt.added {
				if calls != 0 || reloads != 0 || len(e) != 1 || e[0].Name != name || e[0].Start != "lazy" || e[0].IdleTimeout != "3m" || e[0].Env["TOKEN"] != "value" || !reflect.DeepEqual(e[0].Args, []string{"--verbose"}) || !reflect.DeepEqual(e[0].AllowTools, []string{"read", "write"}) || !strings.Contains(out, "mcp-mesh-node reload") || !strings.Contains(out, "desktop__"+name+"__") {
					t.Fatalf("calls=%d reloads=%d entries=%#v out=%s", calls, reloads, e, out)
				}
			} else if calls != 1 || reloads != 0 || len(e) != 0 {
				t.Fatalf("calls=%d reloads=%d entries=%#v", calls, reloads, e)
			}
			if strings.Contains(notices, "apply it with mcp-mesh-node reload [Y/n]") != tt.prompt {
				t.Fatalf("prompt: %s", notices)
			}
			if !tt.added && strings.HasPrefix(tt.wantNode, "desktop-") && !strings.Contains(notices, `using "desktop-weather"`) {
				t.Fatalf("missing node notice: %s", notices)
			}
			if strings.Contains(notices, "Tip:") {
				t.Fatalf("running node tip: %s", notices)
			}
		})
	}
}

// MCP changes are saved, not applied: the running node keeps its MCPs until
// the user runs reload, like an MCP client that reads its config at start.
func TestConfigActionsSaveOnly(t *testing.T) {
	path, socket, _ := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	b := serveCLI(t, socket)
	for _, args := range [][]string{{"add", "--", "weather"}, {"disable", "weather"}, {"enable", "weather"}, {"remove", "weather"}} {
		out, _, err := runCLI(t, "", args...)
		if err != nil || !strings.Contains(out, "Apply the change with `mcp-mesh-node reload`") {
			t.Fatalf("%v: %s %v", args, out, err)
		}
	}
	b.mu.Lock()
	reloads := b.reloads
	b.mu.Unlock()
	if reloads != 0 {
		t.Fatalf("MCP changes reloaded the node %d times", reloads)
	}
	out, _, err := runCLI(t, "", "reload")
	if err != nil || !strings.Contains(out, "node reloaded") {
		t.Fatalf("reload: %s %v", out, err)
	}
	b.mu.Lock()
	reloads = b.reloads
	b.mu.Unlock()
	if reloads != 1 {
		t.Fatalf("reloads=%d after reload", reloads)
	}
}

func TestReloadFails(t *testing.T) {
	_, socket, _ := setupCLI(t, false)
	b := serveCLI(t, socket)
	b.mu.Lock()
	b.reloadErr = fmt.Errorf("bad config: %w", control.ErrInvalidConfig)
	b.mu.Unlock()
	if _, _, err := runCLI(t, "", "reload"); err == nil || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("err=%v", err)
	}
}

func TestStatusRunning(t *testing.T) {
	path, socket, manager := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	serveCLI(t, socket)
	for _, via := range []string{"background", "service"} {
		manager.mu.Lock()
		manager.state.Running = via == "service"
		manager.mu.Unlock()
		out, _, err := runCLI(t, "", "status")
		if err != nil || !strings.Contains(out, "Node: running") || !strings.Contains(out, "pid 1234, up") || !strings.Contains(out, "connected") || !strings.Contains(out, "live") {
			t.Fatalf("status: %s %v", out, err)
		}
	}
	out, _, err := runCLI(t, "", "status", "--json")
	var status statusJSON
	if err != nil || json.Unmarshal([]byte(out), &status) != nil || !status.Daemon.Running || status.Daemon.Status == nil || status.Daemon.Status.PID != 1234 || status.Daemon.Status.MCPs[0].Name != "live" {
		t.Fatalf("json: %s %v", out, err)
	}
}

// edit saves and validates; a running node keeps its config until reload,
// and edit says so when the file changed.
func TestEditSavesOnly(t *testing.T) {
	path, socket, _ := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	b := serveCLI(t, socket)
	b.mu.Lock()
	b.status.ConfigChanged = true
	b.mu.Unlock()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", exe)
	editorRunner = func(*exec.Cmd) error { return nil }
	out, _, err := runCLI(t, "", "edit")
	if err != nil || !strings.Contains(out, "mcp-mesh-node reload") {
		t.Fatalf("edit: %s %v", out, err)
	}
	b.mu.Lock()
	reloads := b.reloads
	b.mu.Unlock()
	if reloads != 0 {
		t.Fatalf("edit reloaded the node %d times", reloads)
	}
	out, _, err = runCLI(t, "", "status")
	if err != nil || !strings.Contains(out, "Config changes not applied yet") {
		t.Fatalf("status: %s %v", out, err)
	}
}

func TestStartStopService(t *testing.T) {
	path, socket, manager := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	manager.state.Installed = true
	var b *cliBackend
	manager.start = func() { b = serveCLI(t, socket) }
	manager.stop = func() { b.cancel() }
	out, _, err := runCLI(t, "", "start")
	if err != nil || !strings.Contains(out, "Started the node via the user service") {
		t.Fatalf("start: %s %v", out, err)
	}
	out, _, err = runCLI(t, "", "start")
	if err != nil || !strings.Contains(out, "Node already running") {
		t.Fatalf("start again: %s %v", out, err)
	}
	out, _, err = runCLI(t, "", "stop")
	if err != nil || !strings.Contains(out, "Stopped the node") {
		t.Fatalf("stop: %s %v", out, err)
	}
	if _, _, err := runCLI(t, "", "stop"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manager.calls, []string{"start", "stop"}) {
		t.Fatalf("calls=%v", manager.calls)
	}
}

func TestInstallUninstallService(t *testing.T) {
	path, socket, manager := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	manager.state.UnitPath = "/fake/node.service"
	manager.start = func() { serveCLI(t, socket) }
	// CaptureLoginEnv is owned by nodectl. A temporary shell emits the requested
	// marker command without reading the user's login profile.
	shell := filepath.Join(filepath.Dir(path), "shell")
	if err := os.WriteFile(shell, []byte("#!/bin/sh\nexec /bin/sh -c \"$2\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SHELL", shell)
	out, _, err := runCLI(t, "", "install")
	if err != nil || !strings.Contains(out, "The node will start at login (user service: /fake/node.service)") || !strings.Contains(out, "Logs:") {
		t.Fatalf("install: %s %v", out, err)
	}
	if manager.spec.Executable != nodectlOptions.Executable || manager.spec.LogFile != filepath.Join(nodectlOptions.StateDir, "node.log") || !reflect.DeepEqual(manager.spec.Args, []string{"run", "--config", path, "--socket", socket}) {
		t.Fatalf("spec: %#v", manager.spec)
	}
	if _, err := os.Stat(filepath.Join(nodectlOptions.StateDir, "login.env")); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, "", "uninstall")
	if err != nil || !strings.Contains(out, "The node no longer starts at login · stopped") {
		t.Fatalf("uninstall: %s %v", out, err)
	}
	if !reflect.DeepEqual(manager.calls, []string{"install", "uninstall"}) {
		t.Fatalf("calls: %v", manager.calls)
	}
}

func TestWrapUnresponsiveDaemon(t *testing.T) {
	path, socket, _ := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	listener, err := control.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done; _ = os.Remove(socket) })
	calls := 0
	agentRunner = func(context.Context, agent.Config) error { calls++; return nil }
	start := time.Now()
	_, notices, err := runCLI(t, "", "wrap", "--", "weather")
	if err != nil || calls != 1 || time.Since(start) > 3*time.Second || !strings.Contains(notices, "Tip:") {
		t.Fatalf("notices=%s duration=%s calls=%d err=%v", notices, time.Since(start), calls, err)
	}
}

// bootService is a service manager that can start at boot (systemd with
// lingering).
type bootService struct {
	*fakeService
	linger  bool
	enabled int
}

func (s *bootService) StartsAtBoot(context.Context) (bool, error) { return s.linger, nil }
func (s *bootService) EnableAtBoot(context.Context) error {
	s.enabled++
	s.linger = true
	return nil
}

// On a machine where services start at login only, install says what
// happens after a reboot and how to start at boot; --at-boot does it.
func TestInstallAtBoot(t *testing.T) {
	for _, tt := range []struct {
		name    string
		args    []string
		linger  bool
		want    string
		enabled int
	}{
		{"login only explains", []string{"install"}, false, "run mcp-mesh-node install --at-boot", 0},
		{"at boot already", []string{"install"}, true, "start when the machine boots", 0},
		{"--at-boot enables", []string{"install", "--at-boot"}, false, "start when the machine boots", 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, socket, manager := setupCLI(t, false)
			writeCLIConfig(t, path, cliConfig)
			manager.state.UnitPath = "/fake/node.service"
			manager.start = func() { serveCLI(t, socket) }
			boot := &bootService{fakeService: manager, linger: tt.linger}
			nodectlOptions.Service = boot
			shell := filepath.Join(filepath.Dir(path), "shell")
			if err := os.WriteFile(shell, []byte("#!/bin/sh\nexec /bin/sh -c \"$2\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("SHELL", shell)
			out, _, err := runCLI(t, "", tt.args...)
			if err != nil || !strings.Contains(out, tt.want) {
				t.Fatalf("%v: %s %v", tt.args, out, err)
			}
			if boot.enabled != tt.enabled {
				t.Fatalf("EnableAtBoot called %d times, want %d", boot.enabled, tt.enabled)
			}
		})
	}
}
