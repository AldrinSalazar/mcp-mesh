package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/activity"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/nodes"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

const (
	goodCookie = "sid=ok"
	goodCSRF   = "tok"
)

type fakeSessions struct {
	loginErr error
	logouts  int
	user     string
	ended    atomic.Bool // the session was signed out elsewhere
}

func (f *fakeSessions) Login(w http.ResponseWriter, r *http.Request, u, p string) (auth.Session, error) {
	if f.loginErr != nil {
		return auth.Session{}, f.loginErr
	}
	if u != "alice" || p != "correct horse battery" {
		return auth.Session{}, auth.ErrBadCredentials
	}
	http.SetCookie(w, &http.Cookie{Name: "sid", Value: "ok"})
	return auth.Session{Username: u, CSRF: goodCSRF, Expires: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}, nil
}

func (f *fakeSessions) Current(r *http.Request) (auth.Session, bool) {
	c, err := r.Cookie("sid")
	if err != nil || c.Value != "ok" || f.ended.Load() {
		return auth.Session{}, false
	}
	user := f.user
	if user == "" {
		user = "alice"
	}
	return auth.Session{Username: user, CSRF: goodCSRF, Expires: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}, true
}

func (f *fakeSessions) Logout(w http.ResponseWriter, r *http.Request) { f.logouts++ }

func (f *fakeSessions) CheckCSRF(r *http.Request, s auth.Session) bool {
	return r.Header.Get("X-CSRF-Token") == s.CSRF
}

type fakeAdmins struct {
	mu      sync.Mutex
	users   []auth.User
	passes  map[string]string
	setCall []string
}

func newFakeAdmins() *fakeAdmins {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &fakeAdmins{
		users: []auth.User{
			{ID: "u_1", Username: "alice", Created: t0, PasswordChanged: t0, LastLogin: t0.Add(time.Hour)},
			{ID: "u_2", Username: "bob", Created: t0, PasswordChanged: t0},
		},
		passes: map[string]string{"alice": "correct horse battery", "bob": "bobs password 123"},
	}
}

func (f *fakeAdmins) List(ctx context.Context) ([]auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]auth.User(nil), f.users...), nil
}

func (f *fakeAdmins) Create(ctx context.Context, name, pw string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(pw) < auth.MinPasswordLen {
		return auth.User{}, auth.ErrWeakPassword
	}
	if strings.ContainsAny(name, " /") || name == "" {
		return auth.User{}, errors.New("invalid username")
	}
	for _, u := range f.users {
		if u.Username == name {
			return auth.User{}, auth.ErrUserExists
		}
	}
	u := auth.User{ID: "u_n", Username: name, Created: time.Now(), PasswordChanged: time.Now()}
	f.users = append(f.users, u)
	f.passes[name] = pw
	return u, nil
}

func (f *fakeAdmins) SetPassword(ctx context.Context, name, pw string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(pw) < auth.MinPasswordLen {
		return auth.ErrWeakPassword
	}
	if _, ok := f.passes[name]; !ok {
		return auth.ErrNoUser
	}
	f.passes[name] = pw
	f.setCall = append(f.setCall, name)
	return nil
}

func (f *fakeAdmins) Delete(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, u := range f.users {
		if u.Username == name {
			if len(f.users) == 1 {
				return auth.ErrLastAdmin
			}
			f.users = append(f.users[:i], f.users[i+1:]...)
			return nil
		}
	}
	return auth.ErrNoUser
}

func (f *fakeAdmins) Verify(ctx context.Context, name, pw string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.passes[name]; !ok || p != pw {
		return auth.User{}, auth.ErrBadCredentials
	}
	return auth.User{Username: name}, nil
}

type fakeKeys struct {
	mu   sync.Mutex
	keys []auth.Key
	// revoked records the argument of each Revoke call.
	revoked []string
}

func (f *fakeKeys) List() ([]auth.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]auth.Key(nil), f.keys...), nil
}

func (f *fakeKeys) Get(id string) (auth.Key, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.keys {
		if k.ID == id {
			return k, true
		}
	}
	return auth.Key{}, false
}

func (f *fakeKeys) Create(kind auth.Kind, name string) (string, auth.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if name == "" {
		return "", auth.Key{}, errors.New("name is empty")
	}
	k := auth.Key{ID: fmt.Sprintf("k_%08d", len(f.keys)+10), Kind: kind, Name: name, Hash: "secrethash", Created: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}
	f.keys = append(f.keys, k)
	return "mm_" + string(kind) + "_secret", k, nil
}

func (f *fakeKeys) Revoke(idOrName string) (auth.Key, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked = append(f.revoked, idOrName)
	for i, k := range f.keys {
		if k.ID == idOrName {
			f.keys = append(f.keys[:i], f.keys[i+1:]...)
			return k, nil
		}
	}
	return auth.Key{}, errors.New("no such key")
}

type fakeAudit struct {
	got     activity.AuditQuery
	entries []activity.Entry

	mu     sync.Mutex // the events feed counts concurrently
	since  time.Time
	counts activity.Counts
}

func (f *fakeAudit) Query(ctx context.Context, q activity.AuditQuery) ([]activity.Entry, error) {
	f.got = q
	return f.entries, nil
}

func (f *fakeAudit) Count(ctx context.Context, since time.Time) (activity.Counts, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.since = since
	return f.counts, nil
}

