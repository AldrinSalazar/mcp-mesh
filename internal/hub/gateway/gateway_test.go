package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var protocolVersions = []string{"2026-07-28", "2025-11-25"}

type callerFunc func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)

func (f callerFunc) CallTool(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return f(ctx, p)
}

func tool(name, description string) *mcp.Tool {
	return &mcp.Tool{
		Name: name, Description: description,
		InputSchema: map[string]any{"type": "object"},
	}
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func assertResult(t *testing.T, got, want *mcp.CallToolResult) {
	t.Helper()
	normalize := func(result *mcp.CallToolResult) map[string]any {
		data, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal result: %v", err)
		}
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		// The new protocol adds serverInfo and the explicit completion marker
		// in SDK middleware, after our forwarding handler returns.
		if meta, ok := value["_meta"].(map[string]any); ok {
			delete(meta, mcp.MetaKeyServerInfo)
			if len(meta) == 0 {
				delete(value, "_meta")
			}
		}
		if value["resultType"] == "complete" {
			delete(value, "resultType")
		}
		return value
	}
	if gotJSON, wantJSON := normalize(got), normalize(want); !reflect.DeepEqual(gotJSON, wantJSON) {
		t.Fatalf("result = %+v, want %+v", gotJSON, wantJSON)
	}
}

func connectClient(t *testing.T, g *Gateway, version string, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test", Version: "1"}, opts)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp"}, &mcp.ClientSessionOptions{
		ProtocolVersion: version,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() {
		if err := session.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	if got := session.InitializeResult().ProtocolVersion; got != version {
		t.Fatalf("negotiated protocol = %q, want %q", got, version)
	}
	return session
}

func runGateway(t *testing.T, g *Gateway) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("gateway Run did not stop")
		}
	})
}

func TestListAndForward(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			arguments := json.RawMessage(`{"path":"/tmp/file","number":9007199254740993,"nested":{"enabled":true}}`)
			want := &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: "contents"}},
				StructuredContent: map[string]any{"path": "/tmp/file"},
				Meta:              mcp.Meta{"trace": "upstream"},
			}
			c.SetMCP("work-pc", "fs", []*mcp.Tool{tool("read/file", "Reads a file."), tool("empty", "")}, callerFunc(func(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				if p.Name != "read/file" {
					t.Errorf("upstream name = %q", p.Name)
				}
				raw, ok := p.Arguments.(json.RawMessage)
				if !ok || !bytes.Equal(raw, arguments) {
					t.Errorf("arguments = %#v, want raw %s", p.Arguments, arguments)
				}
				deadline, ok := ctx.Deadline()
				if remaining := time.Until(deadline); !ok || remaining <= 119*time.Second || remaining > 120*time.Second {
					t.Errorf("default deadline remaining = %s, present = %v", remaining, ok)
				}
				return want, nil
			}))
			g := New(Options{Catalog: c, Version: "test-version"})
			session := connectClient(t, g, version, nil)
			if info := session.InitializeResult().ServerInfo; info.Name != "mcp-mesh" || info.Version != "test-version" {
				t.Errorf("server info = %+v", info)
			}
			listed, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(listed.Tools) != 2 {
				t.Fatalf("listed %d tools, want 2", len(listed.Tools))
			}
			for _, got := range listed.Tools {
				entry, ok := c.Lookup(got.Name)
				if !ok || got.Description != entry.Exposed().Description {
					t.Errorf("unexpected exposed tool: %+v", got)
				}
			}
			got, err := session.CallTool(t.Context(), &mcp.CallToolParams{
				Name: "work-pc__fs__read_file", Arguments: arguments,
			})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			assertResult(t, got, want)
		})
	}
}

