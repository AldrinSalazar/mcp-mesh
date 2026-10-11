package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestEntries(t *testing.T) {
	unsetEnv(t, "MCPMESH_EDIT_MISSING")
	const mcps = `mcps:
  z_stdio:
    command: '${env:MCPMESH_EDIT_MISSING}'
    args: ["-y", pkg]
    env: {TOKEN: '${env:MCPMESH_EDIT_MISSING}'}
    dir: ~/code
    start: lazy
    idle_timeout: 3m
    allow_tools: []
  a_http:
    url: http://localhost/mcp
    headers: {Authorization: 'Bearer ${env:MCPMESH_EDIT_MISSING}'}
    disabled: true
    future_setting: true
`
	want := []Entry{
		{Name: "a_http", URL: "http://localhost/mcp", Headers: map[string]string{"Authorization": "Bearer ${env:MCPMESH_EDIT_MISSING}"}, Disabled: true},
		{Name: "z_stdio", Command: "${env:MCPMESH_EDIT_MISSING}", Args: []string{"-y", "pkg"}, Env: map[string]string{"TOKEN": "${env:MCPMESH_EDIT_MISSING}"}, Dir: "~/code", Start: "lazy", IdleTimeout: "3m", AllowTools: []string{}},
	}
	for _, test := range []struct {
		name string
		data string
		want []Entry
	}{
		{"valid", "hub: {url: ws://localhost/node}\nmcps: {test: {command: echo}}", []Entry{{Name: "test", Command: "echo"}}},
		{"raw values and unknown keys", "hub: {url: ws://localhost/node}\n" + mcps, want},
		{"bad hub URL", "hub: {url: bad}\n" + mcps, want},
		{"bad log level", "log_level: trace\n" + mcps, want},
		{"unrelated type error", "hub: [not, a, mapping]\n" + mcps, want},
		{"missing MCPs", "hub: {url: bad}\n", []Entry{}},
		{"null MCPs", "mcps: null\n", []Entry{}},
		{"empty", "", []Entry{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeEditConfig(t, test.data)
			got, err := Entries(path)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Entries = %#v, %v; want %#v", got, err, test.want)
			}
		})
	}
	for _, test := range []struct {
		name string
		data string
		line string
	}{
		{"malformed", "mcps:\n  test: [\n", ":2:"},
		{"wrong field type", "mcps:\n  test: {args: wrong}\n", ":2:"},
		{"multiple documents", "mcps: {}\n---\nmcps: {}\n", ":2:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeEditConfig(t, test.data)
			_, err := Entries(path)
			if err == nil || !strings.Contains(err.Error(), path+test.line) {
				t.Fatalf("want error with path and line %s, got %v", test.line, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "missing.yaml")
	if _, err := Entries(path); !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), path) {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestCreate(t *testing.T) {
	for _, test := range []struct {
		name   string
		hubURL string
	}{
		{"default URL", ""},
		{"custom URL", "ws://localhost:7001/node"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "new", "nested", "node.yaml")
			if err := Create(path, test.hubURL, false); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			wantURL := test.hubURL
			if wantURL == "" {
				wantURL = "wss://mesh.example.com/node"
			}
			// The starter config only has commented-out examples, so a fresh
			// daemon does not launch anything the user did not add.
			if cfg.Hub.URL != wantURL || len(cfg.MCPs) != 0 {
				t.Fatalf("starter config = %#v", cfg)
			}
			checkEditMode(t, path, 0600)
			checkEditMode(t, filepath.Dir(path), 0700)
			checkEditMode(t, filepath.Dir(filepath.Dir(path)), 0700)
			before := readEditConfig(t, path)
			if err := Create(path, "ws://other/node", false); !errors.Is(err, ErrConfigExists) || !strings.Contains(err.Error(), path) {
				t.Fatalf("existing config error = %v", err)
			}
			if after := readEditConfig(t, path); after != before {
				t.Fatal("Create without force changed the file")
			}
			if err := Create(path, "ws://other/node", true); err != nil {
				t.Fatal(err)
			}
			cfg, err = Load(path)
			if err != nil || cfg.Hub.URL != "ws://other/node" {
				t.Fatalf("forced config = %#v, %v", cfg, err)
			}
		})
	}
}

func TestCreateInvalid(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "existing"}[existing], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "parent", "node.yaml")
			if existing {
				path = writeEditConfig(t, "original content\n")
			}
			if err := Create(path, "https://wrong-scheme/node", true); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("invalid hub error = %v", err)
			}
			if existing {
				if got := readEditConfig(t, path); got != "original content\n" {
					t.Fatal("invalid Create changed the file")
				}
			} else if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("invalid Create made a directory: %v", err)
			}
		})
	}
}

func TestCreateConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node.yaml")
	const creators = 8
	start := make(chan struct{})
	results := make(chan error, creators)
	for range creators {
		go func() {
			<-start
			results <- Create(path, "", false)
		}()
	}
	close(start)
	created := 0
	for range creators {
		if err := <-results; err == nil {
			created++
		} else if !errors.Is(err, ErrConfigExists) {
			t.Errorf("concurrent Create: %v", err)
		}
	}
	if created != 1 {
		t.Fatalf("successful creators = %d, want 1", created)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(files) != 1 {
		t.Fatalf("temporary files remain: %v, %v", files, err)
	}
}

func TestAddMCP(t *testing.T) {
	t.Setenv("MCPMESH_EDIT_TOKEN", "secret")
	path := writeEditConfig(t, "hub: {url: ws://localhost/node}\n")
	stdio := Entry{Name: "stdio", Command: "npx", Args: []string{"-y", "pkg"}, Env: map[string]string{"TOKEN": "${env:MCPMESH_EDIT_TOKEN}"}, Dir: "~/code", Start: "lazy", IdleTimeout: "3m", AllowTools: []string{"read_file"}}
	http := Entry{Name: "http", URL: "https://localhost/mcp", Headers: map[string]string{"Authorization": "Bearer ${env:MCPMESH_EDIT_TOKEN}"}}
	for _, entry := range []Entry{stdio, http, {Name: "disabled", Command: "unused", Disabled: true}, {Name: "no_tools", Command: "echo", AllowTools: []string{}}} {
		if err := AddMCP(path, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Entries(path)
	if err != nil || !reflect.DeepEqual(entries, []Entry{{Name: "disabled", Command: "unused", Disabled: true}, http, {Name: "no_tools", Command: "echo", AllowTools: []string{}}, stdio}) {
		t.Fatalf("added entries = %#v, %v", entries, err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPs) != 3 || cfg.MCPs[0].Transport != proto.TransportHTTP || cfg.MCPs[0].Headers["Authorization"] != "Bearer secret" || cfg.MCPs[1].AllowTools == nil || len(cfg.MCPs[1].AllowTools) != 0 || cfg.MCPs[2].Transport != proto.TransportStdio || cfg.MCPs[2].IdleTimeout != 3*time.Minute || !reflect.DeepEqual(cfg.MCPs[2].Env, []string{"TOKEN=secret"}) {
		t.Fatalf("loaded MCPs = %#v", cfg.MCPs)
	}
	doc, err := readDocument([]byte(readEditConfig(t, path)), path)
	if err != nil {
		t.Fatal(err)
	}
	mcps, err := mcpMapping(doc.Content[0])
	if err != nil {
		t.Fatal(err)
	}
	node := mcps.Content[mappingIndex(mcps, "stdio")+1]
	var keys []string
	for i := 0; i < len(node.Content); i += 2 {
		keys = append(keys, node.Content[i].Value)
	}
	if want := []string{"command", "args", "env", "dir", "start", "idle_timeout", "allow_tools"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("field order = %v, want %v", keys, want)
	}
	for _, name := range []string{"stdio", "disabled"} {
		before := readEditConfig(t, path)
		err := AddMCP(path, Entry{Name: name, Command: "echo"})
		if !errors.Is(err, ErrMCPExists) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), name) {
			t.Fatalf("duplicate error = %v", err)
		}
		if readEditConfig(t, path) != before {
			t.Fatal("duplicate changed file")
		}
	}
}

func TestAddInvalidMCP(t *testing.T) {
	unsetEnv(t, "MCPMESH_EDIT_MISSING")
	for _, test := range []struct {
		name  string
		entry Entry
	}{
		{"bad name", Entry{Name: "bad.name", Command: "echo"}},
		{"empty name", Entry{Command: "echo"}},
		{"no transport", Entry{Name: "test"}},
		{"both transports", Entry{Name: "test", Command: "echo", URL: "http://localhost/mcp"}},
		{"disabled no transport", Entry{Name: "test", Disabled: true}},
		{"bad URL", Entry{Name: "test", URL: "ws://localhost/mcp"}},
		{"bad start", Entry{Name: "test", Command: "echo", Start: "invalid"}},
		{"bad timeout", Entry{Name: "test", Command: "echo", Start: "lazy", IdleTimeout: "0s"}},
		{"missing env", Entry{Name: "test", Command: "${env:MCPMESH_EDIT_MISSING}"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeEditConfig(t, commentedConfig)
			before := readEditConfig(t, path)
			err := AddMCP(path, test.entry)
			if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), test.entry.Name) {
				t.Fatalf("invalid entry error = %v", err)
			}
			if readEditConfig(t, path) != before {
				t.Fatal("invalid entry changed the file")
			}
			files, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(files) != 1 {
				t.Fatalf("temporary files remain: %v, %v", files, err)
			}
		})
	}
}

