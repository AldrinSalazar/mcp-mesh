package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/spf13/cobra"
)

func TestConnectCLI(t *testing.T) {
	for _, command := range []string{"add", "wrap"} {
		t.Run(command, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			writeCLIConfig(t, path, cliConfig)
			var gets atomic.Int32
			hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gets.Add(1)
				if r.Method != "GET" || r.Header.Get("X-Test") != "present" {
					t.Errorf("probe: %s %v", r.Method, r.Header)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "event: endpoint\ndata: /messages\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer hs.Close()
			args := []string{command, "--name", "blender", "--url", hs.URL + "/mcp", "--header", "X-Test=present"}
			if command == "wrap" {
				args = append(args, "--add")
			}
			_, notices, err := runCLI(t, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			e := entries(t, path)
			if e[0].Transport != "sse" || e[0].Start != "lazy" || gets.Load() != 1 || strings.Contains(notices, "Couldn't reach") {
				t.Fatalf("connect: %+v; %s", e, notices)
			}
		})
	}
}

func TestConnectCLIFallbackAndOverride(t *testing.T) {
	for _, tc := range []struct {
		name, path, transport, start, want, policy string
		notice                                     bool
	}{
		{"sse fallback", "/sse", "", "", "sse", "lazy", true},
		{"http fallback", "/mcp", "", "", "http", "lazy", true},
		{"explicit eager", "/sse", "sse", "eager", "sse", "eager", false},
		{"override path", "/sse", "http", "", "http", "lazy", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			writeCLIConfig(t, path, cliConfig)
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := "http://" + l.Addr().String() + tc.path
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			args := []string{"add", "--name", "app", "--url", endpoint}
			if tc.transport != "" {
				args = append(args, "--transport", tc.transport)
			}
			if tc.start != "" {
				args = append(args, "--start", tc.start)
			}
			_, notices, err := runCLI(t, "", args...)
			if err != nil {
				t.Fatal(err)
			}
			e := entries(t, path)[0]
			if e.Transport != tc.want || e.Start != tc.policy || strings.Contains(notices, "Couldn't reach") != tc.notice {
				t.Fatalf("entry: %+v; %s", e, notices)
			}
			if tc.notice && !strings.Contains(notices, "Set --transport to override.") {
				t.Fatal(notices)
			}
		})
	}
}

func TestTransportFlagValidation(t *testing.T) {
	for _, command := range []string{"add", "wrap"} {
		t.Run(command, func(t *testing.T) {
			path, _, _ := setupCLI(t, false)
			writeCLIConfig(t, path, cliConfig)
			for _, args := range [][]string{{"--transport", "sse", "--", "server"}, {"--transport", "unknown", "--url", "http://localhost/mcp"}} {
				if _, _, err := runCLI(t, "", append([]string{command}, args...)...); err == nil || !strings.Contains(err.Error(), "--transport") {
					t.Fatalf("flags %v: %v", args, err)
				}
			}
		})
	}
}

func TestWrapSSEForeground(t *testing.T) {
	path, _, _ := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	agentRunner = func(_ context.Context, cfg agent.Config) error {
		if cfg.MCPs[0].Upstream.Transport() != proto.TransportSSE {
			t.Fatalf("transport: %s", cfg.MCPs[0].Upstream.Transport())
		}
		return nil
	}
	if _, _, err := runCLI(t, "", "wrap", "--foreground", "--name", "app", "--url", "http://localhost/sse", "--transport", "sse"); err != nil {
		t.Fatal(err)
	}
}

func TestConnectStatus(t *testing.T) {
	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	snapshot := nodectl.Snapshot{Config: nodectl.ConfigState{Exists: true, Entries: []config.Entry{{Name: "launch", Command: "server"}, {Name: "connect", URL: "http://localhost/sse", Transport: "sse"}}}}
	if err := printStatus(cmd, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "connect (sse)") || !strings.Contains(out.String(), "launch") {
		t.Fatal(out.String())
	}
	out.Reset()
	snapshot.Daemon = nodectl.DaemonState{Running: true, Status: control.Status{MCPs: []control.MCPStatus{{Name: "app", Transport: "sse", State: control.MCPBackoff, Unreachable: true}}}}
	if err := printStatus(cmd, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "waiting for app") || !strings.Contains(out.String(), "connect (sse)") || strings.Contains(out.String(), "backoff") {
		t.Fatal(out.String())
	}
	path, _, _ := setupCLI(t, false)
	writeCLIConfig(t, path, cliConfig)
	if _, _, err := runCLI(t, "", "list"); err != nil {
		t.Fatal(err)
	}
}