func TestCallOutcomes(t *testing.T) {
	rpcErr := &jsonrpc.Error{Code: -32042, Message: "upstream refused", Data: json.RawMessage(`{"reason":"denied"}`)}
	tests := []struct {
		name       string
		caller     callerFunc
		timeout    time.Duration
		callName   string
		wantResult *mcp.CallToolResult
		wantRPC    *jsonrpc.Error
		outcome    string
		errorText  string
	}{
		{
			name: "ok", outcome: "ok", wantResult: textResult("done"),
			caller: func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return textResult("done"), nil
			},
		},
		{
			name: "tool error", outcome: "tool_error", errorText: "cannot read",
			wantResult: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "cannot read"}}, StructuredContent: map[string]any{"reason": "missing"}},
			caller: func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "cannot read"}}, StructuredContent: map[string]any{"reason": "missing"}}, nil
			},
		},
		{
			name: "unavailable", outcome: "error", errorText: "channel closed",
			wantResult: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "mcp-mesh: work-pc/fs is unavailable: channel closed"}}},
			caller: func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return nil, errors.New("channel closed")
			},
		},
		{
			name: "timeout", timeout: 20 * time.Millisecond, outcome: "error", errorText: "context deadline exceeded",
			wantResult: &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "mcp-mesh: work-pc/fs is unavailable: context deadline exceeded"}}},
			caller: func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			name: "protocol error", outcome: "error", errorText: "upstream refused", wantRPC: rpcErr,
			caller: func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return nil, fmt.Errorf("calling upstream: %w", rpcErr)
			},
		},
		{
			name: "unknown tool", callName: "missing", outcome: "error", errorText: `unknown tool "missing"`,
			wantRPC: &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: `unknown tool "missing"`},
			caller: func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return nil, errors.New("unknown tool reached upstream")
			},
		},
	}
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					c := catalog.New()
					c.SetMCP("work-pc", "fs", []*mcp.Tool{tool("read", "")}, tt.caller)
					log := activity.New(10)
					g := New(Options{Catalog: c, Activity: log, CallTimeout: tt.timeout})
					session := connectClient(t, g, version, nil)
					name := tt.callName
					if name == "" {
						name = "work-pc__fs__read"
					}
					start := time.Now()
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					got, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name})
					if tt.wantRPC != nil {
						var wireErr *jsonrpc.Error
						if !errors.As(err, &wireErr) || !reflect.DeepEqual(wireErr, tt.wantRPC) {
							t.Fatalf("call error = %v (%+v), want %+v", err, wireErr, tt.wantRPC)
						}
					} else {
						if err != nil {
							t.Fatalf("call: %v", err)
						}
						assertResult(t, got, tt.wantResult)
					}
					calls := log.Recent()
					if len(calls) != 1 {
						t.Fatalf("activity count = %d, want 1", len(calls))
					}
					call := calls[0]
					if call.Tool != name || call.Outcome != tt.outcome || !strings.Contains(call.Error, tt.errorText) {
						t.Errorf("activity = %+v", call)
					}
					if tt.callName == "" && (call.Node != "work-pc" || call.MCP != "fs") {
						t.Errorf("activity source = %s/%s", call.Node, call.MCP)
					}
					if call.Time.Before(start) || call.Time.After(time.Now()) || call.Duration <= 0 || call.Duration > time.Since(start) {
						t.Errorf("activity timing = %+v", call)
					}
					if tt.outcome == "ok" && call.Error != "" {
						t.Errorf("successful activity error = %q", call.Error)
					}
				})
			}
		})
	}
}

