package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/acl"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/adminlog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/store"
)

const (
	adminName = "admin"
	adminPass = "correct horse battery staple 42"
	cbURL     = "https://claude.example/callback"
	teamGroup = "g_team"
)

type fakeGroups struct {
	mu   sync.Mutex
	keys map[string][]string
	// onSet, if set, runs at the start of SetKeyGroups (no lock held).
	onSet func()
}

func (g *fakeGroups) List(context.Context) ([]acl.Group, error) {
	return []acl.Group{
		{ID: acl.Everything, Name: "Everything", Description: "Every tool"},
		{ID: teamGroup, Name: "Team"},
	}, nil
}

func (g *fakeGroups) SetKeyGroups(_ context.Context, keyID string, ids []string) error {
	if g.onSet != nil {
		g.onSet()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.keys[keyID] = ids
	return nil
}

func (g *fakeGroups) of(keyID string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.keys[keyID]
}

type recLog struct {
	mu sync.Mutex
	es []adminlog.Entry
}

func (l *recLog) Record(_ context.Context, e adminlog.Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.es = append(l.es, e)
}

func (l *recLog) actions() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []string
	for _, e := range l.es {
		out = append(out, e.Action)
	}
	return out
}

func (l *recLog) last() adminlog.Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.es[len(l.es)-1]
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	t      *testing.T
	s      *Server
	srv    *httptest.Server
	db     *store.DB
	keys   *auth.Store
	groups *fakeGroups
	alog   *recLog
	clk    *clock
	seen   chan auth.Key // keys the protected handler saw
}

func newEnv(t *testing.T, mod func(*Options)) *env {
	t.Helper()
	ctx := t.Context()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "hub.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	keys, err := auth.Open(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	users, err := auth.OpenUsers(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.Create(ctx, adminName, adminPass); err != nil {
		t.Fatal(err)
	}
	e := &env{
		t: t, db: db, keys: keys, groups: &fakeGroups{keys: map[string][]string{}}, alog: &recLog{},
		clk: &clock{t: time.Now()}, seen: make(chan auth.Key, 16),
	}
	var h http.Handler
	e.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.ServeHTTP(w, r) }))
	t.Cleanup(e.srv.Close)
	opts := Options{
		DB: db, Keys: keys, Sessions: auth.NewSessions(db, users, auth.SessionOptions{}), Groups: e.groups, Log: e.alog,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Issuer: e.srv.URL, Resource: e.srv.URL + "/mcp", HubName: "Test Hub",
	}
	if mod != nil {
		mod(&opts)
	}
	e.s = New(opts)
	e.s.now = e.clk.Now
	mux := http.NewServeMux()
	mux.Handle("/.well-known/", e.s.Handler())
	mux.Handle("/oauth/", e.s.Handler())
	mux.Handle("/mcp", e.s.RequireClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k, _ := auth.KeyFrom(r.Context())
		e.seen <- k
		w.WriteHeader(http.StatusNoContent)
	})))
	h = mux
	return e
}