func TestRemoveAndSetDisabled(t *testing.T) {
	path := writeEditConfig(t, "hub: {url: ws://localhost/node}\nmcps:\n  first: {command: echo}\n  second: {url: http://localhost/mcp}\n")
	for _, disabled := range []bool{true, true, false, false} {
		if err := SetDisabled(path, "first", disabled); err != nil {
			t.Fatal(err)
		}
		entries, err := Entries(path)
		if err != nil || len(entries) != 2 || entries[0].Disabled != disabled {
			t.Fatalf("entries = %#v, %v", entries, err)
		}
		cfg, err := Load(path)
		wantCount := 2
		if disabled {
			wantCount = 1
		}
		if err != nil || len(cfg.MCPs) != wantCount {
			t.Fatalf("loaded config = %#v, %v", cfg, err)
		}
		if !disabled && strings.Contains(readEditConfig(t, path), "disabled:") {
			t.Fatal("enabling retained disabled key")
		}
	}
	if err := SetDisabled(path, "first", true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"first", "second"} {
		if err := RemoveMCP(path, name); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := Entries(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries after removing all = %#v, %v", entries, err)
	}
	for _, edit := range []func() error{
		func() error { return RemoveMCP(path, "absent") },
		func() error { return SetDisabled(path, "absent", true) },
		func() error { return SetDisabled(path, "absent", false) },
	} {
		before := readEditConfig(t, path)
		err := edit()
		if !errors.Is(err, ErrNoMCP) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "absent") {
			t.Fatalf("missing MCP error = %v", err)
		}
		if readEditConfig(t, path) != before {
			t.Fatal("missing MCP changed file")
		}
	}
}

func TestEditDocumentShapes(t *testing.T) {
	for _, suffix := range []string{"", "mcps:\n", "mcps: null # keep me\n", "mcps: {}\n"} {
		t.Run(suffix, func(t *testing.T) {
			path := writeEditConfig(t, "hub: {url: ws://localhost/node}\n"+suffix)
			if err := AddMCP(path, Entry{Name: "test", Command: "echo"}); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(suffix, "keep me") && !strings.Contains(readEditConfig(t, path), "# keep me") {
				t.Fatal("null MCP comment lost")
			}
		})
	}
	for _, data := range []string{"", "# comments only\n", "null\n", "[]\n", "hub: [\n", "hub: {url: ws://localhost/node}\nmcps: []\n"} {
		t.Run(data, func(t *testing.T) {
			path := writeEditConfig(t, data)
			if err := AddMCP(path, Entry{Name: "test", Command: "echo"}); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("invalid document error = %v", err)
			}
			if readEditConfig(t, path) != data {
				t.Fatal("invalid document changed file")
			}
		})
	}
}

func TestEditInvalidOriginal(t *testing.T) {
	for _, edit := range []struct {
		name string
		fn   func(string) error
	}{
		{"add", func(path string) error { return AddMCP(path, Entry{Name: "new", Command: "echo"}) }},
		{"remove", func(path string) error { return RemoveMCP(path, "test") }},
		{"disable", func(path string) error { return SetDisabled(path, "test", true) }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			path := writeEditConfig(t, "hub: {url: ws://localhost/node}\nlog_level: trace\nmcps: {test: {command: echo}}\n")
			before := readEditConfig(t, path)
			err := edit.fn(path)
			if err == nil || !strings.Contains(err.Error(), "node.yaml is invalid; fix it first:") || !strings.Contains(err.Error(), "log_level") {
				t.Fatalf("invalid original error = %v", err)
			}
			if readEditConfig(t, path) != before {
				t.Fatal("invalid original changed file")
			}
		})
	}
	for _, remove := range []bool{true, false} {
		path := writeEditConfig(t, "hub: {url: ws://localhost/node}\nmcps: {test: {url: bad}}\n")
		var err error
		if remove {
			err = RemoveMCP(path, "test")
		} else {
			err = SetDisabled(path, "test", true)
		}
		if err != nil {
			t.Fatalf("edit fixing invalid original: %v", err)
		}
		if _, err := Load(path); err != nil {
			t.Fatal(err)
		}
		if !remove {
			before := readEditConfig(t, path)
			if err := SetDisabled(path, "test", false); err == nil {
				t.Fatal("enabled invalid entry")
			}
			if readEditConfig(t, path) != before {
				t.Fatal("invalid enabling changed file")
			}
		}
	}
}