func TestCallerLookedUpAtCallTime(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			definition := tool("read", "Reads.")
			c.SetMCP("work-pc", "fs", []*mcp.Tool{definition}, callerFunc(func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return textResult("old caller"), nil
			}))
			log := activity.New(10)
			g := New(Options{Catalog: c, Activity: log})
			session := connectClient(t, g, version, nil)
			// Leave the server unsynced to prove the registered handler does not
			// retain the caller from the old connection.
			c.SetMCP("work-pc", "fs", []*mcp.Tool{definition}, callerFunc(func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return textResult("new caller"), nil
			}))
			params := &mcp.CallToolParams{Name: "work-pc__fs__read"}
			got, err := session.CallTool(t.Context(), params)
			if err != nil {
				t.Fatalf("reconnected call: %v", err)
			}
			assertResult(t, got, textResult("new caller"))
			c.RemoveNode("work-pc")
			_, err = session.CallTool(t.Context(), params)
			var wireErr *jsonrpc.Error
			if !errors.As(err, &wireErr) || wireErr.Code != jsonrpc.CodeInvalidParams || !strings.Contains(wireErr.Message, "unknown tool") {
				t.Fatalf("stale tool call = %v, want unknown-tool protocol error", err)
			}
			if calls := log.Recent(); len(calls) != 2 || calls[0].Outcome != "error" || calls[1].Outcome != "ok" {
				t.Errorf("activity after reconnect/removal = %+v", calls)
			}
		})
	}
}

func TestCatalogSync(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			g := New(Options{Catalog: c})
			changed := make(chan struct{}, 100)
			session := connectClient(t, g, version, &mcp.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
			})
			assertTools := func(want []*mcp.Tool) {
				t.Helper()
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				defer cancel()
				listed, err := session.ListTools(ctx, nil)
				if err != nil {
					t.Fatalf("list: %v", err)
				}
				if len(listed.Tools) != len(want) {
					t.Fatalf("listed %d tools, want %d", len(listed.Tools), len(want))
				}
				for i := range want {
					// Compare JSON because schemas arrive as decoded JSON values.
					gotJSON, _ := json.Marshal(listed.Tools[i])
					wantJSON, _ := json.Marshal(want[i])
					if !bytes.Equal(gotJSON, wantJSON) {
						t.Errorf("tool = %s, want %s", gotJSON, wantJSON)
					}
				}
			}
			awaitChange := func() {
				t.Helper()
				select {
				case <-changed:
				case <-time.After(5 * time.Second):
					t.Fatal("no tool-list-changed notification")
				}
			}
			assertTools(nil)
			caller := callerFunc(func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) { return textResult("ok"), nil })
			// Run must catch changes between construction and subscribing.
			c.SetMCP("work-pc", "fs", []*mcp.Tool{tool("read", "Before.")}, caller)
			runGateway(t, g)
			awaitChange()
			assertTools([]*mcp.Tool{c.List()[0].Exposed()})
			// A burst of definitions should expose the final snapshot in one batch.
			var tools []*mcp.Tool
			for i := range 30 {
				tools = append(tools, tool(fmt.Sprintf("read_%02d", i), "Reads."))
				c.SetMCP("work-pc", "fs", tools, caller)
			}
			awaitChange()
			want := make([]*mcp.Tool, 0, len(tools))
			for _, entry := range c.List() {
				want = append(want, entry.Exposed())
			}
			assertTools(want)
			select {
			case <-changed:
				t.Fatal("extra notification for catalog burst")
			default:
			}
			updated := tool("read_00", "New description.")
			updated.InputSchema = map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}
			updated.Annotations = &mcp.ToolAnnotations{ReadOnlyHint: true}
			c.SetMCP("work-pc", "fs", []*mcp.Tool{updated}, caller)
			awaitChange()
			assertTools([]*mcp.Tool{c.List()[0].Exposed()})
			c.RemoveMCP("work-pc", "fs")
			awaitChange()
			assertTools(nil)
		})
	}
}

func TestClientCancellation(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			started := make(chan struct{})
			finished := make(chan error, 1)
			c := catalog.New()
			c.SetMCP("work-pc", "fs", []*mcp.Tool{tool("read", "")}, callerFunc(func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				close(started)
				<-ctx.Done()
				finished <- ctx.Err()
				return nil, ctx.Err()
			}))
			g := New(Options{Catalog: c, CallTimeout: 30 * time.Second})
			session := connectClient(t, g, version, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			callDone := make(chan error, 1)
			go func() {
				_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "work-pc__fs__read"})
				callDone <- err
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("upstream call did not start")
			}
			cancel()
			select {
			case err := <-finished:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("upstream cancellation = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client cancellation did not propagate upstream")
			}
			select {
			case err := <-callDone:
				if !errors.Is(err, context.Canceled) {
					t.Errorf("client call error = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("client call did not stop")
			}
		})
	}
}

