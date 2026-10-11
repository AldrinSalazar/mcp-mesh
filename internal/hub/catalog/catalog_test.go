package catalog

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExposedName(t *testing.T) {
	if got := ExposedName("work-pc", "fs", "read_file"); got != "work-pc__fs__read_file" {
		t.Errorf("simple: got %q", got)
	}
	if got := ExposedName("my.laptop", "git hub", "list/repos"); got != "my_laptop__git_hub__list_repos" {
		t.Errorf("sanitize: got %q", got)
	}
	long := ExposedName("node", "mcp", strings.Repeat("x", 100))
	if len(long) != MaxNameLen {
		t.Errorf("long: len %d, want %d", len(long), MaxNameLen)
	}
	if long == ExposedName("node", "mcp", strings.Repeat("x", 101)) {
		t.Error("long names with different tools must differ")
	}
}

func TestCollisionAndRemove(t *testing.T) {
	c := New()
	changes, cancel := c.Changes()
	defer cancel()

	c.SetMCP("a", "b", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil)
	c.SetMCP("a", "b.", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil)
	c.SetMCP("a.b", "c", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil)
	c.SetMCP("a_b", "c", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil) // collides with a.b
	<-changes

	tools := c.List()
	if len(tools) != 4 {
		t.Fatalf("got %d tools, want 4", len(tools))
	}
	seen := map[string]bool{}
	for _, tool := range tools {
		if seen[tool.Name] {
			t.Errorf("duplicate name %q", tool.Name)
		}
		seen[tool.Name] = true
	}

	// While they collide, neither has the plain name: a newcomer can't take
	// it over from the tool clients knew by it.
	if _, ok := c.Lookup("a_b__c__t"); ok {
		t.Error("a colliding tool kept the plain name")
	}
	c.RemoveNode("a_b")
	if _, ok := c.Lookup("a_b__c__t"); !ok {
		t.Error("the remaining tool should get the plain name back")
	}
	c.RemoveNode("a.b")
	if n := len(c.List()); n != 2 {
		t.Errorf("after removes: %d tools, want 2", n)
	}
}

func TestExposedDescription(t *testing.T) {
	tool := &Tool{Name: "n__m__t", Node: "n", MCP: "m", Upstream: &mcp.Tool{Name: "t", Description: "Reads."}}
	if d := tool.Exposed().Description; d != "[n/m] Reads." {
		t.Errorf("got %q", d)
	}
	if tool.Upstream.Name != "t" {
		t.Error("Exposed must not modify the upstream tool")
	}
}

type fullCaller struct{ id string }

func (fullCaller) CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	return nil, nil
}
func (fullCaller) GetPrompt(context.Context, *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	return nil, nil
}
func (fullCaller) ReadResource(context.Context, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	return nil, nil
}

func names(c *Catalog) []string {
	var out []string
	for _, t := range c.List() {
		out = append(out, t.Name)
	}
	return out
}

func TestOverrides(t *testing.T) {
	c := New()
	c.SetMCP("pc", "fs", []*mcp.Tool{{Name: "read", Description: "Read", InputSchema: obj}, {Name: "write", InputSchema: obj}}, nil)
	c.SetMCP("mac", "fs", []*mcp.Tool{{Name: "read", InputSchema: obj}}, nil)
	c.SetOverrides(map[ToolKey]Override{
		{"pc", "fs", "read"}:  {Name: "read_file", Description: "Read a file on the PC"},
		{"mac", "fs", "read"}: {Name: "read_file"}, // collides: the lower key (mac) wins
		{"pc", "fs", "write"}: {Name: "bad name!"}, // invalid: ignored
		{"gone", "x", "tool"}: {Name: "for_later"}, // not in the catalog yet
	})
	got := names(c)
	if !slices.Contains(got, "read_file") || !slices.Contains(got, "pc__fs__write") {
		t.Fatalf("names = %v", got)
	}
	mac, _ := c.ToolByKey(ToolKey{"mac", "fs", "read"})
	pc, _ := c.ToolByKey(ToolKey{"pc", "fs", "read"})
	if mac.Name != "read_file" || !mac.Aliased {
		t.Errorf("mac read = %q aliased=%v", mac.Name, mac.Aliased)
	}
	if pc.Name != "pc__fs__read" || pc.Aliased {
		t.Errorf("pc read = %q (its alias was taken, so it keeps its generated name)", pc.Name)
	}
	if d := pc.Exposed().Description; d != "[pc/fs] Read a file on the PC" {
		t.Errorf("override description = %q", d)
	}
	// The alias applies as soon as its tool appears.
	c.SetMCP("gone", "x", []*mcp.Tool{{Name: "tool", InputSchema: obj}}, nil)
	if tl, ok := c.Lookup("for_later"); !ok || tl.Node != "gone" {
		t.Errorf("for_later = %v %v", tl, ok)
	}
	// An alias taking a generated name pushes the generated one to a hash.
	c.SetOverrides(map[ToolKey]Override{{"mac", "fs", "read"}: {Name: "pc__fs__write"}})
	w, _ := c.ToolByKey(ToolKey{"pc", "fs", "write"})
	if w.Name == "pc__fs__write" || !strings.HasPrefix(w.Name, "pc__fs__write_") {
		t.Errorf("generated name not moved aside: %q", w.Name)
	}
	if !ValidAlias("ok_name-1") || ValidAlias("") || ValidAlias("a b") || ValidAlias(strings.Repeat("x", 65)) {
		t.Error("ValidAlias")
	}
}