type fakeNodes struct {
	mu    sync.Mutex
	nodes []nodes.NodeInfo
}

func (f *fakeNodes) Nodes() []nodes.NodeInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]nodes.NodeInfo(nil), f.nodes...)
}

func (f *fakeNodes) set(n []nodes.NodeInfo) {
	f.mu.Lock()
	f.nodes = n
	f.mu.Unlock()
}

type env struct {
	srv      *Server
	h        http.Handler
	sessions *fakeSessions
	admins   *fakeAdmins
	keys     *fakeKeys
	audit    *fakeAudit
	nodes    *fakeNodes
	catalog  *catalog.Catalog
	recent   *activity.Log
}

func newEnv(t *testing.T, authOn bool) *env {
	t.Helper()
	e := &env{
		audit: &fakeAudit{}, nodes: &fakeNodes{}, catalog: catalog.New(), recent: activity.New(10),
	}
	e.nodes.set([]nodes.NodeInfo{{
		Name: "laptop", NodeVersion: "0.2.0", OS: "linux", RemoteAddr: "10.0.0.5:1234", KeyID: "k_1",
		ConnectedAt: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Online: true,
		MCPs: []nodes.MCPInfo{{Name: "fs", Transport: proto.Transport("stdio"), State: nodes.MCPReady, Tools: 2, Server: "fs 1.0"}},
	}, {
		Name: "old", Online: false, DisconnectedAt: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC),
	}})
	e.catalog.SetMCP("laptop", "fs", []*mcp.Tool{{Name: "read", Description: "Read a file", InputSchema: map[string]any{"type": "object"}}}, nil)
	e.recent.Add(activity.Call{Time: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), ClientID: "k_c", Client: "claude", Tool: "laptop_fs_read", Node: "laptop", MCP: "fs", Duration: 1500 * time.Millisecond, Outcome: "ok"})
	opts := Options{
		Nodes: e.nodes, Catalog: e.catalog, Recent: e.recent, Audit: e.audit,
		Version: "v-test", NodeURL: "wss://hub/node", ClientURL: "https://hub/mcp", DiscoveryName: "hub-1",
		Poll: 5 * time.Millisecond,
	}
	if authOn {
		e.sessions = &fakeSessions{}
		e.admins = newFakeAdmins()
		e.keys = &fakeKeys{keys: []auth.Key{{ID: "k_1", Kind: auth.KindNode, Name: "laptop", Hash: "deadbeef", Created: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), LastUsed: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), LastIP: "1.2.3.4"}}}
		opts.Sessions, opts.Admins, opts.Keys = e.sessions, e.admins, e.keys
	}
	e.srv = New(opts)
	e.h = e.srv.Handler()
	return e
}

type reqOpt func(*http.Request)

func anon(r *http.Request) { r.Header.Del("Cookie"); r.Header.Del("X-CSRF-Token") }
func noCSRF(r *http.Request) {
	r.Header.Del("X-CSRF-Token")
}
func header(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

// do sends a signed-in request (cookie and CSRF token) unless opts say otherwise.
func (e *env) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Header.Set("Cookie", goodCookie)
	r.Header.Set("X-CSRF-Token", goodCSRF)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	return w
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body %s", w.Code, code, w.Body.String())
	}
}

func wantError(t *testing.T, w *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	wantStatus(t, w, code)
	var b map[string]string
	decodeBody(t, w, &b)
	if msg != "" && b["error"] != msg {
		t.Fatalf("error = %q, want %q", b["error"], msg)
	}
	if b["error"] == "" {
		t.Fatalf("no error message in %s", w.Body.String())
	}
}

func TestInfoOpen(t *testing.T) {
	e := newEnv(t, true)
	w := e.do("GET", "/api/v1/info", "", anon)
	wantStatus(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
	var got map[string]any
	decodeBody(t, w, &got)
	want := map[string]any{"version": "v-test", "auth": true, "node_url": "wss://hub/node", "client_url": "https://hub/mcp", "discovery_name": "hub-1"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	off := newEnv(t, false)
	decodeBody(t, off.do("GET", "/api/v1/info", ""), &got)
	if got["auth"] != false {
		t.Errorf("auth = %v with auth off", got["auth"])
	}
}

func TestProtectedNeedSession(t *testing.T) {
	e := newEnv(t, true)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/session", ""},
		{"GET", "/api/v1/nodes", ""},
		{"GET", "/api/v1/tools", ""},
		{"GET", "/api/v1/calls", ""},
		{"GET", "/api/v1/audit", ""},
		{"GET", "/api/v1/events", ""},
		{"GET", "/api/v1/keys", ""},
		{"POST", "/api/v1/keys", `{"kind":"node","name":"x"}`},
		{"DELETE", "/api/v1/keys/k_1", ""},
		{"GET", "/api/v1/admins", ""},
		{"POST", "/api/v1/admins", `{"username":"c","password":"long enough password"}`},
		{"PUT", "/api/v1/admins/bob/password", `{"password":"long enough password"}`},
		{"DELETE", "/api/v1/admins/bob", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			wantError(t, e.do(tc.method, tc.path, tc.body, anon), 401, "")
		})
	}
	if len(e.keys.revoked) != 0 {
		t.Error("key revoked without a session")
	}
}

