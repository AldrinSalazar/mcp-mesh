// fakeapp stands in for an MCP. Given an address it plays a desktop app that
// serves its own MCP: HTTP+SSE at /sse and Streamable HTTP at /mcp. Given
// "stdio" it is an MCP a node launches. Dev testing only.
//
//	go run ./tools/fakeapp 127.0.0.1:9123
//	go run ./tools/fakeapp stdio
//	go run ./tools/fakeapp stdio bad-tools
//
// Tools: render (echoes the scene), big (returns mb megabytes of text, for
// large payloads), slow (waits seconds, for calls in flight), crash (exits
// the process, for restart handling), pid (the process ID, to tell
// per-client processes apart) and progress (reports steps progress
// notifications, then answers). Also a prompt "brief", a resource
// "fake://scene/current" and a template "fake://scene/{name}".
//
// hold blocks until its request context ends, counts that, and logs
// "hold: context ended"; cancelled reports the count. Over HTTP, /stateless
// serves the same tools as a stateless Streamable HTTP server whose handler
// context ends with the HTTP request (2026-07-28), so it shows whether an
// upstream request was really stopped.
//
// With "bad-tools" its tool list also has entries a hub must drop rather
// than crash on: a tool without an input schema, one whose schema is a
// number, and a null entry.
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type in struct {
	Scene string `json:"scene"`
}

type size struct {
	MB int `json:"mb"`
}

type wait struct {
	Seconds int `json:"seconds"`
}

type steps struct {
	Steps int `json:"steps"`
}

// cancelledHolds counts hold calls whose context ended.
var cancelledHolds atomic.Int64

func main() {
	s := mcp.NewServer(&mcp.Implementation{Name: "fake-blender", Version: "4.2"}, nil)
	mcp.AddTool(s, &mcp.Tool{Name: "render", Description: "Render a scene"}, func(ctx context.Context, r *mcp.CallToolRequest, a in) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "rendered " + a.Scene}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "big", Description: "Return mb megabytes of text"}, func(ctx context.Context, r *mcp.CallToolRequest, a size) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", a.MB<<20)}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "slow", Description: "Wait seconds, then answer"}, func(ctx context.Context, r *mcp.CallToolRequest, a wait) (*mcp.CallToolResult, any, error) {
		select {
		case <-time.After(time.Duration(a.Seconds) * time.Second):
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "waited"}}}, nil, nil
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	})
	mcp.AddTool(s, &mcp.Tool{Name: "hold", Description: "Block until the request is cancelled"}, func(ctx context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		<-ctx.Done()
		cancelledHolds.Add(1)
		log.Print("hold: context ended")
		return nil, nil, ctx.Err()
	})
	mcp.AddTool(s, &mcp.Tool{Name: "cancelled", Description: "How many hold calls were cancelled"}, func(ctx context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(cancelledHolds.Load())}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "crash", Description: "Exit the process"}, func(ctx context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		os.Exit(3)
		return nil, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "pid", Description: "Return the process ID"}, func(ctx context.Context, r *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprint(os.Getpid())}}}, nil, nil
	})
	mcp.AddTool(s, &mcp.Tool{Name: "progress", Description: "Report progress, then answer"}, func(ctx context.Context, r *mcp.CallToolRequest, a steps) (*mcp.CallToolResult, any, error) {
		token := r.Params.GetProgressToken()
		for i := 1; i <= a.Steps; i++ {
			if token != nil {
				r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: float64(i), Total: float64(a.Steps)})
			}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("done %d steps", a.Steps)}}}, nil, nil
	})
	s.AddPrompt(&mcp.Prompt{Name: "brief", Description: "Brief the artist", Arguments: []*mcp.PromptArgument{{Name: "scene", Required: true}}},
		func(ctx context.Context, r *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "Render " + r.Params.Arguments["scene"]}}}}, nil
		})
	s.AddResource(&mcp.Resource{URI: "fake://scene/current", Name: "current", MIMEType: "text/plain"},
		func(ctx context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, MIMEType: "text/plain", Text: "the current scene"}}}, nil
		})
	s.AddResourceTemplate(&mcp.ResourceTemplate{URITemplate: "fake://scene/{name}", Name: "scene", MIMEType: "text/plain"},
		func(ctx context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, MIMEType: "text/plain", Text: "scene " + strings.TrimPrefix(r.Params.URI, "fake://scene/")}}}, nil
		})
	if len(os.Args) > 2 && os.Args[2] == "bad-tools" {
		s.AddReceivingMiddleware(badTools)
	}
	if os.Args[1] == "stdio" {
		if err := s.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			log.Fatal(err)
		}
		return
	}
	get := func(*http.Request) *mcp.Server { return s }
	http.Handle("/sse", mcp.NewSSEHandler(get, nil))
	http.Handle("/mcp", mcp.NewStreamableHTTPHandler(get, nil))
	http.Handle("/stateless", mcp.NewStreamableHTTPHandler(get, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}))
	log.Fatal(http.ListenAndServe(os.Args[1], nil))
}

// badTools appends malformed entries to every tools/list result. They are
// added after the SDK's own checks, as a misbehaving MCP would send them.
func badTools(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
			out := *list
			out.Tools = append(slices.Clone(list.Tools),
				&mcp.Tool{Name: "no_schema", Description: "Has no input schema"},
				&mcp.Tool{Name: "number_schema", InputSchema: 5},
				nil)
			return &out, nil
		}
		return res, err
	}
}
