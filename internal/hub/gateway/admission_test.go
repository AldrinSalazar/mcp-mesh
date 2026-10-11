package gateway

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
)

// The caller stands in for a handle: by the time it runs the check, the
// lazy start or the queue wait has changed what the gateway first checked.
func TestCallRechecksBeforeSending(t *testing.T) {
	tests := []struct {
		name string
		// change runs where a handle would wait (startup, queue).
		change func(c *catalog.Catalog, hidden *atomic.Bool)
		wantOK bool
	}{
		{"unchanged", func(*catalog.Catalog, *atomic.Bool) {}, true},
		{"access removed", func(_ *catalog.Catalog, hidden *atomic.Bool) { hidden.Store(true) }, false},
		{"definition changed", func(c *catalog.Catalog, _ *atomic.Bool) {
			// The freshly started process lists another description, which
			// isn't the approved one.
			c.SetMCP("n", "m", []*mcp.Tool{tool("t", "v2")}, nil)
		}, false},
		{"tool gone", func(c *catalog.Catalog, _ *atomic.Bool) { c.RemoveMCP("n", "m") }, false},
	}
	for _, tt := range tests {
		for _, version := range protocolVersions {
			t.Run(tt.name+"/"+version, func(t *testing.T) {
				c := catalog.New()
				var hidden atomic.Bool
				var sent atomic.Bool
				caller := callerFunc(func(ctx context.Context, _ *mcp.CallToolParams) (*mcp.CallToolResult, error) {
					tt.change(c, &hidden)
					if err := catalog.RunCheck(ctx, nil); err != nil {
						return nil, err
					}
					sent.Store(true)
					return textResult("ran"), nil
				})
				c.SetMCP("n", "m", []*mcp.Tool{tool("t", "v1")}, caller)
				// Approved means "description v1" here, as a pin would.
				visible := func(_ identity.Client, tl *catalog.Tool) bool {
					return !hidden.Load() && tl.Upstream.Description == "v1"
				}
				g := New(Options{Catalog: c, Identify: byHeader, Visible: visible})
				s := connectAs(t, g, version, "a", nil)
				res, err := s.CallTool(t.Context(), &mcp.CallToolParams{Name: "n__m__t"})
				if tt.wantOK {
					if err != nil || res.IsError || !sent.Load() {
						t.Fatalf("call = %v, %v; sent %v", res, err, sent.Load())
					}
					return
				}
				if err == nil || !strings.Contains(err.Error(), "unknown tool") {
					t.Fatalf("call = %+v, %v; want unknown tool", res, err)
				}
				if sent.Load() {
					t.Fatal("request was sent upstream")
				}
			})
		}
	}
}

func TestAddToolContainsSDKPanic(t *testing.T) {
	g := New(Options{Catalog: catalog.New()})
	m := g.mirrorFor(identity.Client{ID: "a", Name: "a"})
	for _, bad := range []*mcp.Tool{
		{Name: "no_schema"},
		{Name: "number_schema", InputSchema: 5},
		{Name: "array_schema", InputSchema: map[string]any{"type": "array"}},
	} {
		if g.addTool(m, bad) {
			t.Errorf("%s registered", bad.Name)
		}
	}
	if !g.addTool(m, tool("fine", "")) {
		t.Error("valid tool not registered")
	}
}
