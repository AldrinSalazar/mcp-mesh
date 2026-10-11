package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/tui"
)

func execute(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := rootCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(t.Context())
	return out.String(), err
}
func TestInit(t *testing.T) {
	setupCLI(t, false)
	path := filepath.Join(t.TempDir(), "config", "node.yaml")
	out, err := execute(t, "--config", path, "init", "--hub", "ws://localhost:7001/node")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, path) || !strings.Contains(out, "config check") {
		t.Fatalf("output: %s", out)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Hub.URL != "ws://localhost:7001/node" {
		t.Fatalf("hub=%s", cfg.Hub.URL)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "--config", path, "init"); err == nil {
		t.Fatal("overwrote config")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("config changed: %v", err)
	}
	if _, err := execute(t, "--config", path, "init", "--force"); err != nil {
		t.Fatal(err)
	}
}
func TestConfigCheck(t *testing.T) {
	setupCLI(t, false)
	path := filepath.Join(t.TempDir(), "node.yaml")
	data := "hub: {url: ws://localhost/node}\nnode: {name: desktop}\nmcps:\n  local: {command: echo, start: lazy, allow_tools: [read]}\n  remote: {url: http://localhost/mcp}\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := execute(t, "--config", path, "config", "check")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ws://localhost/node", "desktop", "local: transport=stdio start=lazy allow_tools=[read]", "remote: transport=http start=eager allow_tools=all tools"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
	if err := os.WriteFile(path, []byte("hub: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := execute(t, "--config", path, "config", "check"); err == nil {
		t.Fatal("accepted invalid config")
	}
}
func TestRootHelpAndWrap(t *testing.T) {
	setupCLI(t, false)
	out, err := execute(t)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Getting started:") || !strings.Contains(out, "wrap") {
		t.Fatalf("missing help: %s", out)
	}
	out, err = execute(t, "wrap", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--hub", "--name", "--url", "--env"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing wrap flag %s", want)
		}
	}
}

// Every command test uses isolated paths and injected runners, including tests
// that only exercise help, so an unexpected action cannot reach real services.
func setupCLI(t *testing.T, input bool) (string, string, *fakeService) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path, socket := filepath.Join(dir, "node.yaml"), filepath.Join(dir, "s")
	for key, value := range map[string]string{"MCPMESH_CONFIG": path, "MCPMESH_SOCKET": socket, "MCPMESH_NO_INPUT": "", "MCPMESH_HUB": "", "MCPMESH_NODE": ""} {
		t.Setenv(key, value)
	}
	oldTerminal, oldOptions, oldTUI, oldEditor, oldAgent := terminalInput, nodectlOptions, tuiRunner, editorRunner, agentRunner
	oldDiscover := discoverHubs
	t.Cleanup(func() {
		terminalInput, nodectlOptions, tuiRunner, editorRunner, agentRunner = oldTerminal, oldOptions, oldTUI, oldEditor, oldAgent
		discoverHubs = oldDiscover
	})
	discoverHubs = func(context.Context, *nodectl.Node, time.Duration) ([]discovery.Hub, error) { return nil, nil }
	terminalInput = func() bool { return input }
	manager := &fakeService{}
	nodectlOptions = nodectl.Options{Service: manager, Executable: filepath.Join(dir, "node-binary"), StateDir: filepath.Join(dir, "state")}
	tuiRunner = func(context.Context, tui.Node) error {
		t.Error("unexpected dashboard")
		return errors.New("unexpected dashboard")
	}
	editorRunner = func(*exec.Cmd) error { t.Error("unexpected editor"); return errors.New("unexpected editor") }
	agentRunner = func(context.Context, agent.Config) error {
		t.Error("unexpected foreground agent")
		return errors.New("unexpected foreground agent")
	}
	return path, socket, manager
}

type fakeService struct {
	mu          sync.Mutex
	state       service.State
	calls       []string
	start, stop func()
	spec        service.Spec
}

func (s *fakeService) Status(context.Context) (service.State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state, nil
}
func (s *fakeService) Install(_ context.Context, spec service.Spec) error {
	s.mu.Lock()
	s.spec = spec
	s.calls = append(s.calls, "install")
	s.state.Installed, s.state.Running = true, true
	start := s.start
	s.mu.Unlock()
	if start != nil {
		start()
	}
	return nil
}
func (s *fakeService) Uninstall(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "uninstall")
	s.state = service.State{}
	return nil
}
func (s *fakeService) Start(context.Context) error {
	s.mu.Lock()
	s.calls = append(s.calls, "start")
	s.state.Running = true
	start := s.start
	s.mu.Unlock()
	if start != nil {
		start()
	}
	return nil
}
func (s *fakeService) Stop(context.Context) error {
	s.mu.Lock()
	s.calls = append(s.calls, "stop")
	s.state.Running = false
	stop := s.stop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	return nil
}

