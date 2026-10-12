package config

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestFullConfig(t *testing.T) {
	t.Setenv("MCPMESH_TEST_NODE_KEY", "  secret-key\n")
	t.Setenv("MCPMESH_TEST_LOCAL_TOKEN", "local-token")
	data := `hub:
  url: wss://mesh.example.com/node
  key_env: MCPMESH_TEST_NODE_KEY
node:
  name: work-pc
log_level: debug
mcps:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/code"]
    env: { Z: last, DEBUG: "1" }
    dir: /code
    start: lazy
    idle_timeout: 15m
    allow_tools: [read_file, list_directory]
    disabled: false
  browser:
    url: http://127.0.0.1:8931/mcp
    headers: { Authorization: "Bearer ${env:MCPMESH_TEST_LOCAL_TOKEN}" }
    sharing: shared
`
	got, err := Parse([]byte(data), "node.yaml")
	if err != nil {
		t.Fatal(err)
	}
	want := &Config{
		Path: "node.yaml", Hub: Hub{URL: "wss://mesh.example.com/node", Header: http.Header{"Authorization": {"Bearer secret-key"}}},
		NodeName: "work-pc", LogLevel: slog.LevelDebug,
		MCPs: []MCP{
			{Name: "browser", Transport: proto.TransportHTTP, URL: "http://127.0.0.1:8931/mcp", Headers: map[string]string{"Authorization": "Bearer local-token"}, Sharing: proto.SharingShared, Start: proto.StartEager},
			{Name: "filesystem", Transport: proto.TransportStdio, Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "/code"}, Env: []string{"DEBUG=1", "Z=last"}, Dir: "/code", Sharing: proto.SharingShared, Start: proto.StartLazy, IdleTimeout: 15 * time.Minute, AllowTools: []string{"read_file", "list_directory"}},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config = %#v\nMCPs = %#v\nwant %#v", got, got.MCPs, want)
	}
}

func TestDefaults(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		entry string
		start proto.Start
		idle  time.Duration
	}{
		{"eager", "command: echo", proto.StartEager, 0},
		{"explicit eager", "command: echo, start: eager", proto.StartEager, 0},
		{"lazy", "command: echo, start: lazy", proto.StartLazy, 10 * time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := mustParse(t, "hub: {url: ws://localhost/node}\nmcps: {test: {"+test.entry+"}}\n")
			mcp := cfg.MCPs[0]
			if cfg.NodeName != hostname || cfg.LogLevel != slog.LevelInfo || mcp.Start != test.start || mcp.IdleTimeout != test.idle || mcp.Sharing != proto.SharingShared || mcp.AllowTools != nil || cfg.Hub.Header.Get("Authorization") != "" {
				t.Fatalf("incorrect defaults: %#v, %#v", cfg, mcp)
			}
		})
	}
	for _, test := range []struct {
		name  string
		level slog.Level
	}{
		{"debug", slog.LevelDebug}, {"info", slog.LevelInfo}, {"warn", slog.LevelWarn}, {"error", slog.LevelError},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := mustParse(t, "hub: {url: ws://localhost/node}\nlog_level: "+test.name)
			if cfg.LogLevel != test.level {
				t.Fatalf("log level = %v, want %v", cfg.LogLevel, test.level)
			}
		})
	}
}

