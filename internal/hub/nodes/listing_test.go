package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestListAllBudget(t *testing.T) {
	// pages serves n pages of size items each, then ends (n < 0: never).
	pages := func(n, size int, desc string, cursors func(i int) string) func(context.Context, string) ([]*mcp.Tool, string, error) {
		i := 0
		return func(context.Context, string) ([]*mcp.Tool, string, error) {
			i++
			var tools []*mcp.Tool
			for j := range size {
				tools = append(tools, &mcp.Tool{Name: fmt.Sprintf("t%d-%d", i, j), Description: desc})
			}
			if n >= 0 && i >= n {
				return tools, "", nil
			}
			return tools, cursors(i), nil
		}
	}
	unique := func(i int) string { return fmt.Sprint("c", i) }
	tests := []struct {
		name  string
		page  func(context.Context, string) ([]*mcp.Tool, string, error)
		items int    // listed, when it succeeds
		want  string // in the error, when it fails
	}{
		{"within budget", pages(3, 2, "", unique), 6, ""},
		{"endless cursors", pages(-1, 1, "", unique), 0, "more than 5 pages"},
		{"too many items", pages(3, 5, "", unique), 0, "more than 10 items"},
		{"too many bytes", pages(2, 1, strings.Repeat("x", 600), unique), 0, "more than 1000 bytes"},
		{"repeated cursor", pages(-1, 1, "", func(int) string { return "same" }), 0, `cursor "same" repeated`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &listBudget{maxPages: 5, maxItems: 10, maxBytes: 1000}
			got, err := listAll(t.Context(), b, "tools", tt.page)
			if tt.want == "" {
				if err != nil || len(got) != tt.items {
					t.Fatalf("got %d items, %v", len(got), err)
				}
				return
			}
			if !errors.Is(err, errListBudget) || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// An MCP that answers every tools/list with another cursor fails to open,
// saying why, instead of filling the hub's memory.
func TestEndlessListingFailsTheMCP(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	startFake(t, url, hello("machine"), fakeOptions{configure: func(s *mcp.Server) {
		n := 0
		s.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				if method != "tools/list" {
					return next(ctx, method, req)
				}
				n++
				return &mcp.ListToolsResult{
					Tools:      []*mcp.Tool{{Name: fmt.Sprint("t", n), InputSchema: map[string]any{"type": "object"}}},
					NextCursor: fmt.Sprint("c", n),
				}, nil
			}
		})
	}})
	await(t, func() bool {
		mcps := infoOf(r, "machine").MCPs
		return len(mcps) == 1 && mcps[0].State == MCPFailed && strings.Contains(mcps[0].Error, "budget")
	})
	if tools := cat.List(); len(tools) != 0 {
		t.Errorf("catalog has %d tools", len(tools))
	}
}
