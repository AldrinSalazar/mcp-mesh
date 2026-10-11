package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/progress"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// contentCaller is an upstream MCP that serves tools, prompts and resources.
type contentCaller struct {
	call   func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	prompt func(context.Context, *mcp.GetPromptParams) (*mcp.GetPromptResult, error)
	read   func(context.Context, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error)
}

func (c contentCaller) CallTool(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return c.call(ctx, p)
}

func (c contentCaller) GetPrompt(ctx context.Context, p *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	return c.prompt(ctx, p)
}

func (c contentCaller) ReadResource(ctx context.Context, p *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	return c.read(ctx, p)
}

func newContentCaller() contentCaller {
	return contentCaller{
		call: func(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) { return textResult("ok"), nil },
		prompt: func(_ context.Context, p *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{
				{Role: "user", Content: &mcp.TextContent{Text: p.Name + ":" + p.Arguments["who"]}},
			}}, nil
		},
		read: func(_ context.Context, p *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: p.URI, Text: "body of " + p.URI, MIMEType: "text/plain"}}}, nil
		},
	}
}

func setContent(c *catalog.Catalog, node, name string, caller catalog.Caller, timeout time.Duration) {
	c.Set(node, name, catalog.Content{
		Tools:             []*mcp.Tool{tool("slow", "A tool.")},
		Prompts:           []*mcp.Prompt{{Name: "greet", Description: "Greets."}},
		Resources:         []*mcp.Resource{{URI: "file:///notes.txt", Name: "notes"}},
		ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///docs/{name}", Name: "docs"}},
		Timeout:           timeout,
	}, caller)
}

func TestPromptsAndResources(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			setContent(c, "work-pc", "fs", newContentCaller(), 0)
			g := New(Options{Catalog: c})
			session := connectClient(t, g, version, nil)

			prompts, err := session.ListPrompts(t.Context(), nil)
			if err != nil || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "work-pc__fs__greet" {
				t.Fatalf("prompts: %+v, %v", prompts, err)
			}
			got, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "work-pc__fs__greet", Arguments: map[string]string{"who": "ann"}})
			if err != nil {
				t.Fatal(err)
			}
			// The upstream sees the original name.
			if text := got.Messages[0].Content.(*mcp.TextContent).Text; text != "greet:ann" {
				t.Errorf("prompt text %q", text)
			}

			exposed := catalog.ResourceURI("work-pc", "fs", "file:///notes.txt")
			resources, err := session.ListResources(t.Context(), nil)
			if err != nil || len(resources.Resources) != 1 || resources.Resources[0].URI != exposed {
				t.Fatalf("resources: %+v, %v", resources, err)
			}
			templates, err := session.ListResourceTemplates(t.Context(), nil)
			wantTemplate := catalog.ResourceURI("work-pc", "fs", "file:///docs/{name}")
			if err != nil || len(templates.ResourceTemplates) != 1 || templates.ResourceTemplates[0].URITemplate != wantTemplate {
				t.Fatalf("templates: %+v, %v", templates, err)
			}

			for _, upstream := range []string{"file:///notes.txt", "file:///docs/readme"} {
				uri := catalog.ResourceURI("work-pc", "fs", upstream)
				read, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri})
				if err != nil {
					t.Fatalf("read %s: %v", uri, err)
				}
				if len(read.Contents) != 1 || read.Contents[0].URI != uri || read.Contents[0].Text != "body of "+upstream {
					t.Errorf("read %s = %+v", uri, read.Contents)
				}
			}
			if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: catalog.ResourceURI("work-pc", "fs", "file:///unknown")}); err == nil {
				t.Error("read of a URI that matches nothing succeeded")
			}
		})
	}
}

