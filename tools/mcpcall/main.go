package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer adds the hub client key to every request.
type bearer struct {
	key  string
	next http.RoundTripper
}

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return b.next.RoundTrip(r)
}

// mcpcall calls one tool on an MCP endpoint (e.g. the hub) and prints the
// result, or lists the tool names when no tool is given. Long results print
// their size instead of their text. Tool calls ask for progress and print
// how many notifications arrived. Dev testing only.
//
//	go run ./tools/mcpcall http://127.0.0.1:7002/mcp NODE__MCP__TOOL '{"arg":1}'
//	go run ./tools/mcpcall http://127.0.0.1:7002/mcp
//	go run ./tools/mcpcall URL :prompts | :resources | :templates
//	go run ./tools/mcpcall URL :prompt NAME '{"arg":"value"}'
//	go run ./tools/mcpcall URL :read URI
//
// With MCPMESH_CLIENT_KEY set, it authenticates to the hub with that key.
//
// Exit status: 0 when the connection and the call worked, 1 when connecting,
// listing or the call failed, or the tool answered with isError=true (the
// result is still printed). Scripts can then tell a refused or failed call
// from a good one without matching text.
func main() {
	ctx := context.Background()
	transport := &mcp.StreamableClientTransport{Endpoint: os.Args[1]}
	if key := os.Getenv("MCPMESH_CLIENT_KEY"); key != "" {
		transport.HTTPClient = &http.Client{Transport: bearer{key: key, next: http.DefaultTransport}}
	}
	var progress atomic.Int64
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "call", Version: "1"}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(context.Context, *mcp.ProgressNotificationClientRequest) { progress.Add(1) },
	}).Connect(ctx, transport, nil)
	if err != nil {
		fmt.Println("connect:", err)
		os.Exit(1)
	}
	defer cs.Close()
	if len(os.Args) < 3 {
		for tool, err := range cs.Tools(ctx, nil) {
			if err != nil {
				fmt.Println("list:", err)
				os.Exit(1)
			}
			fmt.Println(tool.Name)
		}
		return
	}
	if strings.HasPrefix(os.Args[2], ":") {
		if err := other(ctx, cs, os.Args[2:]); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		return
	}
	var args map[string]any
	if len(os.Args) > 3 {
		json.Unmarshal([]byte(os.Args[3]), &args)
	}
	start := time.Now()
	params := &mcp.CallToolParams{Name: os.Args[2], Arguments: args}
	params.SetProgressToken("mcpcall")
	res, err := cs.CallTool(ctx, params)
	if n := progress.Load(); n > 0 {
		fmt.Printf("progress: %d\n", n)
	}
	if err != nil {
		fmt.Printf("error after %s: %v\n", time.Since(start).Round(time.Millisecond), err)
		os.Exit(1)
	}
	out := ""
	if len(res.Content) > 0 {
		if tc, ok := res.Content[0].(*mcp.TextContent); ok {
			out = tc.Text
		}
	}
	if len(out) > 300 {
		out = fmt.Sprintf("%d bytes", len(out))
	}
	fmt.Printf("ok after %s (isError=%v): %s\n", time.Since(start).Round(time.Millisecond), res.IsError, strings.ReplaceAll(strings.TrimSpace(out), "\n", " | "))
	if res.IsError {
		cs.Close()
		os.Exit(1)
	}
}

// other runs the prompt and resource commands.
func other(ctx context.Context, cs *mcp.ClientSession, args []string) error {
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch args[0] {
	case ":prompts":
		for p, err := range cs.Prompts(ctx, nil) {
			if err != nil {
				return err
			}
			fmt.Println(p.Name)
		}
	case ":resources":
		for r, err := range cs.Resources(ctx, nil) {
			if err != nil {
				return err
			}
			fmt.Println(r.URI)
		}
	case ":templates":
		for r, err := range cs.ResourceTemplates(ctx, nil) {
			if err != nil {
				return err
			}
			fmt.Println(r.URITemplate)
		}
	case ":prompt":
		var pargs map[string]string
		json.Unmarshal([]byte(arg(2)), &pargs)
		res, err := cs.GetPrompt(ctx, &mcp.GetPromptParams{Name: arg(1), Arguments: pargs})
		if err != nil {
			return err
		}
		for _, m := range res.Messages {
			if tc, ok := m.Content.(*mcp.TextContent); ok {
				fmt.Printf("%s: %s\n", m.Role, tc.Text)
			}
		}
	case ":read":
		res, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: arg(1)})
		if err != nil {
			return err
		}
		for _, c := range res.Contents {
			fmt.Printf("%s: %s\n", c.URI, c.Text)
		}
	default:
		return fmt.Errorf("unknown command %s", args[0])
	}
	return nil
}