func TestAuthOff(t *testing.T) {
	e := newEnv(t, false)
	for _, p := range []string{"nodes", "tools", "calls", "audit", "stats", "events"} {
		w := e.do("GET", "/api/v1/"+p, "", anon, func(r *http.Request) {
			// events streams forever; a cancelled context ends it at once.
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			*r = *r.WithContext(ctx)
		})
		wantStatus(t, w, 200)
	}
	w := e.do("GET", "/api/v1/session", "", anon)
	wantStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"auth":false}` {
		t.Errorf("session = %s", w.Body.String())
	}
	wantStatus(t, e.do("DELETE", "/api/v1/session", "", anon), 204)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/v1/keys", ""},
		{"POST", "/api/v1/keys", `{"kind":"node","name":"x"}`},
		{"DELETE", "/api/v1/keys/k_1", ""},
		{"GET", "/api/v1/admins", ""},
		{"POST", "/api/v1/admins", `{"username":"c","password":"long enough password"}`},
		{"PUT", "/api/v1/admins/bob/password", `{"password":"x"}`},
		{"DELETE", "/api/v1/admins/bob", ""},
		{"POST", "/api/v1/session", `{"username":"a","password":"b"}`},
	} {
		wantError(t, e.do(tc.method, tc.path, tc.body, anon), 404, "")
	}
}

func TestLogin(t *testing.T) {
	e := newEnv(t, true)
	w := e.do("POST", "/api/v1/session", `{"username":"alice","password":"correct horse battery"}`, anon)
	wantStatus(t, w, 200)
	var s map[string]any
	decodeBody(t, w, &s)
	if s["username"] != "alice" || s["csrf"] != goodCSRF || s["expires"] != "2030-01-02T03:04:05Z" {
		t.Errorf("session = %v", s)
	}
	if !strings.Contains(w.Header().Get("Set-Cookie"), "sid=ok") {
		t.Errorf("no cookie: %v", w.Header())
	}

	wantError(t, e.do("POST", "/api/v1/session", `{"username":"alice","password":"nope"}`, anon), 401, "wrong username or password")

	e.sessions.loginErr = &auth.RateLimitError{RetryAfter: 90*time.Second + time.Millisecond}
	w = e.do("POST", "/api/v1/session", `{"username":"alice","password":"x"}`, anon)
	wantError(t, w, 429, "")
	if got := w.Header().Get("Retry-After"); got != "91" {
		t.Errorf("Retry-After = %q, want 91", got)
	}

	// An existing session doesn't need a CSRF token to sign in again.
	e.sessions.loginErr = nil
	wantStatus(t, e.do("POST", "/api/v1/session", `{"username":"alice","password":"correct horse battery"}`, noCSRF), 200)
}

func TestSessionGetAndLogout(t *testing.T) {
	e := newEnv(t, true)
	w := e.do("GET", "/api/v1/session", "")
	wantStatus(t, w, 200)
	var s map[string]any
	decodeBody(t, w, &s)
	if s["username"] != "alice" || s["csrf"] != goodCSRF {
		t.Errorf("session = %v", s)
	}
	wantStatus(t, e.do("DELETE", "/api/v1/session", ""), 204)
	if e.sessions.logouts != 1 {
		t.Errorf("logouts = %d", e.sessions.logouts)
	}
	// Signing out when already signed out is not an error.
	wantStatus(t, e.do("DELETE", "/api/v1/session", "", anon), 204)
	// But with a session the CSRF token is required.
	wantError(t, e.do("DELETE", "/api/v1/session", "", noCSRF), 403, "missing or wrong CSRF token")
}

func TestCSRF(t *testing.T) {
	e := newEnv(t, true)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/keys", `{"kind":"node","name":"x"}`},
		{"DELETE", "/api/v1/keys/k_1", ""},
		{"POST", "/api/v1/admins", `{"username":"c","password":"long enough password"}`},
		{"PUT", "/api/v1/admins/bob/password", `{"password":"long enough password"}`},
		{"DELETE", "/api/v1/admins/bob", ""},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			wantError(t, e.do(tc.method, tc.path, tc.body, noCSRF), 403, "missing or wrong CSRF token")
			wantError(t, e.do(tc.method, tc.path, tc.body, header("X-CSRF-Token", "wrong")), 403, "missing or wrong CSRF token")
		})
	}
	if len(e.keys.keys) != 1 || len(e.admins.users) != 2 {
		t.Error("state changed without a valid CSRF token")
	}
	// Reads need no token.
	wantStatus(t, e.do("GET", "/api/v1/keys", "", noCSRF), 200)
}

func TestOriginCheck(t *testing.T) {
	e := newEnv(t, true)
	body := `{"kind":"client","name":"x"}`
	wantError(t, e.do("POST", "/api/v1/keys", body, header("Origin", "https://evil.example")), 403, "cross-origin request rejected")
	wantError(t, e.do("POST", "/api/v1/session", `{"username":"alice","password":"correct horse battery"}`, anon, header("Origin", "https://evil.example")), 403, "")
	wantStatus(t, e.do("POST", "/api/v1/keys", body, header("Origin", "http://example.com")), 201) // httptest Host is example.com
	wantStatus(t, e.do("POST", "/api/v1/keys", body, header("Origin", "http://EXAMPLE.com")), 201)
}

func TestBodyRules(t *testing.T) {
	e := newEnv(t, true)
	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"node","name":"x"}`, header("Content-Type", "text/plain")), 415, "")
	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"node","name":"x"}`, header("Content-Type", "")), 415, "")
	wantStatus(t, e.do("POST", "/api/v1/keys", `{"kind":"node","name":"y"}`, header("Content-Type", "application/json; charset=utf-8")), 201)
	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"node","name":"x","extra":1}`), 400, "")
	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"node","name":"x"} {}`), 400, "")
	wantError(t, e.do("POST", "/api/v1/keys", `{not json`), 400, "")
	big := `{"kind":"node","name":"` + strings.Repeat("a", 70<<10) + `"}`
	wantError(t, e.do("POST", "/api/v1/keys", big), 413, "")
	wantError(t, e.do("POST", "/api/v1/session", `{"username":"a","password":"b","x":1}`, anon), 400, "")
}

func TestRouting(t *testing.T) {
	e := newEnv(t, true)
	wantError(t, e.do("GET", "/api/v1/nope", ""), 404, "")
	wantError(t, e.do("GET", "/api/v1/nope", "", anon), 404, "") // no 401 leak either way
	wantError(t, e.do("GET", "/api/v1/", ""), 404, "")
	w := e.do("POST", "/api/v1/nodes", `{}`)
	wantError(t, w, 405, "")
	if w.Header().Get("Allow") != "GET" {
		t.Errorf("Allow = %q", w.Header().Get("Allow"))
	}
	wantError(t, e.do("PATCH", "/api/v1/keys", `{}`), 405, "")
	wantError(t, e.do("GET", "/api/v1/keys/k_1", ""), 405, "")
	if ct := e.do("GET", "/api/v1/nodes", "").Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestViews(t *testing.T) {
	e := newEnv(t, true)

	var ns []map[string]any
	decodeBody(t, e.do("GET", "/api/v1/nodes", ""), &ns)
	if len(ns) != 2 {
		t.Fatalf("nodes = %v", ns)
	}
	n := ns[0]
	for k, want := range map[string]any{"name": "laptop", "node_version": "0.2.0", "os": "linux", "remote_addr": "10.0.0.5:1234", "key_id": "k_1", "online": true, "connected_at": "2026-03-01T00:00:00Z"} {
		if n[k] != want {
			t.Errorf("node %s = %v, want %v", k, n[k], want)
		}
	}
	if _, ok := n["disconnected_at"]; ok {
		t.Error("online node has disconnected_at")
	}
	m := n["mcps"].([]any)[0].(map[string]any)
	for _, k := range []string{"name", "transport", "sharing", "start", "state", "error", "tools", "in_flight", "queued", "server", "problems"} {
		if _, ok := m[k]; !ok {
			t.Errorf("mcp missing %q: %v", k, m)
		}
	}
	if pr, ok := m["problems"].([]any); !ok || len(pr) != 0 {
		t.Errorf("problems = %#v, want []", m["problems"])
	}
	if m["state"] != "ready" || m["tools"] != float64(2) {
		t.Errorf("mcp = %v", m)
	}
	if ns[1]["disconnected_at"] != "2026-03-02T00:00:00Z" || ns[1]["mcps"] == nil {
		t.Errorf("offline node = %v", ns[1])
	}

	var ts []map[string]any
	decodeBody(t, e.do("GET", "/api/v1/tools", ""), &ts)
	if len(ts) != 1 || ts[0]["node"] != "laptop" || ts[0]["mcp"] != "fs" || ts[0]["original_name"] != "read" ||
		ts[0]["description"] != "Read a file" || ts[0]["name"] == "" || ts[0]["input_schema"] == nil {
		t.Errorf("tools = %v", ts)
	}

	e.recent.Add(activity.Call{Time: time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), Tool: "later", Outcome: "error", Error: "boom"})
	var cs []map[string]any
	decodeBody(t, e.do("GET", "/api/v1/calls", ""), &cs)
	if len(cs) != 2 || cs[0]["tool"] != "later" || cs[0]["error"] != "boom" {
		t.Fatalf("calls = %v", cs)
	}
	c := cs[1]
	if c["client_id"] != "k_c" || c["client"] != "claude" || c["duration_ms"] != float64(1500) || c["outcome"] != "ok" || c["time"] != "2026-03-03T00:00:00Z" {
		t.Errorf("call = %v", c)
	}

	// Empty lists are [], not null.
	empty := New(Options{})
	w := httptest.NewRecorder()
	empty.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/nodes", nil))
	if strings.TrimSpace(w.Body.String()) != "[]" {
		t.Errorf("empty nodes = %s", w.Body.String())
	}
}

func TestAudit(t *testing.T) {
	e := newEnv(t, true)
	call := activity.Call{Time: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), ClientID: "k_c", Tool: "t", Duration: 2 * time.Millisecond, Outcome: "ok"}
	e.audit.entries = []activity.Entry{{ID: 9, Call: call}, {ID: 8, Call: call}}

	w := e.do("GET", "/api/v1/audit?since=2026-03-01T00:00:00Z&until=2026-03-05T00:00:00%2B01:00&client_id=k_c&node=n&mcp=m&tool=t&outcome=error&before_id=50&limit=2", "")
	wantStatus(t, w, 200)
	q := e.audit.got
	if !q.Since.Equal(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)) || !q.Until.Equal(time.Date(2026, 3, 4, 23, 0, 0, 0, time.UTC)) ||
		q.ClientID != "k_c" || q.Node != "n" || q.MCP != "m" || q.Tool != "t" || q.Outcome != "error" || q.BeforeID != 50 || q.Limit != 2 {
		t.Errorf("query = %+v", q)
	}
	var out struct {
		Entries []map[string]any `json:"entries"`
		Next    int64            `json:"next_before_id"`
	}
	decodeBody(t, w, &out)
	if len(out.Entries) != 2 || out.Next != 8 {
		t.Errorf("out = %+v", out)
	}
	if out.Entries[0]["id"] != float64(9) || out.Entries[0]["duration_ms"] != float64(2) || out.Entries[0]["client_id"] != "k_c" {
		t.Errorf("entry = %v", out.Entries[0])
	}

	// Fewer entries than the limit: no further page.
	w = e.do("GET", "/api/v1/audit?limit=5", "")
	decodeBody(t, w, &out)
	if out.Next != 0 {
		t.Errorf("next = %d", out.Next)
	}
	// Default limit is 100.
	e.audit.entries = nil
	w = e.do("GET", "/api/v1/audit", "")
	if strings.Contains(w.Body.String(), "null") {
		t.Errorf("entries should be []: %s", w.Body.String())
	}

	for _, bad := range []string{"since=yesterday", "until=1", "before_id=-1", "before_id=x", "limit=0", "limit=x"} {
		wantError(t, e.do("GET", "/api/v1/audit?"+bad, ""), 400, "")
	}

	e.srv.opts.Audit = nil
	wantError(t, e.do("GET", "/api/v1/audit", ""), 404, "")
}

func TestStats(t *testing.T) {
	e := newEnv(t, true)
	e.audit.counts = activity.Counts{Calls: 7, Errors: 2}
	before := time.Now()
	w := e.do("GET", "/api/v1/stats", "")
	wantStatus(t, w, 200)
	if got := strings.TrimSpace(w.Body.String()); got != `{"calls_last_hour":7,"errors_last_hour":2}` {
		t.Errorf("body = %s", got)
	}
	e.audit.mu.Lock()
	since := e.audit.since
	e.audit.mu.Unlock()
	if d := before.Sub(since); d < 59*time.Minute || d > 61*time.Minute {
		t.Errorf("since is %v before now, want an hour", d)
	}

	// Without an audit log the in-memory ring is counted.
	e.srv.opts.Audit = nil
	now := time.Now()
	e.recent.Add(activity.Call{Time: now.Add(-30 * time.Minute), Outcome: "ok"})
	e.recent.Add(activity.Call{Time: now.Add(-time.Minute), Outcome: "tool_error"})
	w = e.do("GET", "/api/v1/stats", "")
	if got := strings.TrimSpace(w.Body.String()); got != `{"calls_last_hour":2,"errors_last_hour":1}` {
		t.Errorf("ring body = %s", got)
	}

	wantError(t, e.do("GET", "/api/v1/stats", "", anon), 401, "")
}

func TestKeys(t *testing.T) {
	e := newEnv(t, true)
	w := e.do("GET", "/api/v1/keys", "")
	wantStatus(t, w, 200)
	if strings.Contains(w.Body.String(), "deadbeef") || strings.Contains(w.Body.String(), "hash") {
		t.Errorf("hash leaked: %s", w.Body.String())
	}
	var ks []map[string]any
	decodeBody(t, w, &ks)
	if ks[0]["id"] != "k_1" || ks[0]["kind"] != "node" || ks[0]["name"] != "laptop" || ks[0]["last_ip"] != "1.2.3.4" || ks[0]["last_used"] != "2026-01-02T00:00:00Z" || ks[0]["created"] == nil {
		t.Errorf("key = %v", ks[0])
	}

	w = e.do("POST", "/api/v1/keys", `{"kind":"node","name":"desk"}`)
	wantStatus(t, w, 201)
	var created struct {
		Key    map[string]any    `json:"key"`
		Secret string            `json:"secret"`
		Setup  map[string]string `json:"setup"`
	}
	decodeBody(t, w, &created)
	if created.Secret != "mm_node_secret" || created.Key["name"] != "desk" || created.Key["kind"] != "node" {
		t.Errorf("created = %+v", created)
	}
	if created.Setup["hub_url"] != "wss://hub/node" || created.Setup["command"] != "mcp-mesh-node key set mm_node_secret" ||
		created.Setup["init_command"] != "mcp-mesh-node init --hub wss://hub/node --name desk --key mm_node_secret" || len(created.Setup) != 3 {
		t.Errorf("node setup = %v", created.Setup)
	}
	if strings.Contains(w.Body.String(), "secrethash") {
		t.Error("hash leaked on create")
	}

	w = e.do("POST", "/api/v1/keys", `{"kind":"client","name":"claude"}`)
	wantStatus(t, w, 201)
	decodeBody(t, w, &created)
	wantSetup := map[string]string{
		"mcp_url":     "https://hub/mcp",
		"header":      "Authorization: Bearer mm_client_secret",
		"claude_code": `claude mcp add --transport http mcp-mesh https://hub/mcp --header "Authorization: Bearer mm_client_secret"`,
	}
	for k, v := range wantSetup {
		if created.Setup[k] != v {
			t.Errorf("client setup %s = %q, want %q", k, created.Setup[k], v)
		}
	}

	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"admin","name":"x"}`), 400, "")
	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"","name":"x"}`), 400, "")
	wantError(t, e.do("POST", "/api/v1/keys", `{"kind":"node","name":""}`), 400, "name is empty")
}

