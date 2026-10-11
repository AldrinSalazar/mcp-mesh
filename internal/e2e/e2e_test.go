// Package e2e runs the whole path in one process: an SDK client calls the
// hub's gateway, which routes through the node registry and a WebSocket to a
// node agent, which runs a real stdio MCP process.
package e2e

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/gateway"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/progress"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

const helperEnv = "MCPMESH_E2E_FAKE_MCP"

// TestMain doubles as the fake MCP server: the node runs this test binary
// with helperEnv set.
func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) != "" {
		runFakeMCP()
		return
	}
	os.Exit(m.Run())
}

type echoArgs struct {
	Text string `json:"text"`
}

func runFakeMCP() {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "echo", Description: "Echoes text."},
		func(_ context.Context, _ *mcp.CallToolRequest, a echoArgs) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: a.Text}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "big", Description: "Returns 3 MB of text."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 3<<20)}}}, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "crash", Description: "Exits the process."},
		func(_ context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})
	mcp.AddTool(s, &mcp.Tool{Name: "work", Description: "Reports progress, then finishes."},
		func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			if token := req.Params.GetProgressToken(); token != nil {
				for i := 1; i <= 2; i++ {
					if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(i), Total: 2}); err != nil {
						return nil, nil, err
					}
				}
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "worked"}}}, nil, nil
		})
	s.AddPrompt(&mcp.Prompt{Name: "hello", Description: "Says hello."},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: "hello " + req.Params.Arguments["who"]}},
			}}, nil
		})
	s.AddResource(&mcp.Resource{URI: "memo://note", Name: "note"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "a note"}}}, nil
		})
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "memo://item/{id}", Name: "item"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "item"}}}, nil
		})
	if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "fake mcp:", err)
		os.Exit(1)
	}
}

type hub struct {
	cat      *catalog.Catalog
	calls    *activity.Log
	registry *nodes.Registry
	nodeURL  string
	mcpURL   string
}

func startHub(t *testing.T) *hub {
	t.Helper()
	ctx := t.Context()
	h := &hub{cat: catalog.New(), calls: activity.New(50)}
	router := progress.New()
	h.registry = nodes.NewRegistry(nodes.Options{Catalog: h.cat, OfflineGrace: 300 * time.Millisecond, Progress: router})
	gw := gateway.New(gateway.Options{Catalog: h.cat, Activity: h.calls, CallTimeout: 10 * time.Second, Progress: router})
	go gw.Run(ctx)

	nodeSrv := httptest.NewServer(h.registry)
	t.Cleanup(nodeSrv.Close)
	mcpSrv := httptest.NewServer(gw.Handler())
	t.Cleanup(mcpSrv.Close)
	h.nodeURL = "ws" + strings.TrimPrefix(nodeSrv.URL, "http") + proto.NodePath
	h.mcpURL = mcpSrv.URL
	return h
}

// startNode runs an agent with the fake MCP and returns a function that
// stops it and waits for it to exit.
func startNode(t *testing.T, h *hub, node string) (stop func()) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	up := upstream.NewStdio(upstream.StdioConfig{
		Command: exe,
		Env:     []string{helperEnv + "=1"},
		Stderr:  io.Discard,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Config{
			HubURL: h.nodeURL,
			Node:   node,
			MCPs:   []agent.MCP{{Name: "fake", Upstream: up, Sharing: proto.SharingShared, CallTimeout: 7 * time.Second}},
		})
	}()
	var once bool
	stop = func() {
		if once {
			return
		}
		once = true
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("agent did not stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func connectClient(t *testing.T, h *hub) (*mcp.ClientSession, <-chan struct{}) {
	t.Helper()
	changed := make(chan struct{}, 16)
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: h.mcpURL, HTTPClient: http.DefaultClient}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, changed
}

