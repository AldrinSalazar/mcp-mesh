package api

import (
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/overrides"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func newOverridesEnv(t *testing.T) *accessEnv {
	t.Helper()
	a := newAccessEnv(t)
	o, err := overrides.Open(t.Context(), a.db, overrides.Options{Catalog: a.catalog})
	if err != nil {
		t.Fatal(err)
	}
	a.srv.opts.Overrides = o
	a.catalog.SetMCP("ws", "blender", []*mcp.Tool{
		{Name: "render", Description: "Renders.", InputSchema: map[string]any{"type": "object"}},
		{Name: "export", InputSchema: map[string]any{"type": "object"}},
	}, nil)
	return a
}

func (a *accessEnv) toolsByOriginal(t *testing.T) map[string]toolView {
	t.Helper()
	w := a.do("GET", "/api/v1/tools", "")
	wantStatus(t, w, 200)
	var v []toolView
	decodeBody(t, w, &v)
	out := map[string]toolView{}
	for _, tv := range v {
		out[tv.OriginalName] = tv
	}
	return out
}

func TestOverridesLifecycle(t *testing.T) {
	a := newOverridesEnv(t)
	w := a.do("PUT", "/api/v1/overrides/ws/blender/render", `{"alias":"render","description":"Make a picture."}`)
	wantStatus(t, w, 200)
	var e overrides.Entry
	decodeBody(t, w, &e)
	if e.Alias != "render" || e.Tool != "render" {
		t.Fatalf("entry %+v", e)
	}

	tv := a.toolsByOriginal(t)["render"]
	if tv.Name != "render" || !tv.Alias || tv.GeneratedName != "ws__blender__render" ||
		tv.Description != "Renders." || tv.DescriptionOverride != "Make a picture." {
		t.Fatalf("tool view %+v", tv)
	}
	if ex := a.toolsByOriginal(t)["export"]; ex.Alias || ex.DescriptionOverride != "" || ex.Name != ex.GeneratedName {
		t.Fatalf("untouched tool %+v", ex)
	}

	w = a.do("GET", "/api/v1/overrides", "")
	wantStatus(t, w, 200)
	var l []overrides.Entry
	decodeBody(t, w, &l)
	if len(l) != 1 {
		t.Fatalf("list %+v", l)
	}

	// Offline tools can have overrides too.
	wantStatus(t, a.do("PUT", "/api/v1/overrides/gone/m/t", `{"alias":"","description":"later"}`), 200)

	wantError(t, a.do("PUT", "/api/v1/overrides/ws/blender/export", `{"alias":"render"}`), 409, "")
	wantError(t, a.do("PUT", "/api/v1/overrides/ws/blender/export", `{"alias":"bad name"}`), 400, "")
	wantError(t, a.do("PUT", "/api/v1/overrides/ws/blender/render", `{"alias":"ws__blender__export"}`), 409, "")
	wantError(t, a.do("PUT", "/api/v1/overrides/ws/blender/export", `{"nope":1}`), 400, "")

	wantStatus(t, a.do("PUT", "/api/v1/overrides/ws/blender/render", `{"alias":"","description":""}`), 204)
	if tv := a.toolsByOriginal(t)["render"]; tv.Alias || tv.Name != "ws__blender__render" {
		t.Fatalf("after clear %+v", tv)
	}
	wantStatus(t, a.do("DELETE", "/api/v1/overrides/gone/m/t", ""), 204)
	wantStatus(t, a.do("DELETE", "/api/v1/overrides/gone/m/t", ""), 204)

	entries, err := a.log.Query(t.Context(), 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, en := range entries {
		if en.Action == adminlog.ToolOverride {
			n++
			if !strings.Contains(en.Target, "/") {
				t.Errorf("target %q", en.Target)
			}
		}
	}
	if n != 5 {
		t.Errorf("logged %d override actions, want 5", n)
	}
}

func TestOverridesEvent(t *testing.T) {
	a := newOverridesEnv(t)
	b, err := a.srv.snapshot(t.Context(), "overrides")
	if err != nil || string(b) != "[]" {
		t.Fatalf("snapshot %s %v", b, err)
	}
	found := false
	for _, typ := range a.srv.eventTypes() {
		found = found || typ == "overrides"
	}
	if !found {
		t.Fatal("no overrides event type")
	}
}

func TestOverridesWithoutStore(t *testing.T) {
	e := newEnv(t, true)
	wantError(t, e.do("GET", "/api/v1/overrides", ""), 404, "")
}