func TestKeysDelete(t *testing.T) {
	e := newEnv(t, true)
	// By name: the key exists but the ID doesn't match, so nothing is revoked.
	wantError(t, e.do("DELETE", "/api/v1/keys/laptop", ""), 404, "")
	wantError(t, e.do("DELETE", "/api/v1/keys/k_missing", ""), 404, "")
	if len(e.keys.revoked) != 0 {
		t.Fatalf("revoked %v", e.keys.revoked)
	}
	w := e.do("DELETE", "/api/v1/keys/k_1", "")
	wantStatus(t, w, 200)
	var out struct{ Key map[string]any }
	decodeBody(t, w, &out)
	if out.Key["id"] != "k_1" {
		t.Errorf("out = %v", out)
	}
	if len(e.keys.keys) != 0 {
		t.Error("key still present")
	}
}

func TestAdmins(t *testing.T) {
	e := newEnv(t, true)
	w := e.do("GET", "/api/v1/admins", "")
	wantStatus(t, w, 200)
	var us []map[string]any
	decodeBody(t, w, &us)
	if len(us) != 2 || us[0]["username"] != "alice" || us[0]["last_login"] != "2026-01-01T01:00:00Z" || us[0]["created"] == nil || us[0]["password_changed"] == nil {
		t.Errorf("admins = %v", us)
	}
	if _, ok := us[1]["last_login"]; ok {
		t.Error("never-signed-in admin has last_login")
	}
	if strings.Contains(w.Body.String(), "u_1") {
		t.Error("internal ID exposed")
	}

	w = e.do("POST", "/api/v1/admins", `{"username":"carol","password":"a long enough password"}`)
	wantStatus(t, w, 201)
	var u map[string]any
	decodeBody(t, w, &u)
	if u["username"] != "carol" {
		t.Errorf("created = %v", u)
	}
	wantError(t, e.do("POST", "/api/v1/admins", `{"username":"carol","password":"a long enough password"}`), 409, auth.ErrUserExists.Error())
	wantError(t, e.do("POST", "/api/v1/admins", `{"username":"dave","password":"short"}`), 400, auth.ErrWeakPassword.Error())
	wantError(t, e.do("POST", "/api/v1/admins", `{"username":"bad name","password":"a long enough password"}`), 400, "invalid username")
}

