package config

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestTransport(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		want        proto.Transport
		err         string
	}{
		{"default", "url: http://localhost/sse", proto.TransportHTTP, ""},
		{"http", "url: http://localhost/sse\n    transport: http", proto.TransportHTTP, ""},
		{"sse", "url: http://localhost/mcp\n    transport: sse", proto.TransportSSE, ""},
		{"unknown", "url: http://localhost/mcp\n    transport: websocket", "", "transport must be http or sse"},
		{"command", "command: server\n    transport: http", "", "transport is only allowed with url"},
		{"empty on command", "command: server\n    transport: ''", "", "transport is only allowed with url"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("hub:\n  url: ws://hub/node\nmcps:\n  app:\n    " + tc.entry + "\n")
			cfg, err := Parse(data, "node.yaml")
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), "node.yaml:6:") || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("validation: %v", err)
				}
			} else if err != nil || cfg.MCPs[0].Transport != tc.want {
				t.Fatalf("transport: %v %v", cfg, err)
			}
		})
	}
}

func TestTransportEdits(t *testing.T) {
	for _, transport := range []string{"", "http", "sse"} {
		t.Run(fmt.Sprintf("transport=%s", transport), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "node.yaml")
			if err := Create(path, "ws://hub/node", false); err != nil {
				t.Fatal(err)
			}
			if err := AddMCP(path, Entry{Name: "app", URL: "http://localhost/mcp", Transport: transport, Start: "lazy"}); err != nil {
				t.Fatal(err)
			}
			entries, err := Entries(path)
			if err != nil || len(entries) != 1 || entries[0].Transport != transport {
				t.Fatalf("entries: %v %v", entries, err)
			}
			cfg, err := Load(path)
			want := proto.TransportHTTP
			if transport == "sse" {
				want = proto.TransportSSE
			}
			if err != nil || cfg.MCPs[0].Transport != want {
				t.Fatalf("config: %v %v", cfg, err)
			}
		})
	}
}
