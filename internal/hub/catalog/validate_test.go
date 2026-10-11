package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSanitizeDropsWhatClientsCantBeServed(t *testing.T) {
	big := strings.Repeat("x", MaxDefinitionBytes)
	tests := []struct {
		name    string
		content Content
		problem string // "" means kept
	}{
		{"valid tool", Content{Tools: []*mcp.Tool{{Name: "t", InputSchema: obj}}}, ""},
		{"null tool", Content{Tools: []*mcp.Tool{nil}}, "tool #1 dropped: null entry"},
		{"no name", Content{Tools: []*mcp.Tool{{InputSchema: obj}}}, "tool #1 dropped: no name"},
		{"long name", Content{Tools: []*mcp.Tool{{Name: strings.Repeat("n", 129), InputSchema: obj}}}, "name longer than 128"},
		{"no schema", Content{Tools: []*mcp.Tool{{Name: "t"}}}, `tool "t" dropped: AddTool "t": missing input schema`},
		{"number schema", Content{Tools: []*mcp.Tool{{Name: "t", InputSchema: 5}}}, `tool "t" dropped`},
		{"array schema", Content{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{"type": "array"}}}}, `must have type "object"`},
		{"raw number schema", Content{Tools: []*mcp.Tool{{Name: "t", InputSchema: json.RawMessage("5")}}}, `tool "t" dropped`},
		{"bad header annotation", Content{Tools: []*mcp.Tool{{Name: "t", InputSchema: map[string]any{
			"type": "object", "properties": map[string]any{"a": map[string]any{"type": "object", "x-mcp-header": "A"}},
		}}}}, "parameter header"},
		{"huge description", Content{Tools: []*mcp.Tool{{Name: "t", Description: big, InputSchema: obj}}}, "bytes (max"},
		{"null prompt", Content{Prompts: []*mcp.Prompt{nil}}, "prompt #1 dropped: null entry"},
		{"valid prompt", Content{Prompts: []*mcp.Prompt{{Name: "p"}}}, ""},
		{"null resource", Content{Resources: []*mcp.Resource{nil}}, "resource #1 dropped: null entry"},
		{"resource without URI", Content{Resources: []*mcp.Resource{{Name: "r"}}}, "no name"},
		{"unparsable resource URI", Content{Resources: []*mcp.Resource{{URI: "%zz"}}}, `resource "%zz" dropped`},
		{"valid resource", Content{Resources: []*mcp.Resource{{URI: "file:///a"}}}, ""},
		{"null template", Content{ResourceTemplates: []*mcp.ResourceTemplate{nil}}, "resource template #1 dropped"},
		{"bad template", Content{ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///{a"}}}, "resource template"},
		{"valid template", Content{ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///{a}"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, problems := Sanitize("n", "m", tt.content)
			kept := len(out.Tools) + len(out.Prompts) + len(out.Resources) + len(out.ResourceTemplates)
			if tt.problem == "" {
				if kept != 1 || len(problems) != 0 {
					t.Fatalf("kept %d, problems %q", kept, problems)
				}
				return
			}
			if kept != 0 || len(problems) != 1 || !strings.Contains(problems[0], tt.problem) {
				t.Fatalf("kept %d, problems %q; want one containing %q", kept, problems, tt.problem)
			}
		})
	}
}

func TestSanitizeKeepsTheRest(t *testing.T) {
	in := Content{Tools: []*mcp.Tool{
		{Name: "a", InputSchema: obj}, nil, {Name: "b"}, {Name: "a", InputSchema: obj}, {Name: "c", InputSchema: obj},
	}}
	out, problems := Sanitize("n", "m", in)
	var names []string
	for _, tl := range out.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "a,c" || len(problems) != 3 {
		t.Fatalf("kept %v, problems %q", names, problems)
	}
	if !strings.Contains(problems[2], `tool "a" dropped: listed twice`) {
		t.Errorf("duplicate problem = %q", problems[2])
	}
	if len(in.Tools) != 5 || in.Tools[1] != nil {
		t.Error("input modified")
	}
}

// A node's bad definitions must not reach the catalog, where the gateway
// would hand them to the SDK (which panics) and rebuildLocked would
// dereference nil entries.
func TestSetReportsAndDropsInvalidEntries(t *testing.T) {
	c := New()
	problems := c.Set("n", "m", Content{
		Tools:   []*mcp.Tool{nil, {Name: "bad", InputSchema: 5}, {Name: "ok", InputSchema: obj}},
		Prompts: []*mcp.Prompt{nil},
	}, nil)
	if len(problems) != 3 {
		t.Fatalf("problems = %q", problems)
	}
	if tools := c.List(); len(tools) != 1 || tools[0].Name != "n__m__ok" {
		t.Fatalf("tools = %v", tools)
	}
	if len(c.Prompts()) != 0 {
		t.Error("null prompt kept")
	}
}