const cliConfig = "hub: {url: 'ws://localhost:7001/node'}\nnode: {name: desktop}\nmcps: {}\n"

func writeCLIConfig(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}
func runCLI(t *testing.T, input string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := rootCmd()
	var out, notices bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&notices)
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	err = cmd.ExecuteContext(t.Context())
	return out.String(), notices.String(), err
}
func entries(t *testing.T, path string) []config.Entry {
	t.Helper()
	e, err := config.Entries(path)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestDashboardAndNoInput(t *testing.T) {
	_, _, _ = setupCLI(t, true)
	calls := 0
	tuiRunner = func(_ context.Context, node tui.Node) error {
		calls++
		if _, ok := node.(*nodectl.Node); !ok {
			t.Fatalf("node %T", node)
		}
		return nil
	}
	for _, args := range [][]string{nil, {"tui"}} {
		if _, _, err := runCLI(t, "", args...); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("dashboard calls %d", calls)
	}
	for _, args := range [][]string{{"--no-input"}, {"--no-input", "--help"}} {
		out, _, err := runCLI(t, "", args...)
		if err != nil || !strings.Contains(out, "Getting started:") {
			t.Fatalf("%v: %s, %v", args, out, err)
		}
	}
	t.Setenv("MCPMESH_NO_INPUT", "1")
	out, _, err := runCLI(t, "")
	if err != nil || !strings.Contains(out, "Getting started:") || calls != 2 {
		t.Fatalf("env: %s %v calls=%d", out, err, calls)
	}
}

func TestCommandHelp(t *testing.T) {
	setupCLI(t, false)
	root := rootCmd()
	for _, cmd := range root.Commands() {
		if cmd.Long == "" || cmd.Example == "" || cmd.GroupID == "" {
			t.Errorf("missing help for %s", cmd.Name())
		}
		for _, child := range cmd.Commands() {
			if child.Long == "" || child.Example == "" {
				t.Errorf("missing help for %s", child.CommandPath())
			}
		}
	}
	out, _, err := runCLI(t, "", "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, group := range []string{"Setup:", "Node:", "MCPs:"} {
		if !strings.Contains(out, group) {
			t.Errorf("missing %s: %s", group, out)
		}
	}
}

func TestInitPrompt(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
		interactive       bool
		args              []string
	}{
		{"answer", "ws://other:7001/node\n", "ws://other:7001/node", true, nil},
		{"eof", "", "ws://192.168.1.10:7001/node", true, nil},
		{"script", "", "wss://mesh.example.com/node", false, nil},
		{"flag", "", "ws://flag/node", true, []string{"--hub", "ws://flag/node"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, tt.interactive)
			out, notices, err := runCLI(t, tt.input, append([]string{"init"}, tt.args...)...)
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(path)
			if err != nil || cfg.Hub.URL != tt.want {
				t.Fatalf("config=%v err=%v", cfg, err)
			}
			if !strings.Contains(out, "dashboard") || strings.Contains(out, "Hub URL") {
				t.Fatalf("stdout: %s", out)
			}
			if strings.Contains(notices, "Hub URL") != (tt.interactive && len(tt.args) == 0) {
				t.Fatalf("stderr: %s", notices)
			}
		})
	}
}