func TestNamesDontDependOnOrder(t *testing.T) {
	// "a-b"/"c" and "a_b"/"c" sanitize to the same name; which one gets the
	// hash must not depend on which MCP connected first.
	build := func(first, second string) map[string]string {
		c := New()
		c.SetMCP(first, "m", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil)
		c.SetMCP(second, "m", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil)
		out := map[string]string{}
		for _, tl := range c.List() {
			out[tl.Node] = tl.Name
		}
		return out
	}
	a, b := build("a-b", "a_b"), build("a_b", "a-b")
	if !maps.Equal(a, b) {
		t.Errorf("order-dependent names: %v vs %v", a, b)
	}
}

// A tool known by its plain name must not lose it to a newcomer that
// sanitizes to the same name and sorts first: both get hashed names.
func TestNewCollisionDoesNotTakeTheName(t *testing.T) {
	c := New()
	c.SetMCP("b_x", "m", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil)
	if tl, ok := c.Lookup("b_x__m__t"); !ok || tl.Node != "b_x" {
		t.Fatalf("plain name = %v %v", tl, ok)
	}
	c.SetMCP("b.x", "m", []*mcp.Tool{{Name: "t", InputSchema: obj}}, nil) // sorts before b_x
	if tl, ok := c.Lookup("b_x__m__t"); ok {
		t.Fatalf("plain name now reaches %s/%s/%s", tl.Node, tl.MCP, tl.Upstream.Name)
	}
	for _, node := range []string{"b_x", "b.x"} {
		tl, ok := c.ToolByKey(ToolKey{node, "m", "t"})
		if !ok || !strings.HasPrefix(tl.Name, "b_x__m__t_") {
			t.Errorf("%s's tool = %v", node, tl)
		}
	}
	c.Set("p_q", "m", Content{Prompts: []*mcp.Prompt{{Name: "p"}}}, nil)
	c.Set("p.q", "m", Content{Prompts: []*mcp.Prompt{{Name: "p"}}}, nil)
	for _, p := range c.Prompts() {
		if p.Name == "p_q__m__p" {
			t.Errorf("colliding prompt kept the plain name (%s)", p.Node)
		}
	}
}

func TestPromptsAndResources(t *testing.T) {
	c := New()
	caller := fullCaller{}
	c.Set("pc", "notes", Content{
		Tools:             []*mcp.Tool{{Name: "t", InputSchema: obj}},
		Prompts:           []*mcp.Prompt{{Name: "summarize", Description: "Sum it up"}},
		Resources:         []*mcp.Resource{{URI: "file:///notes/a.md", Name: "a"}},
		ResourceTemplates: []*mcp.ResourceTemplate{{URITemplate: "file:///notes/{name}.md", Name: "note"}},
		Timeout:           5 * time.Minute,
	}, caller)

	ps := c.Prompts()
	if len(ps) != 1 || ps[0].Name != "pc__notes__summarize" || ps[0].Exposed().Description != "[pc/notes] Sum it up" || ps[0].Timeout != 5*time.Minute {
		t.Fatalf("prompts = %+v", ps)
	}
	if _, ok := c.LookupPrompt("pc__notes__summarize"); !ok {
		t.Error("LookupPrompt")
	}
	rs := c.Resources()
	if len(rs) != 1 || rs[0].URI != "mcp-mesh://pc/notes/file:///notes/a.md" {
		t.Fatalf("resources = %+v", rs)
	}
	ts := c.ResourceTemplates()
	if len(ts) != 1 || ts[0].URITemplate != "mcp-mesh://pc/notes/file:///notes/{name}.md" {
		t.Fatalf("templates = %+v", ts)
	}
	// A URI expanded from the template routes to the MCP with the upstream URI.
	r, ok := c.ResolveResource("mcp-mesh://pc/notes/file:///notes/b.md")
	if !ok || r.Node != "pc" || r.MCP != "notes" || r.URI != "file:///notes/b.md" || r.Timeout != 5*time.Minute {
		t.Fatalf("route = %+v %v", r, ok)
	}
	for _, bad := range []string{"file:///notes/a.md", "mcp-mesh://pc/other/x", "mcp-mesh://pc/notes/", "mcp-mesh://pc"} {
		if _, ok := c.ResolveResource(bad); ok {
			t.Errorf("ResolveResource(%q) succeeded", bad)
		}
	}
	tl, _ := c.Lookup("pc__notes__t")
	if tl.Timeout != 5*time.Minute {
		t.Errorf("tool timeout = %v", tl.Timeout)
	}
	c.RemoveNode("pc")
	if len(c.Prompts())+len(c.Resources())+len(c.ResourceTemplates())+len(c.List()) != 0 {
		t.Error("RemoveNode left entries")
	}
}

func TestResourceURIRoundTrip(t *testing.T) {
	for _, tt := range []struct{ node, mcp, up string }{
		{"pc", "fs", "file:///a b/c"},
		{"my pc", "f/s", "https://x.example/?q=1#f"},
		{"n", "m", "ui://widget/{id}"},
	} {
		u := ResourceURI(tt.node, tt.mcp, tt.up)
		n, m, up, ok := ParseResourceURI(u)
		if !ok || n != tt.node || m != tt.mcp || up != tt.up {
			t.Errorf("%q → (%q %q %q %v)", u, n, m, up, ok)
		}
	}
}

// obj is the smallest input schema the SDK accepts.
var obj = map[string]any{"type": "object"}
