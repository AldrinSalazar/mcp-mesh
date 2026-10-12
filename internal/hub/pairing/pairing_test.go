package pairing

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

func newServer(t *testing.T, opts Options) (*Manager, string) {
	t.Helper()
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	m := New(opts)
	server := httptest.NewServer(m)
	t.Cleanup(server.Close)
	return m, "ws" + strings.TrimPrefix(server.URL, "http") + proto.PairPath
}

// ask connects and sends a pair frame; it returns the code the hub answers
// with and the connection, which waits for the outcome.
func ask(t *testing.T, ctx context.Context, url string, p proto.Pair) (string, *wire.Conn) {
	t.Helper()
	conn, err := wire.Dial(ctx, url, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.CloseNow() })
	if err := conn.Send(ctx, &proto.Envelope{T: proto.TPair, Pair: &p}); err != nil {
		t.Fatal(err)
	}
	e, err := conn.Recv(ctx)
	if err != nil {
		t.Fatal("receive pairing:", err)
	}
	if e.T != proto.TPairing || e.Pairing == nil {
		t.Fatalf("got %+v, want pairing", e)
	}
	return e.Pairing.Code, conn
}

func waitPending(t *testing.T, m *Manager, n int) []Request {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		p := m.Pending()
		if len(p) == n {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d requests pending, want %d", len(p), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestApprove(t *testing.T) {
	m, url := newServer(t, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	code, conn := ask(t, ctx, url, proto.Pair{Version: proto.Version, Node: "desk", Hostname: "desk.lan\x07", OS: "linux", NodeVersion: "1.2.0"})
	if !regexp.MustCompile(`^[A-HJKMNP-Z2-9]{3}-[A-HJKMNP-Z2-9]{3}$`).MatchString(code) {
		t.Fatalf("code %q isn't three and three unambiguous characters", code)
	}
	p := waitPending(t, m, 1)[0]
	if p.Node != "desk" || p.Hostname != "desk.lan" || p.OS != "linux" || p.NodeVersion != "1.2.0" || p.Code != code || p.IP != "127.0.0.1" {
		t.Fatalf("request = %+v", p)
	}
	if got, ok := m.Get(p.ID); !ok || got != p {
		t.Fatalf("Get = %+v, %v", got, ok)
	}
	received := make(chan *proto.Envelope, 1)
	go func() {
		e, _ := conn.Recv(ctx)
		received <- e
		conn.Close(websocket.StatusNormalClosure, "paired")
	}()
	if err := m.Deliver(p.ID, Grant{Node: "studio", Key: "mm_node_secret"}); err != nil {
		t.Fatal("Deliver:", err)
	}
	e := <-received
	if e == nil || e.T != proto.TPaired || e.Paired == nil || *e.Paired != (proto.Paired{Node: "studio", Key: "mm_node_secret"}) {
		t.Fatalf("node received %+v, want the grant", e)
	}
	if len(m.Pending()) != 0 {
		t.Fatal("an approved request is still pending")
	}
	if err := m.Deliver(p.ID, Grant{Node: "studio", Key: "again"}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("second Deliver = %v, want ErrNotPending", err)
	}
}

func TestDeny(t *testing.T) {
	m, url := newServer(t, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, conn := ask(t, ctx, url, proto.Pair{Version: proto.Version, Node: "desk"})
	p := waitPending(t, m, 1)[0]
	if got, ok := m.Deny(p.ID); !ok || got.Node != "desk" {
		t.Fatalf("Deny = %+v, %v", got, ok)
	}
	_, err := conn.Recv(ctx)
	if wire.CloseStatus(err) != proto.CloseDenied {
		t.Fatalf("node got %v, want close %d", err, proto.CloseDenied)
	}
	if _, ok := m.Deny(p.ID); ok {
		t.Fatal("a declined request was declined again")
	}
	waitPending(t, m, 0)
}

// A node that disconnects withdraws its request, and an approval that comes
// too late says the key went nowhere.
func TestWithdraw(t *testing.T) {
	m, url := newServer(t, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	_, conn := ask(t, ctx, url, proto.Pair{Version: proto.Version, Node: "desk"})
	p := waitPending(t, m, 1)[0]
	conn.Close(websocket.StatusNormalClosure, "bye")
	waitPending(t, m, 0)
	if err := m.Deliver(p.ID, Grant{Node: "desk", Key: "k"}); !errors.Is(err, ErrNotPending) {
		t.Fatalf("Deliver = %v, want ErrNotPending", err)
	}
}

func TestLimits(t *testing.T) {
	m, url := newServer(t, Options{MaxPending: 3, MaxPerAddress: 2})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ask(t, ctx, url, proto.Pair{Version: proto.Version, Node: "a"})
	ask(t, ctx, url, proto.Pair{Version: proto.Version, Node: "b"})
	conn, err := wire.Dial(ctx, url, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Send(ctx, &proto.Envelope{T: proto.TPair, Pair: &proto.Pair{Version: proto.Version, Node: "c"}}); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Recv(ctx)
	if wire.CloseStatus(err) != websocket.StatusTryAgainLater || !strings.Contains(wire.CloseReason(err), "from this address") {
		t.Fatalf("third request from one address got %v, want try again later", err)
	}
	if n := len(m.Pending()); n != 2 {
		t.Fatalf("%d pending, want 2", n)
	}

	// The total applies across addresses.
	other := New(Options{Logger: slog.New(slog.DiscardHandler), MaxPending: 1, ClientIP: func(r *http.Request) string { return r.Header.Get("X-Test-IP") }})
	e := func(ip string) *entry { return &entry{req: Request{ID: newID(), IP: ip}} }
	if r := other.add(e("10.0.0.1")); r != nil {
		t.Fatal(r.reason)
	}
	if r := other.add(e("10.0.0.2")); r == nil || !strings.Contains(r.reason, "too many machines") {
		t.Fatalf("add over the total = %v", r)
	}
}

func TestRefusesBadFirstFrame(t *testing.T) {
	_, url := newServer(t, Options{})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for _, test := range []struct {
		name string
		e    *proto.Envelope
		code websocket.StatusCode
	}{
		{"hello", &proto.Envelope{T: proto.THello, Hello: &proto.Hello{Version: proto.Version, Node: "desk"}}, websocket.StatusPolicyViolation},
		{"no name", &proto.Envelope{T: proto.TPair, Pair: &proto.Pair{Version: proto.Version}}, websocket.StatusPolicyViolation},
		{"space in name", &proto.Envelope{T: proto.TPair, Pair: &proto.Pair{Version: proto.Version, Node: "my desk"}}, websocket.StatusPolicyViolation},
		{"old version", &proto.Envelope{T: proto.TPair, Pair: &proto.Pair{Version: 0, Node: "desk"}}, proto.CloseUnsupportedVersion},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, err := wire.Dial(ctx, url, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Send(ctx, test.e); err != nil {
				t.Fatal(err)
			}
			if _, err := conn.Recv(ctx); wire.CloseStatus(err) != test.code {
				t.Fatalf("got %v, want close %d", err, test.code)
			}
		})
	}
}

func TestClean(t *testing.T) {
	long := strings.Repeat("é", 40) // 80 bytes
	if got := clean(long); len(got) > maxField || strings.ContainsRune(got, '�') || !strings.HasPrefix(long, got) {
		t.Fatalf("clean cut %q badly", got)
	}
	if got := clean("  a\nb\tc\x1b[31m "); got != "abc[31m" {
		t.Fatalf("clean = %q", got)
	}
}