func TestIdentityReachesCallerAndActivity(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			seen := make(chan identity.Client, 1)
			c.SetMCP("work-pc", "fs", []*mcp.Tool{tool("read", "")}, callerFunc(func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				seen <- identity.From(ctx)
				return textResult("ok"), nil
			}))
			calls := activity.New(10)
			g := New(Options{
				Catalog:  c,
				Activity: calls,
				Identify: func(r *http.Request) identity.Client {
					if r.Header.Get("X-Test-Client") == "" {
						return identity.Anonymous
					}
					name := r.Header.Get("X-Test-Client")
					return identity.Client{ID: "id-" + name, Name: name}
				},
			})
			server := httptest.NewServer(g.Handler())
			t.Cleanup(server.Close)
			httpClient := &http.Client{Transport: headerTransport{"X-Test-Client", "phone"}}
			client := mcp.NewClient(&mcp.Implementation{Name: "identity-test", Version: "1"}, nil)
			session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL, HTTPClient: httpClient},
				&mcp.ClientSessionOptions{ProtocolVersion: version})
			if err != nil {
				t.Fatalf("connect: %v", err)
			}
			t.Cleanup(func() { session.Close() })

			if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "work-pc__fs__read", Arguments: map[string]any{}}); err != nil {
				t.Fatalf("call: %v", err)
			}
			if got := <-seen; got.ID != "id-phone" || got.Name != "phone" {
				t.Errorf("caller saw identity %+v", got)
			}
			if recent := calls.Recent(); len(recent) != 1 || recent[0].Client != "phone" {
				t.Errorf("activity = %+v", recent)
			}
		})
	}
}

type headerTransport struct{ key, value string }

func (h headerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set(h.key, h.value)
	return http.DefaultTransport.RoundTrip(r)
}

// byHeader identifies clients by a test header; ownTools shows a client the
// tools of the MCP named after it, plus those of the "shared" MCP.
func byHeader(r *http.Request) identity.Client {
	name := r.Header.Get("X-Test-Client")
	if name == "" {
		return identity.Anonymous
	}
	return identity.Client{ID: "id-" + name, Name: name}
}

func ownTools(c identity.Client, tool *catalog.Tool) bool {
	return tool.MCP == c.Name || tool.MCP == "shared"
}