func TestEnvironmentExpansion(t *testing.T) {
	for key, value := range map[string]string{
		"HUB": "ws://localhost/node", "NAME": "expanded-node", "LEVEL": "warn", "COMMAND": "echo",
		"ARG": "expanded-argument", "DIR": "/code", "VALUE": "value", "START": "lazy", "IDLE": "3m",
		"SHARING": "shared", "TOOL": "read_file", "URL": "https://localhost/mcp", "HEADER": "token", "EMPTY": "",
	} {
		t.Setenv("MCPMESH_TEST_"+key, value)
	}
	cfg := mustParse(t, `hub: {url: "${env:MCPMESH_TEST_HUB}"}
node: {name: "${env:MCPMESH_TEST_NAME}"}
log_level: "${env:MCPMESH_TEST_LEVEL}"
mcps:
  stdio:
    command: "${env:MCPMESH_TEST_COMMAND}"
    args: ["${env:MCPMESH_TEST_ARG}", "$$", "$PLAIN", "${PLAIN}", "$${env:MCPMESH_TEST_VALUE}", "${env:MCPMESH_TEST_EMPTY}"]
    env: { Z: "before-${env:MCPMESH_TEST_VALUE}-after", A: "${env:MCPMESH_TEST_VALUE}${env:MCPMESH_TEST_VALUE}" }
    dir: "${env:MCPMESH_TEST_DIR}"
    start: "${env:MCPMESH_TEST_START}"
    idle_timeout: "${env:MCPMESH_TEST_IDLE}"
    sharing: "${env:MCPMESH_TEST_SHARING}"
    allow_tools: ["${env:MCPMESH_TEST_TOOL}"]
  http:
    url: "${env:MCPMESH_TEST_URL}"
    headers: { Authorization: "Bearer ${env:MCPMESH_TEST_HEADER}" }
`)
	if cfg.Hub.URL != "ws://localhost/node" || cfg.NodeName != "expanded-node" || cfg.LogLevel != slog.LevelWarn {
		t.Fatalf("expanded config: %#v", cfg)
	}
	httpMCP, stdio := cfg.MCPs[0], cfg.MCPs[1]
	if httpMCP.URL != "https://localhost/mcp" || httpMCP.Headers["Authorization"] != "Bearer token" {
		t.Fatalf("expanded HTTP MCP: %#v", httpMCP)
	}
	wantArgs := []string{"expanded-argument", "$$", "$PLAIN", "${PLAIN}", "$value", ""}
	if stdio.Command != "echo" || stdio.Dir != "/code" || stdio.Start != proto.StartLazy || stdio.IdleTimeout != 3*time.Minute || stdio.Sharing != proto.SharingShared || !reflect.DeepEqual(stdio.Args, wantArgs) || !reflect.DeepEqual(stdio.Env, []string{"A=valuevalue", "Z=before-value-after"}) || !reflect.DeepEqual(stdio.AllowTools, []string{"read_file"}) {
		t.Fatalf("expanded stdio MCP: %#v", stdio)
	}
	t.Setenv("MCPMESH_TEST_VALUE", "${env:MCPMESH_TEST_NOT_RECURSIVE}")
	cfg = mustParse(t, `hub: {url: ws://localhost/node}
mcps: {test: {command: echo, args: ["${env:MCPMESH_TEST_VALUE}"]}}`)
	if cfg.MCPs[0].Args[0] != "${env:MCPMESH_TEST_NOT_RECURSIVE}" {
		t.Fatal("replacement was expanded recursively")
	}
}

func TestMissingEnvironment(t *testing.T) {
	const variable = "MCPMESH_TEST_MISSING"
	unsetEnv(t, variable)
	for _, test := range []struct {
		name string
		data string
		line int
	}{
		{"hub.url", "hub:\n  url: '${env:" + variable + "}'", 2},
		{"node.name", "hub: {url: ws://localhost/node}\nnode: {name: '${env:" + variable + "}'}", 2},
		{"mcps.test.command", "hub: {url: ws://localhost/node}\nmcps:\n  test:\n    command: '${env:" + variable + "}'", 4},
		{"mcps.test.args.0", "hub: {url: ws://localhost/node}\nmcps:\n  test:\n    command: echo\n    args:\n      - '${env:" + variable + "}'", 6},
		{"mcps.test.env.TOKEN", "hub: {url: ws://localhost/node}\nmcps: {test: {command: echo, env: {TOKEN: '${env:" + variable + "}'}}}", 2},
		{"mcps.test.dir", "hub: {url: ws://localhost/node}\nmcps: {test: {command: echo, dir: '${env:" + variable + "}'}}", 2},
		{"mcps.test.url", "hub: {url: ws://localhost/node}\nmcps: {test: {url: '${env:" + variable + "}'}}", 2},
		{"mcps.test.headers.Authorization", "hub: {url: ws://localhost/node}\nmcps: {test: {url: http://localhost/mcp, headers: {Authorization: '${env:" + variable + "}'}}}", 2},
		{"mcps.test.headers.X.With.Dot", "hub: {url: ws://localhost/node}\nmcps:\n  test:\n    url: http://localhost/mcp\n    headers:\n      X.With.Dot: '${env:" + variable + "}'", 6},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data), "node.yaml")
			want := fmt.Sprintf("node.yaml:%d: %s:", test.line, test.name)
			if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), variable) {
				t.Fatalf("want %q and variable name, got %v", want, err)
			}
		})
	}
}