// browser is an HTTP client with a cookie jar that does not follow redirects.
func browser(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func challengeFor(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"

func (e *env) register(redirect string) string {
	e.t.Helper()
	resp, err := http.Post(e.srv.URL+PathRegister, "application/json",
		strings.NewReader(`{"client_name":"Claude Web","redirect_uris":["`+redirect+`"],"token_endpoint_auth_method":"none"}`))
	if err != nil {
		e.t.Fatal(err)
	}
	var out struct {
		ClientID string `json:"client_id"`
	}
	b := body(e.t, resp)
	if resp.StatusCode != 201 || json.Unmarshal([]byte(b), &out) != nil {
		e.t.Fatalf("register: %d %s", resp.StatusCode, b)
	}
	return out.ClientID
}

func (e *env) authURL(clientID, redirect string, extra url.Values) string {
	q := url.Values{
		"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect},
		"code_challenge": {challengeFor(verifier)}, "code_challenge_method": {"S256"},
		"state": {"st4te"}, "resource": {e.s.opts.Resource},
	}
	for k, v := range extra {
		q[k] = v
	}
	return e.srv.URL + PathAuthorize + "?" + q.Encode()
}

var hiddenRe = regexp.MustCompile(`<input type="hidden" name="([^"]*)" value="([^"]*)">`)

func hidden(page string) url.Values {
	v := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		v.Add(html.UnescapeString(m[1]), html.UnescapeString(m[2]))
	}
	// A browser also sends the checked radio button (the key choice).
	for _, m := range checkedRadioRe.FindAllStringSubmatch(page, -1) {
		v.Set(html.UnescapeString(m[1]), html.UnescapeString(m[2]))
	}
	return v
}

var checkedRadioRe = regexp.MustCompile(`<input type="radio" name="([^"]+)" value="([^"]*)" checked>`)

func get(t *testing.T, c *http.Client, u string, hdr ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "GET", u, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body(t, resp)
}

func post(t *testing.T, c *http.Client, u string, form url.Values) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(t.Context(), "POST", u, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp, body(t, resp)
}

// signIn opens the authorize page, signs in on it and returns the consent
// page's hidden fields.
func (e *env) signIn(c *http.Client, authURL string) url.Values {
	e.t.Helper()
	resp, page := get(e.t, c, authURL)
	if resp.StatusCode != 200 || !strings.Contains(page, `name="password"`) {
		e.t.Fatalf("expected sign-in page, got %d: %s", resp.StatusCode, page)
	}
	f := hidden(page)
	f.Set("action", "signin")
	f.Set("username", adminName)
	f.Set("password", adminPass)
	resp, page = post(e.t, c, e.srv.URL+PathAuthorize, f)
	if resp.StatusCode != 200 || !strings.Contains(page, "Allow") {
		e.t.Fatalf("expected consent page, got %d: %s", resp.StatusCode, page)
	}
	return hidden(page)
}

// approve answers the consent page and returns the redirect's query.
func (e *env) approve(c *http.Client, consent url.Values, action string, groups ...string) (*http.Response, url.Values) {
	e.t.Helper()
	f := url.Values{}
	for k, v := range consent {
		f[k] = v
	}
	f.Set("action", action)
	f.Set("key_name", "Claude Web Key")
	f["group"] = groups
	resp, _ := post(e.t, c, e.srv.URL+PathAuthorize, f)
	if resp.StatusCode != http.StatusSeeOther {
		e.t.Fatalf("consent answered %d", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		e.t.Fatal(err)
	}
	return resp, loc.Query()
}

// fullFlow signs in, allows the Team group and returns the authorization code.
func (e *env) fullFlow(clientID string) string {
	e.t.Helper()
	c := browser(e.t)
	consent := e.signIn(c, e.authURL(clientID, cbURL, nil))
	resp, q := e.approve(c, consent, "allow", teamGroup)
	if !strings.HasPrefix(resp.Header.Get("Location"), cbURL+"?") {
		e.t.Fatalf("redirected to %s", resp.Header.Get("Location"))
	}
	if q.Get("state") != "st4te" || q.Get("iss") != e.srv.URL || q.Get("code") == "" {
		e.t.Fatalf("redirect query = %v", q)
	}
	return q.Get("code")
}

func (e *env) token(form url.Values) (int, map[string]any) {
	e.t.Helper()
	resp, b := post(e.t, http.DefaultClient, e.srv.URL+PathToken, form)
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		e.t.Errorf("Cache-Control = %q", cc)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(b), &out); err != nil {
		e.t.Fatalf("token response %q: %v", b, err)
	}
	return resp.StatusCode, out
}

func (e *env) exchange(clientID, code string) (int, map[string]any) {
	return e.token(url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {clientID},
		"redirect_uri": {cbURL}, "code_verifier": {verifier}, "resource": {e.s.opts.Resource},
	})
}

func (e *env) mcp(token string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequestWithContext(e.t.Context(), "POST", e.srv.URL+"/mcp", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (e *env) tokens() (access, refresh string) {
	e.t.Helper()
	id := e.register(cbURL)
	code := e.fullFlow(id)
	status, tok := e.exchange(id, code)
	if status != 200 {
		e.t.Fatalf("exchange = %d %v", status, tok)
	}
	return tok["access_token"].(string), tok["refresh_token"].(string)
}

func TestMetadata(t *testing.T) {
	e := newEnv(t, nil)
	for _, p := range []string{PathResourceMeta, PathResourceMeta + "/mcp"} {
		_, b := get(t, http.DefaultClient, e.srv.URL+p)
		var m map[string]any
		if err := json.Unmarshal([]byte(b), &m); err != nil {
			t.Fatal(err)
		}
		if m["resource"] != e.srv.URL+"/mcp" || m["authorization_servers"].([]any)[0] != e.srv.URL ||
			m["bearer_methods_supported"].([]any)[0] != "header" || m["resource_name"] != "Test Hub" {
			t.Errorf("%s = %v", p, m)
		}
	}
	_, b := get(t, http.DefaultClient, e.srv.URL+PathServerMeta)
	var m map[string]any
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"issuer": e.srv.URL, "authorization_endpoint": e.srv.URL + PathAuthorize, "token_endpoint": e.srv.URL + PathToken,
		"registration_endpoint": e.srv.URL + PathRegister, "revocation_endpoint": e.srv.URL + PathRevoke,
		"client_id_metadata_document_supported": true,
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %v, want %v", k, m[k], v)
		}
	}
	for k, v := range map[string]string{
		"response_types_supported": "code", "code_challenge_methods_supported": "S256",
		"token_endpoint_auth_methods_supported": "none", "scopes_supported": "mcp",
	} {
		if l, _ := m[k].([]any); len(l) != 1 || l[0] != v {
			t.Errorf("%s = %v", k, m[k])
		}
	}
	if l := m["grant_types_supported"].([]any); len(l) != 2 {
		t.Errorf("grant_types_supported = %v", l)
	}
}