func TestAddNamesAndFlags(t *testing.T) {
	for _, tt := range []struct {
		name, input, want string
		interactive       bool
		extra             []string
	}{
		{"script", "", "filesystem", false, nil},
		{"prompt", "files\n", "files", true, nil},
		{"default", "\n", "filesystem", true, nil},
		{"eof", "", "filesystem", true, nil},
		{"explicit", "", "named", true, []string{"--name", "named"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, tt.interactive)
			writeCLIConfig(t, path, cliConfig)
			args := append([]string{"add", "--env", "TOKEN=a=b", "--dir", "/tmp", "--start", "lazy", "--idle-timeout", "2m", "--allow-tools", "read,write"}, tt.extra...)
			args = append(args, "--", "npx", "-y", "@modelcontextprotocol/server-filesystem", "~/code")
			out, notices, err := runCLI(t, tt.input, args...)
			if err != nil {
				t.Fatal(err)
			}
			e := entries(t, path)
			if len(e) != 1 || e[0].Name != tt.want || e[0].Command != "npx" || e[0].Env["TOKEN"] != "a=b" || e[0].Dir != "/tmp" || e[0].Start != "lazy" || e[0].IdleTimeout != "2m" || !reflect.DeepEqual(e[0].AllowTools, []string{"read", "write"}) {
				t.Fatalf("entry %#v", e)
			}
			if !strings.Contains(out, "Added "+tt.want+" to "+path) || !strings.Contains(out, "mcp-mesh-node start") {
				t.Fatalf("out: %s", out)
			}
			if strings.Contains(notices, "MCP name") != (tt.interactive && len(tt.extra) == 0) {
				t.Fatalf("notices: %s", notices)
			}
		})
	}
}

func TestAddDuplicate(t *testing.T) {
	for _, tt := range []struct {
		name, input       string
		interactive, fail bool
	}{
		{"script", "", false, true}, {"rename", "weather\nfetch\n", true, false}, {"eof", "", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, tt.interactive)
			writeCLIConfig(t, path, strings.Replace(cliConfig, "mcps: {}", "mcps: {weather: {command: weather}}", 1))
			out, notices, err := runCLI(t, tt.input, "add", "--name", "weather", "--", "weather")
			if tt.fail {
				if !errors.Is(err, config.ErrMCPExists) {
					t.Fatalf("err %v", err)
				}
				if len(entries(t, path)) != 1 {
					t.Fatal("changed config")
				}
				return
			}
			if err != nil || !strings.Contains(out, "Added fetch") || strings.Count(notices, "Choose another name") != 2 {
				t.Fatalf("out=%s notices=%s err=%v", out, notices, err)
			}
		})
	}
}

func TestRemoveAndToggle(t *testing.T) {
	for _, tt := range []struct {
		name, input               string
		interactive, yes, removed bool
	}{
		{"decline", "n\n", true, false, false}, {"default", "", true, false, false}, {"confirm", "yes\n", true, false, true}, {"yes flag", "", true, true, true}, {"script", "", false, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, tt.interactive)
			writeCLIConfig(t, path, strings.Replace(cliConfig, "mcps: {}", "mcps: {weather: {command: weather}}", 1))
			for _, action := range []string{"disable", "enable"} {
				out, notices, err := runCLI(t, "", action, "weather")
				if err != nil || notices != "" || !strings.Contains(out, "mcp-mesh-node start") || entries(t, path)[0].Disabled != (action == "disable") {
					t.Fatalf("%s: %s %s %v", action, out, notices, err)
				}
			}
			args := []string{"remove", "weather"}
			if tt.yes {
				args = append(args, "--yes")
			}
			_, notices, err := runCLI(t, tt.input, args...)
			if err != nil || (len(entries(t, path)) == 0) != tt.removed {
				t.Fatalf("remove: %s %v", notices, err)
			}
			if strings.Contains(notices, "[y/N]") != (tt.interactive && !tt.yes) {
				t.Fatalf("prompt: %s", notices)
			}
		})
	}
}

func TestStatusWithoutDaemon(t *testing.T) {
	for _, state := range []string{"valid", "missing", "invalid"} {
		t.Run(state, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			if state != "missing" {
				data := "hub: {url: ws://localhost/node}\nnode: {name: desktop}\nmcps:\n  weather: {command: weather, env: {TOKEN: env-secret}}\n  remote: {url: http://localhost/mcp, headers: {Authorization: header-secret}, disabled: true}\n"
				if state == "invalid" {
					data += "unknown: true\n"
				}
				writeCLIConfig(t, path, data)
			}
			out, _, err := runCLI(t, "", "status")
			if err != nil || !strings.Contains(out, "Node: stopped") || !strings.Contains(out, "Start at login:") || !strings.Contains(out, "Hub:") || !strings.Contains(out, "Config: "+path) {
				t.Fatalf("status: %s %v", out, err)
			}
			if state != "missing" && (!strings.Contains(out, "weather") || !strings.Contains(out, "disabled")) {
				t.Fatalf("table: %s", out)
			}
			out, _, err = runCLI(t, "", "status", "--json")
			var status statusJSON
			if err != nil || json.Unmarshal([]byte(out), &status) != nil {
				t.Fatalf("json: %s %v", out, err)
			}
			if status.Daemon.Running || status.Daemon.Status != nil || status.Config.Exists != (state != "missing") || (status.Config.Error != "") != (state == "invalid") {
				t.Fatalf("status: %#v", status)
			}
			for _, secret := range []string{"env-secret", "header-secret", `"env"`, `"headers"`} {
				if strings.Contains(out, secret) {
					t.Fatalf("leaked %s: %s", secret, out)
				}
			}
			if state != "missing" && !reflect.DeepEqual(status.Config.MCPs, []string{"remote", "weather"}) {
				t.Fatalf("mcps: %v", status.Config.MCPs)
			}
		})
	}
}

