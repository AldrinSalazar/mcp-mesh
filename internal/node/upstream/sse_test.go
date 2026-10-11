package upstream

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSSEClient(t *testing.T) {
	ctx := testContext(t)
	server := testServer([]string{"2024-11-05"})
	handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
	var mu sync.Mutex
	methods := map[string]int{}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Relay-Test") != "present" {
			t.Errorf("missing header on %s", r.Method)
		}
		mu.Lock()
		methods[r.Method]++
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer hs.Close()
	headers := map[string]string{"X-Relay-Test": "present"}
	up := NewSSE(HTTPConfig{URL: hs.URL + "/sse", Headers: headers})
	headers["X-Relay-Test"] = "changed"
	if up.Transport() != proto.TransportSSE {
		t.Fatal(up.Transport())
	}
	conn, err := up.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	changed := make(chan struct{}, 1)
	session, err := mcp.NewClient(&mcp.Implementation{Name: "relay-test", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	}).Connect(ctx, connTransport{conn}, &mcp.ClientSessionOptions{ProtocolVersion: "2024-11-05"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	checkTools(t, ctx, session, true)
	server.AddTool(&mcp.Tool{Name: "added", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("notification never arrived")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if methods["GET"] != 1 || methods["POST"] < 4 {
		t.Fatalf("methods: %v", methods)
	}
}

func TestSSEStreamEnd(t *testing.T) {
	ctx := testContext(t)
	end := make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: endpoint\ndata: /messages\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-end:
		case <-r.Context().Done():
		}
	}))
	defer hs.Close()
	conn, err := NewSSE(HTTPConfig{URL: hs.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	close(end)
	if _, err := conn.Read(ctx); !errors.Is(err, io.EOF) {
		t.Fatalf("stream end: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRefusedPort(t *testing.T) {
	for _, kind := range []string{"http", "sse"} {
		t.Run(kind, func(t *testing.T) {
			ctx := testContext(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			endpoint := "http://" + listener.Addr().String() + "/" + kind
			if err := listener.Close(); err != nil {
				t.Fatal(err)
			}
			var up Upstream = NewHTTP(HTTPConfig{URL: endpoint})
			if kind == "sse" {
				up = NewSSE(HTTPConfig{URL: endpoint})
			}
			conn, err := up.Open(ctx)
			if conn != nil {
				defer conn.Close()
				msg, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
				if err := conn.Write(ctx, msg); err != nil {
					t.Fatal(err)
				}
				_, err = conn.Read(ctx)
			}
			if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), endpoint) {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

func TestSSECloseCancelsPOST(t *testing.T) {
	ctx := testContext(t)
	started, ended := make(chan struct{}), make(chan struct{})
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "event: endpoint\ndata: /messages\n\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		} else {
			io.Copy(io.Discard, r.Body)
			close(started)
			<-r.Context().Done()
			close(ended)
		}
	}))
	defer hs.Close()
	conn, err := NewSSE(HTTPConfig{URL: hs.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	written := make(chan error, 1)
	go func() { written <- conn.Write(ctx, &jsonrpc.Request{Method: "notifications/test"}) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("write succeeded")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
