//go:build linux || darwin

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/server"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/daemon"
)

// A node daemon started without a key asks the hub to pair; an admin
// approves it under another name in the dashboard API, and the node saves
// the key and the name and connects with them.
func TestPairingRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	quiet := slog.New(slog.DiscardHandler)

	addrs := make(chan [3]string, 1)
	hubDone := make(chan error, 1)
	go func() {
		hubDone <- server.Run(ctx, server.Config{
			AdminListen: "127.0.0.1:0", NodeListen: "127.0.0.1:0", ClientListen: "127.0.0.1:0",
			PublicHost: "127.0.0.1", DataDir: t.TempDir(), Auth: true, Pairing: true,
			AdminPassword: "correct horse battery staple", Version: "test", Logger: quiet,
			OnListen: func(admin, node, client string) { addrs <- [3]string{admin, node, client} },
		})
	}()
	var admin, nodeAddr string
	select {
	case a := <-addrs:
		admin, nodeAddr = a[0], a[1]
	case err := <-hubDone:
		t.Fatal("hub:", err)
	}

	// Unix socket paths are short; t.TempDir can be too long on macOS.
	dir, err := os.MkdirTemp("/tmp", "mesh-pair-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgPath := filepath.Join(dir, "node.yaml")
	if err := os.WriteFile(cfgPath, []byte("hub:\n  url: ws://"+nodeAddr+"/node\nnode:\n  name: desk\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nodeCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan error, 1)
	go func() {
		nodeDone <- daemon.Run(nodeCtx, daemon.Options{ConfigPath: cfgPath, SocketPath: filepath.Join(dir, "s"), ToolCacheDir: dir, Logger: quiet})
	}()
	defer func() {
		stopNode()
		<-nodeDone
	}()
	node := control.NewClient(filepath.Join(dir, "s"))
	await := func(what string, ok func(control.Status) bool) control.Status {
		t.Helper()
		for {
			if st, err := node.Status(ctx); err == nil && ok(st) {
				return st
			}
			select {
			case <-ctx.Done():
				t.Fatal("waiting for", what)
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	code := await("a pairing code", func(st control.Status) bool { return st.Hub.PairCode != "" }).Hub.PairCode

	// The admin signs in and finds the request with the same code.
	jar, _ := cookiejar.New(nil)
	web := &http.Client{Jar: jar}
	base := "http://" + admin + "/api/v1"
	var csrf string
	call := func(method, path string, body any, out any) int {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequestWithContext(ctx, method, base+path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-CSRF-Token", csrf)
		res, err := web.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil {
			json.NewDecoder(res.Body).Decode(out)
		}
		return res.StatusCode
	}
	var session struct {
		CSRF string `json:"csrf"`
	}
	if status := call("POST", "/session", map[string]string{"username": "admin", "password": "correct horse battery staple"}, &session); status != 200 {
		t.Fatalf("sign in: %d", status)
	}
	csrf = session.CSRF
	var pending []struct {
		ID, Node, Code string
	}
	call("GET", "/pairings", nil, &pending)
	if len(pending) != 1 || pending[0].Node != "desk" || pending[0].Code != code {
		t.Fatalf("pending = %+v, want desk with code %s", pending, code)
	}
	if status := call("POST", "/pairings/"+pending[0].ID+"/approve", map[string]string{"name": "studio"}, nil); status != 200 {
		t.Fatalf("approve: %d", status)
	}

	st := await("the node to connect", func(st control.Status) bool { return st.Hub.Connected })
	if st.Node != "studio" || st.Hub.PairCode != "" || st.ConfigChanged {
		t.Fatalf("status = node %q, code %q, config changed %v", st.Node, st.Hub.PairCode, st.ConfigChanged)
	}
	// What the node saved works after a restart too.
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeName != "studio" || !strings.HasPrefix(cfg.Hub.Header.Get("Authorization"), "Bearer "+config.KeyPrefix) {
		t.Fatalf("saved config: node %q, key set %v", cfg.NodeName, cfg.Hub.Header.Get("Authorization") != "")
	}
	if info, err := os.Stat(config.DefaultKeyPath(cfgPath)); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key file: %v, %v", info, err)
	}
	var nodes []struct {
		Name   string `json:"name"`
		Online bool   `json:"online"`
	}
	call("GET", "/nodes", nil, &nodes)
	if len(nodes) != 1 || nodes[0].Name != "studio" || !nodes[0].Online {
		t.Fatalf("hub nodes = %+v", nodes)
	}
}