func TestContentListChanged(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			g := New(Options{Catalog: c})
			prompts, resources := make(chan struct{}, 10), make(chan struct{}, 10)
			session := connectClient(t, g, version, &mcp.ClientOptions{
				PromptListChangedHandler:   func(context.Context, *mcp.PromptListChangedRequest) { prompts <- struct{}{} },
				ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) { resources <- struct{}{} },
			})
			runGateway(t, g)
			setContent(c, "work-pc", "fs", newContentCaller(), 0)
			for name, ch := range map[string]chan struct{}{"prompts": prompts, "resources": resources} {
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatalf("no %s list_changed", name)
				}
			}
			listed, err := session.ListPrompts(t.Context(), nil)
			if err != nil || len(listed.Prompts) != 1 {
				t.Fatalf("prompts after change: %+v, %v", listed, err)
			}
			c.RemoveMCP("work-pc", "fs")
			select {
			case <-prompts:
			case <-time.After(5 * time.Second):
				t.Fatal("no prompts list_changed after removal")
			}
			listed, err = session.ListPrompts(t.Context(), nil)
			if err != nil || len(listed.Prompts) != 0 {
				t.Fatalf("prompts after removal: %+v, %v", listed, err)
			}
		})
	}
}

func TestVisibleMCPHidesContent(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			setContent(c, "work-pc", "ann", newContentCaller(), 0)
			setContent(c, "work-pc", "bob", newContentCaller(), 0)
			g := New(Options{
				Catalog:  c,
				Identify: byHeader,
				VisibleMCP: func(client identity.Client, _, mcpName string) bool {
					return mcpName == client.Name
				},
			})
			ann := connectAs(t, g, version, "ann", nil)
			prompts, err := ann.ListPrompts(t.Context(), nil)
			if err != nil || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "work-pc__ann__greet" {
				t.Fatalf("ann prompts: %+v, %v", prompts, err)
			}
			resources, err := ann.ListResources(t.Context(), nil)
			if err != nil || len(resources.Resources) != 1 {
				t.Fatalf("ann resources: %+v, %v", resources, err)
			}
			templates, err := ann.ListResourceTemplates(t.Context(), nil)
			if err != nil || len(templates.ResourceTemplates) != 1 {
				t.Fatalf("ann templates: %+v, %v", templates, err)
			}
			if _, err := ann.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "work-pc__bob__greet"}); err == nil {
				t.Error("ann got bob's prompt")
			}
			hidden := catalog.ResourceURI("work-pc", "bob", "file:///notes.txt")
			_, hiddenErr := ann.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: hidden})
			_, missingErr := ann.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: catalog.ResourceURI("work-pc", "nobody", "file:///notes.txt")})
			if hiddenErr == nil || missingErr == nil {
				t.Fatalf("hidden read err = %v, missing read err = %v", hiddenErr, missingErr)
			}
			// A hidden resource is refused the same way as one that doesn't exist.
			if hiddenErr.Error() != missingErr.Error() {
				t.Errorf("hidden: %q, missing: %q; want the same text", hiddenErr, missingErr)
			}
			if _, err := ann.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: catalog.ResourceURI("work-pc", "bob", "file:///docs/x")}); err == nil {
				t.Error("ann read bob's templated resource")
			}
			// Another client sees only its own.
			bob := connectAs(t, g, version, "bob", nil)
			prompts, err = bob.ListPrompts(t.Context(), nil)
			if err != nil || len(prompts.Prompts) != 1 || prompts.Prompts[0].Name != "work-pc__bob__greet" {
				t.Fatalf("bob prompts: %+v, %v", prompts, err)
			}
		})
	}
}