func TestWrapConfigAndPrecedence(t *testing.T) {
	for _, tt := range []struct {
		name, hubEnv, nodeEnv, wantHub, wantNode, source string
		args                                             []string
	}{
		{"config", "", "", "ws://localhost:7001/node", "desktop", "config", nil},
		{"env", "ws://env/node", "envnode", "ws://env/node", "envnode", "MCPMESH_HUB", nil},
		{"flag", "ws://env/node", "envnode", "ws://flag/node", "flagnode", "--hub", []string{"--hub", "ws://flag/node", "--node", "flagnode"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			t.Setenv("TEST_HUB_KEY", "hub-secret")
			writeCLIConfig(t, path, strings.Replace(cliConfig, "hub: {url: 'ws://localhost:7001/node'}", "hub: {url: 'ws://localhost:7001/node', key_env: TEST_HUB_KEY}", 1))
			t.Setenv("MCPMESH_HUB", tt.hubEnv)
			t.Setenv("MCPMESH_NODE", tt.nodeEnv)
			calls := 0
			agentRunner = func(_ context.Context, cfg agent.Config) error {
				calls++
				// The config's key only goes to the config's hub.
				wantAuth := ""
				if tt.wantHub == "ws://localhost:7001/node" {
					wantAuth = "Bearer hub-secret"
				}
				if cfg.HubURL != tt.wantHub || cfg.Node != tt.wantNode || cfg.Header.Get("Authorization") != wantAuth || cfg.MCPs[0].Name != "weather" {
					t.Fatalf("agent: %#v", cfg)
				}
				if _, ok := cfg.MCPs[0].Upstream.(*filteredUpstream); !ok {
					t.Fatalf("filter: %T", cfg.MCPs[0].Upstream)
				}
				return nil
			}
			args := append([]string{"wrap", "--allow-tools", "read"}, tt.args...)
			args = append(args, "--", "weather")
			out, notices, err := runCLI(t, "", args...)
			source := tt.source
			if source == "config" {
				source = path
			}
			if err != nil || calls != 1 || !strings.Contains(notices, "Using hub "+tt.wantHub+" from "+source) || !strings.Contains(notices, "Tip:") || strings.Contains(out+notices, "hub-secret") {
				t.Fatalf("out=%s notices=%s err=%v", out, notices, err)
			}
		})
	}
}

func TestWrapInvalidAndMissingConfig(t *testing.T) {
	for _, tt := range []struct {
		name, data string
		args       []string
		fail       bool
	}{
		{"invalid", "hub: {}\n", nil, true}, {"invalid with env", "hub: {}\n", nil, true}, {"invalid override", "hub: {}\n", []string{"--hub", "ws://override/node"}, false},
		{"missing", "", nil, true}, {"missing override", "", []string{"--hub", "ws://override/node"}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			if tt.data != "" {
				writeCLIConfig(t, path, tt.data)
			}
			if tt.name == "invalid with env" {
				t.Setenv("MCPMESH_HUB", "ws://env/node")
			}
			calls := 0
			agentRunner = func(context.Context, agent.Config) error { calls++; return nil }
			args := append([]string{"wrap"}, tt.args...)
			args = append(args, "--", "weather")
			_, _, err := runCLI(t, "", args...)
			if (err != nil) != tt.fail || calls != boolInt(!tt.fail) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if tt.name == "missing" && !strings.Contains(err.Error(), "mcp-mesh-node init") {
				t.Fatal(err)
			}
		})
	}
}
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestWrapAddStopped(t *testing.T) {
	path, _, _ := setupCLI(t, true)
	writeCLIConfig(t, path, cliConfig)
	out, notices, err := runCLI(t, "", "wrap", "--add", "--url", "https://mcp.notion.com/mcp", "--header", "Authorization=remote-secret", "--start", "lazy", "--idle-timeout", "1m", "--allow-tools", "read")
	e := entries(t, path)
	if err != nil || len(e) != 1 || e[0].Name != "notion" || e[0].Headers["Authorization"] != "remote-secret" || e[0].Start != "lazy" || e[0].IdleTimeout != "1m" || !reflect.DeepEqual(e[0].AllowTools, []string{"read"}) || !strings.Contains(out, "desktop__notion__") || !strings.Contains(out, "mcp-mesh-node start") || strings.Contains(notices, "MCP name") {
		t.Fatalf("entry=%#v out=%s notices=%s err=%v", e, out, notices, err)
	}
}