// waitTools polls tools/list until want(names) holds.
func waitTools(t *testing.T, cs *mcp.ClientSession, desc string, want func(map[string]*mcp.Tool) bool) map[string]*mcp.Tool {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := cs.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		got := map[string]*mcp.Tool{}
		for _, tool := range res.Tools {
			got[tool.Name] = tool
		}
		if want(got) {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; have %d tools", desc, len(got))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("empty result")
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return tc.Text
}

func TestEndToEnd(t *testing.T) {
	h := startHub(t)
	cs, changed := connectClient(t, h)

	stopA := startNode(t, h, "alpha")
	startNode(t, h, "beta")

	tools := waitTools(t, cs, "both nodes", func(m map[string]*mcp.Tool) bool {
		return m["alpha__fake__echo"] != nil && m["beta__fake__echo"] != nil
	})
	if d := tools["alpha__fake__echo"].Description; d != "[alpha/fake] Echoes text." {
		t.Errorf("description = %q", d)
	}
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Error("no tools/list_changed notification after nodes connected")
	}

	t.Run("call routes to the right node", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "beta__fake__echo", Arguments: map[string]any{"text": "hi beta"}})
		if err != nil {
			t.Fatal(err)
		}
		if got := text(t, res); got != "hi beta" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("large result", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha__fake__big", Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if got := len(text(t, res)); got != 3<<20 {
			t.Errorf("got %d bytes", got)
		}
	})

	t.Run("crashed MCP comes back", func(t *testing.T) {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha__fake__crash", Arguments: map[string]any{}})
		if err == nil && !res.IsError {
			t.Fatal("crash call should fail")
		}
		deadline := time.Now().Add(20 * time.Second)
		for {
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha__fake__echo", Arguments: map[string]any{"text": "back"}})
			if err == nil && !res.IsError && text(t, res) == "back" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("alpha/fake did not come back: err=%v", err)
			}
			time.Sleep(200 * time.Millisecond)
		}
	})

	t.Run("node leaves", func(t *testing.T) {
		stopA()
		waitTools(t, cs, "alpha gone", func(m map[string]*mcp.Tool) bool {
			return m["alpha__fake__echo"] == nil && m["beta__fake__echo"] != nil
		})
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha__fake__echo", Arguments: map[string]any{"text": "x"}})
		if err == nil && !res.IsError {
			t.Error("call to a gone node should fail")
		}
	})

	if len(h.calls.Recent()) == 0 {
		t.Error("activity log is empty")
	}
	if n := h.registry.Nodes(); len(n) != 1 || n[0].Name != "beta" {
		t.Errorf("registry nodes = %+v", n)
	}
}