func TestProgressForwarded(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			router := progress.New()
			c := catalog.New()
			var hubToken string
			caller := newContentCaller()
			caller.call = func(ctx context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				token, _ := p.GetProgressToken().(string)
				hubToken = token
				// What a node's MCP client does with a notification from the
				// upstream: hand it to the router.
				router.Dispatch(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 1, Total: 4, Message: "quarter"})
				router.Dispatch(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 2, Total: 4})
				return textResult("done"), nil
			}
			setContent(c, "work-pc", "fs", caller, 0)
			g := New(Options{Catalog: c, Progress: router})
			var mu sync.Mutex
			var got []*mcp.ProgressNotificationParams
			session := connectClient(t, g, version, &mcp.ClientOptions{
				ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
					mu.Lock()
					got = append(got, req.Params)
					mu.Unlock()
				},
			})
			params := &mcp.CallToolParams{Name: "work-pc__fs__slow"}
			params.SetProgressToken("my-token")
			if _, err := session.CallTool(t.Context(), params); err != nil {
				t.Fatal(err)
			}
			if hubToken == "" || hubToken == "my-token" {
				t.Errorf("upstream got token %q, want one of the hub's", hubToken)
			}
			// Notifications precede the result on the stream, so they have
			// arrived by now; poll anyway because delivery is asynchronous.
			deadline := time.Now().Add(5 * time.Second)
			for {
				mu.Lock()
				n := len(got)
				mu.Unlock()
				if n == 2 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("client got %d progress notifications, want 2", n)
				}
				time.Sleep(time.Millisecond)
			}
			mu.Lock()
			defer mu.Unlock()
			if got[0].ProgressToken != "my-token" || got[0].Progress != 1 || got[0].Total != 4 || got[0].Message != "quarter" || got[1].Progress != 2 {
				t.Errorf("progress = %+v %+v", got[0], got[1])
			}
			if router.Len() != 0 {
				t.Errorf("router still holds %d calls", router.Len())
			}
		})
	}
}

func TestNoProgressTokenNoRegistration(t *testing.T) {
	router := progress.New()
	c := catalog.New()
	caller := newContentCaller()
	caller.call = func(_ context.Context, p *mcp.CallToolParams) (*mcp.CallToolResult, error) {
		if token := p.GetProgressToken(); token != nil {
			t.Errorf("upstream got progress token %v", token)
		}
		return textResult("done"), nil
	}
	setContent(c, "work-pc", "fs", caller, 0)
	g := New(Options{Catalog: c, Progress: router})
	session := connectClient(t, g, protocolVersions[1], nil)
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "work-pc__fs__slow"}); err != nil {
		t.Fatal(err)
	}
}

func TestPerMCPTimeout(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			caller := newContentCaller()
			block := func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}
			caller.call = func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
				return nil, block(ctx)
			}
			caller.prompt = func(ctx context.Context, _ *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
				return nil, block(ctx)
			}
			caller.read = func(ctx context.Context, _ *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
				return nil, block(ctx)
			}
			// The MCP's 50ms replaces the hub's default of two minutes.
			setContent(c, "work-pc", "fs", caller, 50*time.Millisecond)
			g := New(Options{Catalog: c})
			session := connectClient(t, g, version, nil)

			result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "work-pc__fs__slow"})
			if err != nil || !result.IsError {
				t.Errorf("tool: %+v, %v; want an error result", result, err)
			}
			if _, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "work-pc__fs__greet"}); err == nil {
				t.Error("prompt did not time out")
			}
			if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: catalog.ResourceURI("work-pc", "fs", "file:///notes.txt")}); err == nil {
				t.Error("read did not time out")
			}
		})
	}
}

// Prompt fetches and resource reads reach an MCP like tool calls, and a read
// can return a file's contents, so they are recorded too, with their kind.
func TestPromptsAndReadsAreRecorded(t *testing.T) {
	c := catalog.New()
	setContent(c, "work-pc", "fs", newContentCaller(), 0)
	calls := activity.New(10)
	g := New(Options{Catalog: c, Activity: calls})
	session := connectClient(t, g, "2025-11-25", nil)
	if _, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "work-pc__fs__greet"}); err != nil {
		t.Fatal(err)
	}
	uri := catalog.ResourceURI("work-pc", "fs", "file:///notes.txt")
	if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: catalog.ResourceURI("work-pc", "fs", "file:///unknown")})
	got := calls.Recent() // newest first
	if len(got) != 3 {
		t.Fatalf("recorded %d, want 3: %+v", len(got), got)
	}
	want := []struct{ kind, name, outcome, node string }{
		{activity.KindResource, catalog.ResourceURI("work-pc", "fs", "file:///unknown"), "error", "work-pc"},
		{activity.KindResource, uri, "ok", "work-pc"},
		{activity.KindPrompt, "work-pc__fs__greet", "ok", "work-pc"},
	}
	for i, w := range want {
		if c := got[i]; c.Kind != w.kind || c.Tool != w.name || c.Outcome != w.outcome || c.Node != w.node {
			t.Errorf("call %d = %+v, want %+v", i, c, w)
		}
	}
}

