//go:build linux || darwin

package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

// hubPeer plays the hub over a raw node connection, so the test controls the
// exact envelope sequence.
type hubPeer struct {
	t    *testing.T
	ctx  context.Context
	conn *wire.Conn
	next int
}

func (h *hubPeer) send(e *proto.Envelope) {
	h.t.Helper()
	if err := h.conn.Send(h.ctx, e); err != nil {
		h.t.Fatal(err)
	}
}

func (h *hubPeer) recv(typ proto.Type, ch string) *proto.Envelope {
	h.t.Helper()
	for {
		e, err := h.conn.Recv(h.ctx)
		if err != nil {
			h.t.Fatalf("waiting for %s on %q: %v", typ, ch, err)
		}
		if e.T == typ && e.Ch == ch {
			return e
		}
	}
}

func (h *hubPeer) open(mcp, ch, client string) *proto.Envelope {
	h.t.Helper()
	h.send(&proto.Envelope{T: proto.TOpen, MCP: mcp, Ch: ch, Client: client})
	return h.recv(proto.TOpened, ch)
}

func (h *hubPeer) rpc(ch, method, params string) json.RawMessage {
	h.t.Helper()
	h.next++
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q,"params":%s}`, h.next, method, params)
	h.send(&proto.Envelope{T: proto.TMCP, Ch: ch, Msg: json.RawMessage(msg)})
	for {
		e := h.recv(proto.TMCP, ch)
		var resp struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(e.Msg, &resp) == nil && resp.ID == h.next {
			return resp.Result
		}
	}
}

func (h *hubPeer) initialize(ch string) {
	h.t.Helper()
	h.rpc(ch, "initialize", `{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}`)
	h.send(&proto.Envelope{T: proto.TMCP, Ch: ch, Msg: json.RawMessage(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)})
}

func (h *hubPeer) pid(ch string) int {
	h.t.Helper()
	var res struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(h.rpc(ch, "tools/call", `{"name":"pid","arguments":{}}`), &res); err != nil || len(res.Content) == 0 {
		h.t.Fatalf("pid result: %v", err)
	}
	pid, err := strconv.Atoi(res.Content[0].Text)
	if err != nil {
		h.t.Fatal(err)
	}
	return pid
}

func TestPerClientProcesses(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	peers := make(chan *wire.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := wire.Accept(w, r, 0)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		peers <- conn
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := shortDir(t)
	opts := Options{ConfigPath: filepath.Join(dir, "node.yaml"), SocketPath: filepath.Join(dir, "s"), ToolCacheDir: filepath.Join(dir, "tools"), Version: "test", Logger: logger}
	hubURL := "ws" + strings.TrimPrefix(server.URL, "http") + proto.NodePath
	definition := func(name, extra string) string {
		return fmt.Sprintf("  %s:\n    command: %q\n    env: {%s: server}\n%s", name, exe, helperEnv, extra)
	}
	data := fmt.Sprintf("hub: {url: %q}\nnode: {name: desk}\nmcps:\n", hubURL) +
		definition("pc", "    sharing: per-client\n    max_clients: 2\n") + definition("sh", "")
	if err := os.WriteFile(opts.ConfigPath, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	c, _ := startTestDaemon(t, opts)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	conn := <-peers
	h := &hubPeer{t: t, ctx: ctx, conn: conn}
	if e, err := conn.Recv(ctx); err != nil || e.T != proto.THello {
		t.Fatalf("hello: %+v, %v", e, err)
	}
	h.send(&proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{Version: proto.Version, NodeID: "n", MaxMessageSize: proto.DefaultMaxMessageSize}})

	for ch, client := range map[string]string{"a": "alice", "b": "bob"} {
		if e := h.open("pc", ch, client); e.Error != "" {
			t.Fatalf("open %s: %s", ch, e.Error)
		}
		h.initialize(ch)
	}
	pidA, pidB := h.pid("a"), h.pid("b")
	if pidA == pidB || pidA == os.Getpid() {
		t.Fatalf("pids %d and %d are not distinct processes", pidA, pidB)
	}
	if e := h.open("pc", "c", "carol"); !strings.Contains(e.Error, "too many clients for pc (max_clients 2)") {
		t.Fatalf("third client: %+v", e)
	}
	// A shared MCP still serves one channel at a time.
	if e := h.open("sh", "s1", ""); e.Error != "" {
		t.Fatal(e.Error)
	}
	if e := h.open("sh", "s2", ""); !strings.Contains(e.Error, "already has a channel") {
		t.Fatalf("second shared channel: %+v", e)
	}

	// Any process's tool list reaches the shared cache.
	h.listAll("a")
	await(t, func() bool { tools, err := c.Tools(t.Context(), "pc"); return err == nil && len(tools) == 3 })

	st := mcpStatus(t, c, "pc")
	if st.Sharing != "per-client" || st.MaxClients != 2 || st.PID != 0 || len(st.Clients) != 2 || st.State != control.MCPRunning || st.Tools != 3 {
		t.Fatalf("status: %+v", st)
	}
	got := map[string]int{}
	for _, cl := range st.Clients {
		got[cl.Client] = cl.PID
	}
	if got["alice"] != pidA || got["bob"] != pidB {
		t.Fatalf("status pids %v, want alice=%d bob=%d", got, pidA, pidB)
	}
	await(t, func() bool {
		logs, _ := c.Logs(t.Context(), "pc", 4096)
		return strings.Contains(string(logs), "[alice] called pid") && strings.Contains(string(logs), "[bob] called pid")
	})

	// Closing a channel stops only its process.
	h.send(&proto.Envelope{T: proto.TClose, MCP: "pc", Ch: "a"})
	await(t, func() bool { return len(mcpStatus(t, c, "pc").Clients) == 1 })
	await(t, func() bool { return processGone(pidA) })
	if processGone(pidB) {
		t.Fatal("closing alice's channel stopped bob's process")
	}

	// Restart ends every remaining channel with reason restart.
	if err := c.Restart(t.Context(), "pc"); err != nil {
		t.Fatal(err)
	}
	if e := h.recv(proto.TClose, "b"); e.Reason != proto.CloseRestart {
		t.Fatalf("close reason = %q", e.Reason)
	}
	await(t, func() bool { return len(mcpStatus(t, c, "pc").Clients) == 0 && processGone(pidB) })
}

func processGone(pid int) bool { return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) }

// listAll pages through tools/list; the node reports a tool list to its cache
// only after seeing every page.
func (h *hubPeer) listAll(ch string) {
	h.t.Helper()
	params := `{}`
	for {
		var page struct {
			NextCursor string `json:"nextCursor"`
		}
		if err := json.Unmarshal(h.rpc(ch, "tools/list", params), &page); err != nil {
			h.t.Fatal(err)
		}
		if page.NextCursor == "" {
			return
		}
		params = fmt.Sprintf(`{"cursor":%q}`, page.NextCursor)
	}
}
