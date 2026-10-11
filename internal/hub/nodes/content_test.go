package nodes

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/progress"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func addPrompt(server *mcp.Server, name string) {
	server.AddPrompt(&mcp.Prompt{Name: name, Description: "prompt " + name},
		func(_ context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: name + ":" + req.Params.Arguments["who"]}},
			}}, nil
		})
}

// contentFake offers two prompts (more than the fake's page size, to page),
// a resource, a template and a tool that reports progress when asked.
func contentFake(server *mcp.Server) {
	addPrompt(server, "greet")
	addPrompt(server, "farewell")
	server.AddResource(&mcp.Resource{URI: "file:///notes.txt", Name: "notes", MIMEType: "text/plain"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "hello notes"}}}, nil
		})
	server.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "file:///docs/{name}", Name: "docs"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "doc"}}}, nil
		})
	server.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}},
		func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if token := req.Params.GetProgressToken(); token != nil {
				if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 1, Total: 2, Message: "half"}); err != nil {
					return nil, err
				}
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "slow done"}}}, nil
		})
}

func TestRegistryPromptsAndResources(t *testing.T) {
	router := progress.New()
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 500 * time.Millisecond, Progress: router})
	h := hello("machine")
	h.MCPs[0].CallTimeoutMs = 1500
	f := startFake(t, url, h, fakeOptions{configure: contentFake})
	await(t, func() bool {
		nodes := r.Nodes()
		return len(nodes) == 1 && nodes[0].MCPs[0].State == MCPReady
	})
	info := r.Nodes()[0].MCPs[0]
	if info.Prompts != 2 || info.Resources != 2 || info.Tools != 3 {
		t.Fatalf("MCP info: %+v", info)
	}
	if got := len(cat.Prompts()); got != 2 {
		t.Fatalf("prompts: %d", got)
	}
	if got := cat.Resources(); len(got) != 1 || got[0].URI != catalog.ResourceURI("machine", "tools", "file:///notes.txt") {
		t.Fatalf("resources: %+v", got)
	}
	if got := cat.ResourceTemplates(); len(got) != 1 {
		t.Fatalf("templates: %+v", got)
	}
	tool, ok := cat.Lookup("machine__tools__echo")
	if !ok || tool.Timeout != 1500*time.Millisecond {
		t.Fatalf("tool timeout: %+v", tool)
	}

	// Prompts and reads go through the handle like calls.
	prompt, ok := cat.LookupPrompt("machine__tools__greet")
	if !ok || prompt.Timeout != 1500*time.Millisecond {
		t.Fatalf("prompt: %+v", prompt)
	}
	got, err := prompt.Get(t.Context(), &mcp.GetPromptParams{Arguments: map[string]string{"who": "ann"}})
	if err != nil {
		t.Fatal(err)
	}
	if text := got.Messages[0].Content.(*mcp.TextContent).Text; text != "greet:ann" {
		t.Errorf("prompt text %q", text)
	}
	for _, uri := range []string{"file:///notes.txt", "file:///docs/a"} {
		route, ok := cat.ResolveResource(catalog.ResourceURI("machine", "tools", uri))
		if !ok || route.Timeout != 1500*time.Millisecond {
			t.Fatalf("route for %s: %+v, %v", uri, route, ok)
		}
		res, err := route.Reader.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: route.URI})
		if err != nil || res.Contents[0].URI != uri {
			t.Fatalf("read %s: %+v, %v", uri, res, err)
		}
	}

	// Progress sent for a call reaches the router under the hub's token.
	notified := make(chan *mcp.ProgressNotificationParams, 1)
	hubToken, release := router.Register("client-token", func(_ context.Context, p *mcp.ProgressNotificationParams) error {
		notified <- p
		return nil
	})
	defer release()
	slow, _ := cat.Lookup("machine__tools__slow")
	params := &mcp.CallToolParams{}
	params.SetProgressToken(hubToken)
	if _, err := slow.CallWith(t.Context(), params); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-notified:
		if p.ProgressToken != "client-token" || p.Progress != 1 || p.Total != 2 || p.Message != "half" {
			t.Errorf("progress: %+v", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no progress reached the router")
	}

	// list_changed refreshes prompts, then resources.
	addPrompt(f.server, "third")
	await(t, func() bool { return len(cat.Prompts()) == 3 && r.Nodes()[0].MCPs[0].Prompts == 3 })
	f.server.AddResource(&mcp.Resource{URI: "file:///more.txt", Name: "more"},
		func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "more"}}}, nil
		})
	await(t, func() bool { return len(cat.Resources()) == 2 })
	f.server.RemovePrompts("greet", "farewell", "third")
	await(t, func() bool { return len(cat.Prompts()) == 0 })
}

func TestRegistryNoPromptsOrResources(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	startFake(t, url, hello("machine"), fakeOptions{})
	await(t, func() bool { return len(r.Nodes()) == 1 && r.Nodes()[0].MCPs[0].State == MCPReady })
	if len(cat.Prompts()) != 0 || len(cat.Resources()) != 0 || len(cat.ResourceTemplates()) != 0 {
		t.Error("catalog has prompts or resources the server doesn't offer")
	}
}

// A lazy MCP lists tools from the node's cache; prompts and resources show
// up once a call has opened it.
func TestRegistryLazyContentAfterOpen(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	h := hello("machine")
	h.MCPs[0].Start = proto.StartLazy
	h.MCPs[0].Tools = []byte(`[{"name":"echo","inputSchema":{"type":"object"}}]`)
	startFake(t, url, h, fakeOptions{configure: contentFake})
	await(t, func() bool { return len(r.Nodes()) == 1 && r.Nodes()[0].MCPs[0].State == MCPIdle })
	if len(cat.List()) != 1 || len(cat.Prompts()) != 0 {
		t.Fatalf("before open: %d tools, %d prompts", len(cat.List()), len(cat.Prompts()))
	}
	tool, _ := cat.Lookup("machine__tools__echo")
	if _, err := tool.Call(t.Context(), map[string]any{}); err != nil {
		t.Fatal(err)
	}
	await(t, func() bool { return len(cat.Prompts()) == 2 && len(cat.Resources()) == 1 })
}
