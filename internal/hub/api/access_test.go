package api

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/trust"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// accessEnv is an auth-on env with a real ACL, trust store and admin log on
// a temporary database. Keys stay fake; the rows the ACL's foreign keys
// need are inserted for every key the fake creates.
type accessEnv struct {
	*env
	db     *store.DB
	groups *acl.Store
	trust  *trust.Store
	log    *adminlog.Log
}

func newAccessEnv(t *testing.T) *accessEnv {
	t.Helper()
	e := newEnv(t, true)
	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), store.FileName), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	groups, err := acl.Open(t.Context(), db, acl.Options{})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := trust.Open(t.Context(), db, trust.Options{Catalog: e.catalog})
	if err != nil {
		t.Fatal(err)
	}
	a := &accessEnv{env: e, db: db, groups: groups, trust: tr, log: adminlog.New(db, nil, 0)}
	e.srv.opts.Groups, e.srv.opts.Trust, e.srv.opts.AdminLog = groups, tr, a.log
	e.srv.opts.OAuthIssuer = "https://hub"
	e.srv.opts.OAuthName = func(id string) string { return map[string]string{"oc_1": "Claude"}[id] }
	return a
}

// keyRow mirrors a fake key into the keys table for the ACL's foreign key.
func (a *accessEnv) keyRow(t *testing.T, id string) {
	t.Helper()
	if _, err := a.db.Exec(`INSERT OR IGNORE INTO keys (id, kind, name, hash, created_at) VALUES (?, 'client', ?, ?, 1)`, id, id, "h"+id); err != nil {
		t.Fatal(err)
	}
}

// createClient makes a client key through the API.
func (a *accessEnv) createClient(t *testing.T, body string) map[string]any {
	t.Helper()
	// The fake's next ID is predictable; the row must exist before
	// SetKeyGroups runs inside the handler.
	a.keyRow(t, nextFakeID(a.keys))
	w := a.do("POST", "/api/v1/keys", body)
	wantStatus(t, w, 201)
	var out struct {
		Key map[string]any `json:"key"`
	}
	decodeBody(t, w, &out)
	return out.Key
}

func nextFakeID(f *fakeKeys) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return "k_000000" + string(rune('0'+(len(f.keys)+10)/10)) + string(rune('0'+(len(f.keys)+10)%10))
}

func TestCreateClientKeyGroups(t *testing.T) {
	a := newAccessEnv(t)
	k := a.createClient(t, `{"kind":"client","name":"phone"}`)
	if g, _ := k["groups"].([]any); len(g) != 1 || g[0] != acl.Everything {
		t.Fatalf("default groups = %v", k["groups"])
	}
	ro, err := a.groups.Create(t.Context(), acl.GroupInput{Name: "Read only", Grants: []acl.Grant{{Node: "laptop", MCP: "fs", Tool: "read"}}})
	if err != nil {
		t.Fatal(err)
	}
	k = a.createClient(t, `{"kind":"client","name":"tablet","groups":["`+ro.ID+`"]}`)
	if g, _ := k["groups"].([]any); len(g) != 1 || g[0] != ro.ID {
		t.Fatalf("chosen groups = %v", k["groups"])
	}
	wantError(t, a.do("POST", "/api/v1/keys", `{"kind":"client","name":"x","groups":["g_nope"]}`), 400, `no group "g_nope"`)
	wantError(t, a.do("POST", "/api/v1/keys", `{"kind":"node","name":"x","groups":[]}`), 400, "")
	wantError(t, a.do("POST", "/api/v1/keys", `{"kind":"client","name":"x","trust":"review"}`), 400, "")
	wantError(t, a.do("POST", "/api/v1/keys", `{"kind":"node","name":"x","trust":"paranoid"}`), 400, "")

	w := a.do("POST", "/api/v1/keys", `{"kind":"node","name":"studio","trust":"approve"}`)
	wantStatus(t, w, 201)
	if p := a.trust.Policy("studio"); p.Level != trust.Approve || !p.Explicit {
		t.Errorf("studio policy = %+v", p)
	}
}