func TestValidationErrors(t *testing.T) {
	const hub = "hub: {url: ws://localhost/node}\n"
	for _, test := range []struct {
		name string
		data string
		want string
	}{
		{"missing hub", "mcps: {}", "hub.url"},
		{"empty document", "", "hub.url"},
		{"hub scheme", "hub: {url: https://localhost/node}", "ws or wss"},
		{"hub host", "hub: {url: 'ws:///node'}", "with a host"},
		{"malformed hub URL", "hub: {url: 'ws://[broken'}", "parse URL"},
		{"both key sources", "hub: {url: ws://localhost/node, key_env: KEY, key_file: key}", "mutually exclusive"},
		{"both empty key sources", "hub: {url: ws://localhost/node, key_env: '', key_file: ''}", "mutually exclusive"},
		{"both transports", hub + "mcps: {test: {command: echo, url: http://localhost/mcp}}", "command and url together are not supported yet"},
		{"no transport", hub + "mcps: {test: {}}", "exactly one of command or url"},
		{"invalid MCP name", hub + "mcps: {'bad.name': {command: echo}}", "[A-Za-z0-9_-]+"},
		{"empty MCP name", hub + "mcps: {'': {command: echo}}", "[A-Za-z0-9_-]+"},
		{"duplicate MCP name", hub + "mcps:\n  test: {command: echo}\n  test: {command: echo}", "already defined"},
		{"idle eager", hub + "mcps: {test: {command: echo, start: eager, idle_timeout: 1m}}", "only allowed with start: lazy"},
		{"idle implicit eager", hub + "mcps: {test: {command: echo, idle_timeout: 1m}}", "only allowed with start: lazy"},
		{"invalid idle", hub + "mcps: {test: {command: echo, start: lazy, idle_timeout: nonsense}}", "parse duration"},
		{"zero idle", hub + "mcps: {test: {command: echo, start: lazy, idle_timeout: 0s}}", "positive duration"},
		{"negative idle", hub + "mcps: {test: {command: echo, start: lazy, idle_timeout: -1m}}", "positive duration"},
		{"per-client eager", hub + "mcps: {test: {command: echo, sharing: per-client, start: eager}}", "start: lazy"},
		{"per-client max clients", hub + "mcps: {test: {command: echo, sharing: per-client, max_clients: 0}}", "between 1 and 256"},
		{"max clients when shared", hub + "mcps: {test: {command: echo, max_clients: 3}}", "only allowed with sharing: per-client"},
		{"unknown sharing", hub + "mcps: {test: {command: echo, sharing: invalid}}", "shared or per-client"},
		{"bad call timeout", hub + "mcps: {test: {command: echo, call_timeout: soon}}", "parse duration"},
		{"long call timeout", hub + "mcps: {test: {command: echo, call_timeout: 48h}}", "up to 24h"},
		{"invalid start", hub + "mcps: {test: {command: echo, start: invalid}}", "eager or lazy"},
		{"invalid log level", hub + "log_level: trace", "debug, info, warn or error"},
		{"invalid HTTP URL", hub + "mcps: {test: {url: ws://localhost/mcp}}", "http or https"},
		{"HTTP host missing", hub + "mcps: {test: {url: /mcp}}", "with a host"},
		{"extra document", hub + "---\nhub: {url: ws://other/node}", "only one YAML document"},
		{"invalid YAML", "hub: [", "node.yaml:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data), "node.yaml")
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "node.yaml:") {
				t.Fatalf("want error containing %q with path, got %v", test.want, err)
			}
		})
	}
}

func TestUnknownFields(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
		line int
	}{
		{"top level", "hub: {url: ws://localhost/node}\nunknown: true", 2},
		{"hub", "hub:\n  url: ws://localhost/node\n  unknown: true", 3},
		{"node", "hub: {url: ws://localhost/node}\nnode:\n  unknown: true", 3},
		{"MCP", "hub: {url: ws://localhost/node}\nmcps:\n  test:\n    command: echo\n    unknown: true", 5},
		{"disabled MCP", "hub: {url: ws://localhost/node}\nmcps:\n  test:\n    disabled: true\n    unknown: true", 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.data), "node.yaml")
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("node.yaml:%d:", test.line)) || !strings.HasSuffix(err.Error(), `unknown key "unknown"`) {
				t.Fatalf("want unknown field on line %d, got %v", test.line, err)
			}
		})
	}
}

func TestDisabledAndAllowTools(t *testing.T) {
	unsetEnv(t, "MCPMESH_TEST_DISABLED_MISSING")
	cfg := mustParse(t, `hub: {url: ws://localhost/node}
mcps:
  z_all: {command: echo}
  b_none: {command: echo, allow_tools: []}
  a_some: {command: echo, allow_tools: [read_file]}
  disabled: {disabled: true, command: '${env:MCPMESH_TEST_DISABLED_MISSING}'}
`)
	if len(cfg.MCPs) != 3 || cfg.MCPs[0].Name != "a_some" || cfg.MCPs[1].Name != "b_none" || cfg.MCPs[2].Name != "z_all" {
		t.Fatalf("enabled/sorted MCPs: %#v", cfg.MCPs)
	}
	if !reflect.DeepEqual(cfg.MCPs[0].AllowTools, []string{"read_file"}) || cfg.MCPs[1].AllowTools == nil || len(cfg.MCPs[1].AllowTools) != 0 || cfg.MCPs[2].AllowTools != nil {
		t.Fatalf("allowlist semantics lost: %#v", cfg.MCPs)
	}
}

