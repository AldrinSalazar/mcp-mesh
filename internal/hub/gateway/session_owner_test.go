package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
)

// A session-based client's Mcp-Session-Id is not a credential: another
// client's key with it reaches nothing.
func TestSessionBelongsToItsClient(t *testing.T) {
	c := catalog.New()
	c.SetMCP("n", "m", []*mcp.Tool{tool("t", "")}, okCaller("ok"))
	g := New(Options{Catalog: c, Identify: byHeader})
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)

	// send makes one raw request as client (""= no bearer token at all).
	send := func(method, client, session, body string) (*http.Response, string) {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json, text/event-stream")
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if client != "" {
			req.Header.Set("Authorization", "Bearer key-of-"+client)
			req.Header.Set("X-Test-Client", client)
		}
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
			req.Header.Set("MCP-Protocol-Version", "2025-11-25")
		}
		if method == http.MethodGet {
			// A standalone stream stays open; the status line is all we need.
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			return resp, ""
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp, string(data)
	}

	resp, body := send(http.MethodPost, "alice", "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"alice","version":"1"}}}`)
	session := resp.Header.Get("Mcp-Session-Id")
	if resp.StatusCode != http.StatusOK || session == "" {
		t.Fatalf("initialize: %d %q, session %q", resp.StatusCode, body, session)
	}
	if resp, body := send(http.MethodPost, "alice", session, `{"jsonrpc":"2.0","method":"notifications/initialized"}`); resp.StatusCode >= 300 {
		t.Fatalf("initialized: %d %q", resp.StatusCode, body)
	}
	list := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`

	for _, tt := range []struct{ name, method, client, body string }{
		{"another key lists tools", http.MethodPost, "mallory", list},
		{"another key calls a tool", http.MethodPost, "mallory", `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"n__m__t","arguments":{}}}`},
		{"another key opens the stream", http.MethodGet, "mallory", ""},
		{"another key ends the session", http.MethodDelete, "mallory", ""},
		{"no token at all", http.MethodPost, "", list},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := send(tt.method, tt.client, session, tt.body)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status %d %q, want 403", resp.StatusCode, body)
			}
			if strings.Contains(body, "n__m__t") || strings.Contains(body, `"result"`) {
				t.Errorf("leaked: %q", body)
			}
		})
	}

	// The owner is unaffected, the refused DELETE above left the session in
	// place, and the owner can still end it.
	if resp, body := send(http.MethodPost, "alice", session, list); resp.StatusCode != http.StatusOK || !strings.Contains(body, "n__m__t") {
		t.Fatalf("owner's tools/list: %d %q", resp.StatusCode, body)
	}
	if resp, body := send(http.MethodDelete, "alice", session, ""); resp.StatusCode >= 300 {
		t.Fatalf("owner's DELETE: %d %q", resp.StatusCode, body)
	}
	if resp, _ := send(http.MethodPost, "alice", session, list); resp.StatusCode != http.StatusNotFound {
		t.Errorf("after DELETE: %d, want 404", resp.StatusCode)
	}
}
