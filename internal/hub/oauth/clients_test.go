package oauth

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// cimdServer serves a client ID metadata document over TLS.
func cimdServer(t *testing.T, doc func(id string) string) (id string, hits *atomic.Int32, client *http.Client) {
	t.Helper()
	hits = new(atomic.Int32)
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, doc(srv.URL+r.URL.Path))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/client.json", hits, srv.Client()
}

func TestClientIDMetadataDocument(t *testing.T) {
	good := func(id string) string {
		return fmt.Sprintf(`{"client_id":%q,"client_name":"Doc Client","client_uri":"https://doc.example","redirect_uris":[%q]}`, id, cbURL)
	}
	id, hits, hc := cimdServer(t, good)
	e := newEnv(t, func(o *Options) { o.HTTPClient = hc })

	resp, page := get(t, browser(t), e.authURL(id, cbURL, nil))
	if resp.StatusCode != 200 || !strings.Contains(page, "Doc Client wants to use the tools") || !strings.Contains(page, "https://doc.example") {
		t.Fatalf("%d %s", resp.StatusCode, page)
	}
	if hits.Load() != 1 {
		t.Errorf("fetches = %d", hits.Load())
	}
	// Cached for a day, refreshed after.
	get(t, browser(t), e.authURL(id, cbURL, nil))
	if hits.Load() != 1 {
		t.Errorf("fetches after cache hit = %d", hits.Load())
	}
	e.clk.advance(25 * time.Hour)
	get(t, browser(t), e.authURL(id, cbURL, nil))
	if hits.Load() != 2 {
		t.Errorf("fetches after expiry = %d", hits.Load())
	}
	var name string
	if err := e.db.QueryRow("SELECT name FROM oauth_clients WHERE id = ?", id).Scan(&name); err != nil || name != "Doc Client" {
		t.Errorf("stored client = %q %v", name, err)
	}

	// The full flow works for a metadata document client, and the key refers
	// to it.
	c := browser(t)
	consent := e.signIn(c, e.authURL(id, cbURL, nil))
	_, q := e.approve(c, consent, "allow", teamGroup)
	status, tok := e.exchange(id, q.Get("code"))
	if status != 200 {
		t.Fatalf("exchange = %d %v", status, tok)
	}
	if resp := e.mcp(tok["access_token"].(string)); resp.StatusCode != 204 {
		t.Fatalf("mcp = %d", resp.StatusCode)
	}
	if k := <-e.seen; k.OAuthClient != id {
		t.Errorf("key client = %q", k.OAuthClient)
	}
}

func TestClientIDMetadataDocumentRejected(t *testing.T) {
	for _, tt := range []struct {
		name string
		doc  func(id string) string
	}{
		{"id mismatch", func(string) string {
			return `{"client_id":"https://elsewhere.example/c","redirect_uris":["` + cbURL + `"]}`
		}},
		{"no redirects", func(id string) string { return fmt.Sprintf(`{"client_id":%q}`, id) }},
		{"bad redirect", func(id string) string {
			return fmt.Sprintf(`{"client_id":%q,"redirect_uris":["javascript:alert(1)"]}`, id)
		}},
		{"confidential", func(id string) string {
			return fmt.Sprintf(`{"client_id":%q,"redirect_uris":[%q],"token_endpoint_auth_method":"private_key_jwt"}`, id, cbURL)
		}},
		{"not json", func(string) string { return `<html>` }},
		{"too large", func(id string) string {
			return fmt.Sprintf(`{"client_id":%q,"redirect_uris":[%q],"pad":%q}`, id, cbURL, strings.Repeat("x", 70<<10))
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			id, _, hc := cimdServer(t, tt.doc)
			e := newEnv(t, func(o *Options) { o.HTTPClient = hc })
			resp, page := get(t, browser(t), e.authURL(id, cbURL, nil))
			if resp.StatusCode != 400 || !strings.Contains(page, "could not be verified") {
				t.Errorf("%d %s", resp.StatusCode, page)
			}
		})
	}
}

