package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

// TestLargeEnvelopesAreChunked runs the node against a raw WebSocket hub and
// checks that a large envelope after welcome goes out as binary chunks.
func TestLargeEnvelopesAreChunked(t *testing.T) {
	tests := []struct {
		name    string
		version int
		typ     websocket.MessageType
	}{
		{"binary chunks", proto.Version, websocket.MessageBinary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			got := make(chan websocket.MessageType, 1)
			handlerDone := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				defer close(handlerDone)
				ws, err := websocket.Accept(w, req, &websocket.AcceptOptions{Subprotocols: []string{proto.Subprotocol}})
				if err != nil {
					return
				}
				defer ws.CloseNow()
				ws.SetReadLimit(1 << 30)
				if _, _, err := ws.Read(ctx); err != nil { // hello
					return
				}
				welcome, _ := json.Marshal(&proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{Version: tt.version, NodeID: "n", MaxMessageSize: proto.DefaultMaxMessageSize}})
				open, _ := json.Marshal(&proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: "c"})
				if ws.Write(ctx, websocket.MessageText, welcome) != nil || ws.Write(ctx, websocket.MessageText, open) != nil {
					return
				}
				typ, _, err := ws.Read(ctx) // opened, carrying a long error
				if err == nil {
					got <- typ
				}
			}))
			defer server.Close()
			up := newUpstream(t)
			up.failure = errors.New(strings.Repeat("x", 3*wire.ChunkSize))
			runCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				done <- Run(runCtx, Config{
					HubURL: "ws" + strings.TrimPrefix(server.URL, "http") + proto.NodePath,
					Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
					MCPs:   []MCP{{Name: "tools", Upstream: up}},
				})
			}()
			defer func() { stop(); <-done; <-handlerDone }()
			if typ := receive(t, ctx, got); typ != tt.typ {
				t.Fatalf("large envelope frame type = %v, want %v", typ, tt.typ)
			}
		})
	}
}