func TestKeyGroupsAndEffectiveTools(t *testing.T) {
	a := newAccessEnv(t)
	a.catalog.SetMCP("laptop", "shell", []*mcp.Tool{{Name: "run", InputSchema: map[string]any{"type": "object"}}}, nil)
	k := a.createClient(t, `{"kind":"client","name":"phone","groups":[]}`)
	id := k["id"].(string)

	tools := func() []string {
		t.Helper()
		w := a.do("GET", "/api/v1/keys/"+id+"/tools", "")
		wantStatus(t, w, 200)
		var v []toolView
		decodeBody(t, w, &v)
		var names []string
		for _, tv := range v {
			names = append(names, tv.Name)
		}
		return names
	}
	if got := tools(); len(got) != 0 {
		t.Fatalf("key in no group sees %v", got)
	}
	ro, _ := a.groups.Create(t.Context(), acl.GroupInput{Name: "fs", Grants: []acl.Grant{{Node: "laptop", MCP: "fs", Tool: "*"}}})
	w := a.do("PUT", "/api/v1/keys/"+id+"/groups", `{"groups":["`+ro.ID+`"]}`)
	wantStatus(t, w, 200)
	if got := tools(); len(got) != 1 || got[0] != "laptop__fs__read" {
		t.Fatalf("fs group sees %v", got)
	}
	// Approvals narrow it further: a node that waits for approval shows nothing.
	if err := a.trust.SetLevel(t.Context(), "laptop", trust.Approve); err != nil {
		t.Fatal(err)
	}
	if got := tools(); len(got) != 0 {
		t.Fatalf("unapproved node still visible: %v", got)
	}
	wantError(t, a.do("PUT", "/api/v1/keys/"+id+"/groups", `{"groups":["g_nope"]}`), 400, "")
	wantError(t, a.do("PUT", "/api/v1/keys/k_1/groups", `{"groups":[]}`), 400, "") // a node key
	wantError(t, a.do("GET", "/api/v1/keys/k_nope/tools", ""), 404, "")
}

func TestGroupsCRUD(t *testing.T) {
	a := newAccessEnv(t)
	w := a.do("POST", "/api/v1/groups", `{"name":"Read only","description":"","grants":[{"node":"laptop","mcp":"*","tool":"*"}]}`)
	wantStatus(t, w, 201)
	var g acl.Group
	decodeBody(t, w, &g)
	wantError(t, a.do("POST", "/api/v1/groups", `{"name":"read ONLY","grants":[]}`), 409, "")
	wantError(t, a.do("POST", "/api/v1/groups", `{"name":"bad","grants":[{"node":"*","mcp":"fs","tool":"*"}]}`), 400, "")
	wantStatus(t, a.do("PUT", "/api/v1/groups/"+g.ID, `{"name":"Renamed","description":"d","grants":[]}`), 200)
	wantError(t, a.do("PUT", "/api/v1/groups/g_nope", `{"name":"x","grants":[]}`), 404, "")
	w = a.do("GET", "/api/v1/groups", "")
	if !strings.Contains(w.Body.String(), `"Renamed"`) || !strings.Contains(w.Body.String(), `"Everything"`) {
		t.Errorf("list = %s", w.Body.String())
	}
	wantStatus(t, a.do("DELETE", "/api/v1/groups/"+g.ID, ""), 204)
	wantError(t, a.do("DELETE", "/api/v1/groups/"+g.ID, ""), 404, "")
	wantError(t, a.do("GET", "/api/v1/groups", "", anon), 401, "")
}