func TestEditValidation(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(stringBool(valid), func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			writeCLIConfig(t, path, cliConfig)
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("VISUAL", "")
			t.Setenv("EDITOR", exe)
			called := false
			editorRunner = func(cmd *exec.Cmd) error {
				called = true
				if cmd.Stdin != os.Stdin || cmd.Stdout != os.Stdout || cmd.Stderr != os.Stderr || cmd.Args[len(cmd.Args)-1] != path {
					t.Fatalf("editor: %#v", cmd)
				}
				if !valid {
					writeCLIConfig(t, path, "hub: {}\n")
				}
				return nil
			}
			_, _, err = runCLI(t, "", "edit")
			if !called || (err == nil) != valid {
				t.Fatalf("called=%v err=%v", called, err)
			}
			if !valid && !strings.Contains(err.Error(), "the node keeps the previous config") {
				t.Fatal(err)
			}
		})
	}
}
func stringBool(b bool) string {
	if b {
		return "valid"
	}
	return "invalid"
}

func TestWrapMissingCredentialFile(t *testing.T) {
	path, _, _ := setupCLI(t, false)
	writeCLIConfig(t, path, strings.Replace(cliConfig, "hub: {url: 'ws://localhost:7001/node'}", "hub: {url: 'ws://localhost:7001/node', key_file: missing.key}", 1))
	_, _, err := runCLI(t, "", "wrap", "--", "weather")
	if err == nil || !strings.Contains(err.Error(), "key_file") || strings.Contains(err.Error(), "--hub is required") {
		t.Fatalf("config error: %v", err)
	}
}

func TestWrapHostnameDefault(t *testing.T) {
	setupCLI(t, false)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	called := false
	agentRunner = func(_ context.Context, cfg agent.Config) error {
		called = true
		if cfg.Node != hostname {
			t.Fatalf("node=%s want=%s", cfg.Node, hostname)
		}
		return nil
	}
	_, notices, err := runCLI(t, "", "wrap", "--hub", "ws://hub/node", "--", "weather")
	if err != nil || !called || !strings.Contains(notices, "from --hub") {
		t.Fatalf("notices=%s err=%v", notices, err)
	}
}

func TestWrapAddDuplicateNeverPrompts(t *testing.T) {
	path, _, _ := setupCLI(t, true)
	writeCLIConfig(t, path, strings.Replace(cliConfig, "mcps: {}", "mcps: {weather: {command: weather}}", 1))
	_, notices, err := runCLI(t, "renamed\n", "wrap", "--add", "--", "weather")
	if !errors.Is(err, config.ErrMCPExists) || strings.Contains(notices, "Choose another name") || len(entries(t, path)) != 1 {
		t.Fatalf("notices=%s err=%v", notices, err)
	}
}

func TestMCPFlagsValidation(t *testing.T) {
	for _, command := range []string{"add", "wrap"} {
		for _, args := range [][]string{
			nil, {"--url", "http://localhost/mcp", "--", "weather"},
			{"--env", "BAD", "--", "weather"}, {"--header", "BAD", "--url", "http://localhost/mcp"},
			{"--start", "invalid", "--", "weather"}, {"--idle-timeout", "invalid", "--", "weather"},
			{"--idle-timeout", "-1m", "--", "weather"},
		} {
			t.Run(command+strings.Join(args, " "), func(t *testing.T) {
				path, _, _ := setupCLI(t, false)
				writeCLIConfig(t, path, cliConfig)
				if _, _, err := runCLI(t, "", append([]string{command}, args...)...); err == nil {
					t.Fatal("accepted invalid flags")
				}
				if len(entries(t, path)) != 0 {
					t.Fatal("saved invalid entry")
				}
			})
		}
	}
}