func TestAdminPassword(t *testing.T) {
	e := newEnv(t, true)
	const path = "/api/v1/admins/alice/password"
	// Own password: current_password required and checked.
	wantError(t, e.do("PUT", path, `{"password":"brand new password"}`), 403, "")
	wantError(t, e.do("PUT", path, `{"password":"brand new password","current_password":"wrong"}`), 403, "")
	if len(e.admins.setCall) != 0 {
		t.Fatal("password changed without proof")
	}
	wantError(t, e.do("PUT", path, `{"password":"short","current_password":"correct horse battery"}`), 400, auth.ErrWeakPassword.Error())
	w := e.do("PUT", path, `{"password":"brand new password","current_password":"correct horse battery"}`)
	wantStatus(t, w, 204)
	if w.Body.Len() != 0 || e.admins.passes["alice"] != "brand new password" {
		t.Errorf("body %q pass %q", w.Body.String(), e.admins.passes["alice"])
	}
	// Someone else's: no current password needed.
	wantStatus(t, e.do("PUT", "/api/v1/admins/bob/password", `{"password":"bob has a new password"}`), 204)
	if e.admins.passes["bob"] != "bob has a new password" {
		t.Error("bob's password not changed")
	}
	wantError(t, e.do("PUT", "/api/v1/admins/nobody/password", `{"password":"some long password"}`), 404, "")
	wantError(t, e.do("PUT", path, `{"password":"x","extra":1}`), 400, "")
}