func TestEditComments(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(string) error
	}{
		{"add", func(path string) error { return AddMCP(path, Entry{Name: "added", Command: "echo"}) }},
		{"disable and enable", func(path string) error {
			if err := SetDisabled(path, "filesystem", true); err != nil {
				return err
			}
			return SetDisabled(path, "filesystem", false)
		}},
		{"remove added", func(path string) error {
			if err := AddMCP(path, Entry{Name: "added", Command: "echo"}); err != nil {
				return err
			}
			return RemoveMCP(path, "added")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := writeEditConfig(t, commentedConfig)
			if err := test.edit(path); err != nil {
				t.Fatal(err)
			}
			after := readEditConfig(t, path)
			for _, line := range strings.Split(commentedConfig, "\n") {
				if _, comment, ok := strings.Cut(line, "#"); ok && !strings.Contains(after, "#"+comment) {
					t.Errorf("comment lost: %s\n%s", line, after)
				}
			}
			cfg, err := Load(path)
			if err != nil || cfg.Hub.URL != "wss://mesh.example.com/node" || cfg.MCPs[len(cfg.MCPs)-1].Command != "npx" {
				t.Fatalf("unrelated settings changed: %#v, %v", cfg, err)
			}
		})
	}
	path := writeEditConfig(t, commentedConfig)
	if err := RemoveMCP(path, "filesystem"); err != nil {
		t.Fatal(err)
	}
	if after := readEditConfig(t, path); !strings.Contains(after, "# browser:") || !strings.Contains(after, "#   headers:") {
		t.Fatalf("removing filesystem lost commented browser block:\n%s", after)
	}
	// The removed entry's own comments go with it; unrelated ones stay.
	after := readEditConfig(t, path)
	for _, gone := range []string{"# eager | lazy", "# per-client is not supported yet", "# dir: ~/code"} {
		if strings.Contains(after, gone) {
			t.Errorf("removing filesystem kept its comment %q:\n%s", gone, after)
		}
	}
	for _, kept := range []string{"# Connect this node", "# key_env:", "# debug | info", "# Connect to a running"} {
		if !strings.Contains(after, kept) {
			t.Errorf("removing filesystem lost unrelated comment %q:\n%s", kept, after)
		}
	}
}

func TestEditPermissions(t *testing.T) {
	for _, edit := range []struct {
		name string
		fn   func(string) error
	}{
		{"add", func(path string) error { return AddMCP(path, Entry{Name: "added", Command: "echo"}) }},
		{"remove", func(path string) error { return RemoveMCP(path, "filesystem") }},
		{"disable", func(path string) error { return SetDisabled(path, "filesystem", true) }},
		{"force create", func(path string) error { return Create(path, "", true) }},
	} {
		t.Run(edit.name, func(t *testing.T) {
			path := writeEditConfig(t, commentedConfig)
			if err := os.Chmod(path, 0640); err != nil {
				t.Fatal(err)
			}
			if err := edit.fn(path); err != nil {
				t.Fatal(err)
			}
			checkEditMode(t, path, 0640)
			files, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(files) != 1 {
				t.Fatalf("temporary files remain: %v, %v", files, err)
			}
		})
	}
}

func writeEditConfig(t *testing.T, data string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node.yaml")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readEditConfig(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func checkEditMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func TestEditThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "node.yaml")
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(commentedConfig), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "node.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := AddMCP(link, Entry{Name: "added", Command: "echo"}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced: %v %v", info, err)
	}
	if err := Create(link, "", true); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("link replaced by Create: %v %v", info, err)
	}
}

// commentedConfig is a hand-edited config: comments on and between entries,
// and a commented-out entry after the last one.
const commentedConfig = `# Connect this node to your hub's node endpoint.
hub:
  url: wss://mesh.example.com/node
  # Choose one credential source; key files must be chmod 600 on Unix.
  # key_env: MCPMESH_NODE_KEY
  # key_file: ~/.config/mcp-mesh/node.key

# Omit the name to use this machine's hostname.
# node:
#   name: work-pc
log_level: info # debug | info | warn | error

mcps:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "~/code"]
    # env: { DEBUG: "1" }
    # dir: ~/code
    start: eager # eager | lazy
    # idle_timeout: 15m # lazy only; defaults to 10m
    sharing: shared # per-client is not supported yet
    # allow_tools: [read_file, list_directory] # omit to allow all tools
    # disabled: true # omit this MCP from the node

  # Connect to a running Streamable HTTP MCP instead of spawning a command.
  # browser:
  #   url: http://127.0.0.1:8931/mcp
  #   headers: { Authorization: "Bearer ${env:LOCAL_TOKEN}" }
`