func connectAs(t *testing.T, g *Gateway, version, name string, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	httpClient := &http.Client{Transport: headerTransport{"X-Test-Client", name}}
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway-test-" + name, Version: "1"}, opts)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: server.URL + "/mcp", HTTPClient: httpClient},
		&mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatalf("connect %s: %v", name, err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func listNames(t *testing.T, s *mcp.ClientSession) []string {
	t.Helper()
	listed, err := s.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := []string{}
	for _, tl := range listed.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func okCaller(text string) callerFunc {
	return func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) { return textResult(text), nil }
}

func TestPerClientVisibility(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			c.SetMCP("n", "alice", []*mcp.Tool{tool("t", "")}, okCaller("a"))
			c.SetMCP("n", "bob", []*mcp.Tool{tool("t", "")}, okCaller("b"))
			c.SetMCP("n", "shared", []*mcp.Tool{tool("t", "")}, okCaller("s"))
			calls := activity.New(10)
			g := New(Options{Catalog: c, Activity: calls, Identify: byHeader, Visible: ownTools})
			alice := connectAs(t, g, version, "alice", nil)
			bob := connectAs(t, g, version, "bob", nil)

			if got, want := listNames(t, alice), []string{"n__alice__t", "n__shared__t"}; !reflect.DeepEqual(got, want) {
				t.Errorf("alice sees %v, want %v", got, want)
			}
			if got, want := listNames(t, bob), []string{"n__bob__t", "n__shared__t"}; !reflect.DeepEqual(got, want) {
				t.Errorf("bob sees %v, want %v", got, want)
			}

			// A hidden tool must fail exactly like one that does not exist.
			_, hiddenErr := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__bob__t"})
			_, missingErr := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__nobody__t"})
			var hidden, missing *jsonrpc.Error
			if !errors.As(hiddenErr, &hidden) || !errors.As(missingErr, &missing) {
				t.Fatalf("errors = %v, %v; want protocol errors", hiddenErr, missingErr)
			}
			if hidden.Code != missing.Code || strings.ReplaceAll(hidden.Message, "n__bob__t", "X") != strings.ReplaceAll(missing.Message, "n__nobody__t", "X") {
				t.Errorf("hidden = %+v, unknown = %+v", hidden, missing)
			}
			got, err := bob.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__bob__t"})
			if err != nil {
				t.Fatalf("bob call: %v", err)
			}
			assertResult(t, got, textResult("b"))

			var recorded []string
			for _, call := range calls.Recent() {
				recorded = append(recorded, call.Client+":"+call.Tool+":"+call.Outcome+":"+call.Node)
			}
			want := []string{"bob:n__bob__t:ok:n", "alice:n__nobody__t:error:", "alice:n__bob__t:error:"} // newest first
			if !reflect.DeepEqual(recorded, want) {
				t.Errorf("activity = %v, want %v", recorded, want)
			}
		})
	}
}

func TestHiddenToolStaleInMirror(t *testing.T) {
	// Visibility can change without the mirror having synced yet; the call
	// must still be refused.
	c := catalog.New()
	c.SetMCP("n", "alice", []*mcp.Tool{tool("t", "")}, okCaller("a"))
	var allowed atomic.Bool
	allowed.Store(true)
	g := New(Options{Catalog: c, Identify: byHeader, Visible: func(identity.Client, *catalog.Tool) bool {
		return allowed.Load()
	}})
	alice := connectAs(t, g, "2025-11-25", "alice", nil)
	if _, err := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__alice__t"}); err != nil {
		t.Fatalf("call: %v", err)
	}
	allowed.Store(false)
	_, err := alice.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__alice__t"})
	var wireErr *jsonrpc.Error
	if !errors.As(err, &wireErr) || wireErr.Code != jsonrpc.CodeInvalidParams || !strings.Contains(wireErr.Message, "unknown tool") {
		t.Fatalf("call after hide = %v", err)
	}
}

func TestChangeVisibleToOneClient(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			g := New(Options{Catalog: c, Identify: byHeader, Visible: ownTools})
			changedA := make(chan struct{}, 10)
			changedB := make(chan struct{}, 10)
			alice := connectAs(t, g, version, "alice", &mcp.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changedA <- struct{}{} },
			})
			bob := connectAs(t, g, version, "bob", &mcp.ClientOptions{
				ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changedB <- struct{}{} },
			})
			listNames(t, alice)
			listNames(t, bob)
			runGateway(t, g)
			await := func(ch chan struct{}, who string) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatalf("%s got no list_changed", who)
				}
			}
			c.SetMCP("n", "bob", []*mcp.Tool{tool("t", "")}, okCaller("b"))
			await(changedB, "bob")
			if got := listNames(t, bob); !reflect.DeepEqual(got, []string{"n__bob__t"}) {
				t.Errorf("bob sees %v", got)
			}
			// A second change that alice can see: if the first one had notified
			// her, she would now have two notifications queued.
			c.SetMCP("n", "alice", []*mcp.Tool{tool("t", "")}, okCaller("a"))
			await(changedA, "alice")
			if got := listNames(t, alice); !reflect.DeepEqual(got, []string{"n__alice__t"}) {
				t.Errorf("alice sees %v", got)
			}
			if n := len(changedA); n != 0 {
				t.Errorf("alice has %d extra list_changed notifications", n)
			}
		})
	}
}