func TestAdminDelete(t *testing.T) {
	e := newEnv(t, true)
	wantStatus(t, e.do("DELETE", "/api/v1/admins/bob", ""), 204)
	wantError(t, e.do("DELETE", "/api/v1/admins/bob", ""), 404, auth.ErrNoUser.Error())
	wantError(t, e.do("DELETE", "/api/v1/admins/alice", ""), 409, auth.ErrLastAdmin.Error())
}

// --- events ---

type sseEvent struct {
	typ  string // "ping" for comment lines
	data string
}

// stream opens /events on a real server and delivers parsed events.
func (e *env) stream(t *testing.T) (<-chan sseEvent, context.CancelFunc) {
	t.Helper()
	ts := httptest.NewServer(e.h)
	t.Cleanup(ts.Close)
	ctx, cancel := context.WithCancel(t.Context())
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/v1/events", nil)
	req.Header.Set("Cookie", goodCookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("status %d type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	out := make(chan sseEvent, 64)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		var cur sseEvent
		buf := make([]byte, 0, 1<<16)
		tmp := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(tmp)
			buf = append(buf, tmp[:n]...)
			for {
				i := strings.Index(string(buf), "\n\n")
				if i < 0 {
					break
				}
				block := string(buf[:i])
				buf = buf[i+2:]
				cur = sseEvent{}
				for _, line := range strings.Split(block, "\n") {
					switch {
					case strings.HasPrefix(line, ": ping"):
						cur.typ = "ping"
					case strings.HasPrefix(line, "event: "):
						cur.typ = strings.TrimPrefix(line, "event: ")
					case strings.HasPrefix(line, "data: "):
						cur.data = strings.TrimPrefix(line, "data: ")
					}
				}
				select {
				case out <- cur:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(cancel)
	return out, cancel
}

func next(t *testing.T, ch <-chan sseEvent) sseEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("stream closed")
		}
		return ev
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for event")
	}
	return sseEvent{}
}

func nextOf(t *testing.T, ch <-chan sseEvent, typ string) sseEvent {
	t.Helper()
	for {
		ev := next(t, ch)
		if ev.typ == typ {
			return ev
		}
	}
}

func TestEventsInitialSnapshots(t *testing.T) {
	e := newEnv(t, true)
	ch, _ := e.stream(t)
	got := map[string]string{}
	// One snapshot per event type, in any order.
	for range e.srv.feed.types {
		ev := next(t, ch)
		got[ev.typ] = ev.data
	}
	for _, typ := range e.srv.feed.types {
		want, err := e.srv.snapshot(t.Context(), typ)
		if err != nil {
			t.Fatal(err)
		}
		if got[typ] != string(want) {
			t.Errorf("%s = %s, want %s", typ, got[typ], want)
		}
	}
	// Same data as the GET endpoint.
	w := e.do("GET", "/api/v1/nodes", "")
	if strings.TrimSpace(w.Body.String()) != got["nodes"] {
		t.Errorf("GET nodes differs from event:\n%s\n%s", w.Body.String(), got["nodes"])
	}
}

func TestEventsAuthOffHasNoKeysAdmins(t *testing.T) {
	e := newEnv(t, false)
	ch, _ := e.stream(t)
	seen := map[string]bool{}
	for range e.srv.feed.types {
		seen[next(t, ch).typ] = true
	}
	if !seen["nodes"] || !seen["tools"] || !seen["calls"] || !seen["stats"] {
		t.Errorf("seen %v", seen)
	}
	go e.srv.Run(t.Context())
	e.recent.Add(activity.Call{Tool: "x"})
	if ev := nextOf(t, ch, "calls"); ev.typ != "calls" {
		t.Errorf("ev = %v", ev)
	}
}

func TestEventsChanges(t *testing.T) {
	e := newEnv(t, true)
	go e.srv.Run(t.Context())
	ch, _ := e.stream(t)
	for range e.srv.feed.types {
		next(t, ch)
	}

	// A change produces exactly one event of its type; nothing else follows.
	e.recent.Add(activity.Call{Time: time.Now(), Tool: "new-call", Outcome: "ok"})
	ev := next(t, ch)
	if ev.typ != "calls" || !strings.Contains(ev.data, "new-call") {
		t.Fatalf("ev = %+v", ev)
	}

	e.nodes.set(nil)
	if ev := next(t, ch); ev.typ != "nodes" || ev.data != "[]" {
		t.Fatalf("ev = %+v", ev)
	}
	e.keys.Create(auth.KindClient, "new")
	if ev := next(t, ch); ev.typ != "keys" || !strings.Contains(ev.data, `"new"`) {
		t.Fatalf("ev = %+v", ev)
	}
	e.admins.Create(t.Context(), "zed", "a long enough password")
	if ev := next(t, ch); ev.typ != "admins" || !strings.Contains(ev.data, "zed") {
		t.Fatalf("ev = %+v", ev)
	}
	e.catalog.RemoveNode("laptop")
	if ev := next(t, ch); ev.typ != "tools" || ev.data != "[]" {
		t.Fatalf("ev = %+v", ev)
	}
}

// A poll that starts before a stream opens and reads after the state changed
// must still reach the stream: it is the newest state, and later polls see
// no change to send.
func TestEventsPollRacingANewStream(t *testing.T) {
	e := newEnv(t, true)
	var mu sync.Mutex
	state := "A"
	// hold, when set, stops the next snapshot read until released.
	var hold chan chan struct{}
	f := newFeed(func(ctx context.Context, typ string) ([]byte, error) {
		mu.Lock()
		h := hold
		hold = nil
		mu.Unlock()
		if h != nil {
			release := make(chan struct{})
			h <- release
			<-release
		}
		mu.Lock()
		defer mu.Unlock()
		return []byte(`"` + state + `"`), nil
	}, []string{"x"})
	e.srv.feed = f
	f.poll(t.Context()) // publishes A

	// A poll starts and stalls before reading.
	held := make(chan chan struct{})
	mu.Lock()
	hold = held
	mu.Unlock()
	polled := make(chan struct{})
	go func() { defer close(polled); f.poll(t.Context()) }()
	release := <-held

	// A stream opens meanwhile and gets A.
	ch, _ := e.stream(t)
	if ev := next(t, ch); ev.typ != "x" || ev.data != `"A"` {
		t.Fatalf("initial = %+v", ev)
	}
	// The state changes, then the stalled poll reads it.
	mu.Lock()
	state = "B"
	mu.Unlock()
	close(release)
	<-polled
	if ev := next(t, ch); ev.typ != "x" || ev.data != `"B"` {
		t.Fatalf("after the poll: %+v, want B", ev)
	}
}

func TestEventsPing(t *testing.T) {
	e := newEnv(t, false)
	e.srv.ping = 5 * time.Millisecond
	ch, _ := e.stream(t)
	nextOf(t, ch, "ping")
}

func TestEventsHandlerExits(t *testing.T) {
	e := newEnv(t, true)

	run := func(ctx context.Context) <-chan struct{} {
		done := make(chan struct{})
		r := httptest.NewRequest("GET", "/api/v1/events", nil).WithContext(ctx)
		r.Header.Set("Cookie", goodCookie)
		go func() {
			defer close(done)
			e.h.ServeHTTP(httptest.NewRecorder(), r)
		}()
		return done
	}
	wait := func(done <-chan struct{}, what string) {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("handler still running after %s", what)
		}
	}

	// Without Run the handler waits until the client goes away.
	reqCtx, cancel := context.WithCancel(t.Context())
	done := run(reqCtx)
	select {
	case <-done:
		t.Fatal("handler returned on its own")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	wait(done, "request cancel")

	// Run ending closes the streams too.
	runCtx, stopRun := context.WithCancel(t.Context())
	runDone := make(chan struct{})
	go func() { defer close(runDone); e.srv.Run(runCtx) }()
	done = run(t.Context())
	stopRun()
	wait(done, "Run end")
	<-runDone

	// And a stream opened after Run has ended returns immediately.
	wait(run(t.Context()), "late connect")
}

func TestFeedUnchangedIsQuiet(t *testing.T) {
	f := newFeed(func(ctx context.Context, typ string) ([]byte, error) { return []byte("same"), nil }, []string{"x"})
	sb, _ := f.subscribe()
	for range 5 {
		f.poll(t.Context())
	}
	if len(sb.ch) != 1 {
		t.Errorf("%d events for unchanged state, want 1", len(sb.ch))
	}
}

func TestFeedSlowSubscriber(t *testing.T) {
	var mu sync.Mutex
	n := 0
	f := newFeed(func(ctx context.Context, typ string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		return []byte(fmt.Sprint(n)), nil
	}, []string{"x"})
	slow, _ := f.subscribe()
	fast, _ := f.subscribe()
	var got []string
	for i := range subBuffer + 4 {
		mu.Lock()
		n = i
		mu.Unlock()
		f.poll(t.Context())
		// The fast subscriber keeps up.
		select {
		case ev := <-fast.ch:
			got = append(got, string(ev.data))
		default:
			t.Fatalf("fast subscriber missed poll %d", i)
		}
	}
	select {
	case <-slow.done:
	default:
		t.Fatal("slow subscriber was not dropped")
	}
	select {
	case <-fast.done:
		t.Fatal("fast subscriber was dropped")
	default:
	}
	if len(got) != subBuffer+4 {
		t.Errorf("fast got %d events", len(got))
	}
	f.mu.Lock()
	_, stillThere := f.subs[slow]
	f.mu.Unlock()
	if stillThere {
		t.Error("slow subscriber still registered")
	}
}

func TestEventsEndWithTheSession(t *testing.T) {
	e := newEnv(t, true)
	go e.srv.Run(t.Context())
	ch, _ := e.stream(t)
	for range e.srv.feed.types {
		next(t, ch)
	}
	e.sessions.ended.Store(true)
	e.recent.Add(activity.Call{Time: time.Now(), Tool: "after-sign-out", Outcome: "ok"})
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if strings.Contains(ev.data, "after-sign-out") {
				t.Fatal("stream kept sending after the session ended")
			}
		case <-deadline:
			t.Fatal("stream stayed open after the session ended")
		}
	}
}