func TestKeyEnv(t *testing.T) {
	const keyName = "MCPMESH_TEST_KEY"
	for _, test := range []struct {
		name  string
		value string
		unset bool
		want  string
	}{
		{"valid", "  a-key\n", false, ""},
		{"empty", "", false, "empty key"},
		{"whitespace", " \n\t", false, "empty key"},
		{"missing", "", true, keyName},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.unset {
				unsetEnv(t, keyName)
			} else {
				t.Setenv(keyName, test.value)
			}
			cfg, err := Parse([]byte("hub: {url: ws://localhost/node, key_env: "+keyName+"}"), "node.yaml")
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "hub.key_env") {
					t.Fatalf("want %q at hub.key_env, got %v", test.want, err)
				}
			} else if err != nil || cfg.Hub.Header.Get("Authorization") != "Bearer a-key" {
				t.Fatalf("key header: %#v, %v", cfg, err)
			}
		})
	}
	t.Setenv("MCPMESH_TEST_KEY_NAME", keyName)
	t.Setenv(keyName, "key")
	cfg := mustParse(t, `hub: {url: ws://localhost/node, key_env: '${env:MCPMESH_TEST_KEY_NAME}'}`)
	if cfg.Hub.Header.Get("Authorization") != "Bearer key" {
		t.Fatal("key_env name was not expanded")
	}
}

func TestKeyFile(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		missing bool
		want    string
	}{
		{"valid", "  file-key\n", false, ""},
		{"empty", "", false, "empty key"},
		{"whitespace", " \n\t", false, "empty key"},
		{"missing", "", true, "open"},
	} {
		t.Run(test.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "node.key")
			if !test.missing {
				if err := os.WriteFile(file, []byte(test.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("MCPMESH_TEST_KEY_FILE", file)
			cfg, err := Parse([]byte(`hub: {url: ws://localhost/node, key_file: '${env:MCPMESH_TEST_KEY_FILE}'}`), "node.yaml")
			if test.want != "" {
				if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), "hub.key_file") {
					t.Fatalf("want %q at hub.key_file, got %v", test.want, err)
				}
			} else if err != nil || cfg.Hub.Header.Get("Authorization") != "Bearer file-key" {
				t.Fatalf("key header: %#v, %v", cfg, err)
			}
		})
	}
}

func TestLoadAndExample(t *testing.T) {
	t.Setenv("MCPMESH_NODE_KEY", "example-key")
	t.Setenv("LOCAL_TOKEN", "example-token")
	file := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(file, Example(), 0600); err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(Example(), file)
	if err != nil {
		t.Fatalf("Example is invalid: %v", err)
	}
	loaded, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, parsed) || loaded.Path != file {
		t.Fatalf("Load disagrees with Parse: %#v, %#v", loaded, parsed)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil || !strings.Contains(err.Error(), "missing.yaml") {
		t.Fatalf("want read error containing file name, got %v", err)
	}
}

func mustParse(t *testing.T, data string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(data), "node.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func unsetEnv(t *testing.T, name string) {
	t.Helper()
	// Setenv registers restoration, then Unsetenv distinguishes missing from empty.
	t.Setenv(name, "")
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
}

func TestPerClientAndCallTimeout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yaml")
	body := "hub: {url: ws://localhost:7001/node}\nmcps:\n" +
		"  pc: {command: echo, sharing: per-client, max_clients: 3, idle_timeout: 2m, call_timeout: 10m}\n" +
		"  plain: {command: echo, sharing: per-client}\n" +
		"  shared: {command: echo, call_timeout: 30s}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]MCP{}
	for _, m := range cfg.MCPs {
		byName[m.Name] = m
	}
	pc := byName["pc"]
	if pc.Sharing != proto.SharingPerClient || pc.Start != proto.StartLazy || pc.MaxClients != 3 || pc.IdleTimeout != 2*time.Minute || pc.CallTimeout != 10*time.Minute {
		t.Errorf("pc = %+v", pc)
	}
	if p := byName["plain"]; p.Start != proto.StartLazy || p.MaxClients != DefaultMaxClients || p.IdleTimeout != 10*time.Minute {
		t.Errorf("plain = %+v", p)
	}
	if sh := byName["shared"]; sh.Sharing != proto.SharingShared || sh.CallTimeout != 30*time.Second || sh.MaxClients != 0 {
		t.Errorf("shared = %+v", sh)
	}
}
