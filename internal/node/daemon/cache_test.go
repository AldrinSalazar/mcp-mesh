package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestCacheFingerprint(t *testing.T) {
	definition := func() config.MCP {
		return config.MCP{Name: "fake", Transport: proto.TransportStdio, Command: "echo", Args: []string{"arg"}, Env: []string{"A=1"}, Dir: "/tmp", URL: "http://localhost/mcp", Headers: map[string]string{"X": "1"}, AllowTools: []string{"echo"}}
	}
	cache := toolCache{dir: t.TempDir(), definition: definition()}
	tools := json.RawMessage(`[{"name":"echo","inputSchema":{"type":"object"}}]`)
	if err := cache.write(tools); err != nil {
		t.Fatal(err)
	}
	if got := cache.read(); string(got) != string(tools) {
		t.Fatalf("read=%s", got)
	}
	for _, tc := range []struct {
		name   string
		change func(*config.MCP)
	}{
		{"transport", func(d *config.MCP) { d.Transport = proto.TransportHTTP }},
		{"command", func(d *config.MCP) { d.Command = "other" }},
		{"args", func(d *config.MCP) { d.Args = []string{"other"} }},
		{"env", func(d *config.MCP) { d.Env = []string{"A=2"} }},
		{"dir", func(d *config.MCP) { d.Dir = "/other" }},
		{"url", func(d *config.MCP) { d.URL = "http://other/mcp" }},
		{"headers", func(d *config.MCP) { d.Headers["X"] = "2" }},
		{"allowlist", func(d *config.MCP) { d.AllowTools = []string{"hidden"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := cache
			changed.definition = definition()
			tc.change(&changed.definition)
			if changed.read() != nil {
				t.Fatal("accepted stale cache")
			}
		})
	}
	changed := cache
	changed.definition.Start = proto.StartLazy
	if changed.read() == nil {
		t.Fatal("lifecycle change invalidated definitions")
	}
	unrestricted := cache
	unrestricted.definition.AllowTools = nil
	empty := unrestricted
	empty.definition.AllowTools = []string{}
	if unrestricted.fingerprint() == empty.fingerprint() {
		t.Fatal("nil and empty allowlist have same fingerprint")
	}
	for _, raw := range []string{"{", `{"tools":[]}`, `{"fingerprint":"` + cache.fingerprint() + `","tools":null}`, `{"fingerprint":"` + cache.fingerprint() + `","tools":{}}`} {
		if err := os.WriteFile(cache.path(), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		if cache.read() != nil {
			t.Fatalf("accepted broken cache: %s", raw)
		}
	}
	if err := cache.write(json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	if string(cache.read()) != "[]" {
		t.Fatal("empty tool array ignored")
	}
	entries, err := os.ReadDir(cache.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(cache.path()) {
		t.Fatalf("temporary cache files remain: %v", entries)
	}
}
