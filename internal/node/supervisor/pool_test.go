package supervisor

import (
	"context"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func testPool(t *testing.T, u *fakeUpstream, opts PoolOptions) *Pool {
	t.Helper()
	opts.Name = "pc"
	if opts.New == nil {
		opts.New = func(io.Writer) upstream.Upstream { return u }
	}
	p := NewPool(opts)
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan struct{})
	go func() { defer close(done); p.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		deadline, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer stop()
		receive(t, deadline, done)
	})
	return p
}

func TestPoolIndependentProcessesAndMax(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	p := testPool(t, u, PoolOptions{MaxClients: 2})
	a, err := p.OpenClient(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	b, err := p.OpenClient(ctx, "bob")
	if err != nil {
		t.Fatal(err)
	}
	ca, cb := receive(t, ctx, u.opened), receive(t, ctx, u.opened)
	if ca == cb {
		t.Fatal("clients share a process")
	}
	if _, err := p.OpenClient(ctx, "carol"); err == nil || !strings.Contains(err.Error(), "too many clients for pc (max_clients 2)") {
		t.Fatalf("third client error = %v", err)
	}
	st := p.Status()
	if st.Sharing != "per-client" || st.MaxClients != 2 || len(st.Clients) != 2 || st.State != control.MCPRunning {
		t.Fatalf("status = %+v", st)
	}
	if st.Clients[0].Client != "alice" || st.Clients[1].Client != "bob" {
		t.Fatalf("clients = %+v", st.Clients)
	}
	// Closing one frees its slot and leaves the other process alone.
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cb.closed:
		t.Fatal("closing one channel stopped another's process")
	default:
	}
	waitFor(t, ctx, func() bool { return len(p.Status().Clients) == 1 })
	c, err := p.OpenClient(ctx, "carol")
	if err != nil {
		t.Fatal(err)
	}
	if cc := receive(t, ctx, u.opened); cc == ca || cc == cb {
		t.Fatal("a closed channel's process was reused")
	}
	b.Close()
	c.Close()
	waitFor(t, ctx, func() bool { return len(p.Status().Clients) == 0 })
	if st := p.Status(); st.State != control.MCPStopped {
		t.Fatalf("empty pool state = %s", st.State)
	}
}

func TestPoolIdleOutIsPerProcess(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	p := testPool(t, u, PoolOptions{Options: Options{IdleTimeout: 40 * time.Millisecond}})
	a, err := p.OpenClient(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	receive(t, ctx, u.opened)
	b, err := p.OpenClient(ctx, "b")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	cb := receive(t, ctx, u.opened)
	// Keep b busy with an unanswered call so only a idles out.
	if err := b.Write(ctx, message(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call"}`)); err != nil {
		t.Fatal(err)
	}
	readError(t, ctx, a, upstream.ErrIdle)
	select {
	case <-cb.closed:
		t.Fatal("idle-out of one client stopped another")
	default:
	}
	a.Close()
	waitFor(t, ctx, func() bool { return len(p.Status().Clients) == 1 })
}

func TestPoolRestartClosesAll(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	p := testPool(t, u, PoolOptions{})
	var conns []mcp.Connection
	for _, name := range []string{"a", "b"} {
		c, err := p.OpenClient(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
		defer c.Close()
	}
	if err := p.Restart(); err != nil {
		t.Fatal(err)
	}
	for _, c := range conns {
		readError(t, ctx, c, upstream.ErrRestarted)
		c.Close()
	}
	waitFor(t, ctx, func() bool { return len(p.Status().Clients) == 0 })
}

func TestPoolOpenFailureFreesSlot(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	u.open = func(context.Context) (mcp.Connection, error) { return nil, io.ErrUnexpectedEOF }
	p := testPool(t, u, PoolOptions{MaxClients: 1})
	for range 3 {
		if _, err := p.OpenClient(ctx, "a"); err == nil || strings.Contains(err.Error(), "too many") {
			t.Fatalf("error = %v", err)
		}
	}
	waitFor(t, ctx, func() bool { return len(p.Status().Clients) == 0 })
}

func TestPoolLogsArePrefixedByClient(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	var writers = make(chan io.Writer, 4)
	p := testPool(t, u, PoolOptions{MaxClients: 4, Options: Options{New: func(stderr io.Writer) upstream.Upstream {
		writers <- stderr
		return u
	}}})
	// NewPool asks once for the transport, with io.Discard.
	receive(t, ctx, writers)
	a, err := p.OpenClient(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	wa := receive(t, ctx, writers)
	b, err := p.OpenClient(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	wb := receive(t, ctx, writers)
	io.WriteString(wa, "hello\nwor")
	io.WriteString(wb, "listing line\n")
	io.WriteString(wa, "ld\n")
	got := string(p.Logs(1 << 10))
	if got != "[alice] hello\n[listing] listing line\n[alice] world\n" {
		t.Fatalf("logs = %q", got)
	}
	io.WriteString(wb, "unfinished")
	b.Close()
	if got := string(p.Logs(1 << 10)); !strings.HasSuffix(got, "[listing] unfinished\n") {
		t.Fatalf("flush on close: %q", got)
	}
}

// A per-client process that can't start must not be respawned by every
// call: the pool backs off for the whole MCP, doubling, and a success resets
// it.
func TestPoolBacksOffFailingStarts(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	var fail atomic.Bool
	fail.Store(true)
	u.open = func(context.Context) (mcp.Connection, error) {
		if fail.Load() {
			return nil, io.ErrUnexpectedEOF
		}
		c := newFakeConn()
		return c, nil
	}
	p := testPool(t, u, PoolOptions{})
	var mu sync.Mutex
	now := time.Unix(1000, 0)
	p.mu.Lock()
	p.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	p.mu.Unlock()
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	if _, err := p.OpenClient(ctx, "a"); err == nil {
		t.Fatal("open succeeded")
	}
	tries := u.calls.Load()
	// Within the backoff: refused without starting anything, for any client.
	for _, client := range []string{"a", "b"} {
		if _, err := p.OpenClient(ctx, client); err == nil || !strings.Contains(err.Error(), "retrying in") {
			t.Fatalf("open during backoff = %v", err)
		}
	}
	if u.calls.Load() != tries {
		t.Fatalf("started %d processes during backoff", u.calls.Load()-tries)
	}
	// After it: tried again; another failure doubles the wait.
	advance(time.Second)
	if _, err := p.OpenClient(ctx, "a"); err == nil || strings.Contains(err.Error(), "retrying in") {
		t.Fatalf("open after the backoff = %v, want a fresh failure", err)
	}
	advance(time.Second)
	if _, err := p.OpenClient(ctx, "a"); err == nil || !strings.Contains(err.Error(), "retrying in") {
		t.Fatalf("open 1s into a 2s backoff = %v", err)
	}
	advance(time.Second)
	fail.Store(false)
	c, err := p.OpenClient(ctx, "a")
	if err != nil {
		t.Fatalf("open after recovery = %v", err)
	}
	c.Close()
	p.mu.Lock()
	reset := p.delay == 0 && p.retryAt.IsZero()
	p.mu.Unlock()
	if !reset {
		t.Error("success did not reset the backoff")
	}
}