func TestRegister(t *testing.T) {
	e := newEnv(t, nil)
	for _, tt := range []struct {
		name, body string
		want       int
		errCode    string
	}{
		{"https", `{"redirect_uris":["https://a.example/cb"]}`, 201, ""},
		{"loopback http", `{"redirect_uris":["http://127.0.0.1:8123/cb","http://localhost/cb","http://[::1]:9/cb"]}`, 201, ""},
		{"private scheme", `{"redirect_uris":["com.example.app:/cb"],"client_name":"App"}`, 201, ""},
		{"explicit none", `{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"none"}`, 201, ""},
		{"none given", `{}`, 400, "invalid_redirect_uri"},
		{"empty list", `{"redirect_uris":[]}`, 400, "invalid_redirect_uri"},
		{"remote http", `{"redirect_uris":["http://evil.example/cb"]}`, 400, "invalid_redirect_uri"},
		{"javascript", `{"redirect_uris":["javascript:alert(1)"]}`, 400, "invalid_redirect_uri"},
		{"data", `{"redirect_uris":["data:text/html,hi"]}`, 400, "invalid_redirect_uri"},
		{"fragment", `{"redirect_uris":["https://a.example/cb#x"]}`, 400, "invalid_redirect_uri"},
		{"relative", `{"redirect_uris":["/cb"]}`, 400, "invalid_redirect_uri"},
		{"one bad of two", `{"redirect_uris":["https://a.example/cb","file:///etc/passwd"]}`, 400, "invalid_redirect_uri"},
		{"confidential", `{"redirect_uris":["https://a.example/cb"],"token_endpoint_auth_method":"client_secret_basic"}`, 400, "invalid_client_metadata"},
		{"bad grant", `{"redirect_uris":["https://a.example/cb"],"grant_types":["password"]}`, 400, "invalid_client_metadata"},
		{"not json", `nope`, 400, "invalid_client_metadata"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e.clk.advance(time.Minute) // keep clear of the rate limit
			resp, err := http.Post(e.srv.URL+PathRegister, "application/json", strings.NewReader(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			b := body(t, resp)
			if resp.StatusCode != tt.want {
				t.Fatalf("status %d, want %d: %s", resp.StatusCode, tt.want, b)
			}
			var m map[string]any
			_ = json.Unmarshal([]byte(b), &m)
			if tt.want == 201 {
				if !strings.HasPrefix(m["client_id"].(string), "oc_") || m["token_endpoint_auth_method"] != "none" || m["client_id_issued_at"] == nil ||
					m["redirect_uris"] == nil || m["grant_types"] == nil || m["response_types"] == nil {
					t.Errorf("response = %v", m)
				}
			} else if m["error"] != tt.errCode {
				t.Errorf("error = %v, want %s", m["error"], tt.errCode)
			}
		})
	}
}

func TestRegisterLimits(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		e := newEnv(t, nil)
		var last int
		for range 12 {
			resp, err := http.Post(e.srv.URL+PathRegister, "application/json", strings.NewReader(`{"redirect_uris":["https://a.example/cb"]}`))
			if err != nil {
				t.Fatal(err)
			}
			body(t, resp)
			last = resp.StatusCode
		}
		if last != http.StatusTooManyRequests {
			t.Errorf("12th registration = %d, want 429", last)
		}
		// The bucket refills with time.
		e.clk.advance(time.Minute)
		resp, _ := http.Post(e.srv.URL+PathRegister, "application/json", strings.NewReader(`{"redirect_uris":["https://a.example/cb"]}`))
		body(t, resp)
		if resp.StatusCode != 201 {
			t.Errorf("after refill = %d", resp.StatusCode)
		}
	})
	t.Run("cap", func(t *testing.T) {
		e := newEnv(t, nil)
		if _, err := e.db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < ?)
			INSERT INTO oauth_clients (id, name, redirect_uris, metadata, created_at) SELECT 'oc_x' || i, 'x', '[]', '{}', 1 FROM n`, maxClients); err != nil {
			t.Fatal(err)
		}
		resp, _ := http.Post(e.srv.URL+PathRegister, "application/json", strings.NewReader(`{"redirect_uris":["https://a.example/cb"]}`))
		body(t, resp)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503", resp.StatusCode)
		}
	})
}

func TestAuthorizeValidation(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	c := browser(t)
	for _, tt := range []struct {
		name     string
		url      string
		wantPage string // error page text, or "" for a redirect
		wantErr  string
	}{
		{"missing pkce", e.authURL(id, cbURL, url.Values{"code_challenge": nil, "code_challenge_method": nil}), "", "invalid_request"},
		{"plain method", e.authURL(id, cbURL, url.Values{"code_challenge_method": {"plain"}}), "", "invalid_request"},
		{"wrong resource", e.authURL(id, cbURL, url.Values{"resource": {"https://other.example/mcp"}}), "", "invalid_target"},
		{"wrong response type", e.authURL(id, cbURL, url.Values{"response_type": {"token"}}), "", "unsupported_response_type"},
		{"wrong redirect", e.authURL(id, "https://evil.example/cb", nil), "does not match", ""},
		{"unknown client", e.authURL("oc_nope", cbURL, nil), "not registered", ""},
		{"no client", e.authURL("", cbURL, nil), "no client_id", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp, page := get(t, c, tt.url)
			if tt.wantPage != "" {
				if resp.StatusCode != 400 || !strings.Contains(page, tt.wantPage) {
					t.Fatalf("%d %s", resp.StatusCode, page)
				}
				return
			}
			if resp.StatusCode != http.StatusFound {
				t.Fatalf("status %d: %s", resp.StatusCode, page)
			}
			loc, _ := url.Parse(resp.Header.Get("Location"))
			if !strings.HasPrefix(loc.String(), cbURL+"?") || loc.Query().Get("error") != tt.wantErr ||
				loc.Query().Get("state") != "st4te" || loc.Query().Get("iss") != e.srv.URL {
				t.Errorf("Location = %s", loc)
			}
		})
	}
}

func TestLoopbackRedirectAnyPort(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register("http://127.0.0.1:1111/cb")
	resp, page := get(t, browser(t), e.authURL(id, "http://127.0.0.1:2222/cb", nil))
	if resp.StatusCode != 200 || !strings.Contains(page, "127.0.0.1:2222") {
		t.Errorf("%d %s", resp.StatusCode, page)
	}
	resp, _ = get(t, browser(t), e.authURL(id, "http://127.0.0.1:2222/other", nil))
	if resp.StatusCode != 400 {
		t.Errorf("different path = %d", resp.StatusCode)
	}
}

func TestPageSecurityHeaders(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	resp, page := get(t, browser(t), e.authURL(id, cbURL, nil))
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'self' https://claude.example"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q lacks %q", csp, want)
		}
	}
	if strings.Contains(page, "<script") || strings.Contains(page, "style=") {
		t.Errorf("page has script or inline style")
	}
	if !strings.Contains(page, "Claude Web wants to use the tools on Test Hub") || !strings.Contains(page, "claude.example") {
		t.Errorf("page: %s", page)
	}
	cssResp, css := get(t, http.DefaultClient, e.srv.URL+pathAssetsCSS)
	if cssResp.StatusCode != 200 || !strings.Contains(css, "prefers-color-scheme") {
		t.Errorf("css = %d", cssResp.StatusCode)
	}

	// Private-use schemes become scheme sources.
	id2 := e.register("com.example.app:/cb")
	resp, _ = get(t, browser(t), e.authURL(id2, "com.example.app:/cb", nil))
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "form-action 'self' com.example.app:") {
		t.Errorf("CSP = %q", csp)
	}
}

func TestFullFlow(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	status, tok := e.exchange(id, code)
	if status != 200 || tok["token_type"] != "Bearer" || tok["scope"] != "mcp" || tok["expires_in"] != float64(3600) ||
		tok["access_token"] == "" || tok["refresh_token"] == "" {
		t.Fatalf("token = %d %v", status, tok)
	}
	access := tok["access_token"].(string)
	if strings.HasPrefix(access, "mm_") {
		t.Errorf("token has a key prefix: %s", access)
	}
	if resp := e.mcp(access); resp.StatusCode != 204 {
		t.Fatalf("mcp with access token = %d", resp.StatusCode)
	}
	key := <-e.seen
	if key.Kind != auth.KindClient || key.OAuthClient != id || key.Name != "claude-web-key" {
		t.Errorf("key = %+v", key)
	}
	if g := e.groups.of(key.ID); !slices.Equal(g, []string{teamGroup}) {
		t.Errorf("groups = %v", g)
	}
	if k, _ := e.keys.Get(key.ID); k.LastUsed.IsZero() || k.LastIP == "" {
		t.Errorf("key not marked used: %+v", k)
	}
	if got := e.alog.actions(); !slices.Equal(got, []string{adminlog.SignIn, adminlog.OAuthAuthorize}) {
		t.Errorf("admin log = %v", got)
	}
	// A pasted client key still works.
	secret, _, _ := e.keys.Create(auth.KindClient, "pasted")
	if resp := e.mcp(secret); resp.StatusCode != 204 {
		t.Errorf("pasted key = %d", resp.StatusCode)
	}
	<-e.seen
}

func TestSignInErrors(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	c := browser(t)
	resp, page := get(t, c, e.authURL(id, cbURL, nil))
	f := hidden(page)
	f.Set("action", "signin")
	f.Set("username", adminName)
	f.Set("password", "wrong")
	resp, page = post(t, c, e.srv.URL+PathAuthorize, f)
	if resp.StatusCode != 401 || !strings.Contains(page, "Wrong username or password.") {
		t.Errorf("%d %s", resp.StatusCode, page)
	}
	var last string
	for range 12 {
		resp, last = post(t, c, e.srv.URL+PathAuthorize, f)
		if resp.StatusCode == 429 {
			break
		}
	}
	if resp.StatusCode != 429 || !strings.Contains(last, "Too many failed sign-ins; try again in") {
		t.Errorf("%d %s", resp.StatusCode, last)
	}
	if got := e.alog.actions(); len(got) == 0 || got[0] != adminlog.SignInFailed {
		t.Errorf("admin log = %v", got)
	}
}

func TestSignInCrossOrigin(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	for _, tc := range []struct {
		name, origin string
		ok           bool
	}{
		{"foreign", "https://evil.example", false},
		{"null", "null", false},
		{"same", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := browser(t)
			_, page := get(t, c, e.authURL(id, cbURL, nil))
			f := hidden(page)
			f.Set("action", "signin")
			f.Set("username", adminName)
			f.Set("password", adminPass)
			origin := tc.origin
			if tc.ok {
				origin = e.srv.URL
			}
			req, _ := http.NewRequestWithContext(t.Context(), "POST", e.srv.URL+PathAuthorize, strings.NewReader(f.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", origin)
			resp, err := c.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			body(t, resp)
			cookies := resp.Cookies()
			if tc.ok {
				if resp.StatusCode != 200 || len(cookies) == 0 {
					t.Errorf("same origin: %d, %d cookies", resp.StatusCode, len(cookies))
				}
				return
			}
			if resp.StatusCode != 403 || len(cookies) != 0 {
				t.Errorf("%d, %d cookies", resp.StatusCode, len(cookies))
			}
		})
	}
	// No Origin header (non-browser clients) still signs in.
	c := browser(t)
	e.signIn(c, e.authURL(id, cbURL, nil))
}

// Under no-referrer a browser posts the page's forms with "Origin: null",
// which TestSignInCrossOrigin rejects: the page must let it send its origin.
func TestAuthorizePageLetsBrowsersSendOrigin(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	resp, _ := get(t, browser(t), e.authURL(id, cbURL, nil))
	if got := resp.Header.Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy = %q, want same-origin", got)
	}
}

func TestConsentChecks(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)

	t.Run("deny", func(t *testing.T) {
		c := browser(t)
		consent := e.signIn(c, e.authURL(id, cbURL, nil))
		_, q := e.approve(c, consent, "deny")
		if q.Get("error") != "access_denied" || q.Get("code") != "" || q.Get("state") != "st4te" {
			t.Errorf("query = %v", q)
		}
		if keys, _ := e.keys.List(); len(keys) != 0 {
			t.Errorf("deny created keys: %v", keys)
		}
		if got := e.alog.actions(); got[len(got)-1] != adminlog.OAuthDenied {
			t.Errorf("admin log = %v", got)
		}
	})
	t.Run("csrf", func(t *testing.T) {
		c := browser(t)
		consent := e.signIn(c, e.authURL(id, cbURL, nil))
		consent.Set("csrf", "forged")
		consent.Set("action", "allow")
		resp, _ := post(t, c, e.srv.URL+PathAuthorize, consent)
		if resp.StatusCode != 403 {
			t.Errorf("status %d", resp.StatusCode)
		}
		if keys, _ := e.keys.List(); len(keys) != 0 {
			t.Errorf("created keys: %v", keys)
		}
	})
	t.Run("no session", func(t *testing.T) {
		consent := e.signIn(browser(t), e.authURL(id, cbURL, nil))
		consent.Set("action", "allow")
		resp, page := post(t, browser(t), e.srv.URL+PathAuthorize, consent)
		if resp.StatusCode != 401 || !strings.Contains(page, `name="password"`) {
			t.Errorf("%d %s", resp.StatusCode, page)
		}
	})
	t.Run("existing session skips sign-in", func(t *testing.T) {
		c := browser(t)
		e.signIn(c, e.authURL(id, cbURL, nil))
		resp, page := get(t, c, e.authURL(id, cbURL, nil))
		if resp.StatusCode != 200 || strings.Contains(page, `name="password"`) || !strings.Contains(page, `value="allow"`) ||
			!strings.Contains(page, `value="g_everything" checked`) || strings.Contains(page, `value="g_team" checked`) ||
			!strings.Contains(page, `value="claude-web"`) {
			t.Errorf("%d %s", resp.StatusCode, page)
		}
	})
}

func TestCrossSiteBounce(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	u := e.authURL(id, cbURL, nil)

	resp, page := get(t, browser(t), u, "Sec-Fetch-Site", "cross-site")
	if resp.StatusCode != 200 || !strings.Contains(page, `http-equiv="refresh"`) || !strings.Contains(page, ">Continue<") ||
		!strings.Contains(html.UnescapeString(page), "_s=1") || strings.Contains(page, `name="password"`) {
		t.Fatalf("bounce page: %d %s", resp.StatusCode, page)
	}
	if !strings.Contains(html.UnescapeString(page), e.srv.URL+PathAuthorize+"?") {
		t.Errorf("bounce does not target the issuer: %s", page)
	}
	// Only once: with the marker the sign-in page is shown.
	resp, page = get(t, browser(t), u+"&_s=1", "Sec-Fetch-Site", "cross-site")
	if resp.StatusCode != 200 || !strings.Contains(page, `name="password"`) || strings.Contains(page, `http-equiv`) {
		t.Errorf("after bounce: %d %s", resp.StatusCode, page)
	}
	// The marker is not carried into the form.
	if h := hidden(page); h.Get("_s") != "" {
		t.Errorf("hidden fields carry _s: %v", h)
	}
	// With a session there is nothing to bounce for.
	c := browser(t)
	e.signIn(c, u)
	resp, page = get(t, c, u, "Sec-Fetch-Site", "cross-site")
	if resp.StatusCode != 200 || !strings.Contains(page, `value="allow"`) {
		t.Errorf("with session: %d %s", resp.StatusCode, page)
	}
}

func TestPKCEFailure(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	status, tok := e.token(url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {id},
		"redirect_uri": {cbURL}, "code_verifier": {strings.Repeat("a", 43)},
	})
	if status != 400 || tok["error"] != "invalid_grant" {
		t.Errorf("wrong verifier = %d %v", status, tok)
	}
	// The attempt spent the code.
	if status, tok = e.exchange(id, code); status != 400 || tok["error"] != "invalid_grant" {
		t.Errorf("retry = %d %v", status, tok)
	}
}

func TestTokenRequestErrors(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	other := e.register("https://other.example/cb")
	good := func() url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {e.fullFlow(id)}, "client_id": {id},
			"redirect_uri": {cbURL}, "code_verifier": {verifier}}
	}
	for _, tt := range []struct {
		name    string
		mod     func(url.Values)
		wantErr string
	}{
		{"other client", func(v url.Values) { v.Set("client_id", other) }, "invalid_grant"},
		{"other redirect", func(v url.Values) { v.Set("redirect_uri", "https://other.example/cb") }, "invalid_grant"},
		{"wrong resource", func(v url.Values) { v.Set("resource", "https://x.example/mcp") }, "invalid_target"},
		{"missing verifier", func(v url.Values) { v.Del("code_verifier") }, "invalid_request"},
		{"no client", func(v url.Values) { v.Del("client_id") }, "invalid_request"},
		{"bad grant", func(v url.Values) { v.Set("grant_type", "password") }, "unsupported_grant_type"},
		{"no grant", func(v url.Values) { v.Del("grant_type") }, "invalid_request"},
		{"secret", func(v url.Values) { v.Set("client_secret", "x") }, "invalid_client"},
		{"unknown code", func(v url.Values) { v.Set("code", "nope") }, "invalid_grant"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			v := good()
			tt.mod(v)
			if _, tok := e.token(v); tok["error"] != tt.wantErr {
				t.Errorf("error = %v, want %s", tok["error"], tt.wantErr)
			}
		})
	}
}

func TestCodeExpires(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	e.clk.advance(2 * time.Minute)
	if status, tok := e.exchange(id, code); status != 400 || tok["error"] != "invalid_grant" {
		t.Errorf("expired code = %d %v", status, tok)
	}
}

func TestCodeReplayRevokes(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	status, tok := e.exchange(id, code)
	if status != 200 {
		t.Fatal(tok)
	}
	access := tok["access_token"].(string)
	if resp := e.mcp(access); resp.StatusCode != 204 {
		t.Fatalf("first use = %d", resp.StatusCode)
	}
	<-e.seen
	if status, tok = e.exchange(id, code); status != 400 || tok["error"] != "invalid_grant" {
		t.Errorf("replay = %d %v", status, tok)
	}
	if resp := e.mcp(access); resp.StatusCode != 401 {
		t.Errorf("token after replay = %d, want 401", resp.StatusCode)
	}
	status, tok = e.token(url.Values{"grant_type": {"refresh_token"}, "client_id": {id}, "refresh_token": {"x"}})
	if status != 400 {
		t.Errorf("status %d", status)
	}
}

func TestRefreshRotation(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	_, tok := e.exchange(id, code)
	oldRefresh := tok["refresh_token"].(string)
	refresh := func(token, client string) (int, map[string]any) {
		return e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {client}})
	}

	if status, out := refresh(oldRefresh, "oc_other"); status != 400 || out["error"] != "invalid_grant" {
		t.Errorf("wrong client = %d %v", status, out)
	}
	// The failed attempt above already spent it; start again.
	_, tok = e.exchange(id, e.fullFlow(id))
	oldRefresh = tok["refresh_token"].(string)
	status, out := refresh(oldRefresh, id)
	if status != 200 || out["refresh_token"] == oldRefresh || out["access_token"] == "" {
		t.Fatalf("refresh = %d %v", status, out)
	}
	// (Using oldRefresh again would revoke the grant: TestRefreshReuseRevokesTheGrant.)
	if resp := e.mcp(out["access_token"].(string)); resp.StatusCode != 204 {
		t.Errorf("new access token = %d", resp.StatusCode)
	}
	<-e.seen
	// Refresh tokens are not access tokens.
	if resp := e.mcp(out["refresh_token"].(string)); resp.StatusCode != 401 {
		t.Errorf("refresh token as access token = %d", resp.StatusCode)
	}
	// And they expire.
	e.clk.advance(31 * 24 * time.Hour)
	if status, bad := refresh(out["refresh_token"].(string), id); status != 400 {
		t.Errorf("expired refresh = %d %v", status, bad)
	}
}

// A refresh token is good for one use. Presenting a used one means it
// leaked: the whole grant (every token rotated from the same code) is
// revoked, whoever holds the newest tokens, while other grants of the key
// stay (RFC 9700 section 4.14.2).
func TestRefreshReuseRevokesTheGrant(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	refresh := func(token string) (int, map[string]any) {
		return e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {id}})
	}
	_, first := e.exchange(id, e.fullFlow(id))
	_, second := refresh(first["refresh_token"].(string))
	_, third := refresh(second["refresh_token"].(string))
	if third["access_token"] == nil {
		t.Fatalf("rotation failed: %v", third)
	}
	// Another grant of the same key: the app re-authorized and kept it.
	c := browser(t)
	consent := e.signIn(c, e.authURL(id, cbURL, nil))
	keyID := consent.Get("key")
	if keyID == "" {
		t.Fatal("consent page offered no existing key")
	}
	_, q := e.approve(c, consent, "allow")
	_, other := e.exchange(id, q.Get("code"))

	// The thief replays the first refresh token.
	if status, out := refresh(first["refresh_token"].(string)); status != 400 || out["error"] != "invalid_grant" ||
		!strings.Contains(out["error_description"].(string), "already used") {
		t.Fatalf("reuse = %d %v", status, out)
	}
	if resp := e.mcp(third["access_token"].(string)); resp.StatusCode != 401 {
		t.Errorf("newest access token after reuse = %d, want 401", resp.StatusCode)
	}
	if status, _ := refresh(third["refresh_token"].(string)); status != 400 {
		t.Errorf("newest refresh token after reuse = %d, want 400", status)
	}
	if resp := e.mcp(other["access_token"].(string)); resp.StatusCode != 204 {
		t.Errorf("the key's other grant = %d, want 204", resp.StatusCode)
	}
	if k := <-e.seen; k.ID != keyID {
		t.Errorf("other grant used key %s, want the reused %s", k.ID, keyID)
	}
	if keys, _ := e.keys.List(); len(keys) != 1 {
		t.Errorf("keys = %d, want 1 (reused)", len(keys))
	}
}

// The key is made when the code is redeemed: a consent whose code never is
// leaves no key behind. Re-authorizing offers the app's key; a new one can
// still be chosen, and another app's key can't.
func TestKeyMadeAtExchangeAndReused(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	if keys, _ := e.keys.List(); len(keys) != 0 {
		t.Fatalf("consent made keys: %v", keys)
	}
	if got := e.alog.actions(); slices.Contains(got, adminlog.OAuthAuthorize) {
		t.Fatalf("authorization logged before the code was redeemed: %v", got)
	}
	e.fullFlow(id) // never redeemed
	if status, tok := e.exchange(id, code); status != 200 {
		t.Fatalf("exchange = %d %v", status, tok)
	}
	keys, _ := e.keys.List()
	if len(keys) != 1 || keys[0].Name != "claude-web-key" || keys[0].OAuthClient != id {
		t.Fatalf("keys = %+v", keys)
	}
	if g := e.groups.of(keys[0].ID); !slices.Equal(g, []string{teamGroup}) {
		t.Errorf("groups = %v", g)
	}
	if l := e.alog.last(); l.Action != adminlog.OAuthAuthorize || l.Target != keys[0].ID || l.Actor != adminName || l.IP == "" {
		t.Errorf("admin log entry = %+v", l)
	}

	// Re-authorizing: the existing key is offered and preselected.
	c := browser(t)
	consent := e.signIn(c, e.authURL(id, cbURL, nil))
	if consent.Get("key") != keys[0].ID {
		t.Fatalf("preselected key = %q, want %s", consent.Get("key"), keys[0].ID)
	}
	consent.Set("key", "new")
	_, q := e.approve(c, consent, "allow")
	if status, tok := e.exchange(id, q.Get("code")); status != 200 {
		t.Fatalf("exchange for a new key = %d %v", status, tok)
	}
	if keys, _ := e.keys.List(); len(keys) != 2 {
		t.Errorf("keys = %d, want 2", len(keys))
	}

	// A key of another app can't be chosen.
	otherID := e.register(cbURL)
	if status, tok := e.exchange(otherID, e.fullFlow(otherID)); status != 200 {
		t.Fatalf("other app = %d %v", status, tok)
	}
	var foreign string
	all, _ := e.keys.List()
	for _, k := range all {
		if k.OAuthClient == otherID {
			foreign = k.ID
		}
	}
	c = browser(t)
	consent = e.signIn(c, e.authURL(id, cbURL, nil))
	consent.Set("key", foreign)
	consent.Set("action", "allow")
	if resp, _ := post(t, c, e.srv.URL+PathAuthorize, consent); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("foreign key = %d, want 400", resp.StatusCode)
	}
}

// A replay that arrives while the first exchange is still making the key
// stops it from issuing anything, and the key it made is removed.
func TestCodeReplayDuringExchange(t *testing.T) {
	e := newEnv(t, nil)
	id := e.register(cbURL)
	code := e.fullFlow(id)
	replays := 0
	e.groups.onSet = func() {
		replays++
		if status, tok := e.exchange(id, code); status != 400 {
			t.Errorf("replay = %d %v", status, tok)
		}
	}
	status, tok := e.exchange(id, code)
	if replays != 1 || status != 400 || tok["error"] != "invalid_grant" {
		t.Fatalf("exchange after a replay = %d %v (replays %d)", status, tok, replays)
	}
	if keys, _ := e.keys.List(); len(keys) != 0 {
		t.Errorf("the key made for it was kept: %v", keys)
	}
	var n int
	if err := e.db.QueryRow("SELECT count(*) FROM oauth_tokens").Scan(&n); err != nil || n != 0 {
		t.Errorf("tokens = %d, %v", n, err)
	}
}

func TestRevokeEndpoint(t *testing.T) {
	e := newEnv(t, nil)
	access, refresh := e.tokens()
	revoke := func(tok string) {
		t.Helper()
		resp, _ := post(t, http.DefaultClient, e.srv.URL+PathRevoke, url.Values{"token": {tok}})
		if resp.StatusCode != 200 {
			t.Errorf("revoke = %d", resp.StatusCode)
		}
	}
	revoke("never-issued")
	revoke("")
	if resp := e.mcp(access); resp.StatusCode != 204 {
		t.Fatalf("access = %d", resp.StatusCode)
	}
	<-e.seen
	revoke(refresh)
	if resp := e.mcp(access); resp.StatusCode != 401 {
		t.Errorf("access after revoking refresh = %d", resp.StatusCode)
	}

	access, _ = e.tokens()
	revoke(access)
	if resp := e.mcp(access); resp.StatusCode != 401 {
		t.Errorf("revoked access = %d", resp.StatusCode)
	}
}

func TestKeyRevocationKillsTokens(t *testing.T) {
	e := newEnv(t, nil)
	access, refresh := e.tokens()
	if resp := e.mcp(access); resp.StatusCode != 204 {
		t.Fatal(resp.StatusCode)
	}
	key := <-e.seen
	if _, ok := e.s.Verify(t.Context(), access); !ok {
		t.Fatal("Verify failed for a live token")
	}
	if _, err := e.keys.Revoke(key.ID); err != nil {
		t.Fatal(err)
	}
	if resp := e.mcp(access); resp.StatusCode != 401 {
		t.Errorf("after key revocation = %d", resp.StatusCode)
	}
	var n int
	if err := e.db.QueryRow("SELECT count(*) FROM oauth_tokens").Scan(&n); err != nil || n != 0 {
		t.Errorf("tokens left: %d %v", n, err)
	}
	id := key.OAuthClient
	if status, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {id}}); status != 400 {
		t.Errorf("refresh after key revocation = %d", status)
	}
}

func TestExpiredAccessToken(t *testing.T) {
	e := newEnv(t, nil)
	access, _ := e.tokens()
	e.clk.advance(59 * time.Minute)
	if resp := e.mcp(access); resp.StatusCode != 204 {
		t.Fatalf("before expiry = %d", resp.StatusCode)
	}
	<-e.seen
	e.clk.advance(2 * time.Minute)
	if resp := e.mcp(access); resp.StatusCode != 401 {
		t.Errorf("after expiry = %d", resp.StatusCode)
	}
	e.s.cleanup(t.Context())
	var n int
	if err := e.db.QueryRow("SELECT count(*) FROM oauth_tokens WHERE kind = 'access'").Scan(&n); err != nil || n != 0 {
		t.Errorf("expired access tokens kept: %d %v", n, err)
	}
	var codes int
	if err := e.db.QueryRow("SELECT count(*) FROM oauth_codes").Scan(&codes); err != nil || codes != 0 {
		t.Errorf("used codes kept after expiry: %d %v", codes, err)
	}
}

func TestRequireClient(t *testing.T) {
	e := newEnv(t, nil)
	nodeSecret, _, err := e.keys.Create(auth.KindNode, "n1")
	if err != nil {
		t.Fatal(err)
	}
	meta := e.srv.URL + "/.well-known/oauth-protected-resource/mcp"
	for _, tt := range []struct {
		name, token, want string
	}{
		{"none", "", `Bearer resource_metadata="` + meta + `"`},
		{"garbage", "garbage", `Bearer resource_metadata="` + meta + `", error="invalid_token"`},
		{"node key", nodeSecret, `Bearer resource_metadata="` + meta + `", error="invalid_token"`},
		{"unknown client key", "mm_client_" + strings.Repeat("0", 43), `Bearer resource_metadata="` + meta + `", error="invalid_token"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resp := e.mcp(tt.token)
			if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") != tt.want {
				t.Errorf("%d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
			}
		})
	}
	select {
	case k := <-e.seen:
		t.Errorf("handler ran for %+v", k)
	default:
	}
}
