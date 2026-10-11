package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
)

// rawClient makes session-based (2025-11-25) requests as one client.
type rawClient struct {
	t      *testing.T
	url    string
	client string
}

func (c rawClient) do(method, session, body string) *http.Response {
	c.t.Helper()
	req, err := http.NewRequestWithContext(c.t.Context(), method, c.url+"/mcp", strings.NewReader(body))
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer key-of-"+c.client)
	req.Header.Set("X-Test-Client", c.client)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp
}

func (c rawClient) initialize() (int, string) {
	c.t.Helper()
	resp := c.do(http.MethodPost, "", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`)
	return resp.StatusCode, resp.Header.Get("Mcp-Session-Id")
}

func TestSessionsPerClientAreCapped(t *testing.T) {
	c := catalog.New()
	c.SetMCP("n", "m", []*mcp.Tool{tool("t", "")}, okCaller("ok"))
	g := New(Options{Catalog: c, Identify: byHeader, MaxSessionsPerClient: 3})
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	alice := rawClient{t, server.URL, "alice"}

	var sessions []string
	for range 3 {
		code, id := alice.initialize()
		if code != http.StatusOK || id == "" {
			t.Fatalf("initialize = %d, %q", code, id)
		}
		sessions = append(sessions, id)
	}
	if code, _ := alice.initialize(); code != http.StatusTooManyRequests {
		t.Fatalf("fourth session = %d, want 429", code)
	}
	if st := g.Stats(); st.Clients != 1 || st.Sessions != 3 || st.SessionsRefused != 1 {
		t.Errorf("stats = %+v", st)
	}
	// Existing sessions keep working, and other keys have their own budget.
	if resp := alice.do(http.MethodPost, sessions[0], `{"jsonrpc":"2.0","id":2,"method":"ping"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("ping on an existing session = %d", resp.StatusCode)
	}
	if code, _ := (rawClient{t, server.URL, "bob"}).initialize(); code != http.StatusOK {
		t.Fatalf("bob = %d", code)
	}
	// Ending one makes room.
	if resp := alice.do(http.MethodDelete, sessions[0], ""); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if code, _ := alice.initialize(); code != http.StatusOK {
		t.Fatalf("after delete = %d", code)
	}
}

func TestIdleSessionsExpire(t *testing.T) {
	c := catalog.New()
	g := New(Options{Catalog: c, Identify: byHeader, SessionTimeout: 50 * time.Millisecond})
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	alice := rawClient{t, server.URL, "alice"}
	code, id := alice.initialize()
	if code != http.StatusOK {
		t.Fatalf("initialize = %d", code)
	}
	if n := g.sessionCount(identity.Client{ID: "id-alice"}); n != 1 {
		t.Fatalf("sessions = %d", n)
	}
	deadline := time.Now().Add(5 * time.Second)
	for g.sessionCount(identity.Client{ID: "id-alice"}) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle session never expired")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if resp := alice.do(http.MethodPost, id, `{"jsonrpc":"2.0","id":2,"method":"ping"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expired session = %d, want 404", resp.StatusCode)
	}
}

// pendingPost starts a request whose body isn't sent yet, so the handler is
// entered (and holds whatever it reserves) until finish is called. The
// response arrives on the returned channel.
func pendingPost(t *testing.T, url, client, version string) (finish func(body string), resp <-chan *http.Response) {
	t.Helper()
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url+"/mcp", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer key-of-"+client)
	req.Header.Set("X-Test-Client", client)
	if version != "" {
		req.Header.Set("MCP-Protocol-Version", version)
	}
	out := make(chan *http.Response, 1)
	go func() {
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			out <- nil
			return
		}
		io.Copy(io.Discard, r.Body)
		r.Body.Close()
		out <- r
	}()
	return func(body string) {
		io.WriteString(pw, body)
		pw.Close()
	}, out
}

// awaitCount waits until the gateway counts n places taken for id.
func awaitCount(t *testing.T, g *Gateway, counts map[string]int, id string, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		got := counts[id]
		g.mu.Unlock()
		if got == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s holds %d places, want %d", id, got, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func status(t *testing.T, resp <-chan *http.Response) int {
	t.Helper()
	select {
	case r := <-resp:
		if r == nil {
			t.Fatal("request failed")
		}
		return r.StatusCode
	case <-time.After(5 * time.Second):
		t.Fatal("no response")
		return 0
	}
}

// Session-creating requests in progress hold their place, so concurrent ones
// can't all get past the cap before any session exists.
func TestSessionCapHoldsUnderConcurrentInitializes(t *testing.T) {
	c := catalog.New()
	g := New(Options{Catalog: c, Identify: byHeader, MaxSessionsPerClient: 2})
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	var finishes []func(string)
	var responses []<-chan *http.Response
	for range 2 {
		finish, resp := pendingPost(t, server.URL, "alice", "")
		finishes, responses = append(finishes, finish), append(responses, resp)
	}
	// The two hold both places (their bodies aren't read yet): a third is
	// refused while they're still in progress.
	awaitCount(t, g, g.opening, "id-alice", 2)
	finish, resp := pendingPost(t, server.URL, "alice", "")
	finish(`{}`)
	if code := status(t, resp); code != http.StatusTooManyRequests {
		t.Fatalf("third initialize = %d, want 429", code)
	}
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	for i, finish := range finishes {
		finish(initialize)
		if code := status(t, responses[i]); code != http.StatusOK {
			t.Fatalf("initialize %d = %d", i, code)
		}
	}
	if n := g.sessionCount(identity.Client{ID: "id-alice"}); n != 2 {
		t.Errorf("sessions = %d, want 2", n)
	}
}

// Stateless (2026-07-28) requests and streams are capped per key.
func TestStatelessRequestsPerClientAreCapped(t *testing.T) {
	c := catalog.New()
	g := New(Options{Catalog: c, Identify: byHeader, MaxRequestsPerClient: 2})
	server := httptest.NewServer(g.Handler())
	t.Cleanup(server.Close)
	var finishes []func(string)
	var responses []<-chan *http.Response
	for range 2 {
		finish, resp := pendingPost(t, server.URL, "alice", "2026-07-28")
		finishes, responses = append(finishes, finish), append(responses, resp)
	}
	awaitCount(t, g, g.active, "id-alice", 2)
	finish, resp := pendingPost(t, server.URL, "alice", "2026-07-28")
	finish(`{}`)
	if code := status(t, resp); code != http.StatusTooManyRequests {
		t.Fatalf("third request = %d, want 429", code)
	}
	// Another key has its own budget.
	finish, resp = pendingPost(t, server.URL, "bob", "2026-07-28")
	finish(`{}`)
	if code := status(t, resp); code == http.StatusTooManyRequests {
		t.Fatalf("bob = %d", code)
	}
	if st := g.Stats(); st.Requests != 2 || st.RequestsRefused == 0 {
		t.Errorf("stats = %+v", st)
	}
	for i, finish := range finishes {
		finish(`{}`)
		status(t, responses[i])
	}
	if st := g.Stats(); st.Requests != 0 {
		t.Errorf("requests after they ended = %d", st.Requests)
	}
}