func TestClientIDMetadataStaleCopyUsedWhenFetchFails(t *testing.T) {
	var fail atomic.Bool
	id, hits, hc := cimdServer(t, func(id string) string {
		return fmt.Sprintf(`{"client_id":%q,"redirect_uris":[%q]}`, id, cbURL)
	})
	e := newEnv(t, func(o *Options) {
		o.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if fail.Load() {
				return nil, fmt.Errorf("network down")
			}
			return hc.Transport.RoundTrip(r)
		})}
	})
	if resp, _ := get(t, browser(t), e.authURL(id, cbURL, nil)); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	fail.Store(true)
	e.clk.advance(48 * time.Hour)
	if resp, page := get(t, browser(t), e.authURL(id, cbURL, nil)); resp.StatusCode != 200 {
		t.Errorf("%d %s", resp.StatusCode, page)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSSRFGuard(t *testing.T) {
	// The default client must refuse a loopback server, whatever the URL.
	id, hits, _ := cimdServer(t, func(id string) string {
		return fmt.Sprintf(`{"client_id":%q,"redirect_uris":[%q]}`, id, cbURL)
	})
	e := newEnv(t, nil)
	resp, page := get(t, browser(t), e.authURL(id, cbURL, nil))
	if resp.StatusCode != 400 || !strings.Contains(page, "not a public address") {
		t.Errorf("%d %s", resp.StatusCode, page)
	}
	if hits.Load() != 0 {
		t.Errorf("the loopback server was reached %d times", hits.Load())
	}
	// "localhost" resolves to loopback too: the check is on the address.
	u, _ := url.Parse(id)
	resp, page = get(t, browser(t), e.authURL("https://localhost:"+u.Port()+"/client.json", cbURL, nil))
	if resp.StatusCode != 400 || hits.Load() != 0 {
		t.Errorf("localhost: %d %s", resp.StatusCode, page)
	}
	// Not https, or not a document URL at all: unknown client, no fetch.
	for _, bad := range []string{"http://example.com/client.json", "https://example.com/", "https://example.com"} {
		resp, _ = get(t, browser(t), e.authURL(bad, cbURL, nil))
		if resp.StatusCode != 400 {
			t.Errorf("%s = %d", bad, resp.StatusCode)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "10.1.2.3": true, "192.168.0.1": true, "172.16.0.1": true, "169.254.169.254": true,
		"100.64.0.1": true, "0.0.0.0": true, "fe80::1": true, "fd00::1": true, "::ffff:127.0.0.1": true,
		"8.8.8.8": false, "93.184.216.34": false, "2606:4700::1111": false,
	} {
		if got := blockedIP(netip.MustParseAddr(ip)); got != want {
			t.Errorf("blockedIP(%s) = %v", ip, got)
		}
	}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Claude Web": "claude-web", "My App_1.0": "my-app_1.0", "¡Hola! Ünï": "hola-n", "": "",
		strings.Repeat("a", 100): strings.Repeat("a", 64),
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSDKClient runs the MCP Go SDK's own authorization code client against
// the server, driving the authorize page like a browser would.
func TestSDKClient(t *testing.T) {
	e := newEnv(t, nil)
	c := browser(t)
	redirect := "http://127.0.0.1:34567/callback"
	var seenCode atomic.Bool
	fetcher := func(ctx context.Context, args *mcpauth.AuthorizationArgs) (*mcpauth.AuthorizationResult, error) {
		consent := e.signIn(c, args.URL)
		f := url.Values{}
		for k, v := range consent {
			f[k] = v
		}
		f.Set("action", "allow")
		f.Set("key_name", "sdk")
		f["group"] = []string{"g_everything"}
		resp, _ := post(t, c, e.srv.URL+PathAuthorize, f)
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(loc.String(), redirect+"?") {
			return nil, fmt.Errorf("redirected to %s", loc)
		}
		seenCode.Store(true)
		q := loc.Query()
		return &mcpauth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
	}
	h, err := mcpauth.NewAuthorizationCodeHandler(&mcpauth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &mcpauth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{redirect}, ClientName: "SDK Client",
			GrantTypes: []string{"authorization_code", "refresh_token"},
		}},
		RedirectURL:              redirect,
		AuthorizationCodeFetcher: fetcher,
	})
	if err != nil {
		t.Fatal(err)
	}

	// An unauthenticated request gets the challenge the client starts from.
	req, _ := http.NewRequestWithContext(t.Context(), "POST", e.srv.URL+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := h.Authorize(t.Context(), req, resp); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !seenCode.Load() {
		t.Fatal("the authorization page was not used")
	}
	ts, err := h.TokenSource(t.Context())
	if err != nil || ts == nil {
		t.Fatalf("TokenSource = %v, %v", ts, err)
	}
	tok, err := ts.Token()
	if err != nil {
		t.Fatal(err)
	}
	if resp := e.mcp(tok.AccessToken); resp.StatusCode != 204 {
		t.Fatalf("mcp with SDK token = %d", resp.StatusCode)
	}
	key := <-e.seen
	if key.OAuthClient == "" || key.Name != "sdk" {
		t.Errorf("key = %+v", key)
	}
	if tok.RefreshToken == "" {
		t.Error("no refresh token")
	}
}