func TestHealthAndReadiness(t *testing.T) {
	e := newEnv(t, true)
	e.srv.opts.Health = func(context.Context) Health {
		return Health{Metrics: map[string]any{"calls_queued": 3}}
	}
	w := e.do("GET", "/api/v1/health", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ready":true`) || !strings.Contains(w.Body.String(), `"calls_queued":3`) {
		t.Fatalf("health = %d %s", w.Code, w.Body.String())
	}
	ready := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		e.srv.Ready(rec, httptest.NewRequest("GET", "/readyz", nil))
		return rec
	}
	if rec := ready(); rec.Code != 200 || rec.Body.String() != "ready\n" {
		t.Fatalf("readyz = %d %q", rec.Code, rec.Body.String())
	}

	// A problem from the hub, and a feed that stopped polling.
	e.srv.opts.Health = func(context.Context) Health {
		return Health{Problems: []string{"database is not writable"}}
	}
	e.srv.feed.mu.Lock()
	e.srv.feed.lastAt = time.Now().Add(-time.Minute)
	e.srv.feed.mu.Unlock()
	rec := ready()
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "not writable") ||
		!strings.Contains(rec.Body.String(), "events are stale") {
		t.Fatalf("readyz = %d %q", rec.Code, rec.Body.String())
	}
}

// A feed that never completed a poll is not ready once the startup
// allowance is over, even with the database fine.
func TestReadinessNeedsAFirstSnapshot(t *testing.T) {
	e := newEnv(t, true)
	f := newFeed(func(context.Context, string) ([]byte, error) { return nil, errors.New("query failed") }, []string{"x"})
	e.srv.feed = f
	f.poll(t.Context())
	if h := e.srv.Health(t.Context()); !h.Ready {
		t.Fatalf("within the startup allowance: %+v", h)
	}
	f.mu.Lock()
	f.created = time.Now().Add(-time.Minute)
	f.mu.Unlock()
	f.poll(t.Context())
	h := e.srv.Health(t.Context())
	if h.Ready || len(h.Problems) != 1 || !strings.Contains(h.Problems[0], "no complete snapshot") {
		t.Fatalf("health = %+v", h)
	}
}