func TestTrustAndApprovals(t *testing.T) {
	a := newAccessEnv(t)
	w := a.do("PUT", "/api/v1/nodes/laptop/trust", `{"level":"approve"}`)
	wantStatus(t, w, 200)
	var tv trustView
	decodeBody(t, w, &tv)
	if tv.Level != trust.Approve || !tv.Explicit || tv.Approved {
		t.Fatalf("after approve level: %+v", tv)
	}
	w = a.do("GET", "/api/v1/approvals", "")
	if !strings.Contains(w.Body.String(), `"state":"node_pending"`) {
		t.Fatalf("approvals = %s", w.Body.String())
	}
	w = a.do("GET", "/api/v1/nodes", "")
	if !strings.Contains(w.Body.String(), `"trust":{"level":"approve","explicit":true,"approved":false},"pending_tools":1`) {
		t.Errorf("nodes = %s", w.Body.String())
	}
	if w = a.do("GET", "/api/v1/tools", ""); !strings.Contains(w.Body.String(), `"approval":"node_pending"`) {
		t.Errorf("tools = %s", w.Body.String())
	}
	var pend []map[string]any
	decodeBody(t, a.do("GET", "/api/v1/approvals", ""), &pend)
	if len(pend) != 1 || pend[0]["hash"] == "" || pend[0]["hash"] == nil {
		t.Fatalf("pending = %v", pend)
	}
	if d, ok := pend[0]["definition"].(map[string]any); !ok || d["name"] != pend[0]["tool"] {
		t.Errorf("pending definition = %v", pend[0]["definition"])
	}
	one := func(hash string) string {
		return fmt.Sprintf(`{"tools":[{"node":"laptop","mcp":%q,"tool":%q,"hash":%q}]}`, pend[0]["mcp"], pend[0]["tool"], hash)
	}
	// A hash the admin did not see is refused with the current one.
	w = a.do("POST", "/api/v1/approvals", one("deadbeef"))
	wantStatus(t, w, 409)
	var conflict struct {
		Error string `json:"error"`
		Stale []struct{ Node, MCP, Tool, Hash string }
	}
	decodeBody(t, w, &conflict)
	if conflict.Error == "" || len(conflict.Stale) != 1 || conflict.Stale[0].Hash != pend[0]["hash"] || conflict.Stale[0].Node != "laptop" {
		t.Errorf("409 body = %s", w.Body.String())
	}
	if a.trust.Policy("laptop").Approved {
		t.Error("stale approval approved the node")
	}
	w = a.do("POST", "/api/v1/approvals", one(pend[0]["hash"].(string)))
	wantStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"approved":1}` {
		t.Errorf("approve = %s", w.Body.String())
	}
	if w = a.do("GET", "/api/v1/tools", ""); !strings.Contains(w.Body.String(), `"approval":"approved"`) {
		t.Errorf("tools after approval = %s", w.Body.String())
	}
	wantError(t, a.do("POST", "/api/v1/approvals", `{"tools":[]}`), 400, "")
	wantError(t, a.do("POST", "/api/v1/approvals", `{"tools":[{"node":"laptop","mcp":"fs"}]}`), 400, "")
	wantError(t, a.do("POST", "/api/v1/approvals", `{"node":"laptop"}`), 400, "")
	wantError(t, a.do("PUT", "/api/v1/nodes/laptop/trust", `{"level":"maybe"}`), 400, "")
	wantStatus(t, a.do("PUT", "/api/v1/nodes/laptop/trust", `{"level":null}`), 200)
	if p := a.trust.Policy("laptop"); p.Explicit {
		t.Errorf("level not cleared: %+v", p)
	}
	wantStatus(t, a.do("POST", "/api/v1/nodes/laptop/approve", ""), 200)
	w = a.do("POST", "/api/v1/nodes/laptop/approve", `{"tools":[{"mcp":"fs","tool":"gone","hash":"x"}]}`)
	wantStatus(t, w, 409)
	if !strings.Contains(w.Body.String(), `"stale":[{"node":"laptop","mcp":"fs","tool":"gone","hash":""}]`) {
		t.Errorf("node approve 409 = %s", w.Body.String())
	}
	wantError(t, a.do("POST", "/api/v1/nodes/laptop/approve", `{"tools":[{"mcp":"fs"}]}`), 400, "")

	w = a.do("PUT", "/api/v1/settings", `{"trust_default":"review"}`)
	wantStatus(t, w, 200)
	if !strings.Contains(w.Body.String(), `"trust_default":"review"`) || !strings.Contains(w.Body.String(), `"issuer":"https://hub"`) {
		t.Errorf("settings = %s", w.Body.String())
	}
	wantError(t, a.do("PUT", "/api/v1/settings", `{"trust_default":""}`), 400, "")
}

func TestAdminLogRecordsActions(t *testing.T) {
	a := newAccessEnv(t)
	wantStatus(t, a.do("POST", "/api/v1/session", `{"username":"alice","password":"wrong"}`, anon), 401)
	wantStatus(t, a.do("POST", "/api/v1/session", `{"username":"alice","password":"correct horse battery"}`, anon), 200)
	k := a.createClient(t, `{"kind":"client","name":"phone"}`)
	wantStatus(t, a.do("DELETE", "/api/v1/keys/"+k["id"].(string), ""), 200)
	wantStatus(t, a.do("PUT", "/api/v1/nodes/laptop/trust", `{"level":"review"}`), 200)

	w := a.do("GET", "/api/v1/admin-log?limit=10", "")
	wantStatus(t, w, 200)
	var out struct {
		Entries []adminlog.Entry `json:"entries"`
	}
	decodeBody(t, w, &out)
	var actions []string
	for _, e := range out.Entries {
		actions = append(actions, e.Actor+":"+e.Action)
	}
	want := "alice:trust.level alice:key.revoke alice:key.create alice:sign_in :sign_in_failed"
	if got := strings.Join(actions, " "); got != want {
		t.Errorf("log = %s\nwant  %s", got, want)
	}
	wantError(t, a.do("GET", "/api/v1/admin-log?limit=0", ""), 400, "")
	wantError(t, a.do("GET", "/api/v1/admin-log", "", anon), 401, "")
}

func TestKeyViewOAuthClient(t *testing.T) {
	a := newAccessEnv(t)
	a.keys.mu.Lock()
	a.keys.keys = append(a.keys.keys, a.keys.keys[0])
	a.keys.keys[1].ID, a.keys.keys[1].Kind, a.keys.keys[1].Name, a.keys.keys[1].OAuthClient = "k_oa", "client", "claude", "oc_1"
	a.keys.mu.Unlock()
	w := a.do("GET", "/api/v1/keys", "")
	if !strings.Contains(w.Body.String(), `"oauth_client":{"id":"oc_1","name":"Claude"}`) || !strings.Contains(w.Body.String(), `"groups":[]`) {
		t.Errorf("keys = %s", w.Body.String())
	}
	if w = a.do("GET", "/api/v1/info", ""); !strings.Contains(w.Body.String(), `"oauth":true`) {
		t.Errorf("info = %s", w.Body.String())
	}
}