// Prompts, resources, progress and the per-MCP timeout cross the whole path.
func TestEndToEndPromptsResourcesProgress(t *testing.T) {
	h := startHub(t)
	startNode(t, h, "alpha")
	progressed := make(chan *mcp.ProgressNotificationParams, 8)
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			progressed <- req.Params
		},
	})
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: h.mcpURL, HTTPClient: http.DefaultClient}, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })

	// The gateway mirrors the catalog asynchronously; wait for the prompt.
	deadline := time.Now().Add(15 * time.Second)
	for {
		listed, err := cs.ListPrompts(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Prompts) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("prompt never listed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, err := cs.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "alpha__fake__hello", Arguments: map[string]string{"who": "ann"}})
	if err != nil {
		t.Fatal(err)
	}
	if msg := got.Messages[0].Content.(*mcp.TextContent).Text; msg != "hello ann" {
		t.Errorf("prompt = %q", msg)
	}

	note := catalog.ResourceURI("alpha", "fake", "memo://note")
	resources, err := cs.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 1 || resources.Resources[0].URI != note {
		t.Fatalf("resources = %+v, %v", resources, err)
	}
	templates, err := cs.ListResourceTemplates(t.Context(), nil)
	if err != nil || len(templates.ResourceTemplates) != 1 {
		t.Fatalf("templates = %+v, %v", templates, err)
	}
	for uri, want := range map[string]string{note: "a note", catalog.ResourceURI("alpha", "fake", "memo://item/7"): "item"} {
		read, err := cs.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("read %s: %v", uri, err)
		}
		if read.Contents[0].URI != uri || read.Contents[0].Text != want {
			t.Errorf("read %s = %+v", uri, read.Contents[0])
		}
	}

	if tool, ok := h.cat.Lookup("alpha__fake__work"); !ok || tool.Timeout != 7*time.Second {
		t.Errorf("tool timeout = %+v, want 7s from the node's offer", tool)
	}
	params := &mcp.CallToolParams{Name: "alpha__fake__work", Arguments: map[string]any{}}
	params.SetProgressToken("tok")
	res, err := cs.CallTool(t.Context(), params)
	if err != nil || text(t, res) != "worked" {
		t.Fatalf("work: %+v, %v", res, err)
	}
	for want := 1.0; want <= 2; want++ {
		select {
		case p := <-progressed:
			if p.ProgressToken != "tok" || p.Progress != want || p.Total != 2 {
				t.Errorf("progress = %+v, want %v of 2 under the client's token", p, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("progress %v never arrived", want)
		}
	}
}

// Cancelling a call reaches a stateless (2026-07-28) HTTP MCP through the
// node: the upstream request ends, and another call on the same MCP goes on.
func TestEndToEndCancelStatelessHTTP(t *testing.T) {
	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run("client "+version, func(t *testing.T) { testCancelStatelessHTTP(t, version) })
	}
}

func testCancelStatelessHTTP(t *testing.T, clientVersion string) {
	entered := make(chan string, 2)
	ended := make(chan struct{})
	release := make(chan struct{})
	s := mcp.NewServer(&mcp.Implementation{Name: "http-fake", Version: "1"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "block", Description: "Runs until cancelled."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			entered <- "block"
			<-ctx.Done()
			close(ended)
			return nil, nil, ctx.Err()
		})
	mcp.AddTool(s, &mcp.Tool{Name: "wait", Description: "Runs until released."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			entered <- "wait"
			select {
			case <-release:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "released"}}}, nil, nil
		})
	upstreamSrv := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}))
	t.Cleanup(upstreamSrv.Close)

	h := startHub(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- agent.Run(ctx, agent.Config{HubURL: h.nodeURL, Node: "alpha", MCPs: []agent.MCP{
			{Name: "web", Upstream: upstream.NewHTTP(upstream.HTTPConfig{URL: upstreamSrv.URL}), Sharing: proto.SharingShared},
		}})
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	c := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "1"}, nil)
	cs, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: h.mcpURL, HTTPClient: http.DefaultClient}, &mcp.ClientSessionOptions{ProtocolVersion: clientVersion})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	waitTools(t, cs, "the HTTP MCP's tools", func(m map[string]*mcp.Tool) bool {
		return m["alpha__web__block"] != nil && m["alpha__web__wait"] != nil
	})

	waited := make(chan error, 1)
	go func() {
		res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha__web__wait", Arguments: map[string]any{}})
		if err == nil && (res.IsError || text(t, res) != "released") {
			err = fmt.Errorf("wait result: %+v", res)
		}
		waited <- err
	}()
	if got := <-entered; got != "wait" {
		t.Fatalf("entered %s first", got)
	}
	callCtx, cancelCall := context.WithCancel(t.Context())
	blocked := make(chan error, 1)
	go func() {
		_, err := cs.CallTool(callCtx, &mcp.CallToolParams{Name: "alpha__web__block", Arguments: map[string]any{}})
		blocked <- err
	}()
	if got := <-entered; got != "block" {
		t.Fatalf("entered %s, want block", got)
	}
	cancelCall()
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the upstream kept running the cancelled call")
	}
	<-blocked
	close(release)
	select {
	case err := <-waited:
		if err != nil {
			t.Fatalf("the other call: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the other call never finished")
	}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha__web__wait", Arguments: map[string]any{}})
	if err != nil || text(t, res) != "released" {
		t.Fatalf("a call after the cancellation: %+v, %v", res, err)
	}
}
