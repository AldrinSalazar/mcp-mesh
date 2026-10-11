package overrides

import (
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func tool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, Description: "upstream " + name, InputSchema: map[string]any{"type": "object"}}
}

type env struct {
	path    string
	db      *store.DB
	cat     *catalog.Catalog
	s       *Store
	changes *atomic.Int32
}

func setup(t *testing.T) *env {
	t.Helper()
	path := filepath.Join(t.TempDir(), store.FileName)
	db, err := store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	cat := catalog.New()
	changes := new(atomic.Int32)
	s, err := Open(t.Context(), db, Options{Catalog: cat, OnChange: func() { changes.Add(1) }})
	if err != nil {
		t.Fatal(err)
	}
	return &env{path, db, cat, s, changes}
}

func TestSetAndClear(t *testing.T) {
	e := setup(t)
	e.cat.SetMCP("ws", "blender", []*mcp.Tool{tool("render")}, nil)
	k := catalog.ToolKey{Node: "ws", MCP: "blender", Tool: "render"}

	got, err := e.s.Set(t.Context(), k, "render", "Renders a scene.", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if got.Alias != "render" || got.UpdatedBy != "admin" {
		t.Fatalf("entry %+v", got)
	}
	tl, ok := e.cat.Lookup("render")
	if !ok || !tl.Aliased || tl.Description != "Renders a scene." {
		t.Fatalf("lookup render: %+v %v", tl, ok)
	}
	if _, ok := e.cat.Lookup("ws__blender__render"); ok {
		t.Fatal("generated name still exposed")
	}
	if e.changes.Load() == 0 {
		t.Fatal("OnChange not called")
	}

	if _, err := e.s.Set(t.Context(), k, "", "", "admin"); err != nil {
		t.Fatal(err)
	}
	if l, _ := e.s.List(t.Context()); len(l) != 0 {
		t.Fatalf("rows left: %+v", l)
	}
	if _, ok := e.cat.Lookup("ws__blender__render"); !ok {
		t.Fatal("generated name not restored")
	}
}

func TestDescriptionOnly(t *testing.T) {
	e := setup(t)
	e.cat.SetMCP("ws", "blender", []*mcp.Tool{tool("render")}, nil)
	k := catalog.ToolKey{Node: "ws", MCP: "blender", Tool: "render"}
	if _, err := e.s.Set(t.Context(), k, "", "Clearer.", "a"); err != nil {
		t.Fatal(err)
	}
	tl, ok := e.cat.Lookup("ws__blender__render")
	if !ok || tl.Aliased || tl.Description != "Clearer." {
		t.Fatalf("got %+v %v", tl, ok)
	}
}

func TestAppliesWhenToolAppears(t *testing.T) {
	e := setup(t)
	k := catalog.ToolKey{Node: "ws", MCP: "blender", Tool: "render"}
	if _, err := e.s.Set(t.Context(), k, "render", "", "a"); err != nil {
		t.Fatal(err)
	}
	e.cat.SetMCP("ws", "blender", []*mcp.Tool{tool("render")}, nil)
	if _, ok := e.cat.Lookup("render"); !ok {
		t.Fatal("alias not applied to a later tool")
	}
}

func TestCollisions(t *testing.T) {
	e := setup(t)
	e.cat.SetMCP("ws", "blender", []*mcp.Tool{tool("render"), tool("export")}, nil)
	a := catalog.ToolKey{Node: "ws", MCP: "blender", Tool: "render"}
	b := catalog.ToolKey{Node: "ws", MCP: "blender", Tool: "export"}
	if _, err := e.s.Set(t.Context(), a, "go", "", "x"); err != nil {
		t.Fatal(err)
	}
	// Another row's alias, here for a tool that is offline.
	if _, err := e.s.Set(t.Context(), catalog.ToolKey{Node: "gone", MCP: "m", Tool: "t"}, "ghost", "", "x"); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"go", "ghost"} {
		if _, err := e.s.Set(t.Context(), b, alias, "", "x"); !errors.Is(err, ErrAliasTaken) {
			t.Errorf("alias %q: err = %v", alias, err)
		}
	}
	if _, err := e.s.Set(t.Context(), b, "ws__blender__export", "", "x"); err != nil {
		t.Fatalf("own generated name: %v", err)
	}
	// A generated name that a different live tool uses.
	_, err := e.s.Set(t.Context(), a, "ws__blender__export", "", "x")
	if !errors.Is(err, ErrAliasTaken) || !strings.Contains(err.Error(), "ws/blender/export") {
		t.Fatalf("live generated name: %v", err)
	}
	if _, err := e.s.Set(t.Context(), a, "go", "new text", "x"); err != nil {
		t.Fatalf("re-saving own alias: %v", err)
	}
}

func TestValidation(t *testing.T) {
	e := setup(t)
	k := catalog.ToolKey{Node: "n", MCP: "m", Tool: "t"}
	for name, c := range map[string][2]string{
		"space":   {"a b", ""},
		"slash":   {"a/b", ""},
		"too big": {strings.Repeat("a", 65), ""},
		"long":    {"", strings.Repeat("x", MaxDescription+1)},
		"control": {"", "a\x00b"},
	} {
		if _, err := e.s.Set(t.Context(), k, c[0], c[1], "x"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := e.s.Set(t.Context(), k, "ok-1_", "line\n\ttab", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Set(t.Context(), catalog.ToolKey{MCP: "m", Tool: "t"}, "z", "", "x"); !errors.Is(err, ErrInvalid) {
		t.Errorf("empty node: %v", err)
	}
}

func TestOpenPushesInitialSet(t *testing.T) {
	e := setup(t)
	if _, err := e.s.Set(t.Context(), catalog.ToolKey{Node: "ws", MCP: "m", Tool: "t"}, "short", "", "x"); err != nil {
		t.Fatal(err)
	}
	cat2 := catalog.New()
	cat2.SetMCP("ws", "m", []*mcp.Tool{tool("t")}, nil)
	if _, err := Open(t.Context(), e.db, Options{Catalog: cat2}); err != nil {
		t.Fatal(err)
	}
	if _, ok := cat2.Lookup("short"); !ok {
		t.Fatal("initial set not pushed")
	}
}

func TestWatchFromSecondHandle(t *testing.T) {
	e := setup(t)
	e.cat.SetMCP("ws", "m", []*mcp.Tool{tool("t")}, nil)
	done := make(chan struct{})
	go func() { defer close(done); e.s.Watch(t.Context(), 10*time.Millisecond) }()

	other, err := store.Open(t.Context(), e.path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	before := e.changes.Load()
	if _, err := other.Exec(`INSERT INTO tool_overrides (node, mcp, tool, alias, updated_at) VALUES ('ws','m','t','viaother',1)`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := e.cat.Lookup("viaother"); ok && e.changes.Load() > before {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("watch did not apply the other handle's change")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
