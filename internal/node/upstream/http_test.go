package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestHTTPClient(t *testing.T) {
	for _, tc := range []struct {
		name            string
		json, stateless bool
	}{
		{"sse", false, false}, {"json", true, false}, {"stateless", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testContext(t)
			server := testServer(nil)
			handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{JSONResponse: tc.json, Stateless: tc.stateless})
			var mu sync.Mutex
			methods := make(map[string]int)
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Relay-Test") != "present" {
					t.Errorf("missing custom header on %s", r.Method)
				}
				if r.Method == http.MethodGet || r.Method == http.MethodDelete {
					if r.Header.Get("Mcp-Protocol-Version") == "" {
						t.Error("missing negotiated version")
					}
					if !tc.stateless && r.Header.Get("Mcp-Session-Id") == "" {
						t.Error("missing session ID")
					}
				}
				mu.Lock()
				methods[r.Method]++
				mu.Unlock()
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			conn, err := NewHTTP(HTTPConfig{URL: httpServer.URL, Headers: map[string]string{"X-Relay-Test": "present"}}).Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			session := connectClient(t, ctx, conn, nil)
			checkTools(t, ctx, session, true)
			if !tc.stateless && conn.SessionID() == "" {
				t.Fatal("no session ID")
			}
			if err := session.Close(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			deletes := methods[http.MethodDelete]
			mu.Unlock()
			if !tc.stateless && deletes != 1 {
				t.Fatalf("DELETE count = %d", deletes)
			}
		})
	}
}

func TestHTTPNotification(t *testing.T) {
	ctx := testContext(t)
	server := testServer([]string{"2025-03-26"})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	getReady := make(chan struct{})
	var once sync.Once
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(&readyWriter{ResponseWriter: w, ready: func() { once.Do(func() { close(getReady) }) }, enabled: r.Method == http.MethodGet}, r)
	}))
	defer httpServer.Close()
	conn, err := NewHTTP(HTTPConfig{URL: httpServer.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changed := make(chan struct{}, 1)
	defer conn.Close()
	session := connectClient(t, ctx, conn, &mcp.ClientOptions{ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
		select {
		case changed <- struct{}{}:
		default:
		}
	}})
	checkTools(t, ctx, session, false)
	select {
	case <-getReady:
	case <-ctx.Done():
		t.Fatal("GET stream never started")
	}
	server.AddTool(&mcp.Tool{Name: "added", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	select {
	case <-changed:
	case <-ctx.Done():
		t.Fatal("notification never arrived")
	}
}

type readyWriter struct {
	http.ResponseWriter
	enabled bool
	ready   func()
}

func (w *readyWriter) Flush() {
	w.ResponseWriter.(http.Flusher).Flush()
	if w.enabled {
		w.ready()
	}
}

func TestHTTPOptionalGETAndClose(t *testing.T) {
	ctx := testContext(t)
	server := testServer([]string{"2025-03-26"})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	get := make(chan struct{})
	var once sync.Once
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			once.Do(func() { close(get) })
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	conn, err := NewHTTP(HTTPConfig{URL: httpServer.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	session := connectClient(t, ctx, conn, nil)
	checkTools(t, ctx, session, false)
	select {
	case <-get:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Read(ctx); err == nil {
		t.Fatal("Read on closed HTTP connection succeeded")
	}
}
func TestHTTPRawRelay(t *testing.T) {
	ctx := testContext(t)
	const outgoing = `{"jsonrpc":"2.0","id":"hub-id","method":"custom/method","params":{"unknown":[1,2]}}`
	const incoming = `{"jsonrpc":"2.0","id":"hub-id","result":{"unknown":[3,4]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != outgoing {
			t.Errorf("message changed: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, incoming)
	}))
	defer server.Close()
	conn, err := NewHTTP(HTTPConfig{URL: server.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg, err := jsonrpc.DecodeMessage([]byte(outgoing))
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, msg); err != nil {
		t.Fatal(err)
	}
	got, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := jsonrpc.EncodeMessage(got)
	if string(raw) != incoming {
		t.Fatalf("response changed: %s", raw)
	}
}

// A session-based server that rejects calls without a session with a plain
// JSON body (as Fusion does) must not end the connection: the call gets a
// JSON-RPC error, and the client falls back from server/discover to initialize.
func TestHTTPPlainErrorBody(t *testing.T) {
	ctx := testContext(t)
	server := testServer(nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	var mu sync.Mutex
	var rejected []string
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				Method string `json:"method"`
			}
			_ = json.Unmarshal(body, &msg)
			if msg.Method != "initialize" {
				mu.Lock()
				rejected = append(rejected, msg.Method)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error": "Missing MCP-Session-Id header"}`)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	conn, err := NewHTTP(HTTPConfig{URL: httpServer.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	msg, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":7,"method":"tools/list"}`))
	if err := conn.Write(ctx, msg); err != nil {
		t.Fatal(err)
	}
	got, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := got.(*jsonrpc.Response)
	var rpcErr *jsonrpc.Error
	if !ok || resp.ID != msg.(*jsonrpc.Request).ID || !errors.As(resp.Error, &rpcErr) ||
		rpcErr.Message != `HTTP POST: 400 Bad Request: {"error": "Missing MCP-Session-Id header"}` {
		t.Fatalf("response = %#v", got)
	}

	session := connectClient(t, ctx, conn, nil)
	checkTools(t, ctx, session, false)
	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(rejected, []string{"tools/list", "server/discover"}) {
		t.Fatalf("rejected = %v", rejected)
	}
}

func TestHTTPFailureAndConcurrentClose(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"server failure", http.StatusServiceUnavailable, "unavailable"},
		{"malformed response", http.StatusOK, "invalid JSON"},
		{"empty stream", http.StatusOK, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testContext(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				typ := "application/json"
				if tc.name == "empty stream" {
					typ = "text/event-stream"
				}
				w.Header().Set("Content-Type", typ)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			conn, err := NewHTTP(HTTPConfig{URL: server.URL}).Open(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			msg, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
			_ = conn.Write(ctx, msg)
			if _, err := conn.Read(ctx); err == nil {
				t.Fatal("Read succeeded after failure")
			}
			closed := make(chan error, 2)
			for range 2 {
				go func() { closed <- conn.Close() }()
			}
			for range 2 {
				select {
				case err := <-closed:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}
func TestHTTPCloseUnblocksRequests(t *testing.T) {
	ctx := testContext(t)
	requestStarted := make(chan struct{})
	requestEnded := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(requestStarted)
		<-r.Context().Done()
		close(requestEnded)
	}))
	defer server.Close()
	conn, err := NewHTTP(HTTPConfig{URL: server.URL}).Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	writeDone := make(chan error, 1)
	readDone := make(chan error, 1)
	msg, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`))
	go func() { writeDone <- conn.Write(ctx, msg) }()
	go func() { _, err := conn.Read(ctx); readDone <- err }()
	select {
	case <-requestStarted:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Calls are posted in the background, so Write returns before the response.
	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("write: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-readDone:
		if err == nil {
			t.Fatal("read succeeded during close")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case <-requestEnded:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