func TestDefaultOptionsOneMirror(t *testing.T) {
	c := catalog.New()
	c.SetMCP("n", "fs", []*mcp.Tool{tool("t", "")}, okCaller("ok"))
	g := New(Options{Catalog: c})
	for _, version := range protocolVersions {
		s := connectClient(t, g, version, nil)
		if got := listNames(t, s); !reflect.DeepEqual(got, []string{"n__fs__t"}) {
			t.Errorf("%s: sees %v", version, got)
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.mirrors) != 1 || g.mirrors[identity.Anonymous.ID] == nil {
		t.Errorf("mirrors = %d, want only anonymous", len(g.mirrors))
	}
}

func TestRefreshResyncsVisibility(t *testing.T) {
	// Grants and approvals change what Visible answers without touching the
	// catalog; Refresh must bring client tool lists in line.
	c := catalog.New()
	c.SetMCP("n", "fs", []*mcp.Tool{tool("t", "")}, okCaller("ok"))
	var allowed atomic.Bool
	g := New(Options{Catalog: c, Identify: byHeader, Visible: func(identity.Client, *catalog.Tool) bool {
		return allowed.Load()
	}})
	changed := make(chan struct{}, 10)
	alice := connectAs(t, g, "2025-11-25", "alice", &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) { changed <- struct{}{} },
	})
	if got := listNames(t, alice); len(got) != 0 {
		t.Fatalf("hidden tool listed: %v", got)
	}
	runGateway(t, g)
	allowed.Store(true)
	g.Refresh()
	g.Refresh() // coalesced, never blocks
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("no list_changed after Refresh")
	}
	if got := listNames(t, alice); !reflect.DeepEqual(got, []string{"n__fs__t"}) {
		t.Errorf("after refresh sees %v", got)
	}
}

// A client request may be as large as one MCP message between hub and node,
// not just the SDK's default 4 MiB.
func TestLargeRequestBody(t *testing.T) {
	c := catalog.New()
	c.SetMCP("n", "m", []*mcp.Tool{tool("t", "")}, callerFunc(func(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		raw, _ := p.Arguments.(json.RawMessage)
		var args struct{ Blob string }
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		return textResult(fmt.Sprint(len(args.Blob))), nil
	}))
	g := New(Options{Catalog: c})
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			session := connectClient(t, g, version, nil)
			blob := strings.Repeat("x", 6<<20)
			res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__m__t", Arguments: map[string]any{"blob": blob}})
			if err != nil {
				t.Fatalf("call: %v", err)
			}
			assertResult(t, res, textResult(fmt.Sprint(len(blob))))
		})
	}
}

// NewClient runs before a client's server is built, once per client, so
// what it loads (the hub reloads group memberships) is already visible in
// the client's first tool list.
func TestNewClientRunsBeforeFirstList(t *testing.T) {
	c := catalog.New()
	c.SetMCP("n", "alice", []*mcp.Tool{tool("t", "")}, okCaller("ok"))
	var mu sync.Mutex
	granted := map[string]bool{}
	calls := map[string]int{}
	g := New(Options{
		Catalog:  c,
		Identify: byHeader,
		NewClient: func(_ context.Context, cl identity.Client) {
			mu.Lock()
			defer mu.Unlock()
			calls[cl.ID]++
			granted[cl.ID] = true
		},
		Visible: func(cl identity.Client, tl *catalog.Tool) bool {
			mu.Lock()
			defer mu.Unlock()
			return granted[cl.ID] && ownTools(cl, tl)
		},
	})
	for _, version := range []string{"2025-11-25", "2026-07-28"} {
		s := connectAs(t, g, version, "alice", nil)
		for range 2 {
			res, err := s.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Tools) != 1 {
				t.Fatalf("%s: tools = %d, want 1 on the first list", version, len(res.Tools))
			}
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["id-alice"] != 1 {
		t.Fatalf("NewClient called %d times for one client, want 1", calls["id-alice"])
	}
}