func TestTruncateKeepsRunesWhole(t *testing.T) {
	if got := truncate("aé", 2); got != "a…" {
		t.Errorf("truncate = %q", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate = %q", got)
	}
}

// A null entry in an upstream's resource contents is refused, and the hub
// keeps serving: dereferencing it would panic and end the process.
func TestNullResourceContent(t *testing.T) {
	for _, version := range protocolVersions {
		t.Run(version, func(t *testing.T) {
			c := catalog.New()
			caller := newContentCaller()
			caller.read = func(context.Context, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
				// What the SDK makes of the wire result.
				var result mcp.ReadResourceResult
				if err := json.Unmarshal([]byte(`{"contents":[null]}`), &result); err != nil {
					return nil, err
				}
				return &result, nil
			}
			setContent(c, "work-pc", "fs", caller, 0)
			g := New(Options{Catalog: c})
			session := connectClient(t, g, version, nil)
			uri := catalog.ResourceURI("work-pc", "fs", "file:///notes.txt")
			if _, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: uri}); err == nil || !strings.Contains(err.Error(), "null") {
				t.Fatalf("read = %v, want an invalid-resource error", err)
			}
			if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "work-pc__fs__slow"}); err != nil {
				t.Fatalf("call after the bad read: %v", err)
			}
		})
	}
}

// A panic in a handler answers an internal error instead of ending the hub.
func TestHandlerPanicContained(t *testing.T) {
	c := catalog.New()
	caller := newContentCaller()
	caller.prompt = func(context.Context, *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
		panic("upstream did something odd")
	}
	setContent(c, "work-pc", "fs", caller, 0)
	g := New(Options{Catalog: c, Logger: slog.New(slog.DiscardHandler)})
	session := connectClient(t, g, protocolVersions[0], nil)
	if _, err := session.GetPrompt(t.Context(), &mcp.GetPromptParams{Name: "work-pc__fs__greet"}); err == nil || !strings.Contains(err.Error(), "internal error") {
		t.Fatalf("prompt = %v, want an internal error", err)
	}
	if _, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "work-pc__fs__slow"}); err != nil {
		t.Fatalf("call after the panic: %v", err)
	}
}

// Results depend on the client's key, so they are never marked cacheable for
// anyone else, whatever the upstream said; a read keeps its upstream's TTL.
func TestResultsArePrivatelyCacheable(t *testing.T) {
	c := catalog.New()
	caller := newContentCaller()
	caller.read = func(_ context.Context, p *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{
			Cacheable: mcp.Cacheable{TTLMs: 60000, CacheScope: "public"},
			Contents:  []*mcp.ResourceContents{{URI: p.URI, Text: "for whoever asked"}},
		}, nil
	}
	setContent(c, "work-pc", "fs", caller, 0)
	g := New(Options{Catalog: c, Identify: byHeader})
	for _, client := range []string{"alice", "bob"} {
		session := connectAs(t, g, "2026-07-28", client, nil)
		tools, err := session.ListTools(t.Context(), nil)
		if err != nil || tools.CacheScope != "private" || tools.TTLMs != 0 {
			t.Fatalf("%s tools/list: %+v, %v", client, tools.Cacheable, err)
		}
		prompts, err := session.ListPrompts(t.Context(), nil)
		if err != nil || prompts.CacheScope != "private" {
			t.Fatalf("%s prompts/list: %+v, %v", client, prompts.Cacheable, err)
		}
		resources, err := session.ListResources(t.Context(), nil)
		if err != nil || resources.CacheScope != "private" {
			t.Fatalf("%s resources/list: %+v, %v", client, resources.Cacheable, err)
		}
		read, err := session.ReadResource(t.Context(), &mcp.ReadResourceParams{URI: catalog.ResourceURI("work-pc", "fs", "file:///notes.txt")})
		if err != nil || read.CacheScope != "private" || read.TTLMs != 60000 {
			t.Fatalf("%s read: %+v, %v", client, read.Cacheable, err)
		}
	}
}
