package supervisor

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type result struct {
	msg jsonrpc.Message
	err error
}
type fakeConn struct {
	incoming chan result
	closed   chan struct{}
	once     sync.Once
	write    func(context.Context, jsonrpc.Message) error
}

func newFakeConn() *fakeConn {
	return &fakeConn{incoming: make(chan result, 1024), closed: make(chan struct{})}
}
func (c *fakeConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, io.EOF
	case r := <-c.incoming:
		return r.msg, r.err
	}
}
func (c *fakeConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if c.write != nil {
		return c.write(ctx, msg)
	}
	return nil
}
func (c *fakeConn) Close() error    { c.once.Do(func() { close(c.closed) }); return nil }
func (*fakeConn) SessionID() string { return "fake-session" }

type fakeUpstream struct {
	open   func(context.Context) (mcp.Connection, error)
	opened chan *fakeConn
	calls  atomic.Int32
}

func newFakeUpstream() *fakeUpstream { return &fakeUpstream{opened: make(chan *fakeConn, 32)} }
func (u *fakeUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	u.calls.Add(1)
	if u.open != nil {
		return u.open(ctx)
	}
	c := newFakeConn()
	u.opened <- c
	return c, nil
}
func (*fakeUpstream) Transport() proto.Transport { return proto.TransportStdio }

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitFor(t *testing.T, ctx context.Context, predicate func() bool) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !predicate() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("condition did not become true: ", ctx.Err())
		}
	}
}
func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		var zero T
		return zero
	}
}
func waitState(t *testing.T, ctx context.Context, s *Supervisor, state control.MCPState) control.MCPStatus {
	t.Helper()
	waitFor(t, ctx, func() bool { return s.Status().State == state })
	return s.Status()
}

// Every test waits for Run and its reader owner, even on a failed assertion.
func runSupervisor(t *testing.T, s *Supervisor) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	s.backoffMin, s.backoffMax, s.healthyAfter = 60*time.Millisecond, 240*time.Millisecond, time.Second
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		deadline, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer stop()
		receive(t, deadline, done)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.p != nil || s.startCancel != nil {
			t.Error("Run left a process or startup behind")
		}
	})
	return cancel, done
}

func fakeSupervisor(t *testing.T, u *fakeUpstream, opts Options) *Supervisor {
	t.Helper()
	opts.Name = "test-mcp"
	opts.New = func(io.Writer) upstream.Upstream { return u }
	s := New(opts)
	runSupervisor(t, s)
	return s
}
func openConn(t *testing.T, ctx context.Context, s *Supervisor) mcp.Connection {
	t.Helper()
	c, err := s.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func message(t *testing.T, raw string) jsonrpc.Message {
	t.Helper()
	msg, err := jsonrpc.DecodeMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return msg
}
func readError(t *testing.T, ctx context.Context, c mcp.Connection, want error) {
	t.Helper()
	_, err := c.Read(ctx)
	if !errors.Is(err, want) {
		t.Fatalf("Read error = %v, want %v", err, want)
	}
}

func TestEagerHandoverPreservesMessagesAndClosePrewarms(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{})
	first := receive(t, ctx, u.opened)
	waitState(t, ctx, s, control.MCPRunning)
	if status := s.Status(); status.Attached || status.Start != "eager" {
		t.Fatalf("prewarm = %+v", status)
	}
	for _, method := range []string{"before-one", "before-two"} {
		first.incoming <- result{msg: &jsonrpc.Request{Method: method}}
	}
	waitFor(t, ctx, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.p.queue) == 2 })
	c := openConn(t, ctx, s)
	if _, err := s.Open(ctx); err == nil || !strings.Contains(err.Error(), "already attached") {
		t.Fatalf("second Open = %v", err)
	}
	for _, want := range []string{"before-one", "before-two"} {
		got, err := c.Read(ctx)
		if err != nil || got.(*jsonrpc.Request).Method != want {
			t.Fatalf("Read = %v, %v", got, err)
		}
	}
	if c.SessionID() != "fake-session" {
		t.Fatal("SessionID not forwarded")
	}
	readDone := make(chan error, 1)
	go func() { _, err := c.Read(ctx); readDone <- err }()
	closed := make(chan error, 2)
	for range 2 {
		go func() { closed <- c.Close() }()
	}
	for range 2 {
		if err := receive(t, ctx, closed); err != nil {
			t.Fatal(err)
		}
	}
	if err := receive(t, ctx, readDone); err == nil {
		t.Fatal("Close did not unblock Read")
	}
	receive(t, ctx, first.closed)
	second := receive(t, ctx, u.opened)
	if second == first {
		t.Fatal("process reused")
	}
	waitState(t, ctx, s, control.MCPRunning)
	if status := s.Status(); status.Restarts != 0 || !status.NextRetry.IsZero() || status.Attached {
		t.Fatalf("normal close = %+v", status)
	}
}

type connTransport struct{ c mcp.Connection }

func (t connTransport) Connect(context.Context) (mcp.Connection, error) { return t.c, nil }

type serverConn struct {
	mcp.Connection
	server *mcp.ServerSession
	once   sync.Once
	err    error
}

func (c *serverConn) Close() error {
	c.once.Do(func() { c.err = errors.Join(c.Connection.Close(), c.server.Close()) })
	return c.err
}

func TestEagerRealInitialize(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	u.open = func(ctx context.Context) (mcp.Connection, error) {
		client, server := mcp.NewInMemoryTransports()
		ss, err := mcp.NewServer(&mcp.Implementation{Name: "prewarm-server", Version: "1"}, nil).Connect(ctx, server, nil)
		if err != nil {
			return nil, err
		}
		conn, err := client.Connect(ctx)
		if err != nil {
			_ = ss.Close()
			return nil, err
		}
		return &serverConn{Connection: conn, server: ss}, nil
	}
	s := fakeSupervisor(t, u, Options{})
	waitState(t, ctx, s, control.MCPRunning)
	c := openConn(t, ctx, s)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "client", Version: "1"}, nil).Connect(ctx, connTransport{c}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cs.InitializeResult().ServerInfo.Name != "prewarm-server" {
		t.Fatal("initialize did not reach prewarmed process")
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCrashBackoffAndReset(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{})
	crash := errors.New("process crashed")
	c := openConn(t, ctx, s)
	for i, wantDelay := range []time.Duration{60 * time.Millisecond, 120 * time.Millisecond, 240 * time.Millisecond, 240 * time.Millisecond, 60 * time.Millisecond} {
		p := receive(t, ctx, u.opened)
		if i == 4 {
			quiet, cancel := context.WithTimeout(ctx, s.healthyAfter)
			readError(t, quiet, c, context.DeadlineExceeded)
			cancel()
		}
		p.incoming <- result{err: crash}
		readError(t, ctx, c, crash)
		status := waitState(t, ctx, s, control.MCPBackoff)
		s.mu.Lock()
		delay := s.delay
		s.mu.Unlock()
		if status.Restarts != i+1 || status.LastError != crash.Error() || delay != wantDelay {
			t.Fatalf("crash %d = %+v, delay %v", i, status, delay)
		}
		// Open waits through backoff and attaches to the next prewarm.
		c = openConn(t, ctx, s)
		if started := s.Status().StartedAt; started.Before(status.NextRetry) {
			t.Fatalf("started %v before retry %v", started, status.NextRetry)
		}
		if status := s.Status(); status.LastError != "" || !status.NextRetry.IsZero() {
			t.Fatalf("recovery = %+v", status)
		}
	}
	_ = c.Close()
}

func TestPrewarmedDeath(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{})
	p := receive(t, ctx, u.opened)
	// More messages than a typical pump buffer, with no attached reader.
	for range 300 {
		p.incoming <- result{msg: &jsonrpc.Request{Method: "notice"}}
	}
	p.incoming <- result{err: io.EOF}
	status := waitState(t, ctx, s, control.MCPBackoff)
	if status.Restarts != 1 || status.Attached {
		t.Fatalf("prewarm crash = %+v", status)
	}
	receive(t, ctx, p.closed)
	receive(t, ctx, u.opened)
	waitState(t, ctx, s, control.MCPRunning)
}

func TestLazyIdleAndOutstandingCall(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{Start: proto.StartLazy, IdleTimeout: 30 * time.Millisecond})
	waitFor(t, ctx, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.ran })
	if u.calls.Load() != 0 || s.Status().State != control.MCPStopped {
		t.Fatal("lazy process started before Open")
	}
	c := openConn(t, ctx, s)
	p := receive(t, ctx, u.opened)
	call := message(t, `{"jsonrpc":"2.0","id":"long-call","method":"tools/call"}`)
	if err := c.Write(ctx, call); err != nil {
		t.Fatal(err)
	}
	// A deadline longer than the idle policy proves a quiet outstanding call
	// keeps the process up; this is a duration assertion, not synchronization.
	quiet, cancel := context.WithTimeout(ctx, 3*s.opts.IdleTimeout)
	readError(t, quiet, c, context.DeadlineExceeded)
	cancel()
	if s.Status().State != control.MCPRunning {
		t.Fatal("long call was idle-killed")
	}
	// An unrelated response must not clear the long call's ID.
	p.incoming <- result{msg: message(t, `{"jsonrpc":"2.0","id":"other","result":{}}`)}
	if _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	outstanding := len(s.p.outstanding)
	s.mu.Unlock()
	if outstanding != 1 {
		t.Fatalf("outstanding = %d", outstanding)
	}
	p.incoming <- result{msg: message(t, `{"jsonrpc":"2.0","id":"long-call","result":{}}`)}
	if _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	readError(t, ctx, c, upstream.ErrIdle)
	receive(t, ctx, p.closed)
	waitState(t, ctx, s, control.MCPStopped)
	if status := s.Status(); status.Restarts != 0 || status.Attached || !status.NextRetry.IsZero() {
		t.Fatalf("idle = %+v", status)
	}
	if u.calls.Load() != 1 {
		t.Fatal("lazy process prewarmed after idle")
	}
	_ = c.Close()
	c = openConn(t, ctx, s)
	if u.calls.Load() != 2 {
		t.Fatal("next Open did not start a fresh process")
	}
	_ = c.Close()
}

func TestFastResponseAndFailedWrite(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "fast response", true: "write failure"}[fail], func(t *testing.T) {
			ctx := testContext(t)
			u := newFakeUpstream()
			u.open = func(context.Context) (mcp.Connection, error) {
				p := newFakeConn()
				p.write = func(_ context.Context, msg jsonrpc.Message) error {
					if fail {
						return errors.New("write failed")
					}
					p.incoming <- result{msg: &jsonrpc.Response{ID: msg.(*jsonrpc.Request).ID, Result: []byte(`{}`)}}
					return nil
				}
				return p, nil
			}
			s := fakeSupervisor(t, u, Options{Start: proto.StartLazy, IdleTimeout: 20 * time.Millisecond})
			c := openConn(t, ctx, s)
			err := c.Write(ctx, message(t, `{"jsonrpc":"2.0","id":1,"method":"call"}`))
			if (err != nil) != fail {
				t.Fatalf("Write = %v", err)
			}
			if !fail {
				if _, err := c.Read(ctx); err != nil {
					t.Fatal(err)
				}
			}
			readError(t, ctx, c, upstream.ErrIdle)
		})
	}
}

func TestRestart(t *testing.T) {
	for _, start := range []proto.Start{proto.StartEager, proto.StartLazy} {
		t.Run(string(start), func(t *testing.T) {
			ctx := testContext(t)
			u := newFakeUpstream()
			s := fakeSupervisor(t, u, Options{Start: start})
			c := openConn(t, ctx, s)
			p := receive(t, ctx, u.opened)
			if err := s.Restart(); err != nil {
				t.Fatal(err)
			}
			readError(t, ctx, c, upstream.ErrRestarted)
			_ = c.Close()
			receive(t, ctx, p.closed)
			if start == proto.StartEager {
				receive(t, ctx, u.opened)
				waitState(t, ctx, s, control.MCPRunning)
			} else {
				waitState(t, ctx, s, control.MCPStopped)
			}
			if status := s.Status(); !status.NextRetry.IsZero() || status.Restarts != 1 {
				t.Fatalf("restart = %+v", status)
			}
			if start == proto.StartLazy {
				if err := s.Restart(); err != nil {
					t.Fatal(err)
				}
				if u.calls.Load() != 1 {
					t.Fatal("empty lazy restart started a process")
				}
			}
		})
	}
}

func TestStartFailureAndRetry(t *testing.T) {
	for _, start := range []proto.Start{proto.StartEager, proto.StartLazy} {
		t.Run(string(start), func(t *testing.T) {
			ctx := testContext(t)
			u := newFakeUpstream()
			failure := errors.New("command not found")
			u.open = func(context.Context) (mcp.Connection, error) {
				if u.calls.Load() <= 2 {
					return nil, failure
				}
				return newFakeConn(), nil
			}
			s := fakeSupervisor(t, u, Options{Start: start})
			if start == proto.StartLazy {
				if _, err := s.Open(ctx); !errors.Is(err, failure) {
					t.Fatalf("Open failure = %v", err)
				}
			}
			status := waitState(t, ctx, s, control.MCPFailed)
			if !strings.Contains(status.LastError, "command not found") || status.NextRetry.IsZero() {
				t.Fatalf("failure = %+v", status)
			}
			if _, err := s.Open(ctx); !errors.Is(err, failure) {
				t.Fatalf("second Open failure = %v", err)
			}
			if time.Now().Before(status.NextRetry) {
				t.Fatal("retried before backoff")
			}
			status = waitState(t, ctx, s, control.MCPFailed)
			c := openConn(t, ctx, s)
			if s.Status().StartedAt.Before(status.NextRetry) || s.Status().LastError != "" {
				t.Fatal("retry ignored backoff or retained error")
			}
			_ = c.Close()
		})
	}
}

func TestOpenCancellationDuringBackoff(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{Start: proto.StartLazy})
	c := openConn(t, ctx, s)
	p := receive(t, ctx, u.opened)
	p.incoming <- result{err: io.EOF}
	readError(t, ctx, c, io.EOF)
	waitState(t, ctx, s, control.MCPBackoff)
	short, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	if _, err := s.Open(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled Open = %v", err)
	}
	if u.calls.Load() != 1 {
		t.Fatal("Open started a process during backoff")
	}
	c = openConn(t, ctx, s)
	_ = c.Close()
}

func TestCallbacksAndStatus(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	var s *Supervisor
	var changes atomic.Int32
	var logWrites atomic.Int32
	var calls int
	var stderr io.Writer
	tools := []string{"echo"}
	s = New(Options{Name: "named", Start: proto.StartLazy, AllowTools: tools, LogSize: 5,
		New:      func(w io.Writer) upstream.Upstream { calls++; stderr = w; return u },
		OnChange: func() { _ = s.Status(); changes.Add(1) },
		OnLog:    func(chunk []byte) { _ = s.Status(); _ = s.Logs(5); clear(chunk); logWrites.Add(1) },
	})
	tools[0] = "mutated"
	if calls != 1 || s.Transport() != proto.TransportStdio {
		t.Fatal("upstream factory or transport")
	}
	if s.opts.IdleTimeout != 10*time.Minute {
		t.Fatal("idle default")
	}
	runSupervisor(t, s)
	c := openConn(t, ctx, s)
	s.SetToolCount(3)
	_, _ = stderr.Write([]byte("abcdefgh"))
	status := s.Status()
	if status.Name != "named" || status.Transport != "stdio" || status.Start != "lazy" || status.Tools != 3 || status.AllowTools[0] != "echo" || !status.Attached || status.LastActivity.IsZero() || status.StartedAt.IsZero() {
		t.Fatalf("status = %+v", status)
	}
	status.AllowTools[0] = "changed"
	if s.Status().AllowTools[0] != "echo" || string(s.Logs(100)) != "defgh" {
		t.Fatal("snapshot or log callback mutated retained data")
	}
	if logWrites.Load() != 1 {
		t.Fatal("OnLog did not fire")
	}
	_ = c.Close()
	waitFor(t, ctx, func() bool { return changes.Load() >= 4 })
}

func TestCancelledStartDoesNotFailNextOpen(t *testing.T) {
	for _, lateSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancelled error", true: "late success"}[lateSuccess], func(t *testing.T) {
			ctx := testContext(t)
			u := newFakeUpstream()
			started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			u.open = func(ctx context.Context) (mcp.Connection, error) {
				if u.calls.Load() == 1 {
					close(started)
					<-ctx.Done()
					close(cancelled)
					<-release
					if lateSuccess {
						return newFakeConn(), nil
					}
					return nil, ctx.Err()
				}
				return newFakeConn(), nil
			}
			s := fakeSupervisor(t, u, Options{Start: proto.StartLazy})
			var releaseOnce sync.Once
			releaseStart := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(releaseStart)
			firstCtx, cancel := context.WithCancel(ctx)
			first := make(chan error, 1)
			go func() { _, err := s.Open(firstCtx); first <- err }()
			receive(t, ctx, started)
			if s.Status().State != control.MCPStarting {
				t.Fatal("startup state not reported")
			}
			if _, err := s.Open(ctx); err == nil {
				t.Fatal("concurrent Open during startup succeeded")
			}
			cancel()
			if err := receive(t, ctx, first); !errors.Is(err, context.Canceled) {
				t.Fatalf("first Open = %v", err)
			}
			receive(t, ctx, cancelled)
			second := make(chan resultConn, 1)
			go func() { c, err := s.Open(ctx); second <- resultConn{c, err} }()
			waitFor(t, ctx, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.opening })
			releaseStart()
			got := receive(t, ctx, second)
			if got.err != nil {
				t.Fatalf("next Open inherited cancelled start: %v", got.err)
			}
			if status := s.Status(); status.LastError != "" || !status.NextRetry.IsZero() || status.Restarts != 0 {
				t.Fatalf("cancelled start = %+v", status)
			}
			_ = got.c.Close()
		})
	}
}

type resultConn struct {
	c   mcp.Connection
	err error
}

func TestRestartWhileStarting(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	started := make(chan struct{})
	u.open = func(ctx context.Context) (mcp.Connection, error) {
		if u.calls.Load() == 1 {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return newFakeConn(), nil
	}
	s := fakeSupervisor(t, u, Options{})
	receive(t, ctx, started)
	if err := s.Restart(); err != nil {
		t.Fatal(err)
	}
	waitState(t, ctx, s, control.MCPRunning)
	if status := s.Status(); status.LastError != "" || !status.NextRetry.IsZero() {
		t.Fatalf("restart during startup = %+v", status)
	}
}

func TestShutdownWhileStarting(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	started := make(chan struct{})
	u.open = func(ctx context.Context) (mcp.Connection, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	s := New(Options{Start: proto.StartLazy, New: func(io.Writer) upstream.Upstream { return u }})
	cancel, done := runSupervisor(t, s)
	opened := make(chan error, 1)
	go func() { _, err := s.Open(ctx); opened <- err }()
	receive(t, ctx, started)
	cancel()
	receive(t, ctx, done)
	if err := receive(t, ctx, opened); err == nil {
		t.Fatal("Open succeeded after shutdown")
	}
	if status := s.Status(); status.State != control.MCPStopped || status.LastError != "" {
		t.Fatalf("shutdown while starting = %+v", status)
	}
}

func TestIdleWaitsForResponseDeliveryAndWrites(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	started, release := make(chan struct{}), make(chan struct{})
	u.open = func(context.Context) (mcp.Connection, error) {
		p := newFakeConn()
		p.write = func(ctx context.Context, _ jsonrpc.Message) error {
			close(started)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			case <-p.closed:
				return mcp.ErrConnectionClosed
			}
		}
		u.opened <- p
		return p, nil
	}
	s := fakeSupervisor(t, u, Options{Start: proto.StartLazy, IdleTimeout: 20 * time.Millisecond})
	c := openConn(t, ctx, s)
	p := receive(t, ctx, u.opened)
	written := make(chan error, 1)
	go func() { written <- c.Write(ctx, &jsonrpc.Request{Method: "notification"}) }()
	receive(t, ctx, started)
	quiet, cancel := context.WithTimeout(ctx, 3*s.opts.IdleTimeout)
	readError(t, quiet, c, context.DeadlineExceeded)
	cancel()
	close(release)
	if err := receive(t, ctx, written); err != nil {
		t.Fatal(err)
	}
	readError(t, ctx, c, upstream.ErrIdle)
	_ = c.Close()

	// A response queued by the pump is still outstanding until the attached
	// channel reads it, even if that channel is slow to drain.
	c = openConn(t, ctx, s)
	p = receive(t, ctx, u.opened)
	// Replace only this fake's writer before it is used by a new channel.
	p.write = nil
	if err := c.Write(ctx, message(t, `{"jsonrpc":"2.0","id":1,"method":"call"}`)); err != nil {
		t.Fatal(err)
	}
	p.incoming <- result{msg: message(t, `{"jsonrpc":"2.0","id":1,"result":{}}`)}
	waitFor(t, ctx, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return len(s.p.queue) == 1 })
	deadline := time.NewTimer(3 * s.opts.IdleTimeout)
	defer deadline.Stop()
	select {
	case <-p.closed:
		t.Fatal("queued response was idle-killed")
	case <-deadline.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	readError(t, ctx, c, upstream.ErrIdle)
	_ = c.Close()
}

func TestRestartFailedEagerProcess(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	u.open = func(context.Context) (mcp.Connection, error) {
		if u.calls.Load() == 1 {
			return nil, errors.New("temporary startup failure")
		}
		return newFakeConn(), nil
	}
	s := fakeSupervisor(t, u, Options{})
	waitState(t, ctx, s, control.MCPFailed)
	// Put retry well into the future; an explicit restart must bypass it.
	s.mu.Lock()
	s.status.NextRetry = time.Now().Add(time.Hour)
	s.mu.Unlock()
	if err := s.Restart(); err != nil {
		t.Fatal(err)
	}
	waitState(t, ctx, s, control.MCPRunning)
	if status := s.Status(); status.Attached || status.Restarts != 0 || !status.NextRetry.IsZero() {
		t.Fatalf("restart failed prewarm = %+v", status)
	}
}

func TestShutdown(t *testing.T) {
	for _, attached := range []bool{false, true} {
		t.Run(map[bool]string{false: "prewarm", true: "attached"}[attached], func(t *testing.T) {
			ctx := testContext(t)
			u := newFakeUpstream()
			s := New(Options{New: func(io.Writer) upstream.Upstream { return u }})
			cancel, done := runSupervisor(t, s)
			receive(t, ctx, u.opened)
			waitState(t, ctx, s, control.MCPRunning)
			var c mcp.Connection
			if attached {
				c = openConn(t, ctx, s)
			}
			s.mu.Lock()
			p := s.p
			s.mu.Unlock()
			cancel()
			receive(t, ctx, done)
			receive(t, ctx, p.readerDone)
			receive(t, ctx, p.done)
			if attached {
				if _, err := c.Read(ctx); err == nil {
					t.Fatal("Read after shutdown succeeded")
				}
				_ = c.Close()
			}
			if _, err := s.Open(ctx); err == nil {
				t.Fatal("Open after shutdown succeeded")
			}
			if err := s.Restart(); err == nil {
				t.Fatal("Restart after shutdown succeeded")
			}
			if status := s.Status(); status.State != control.MCPStopped || status.Attached || status.PID != 0 {
				t.Fatalf("shutdown = %+v", status)
			}
		})
	}
}

func TestCancelledCallDoesNotBlockIdle(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{Start: proto.StartLazy, IdleTimeout: 30 * time.Millisecond})
	waitFor(t, ctx, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.ran })
	c := openConn(t, ctx, s)
	receive(t, ctx, u.opened)
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":7,"method":"tools/call"}`,
		`{"jsonrpc":"2.0","id":"s","method":"tools/call"}`,
		// The server will never answer these; cancellation must clear them.
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`,
		`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"s","reason":"timeout"}}`,
	} {
		if err := c.Write(ctx, message(t, raw)); err != nil {
			t.Fatal(err)
		}
	}
	readError(t, ctx, c, upstream.ErrIdle)
	_ = c.Close()
}

func TestNotificationSubscriptionDoesNotBlockIdle(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := fakeSupervisor(t, u, Options{Start: proto.StartLazy, IdleTimeout: 30 * time.Millisecond})
	c := openConn(t, ctx, s)
	p := receive(t, ctx, u.opened)
	subscription := message(t, `{"jsonrpc":"2.0","id":"notifications","method":"subscriptions/listen","params":{"notifications":{"toolsListChanged":true}}}`)
	if err := c.Write(ctx, subscription); err != nil {
		t.Fatal(err)
	}
	p.incoming <- result{msg: message(t, `{"jsonrpc":"2.0","method":"notifications/subscriptions/acknowledged","params":{}}`)}
	if _, err := c.Read(ctx); err != nil {
		t.Fatal(err)
	}
	readError(t, ctx, c, upstream.ErrIdle)
	receive(t, ctx, p.closed)
	waitState(t, ctx, s, control.MCPStopped)
	_ = c.Close()
}

// Output nobody reads is bounded: an unattached process's notifications are
// dropped past the limit, while a backlog an attached reader doesn't drain
// (or one with responses in it) stops the process as a failure.
func TestBacklogIsBounded(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	s := New(Options{Name: "test-mcp", New: func(io.Writer) upstream.Upstream { return u }})
	s.backlog, s.backlogBytes = 3, 1<<10
	runSupervisor(t, s)
	first := receive(t, ctx, u.opened)
	waitState(t, ctx, s, control.MCPRunning)
	queued := func() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.p.queue) }
	dropped := func() int { s.mu.Lock(); defer s.mu.Unlock(); return s.p.dropped }

	// Unattached: notifications past the limit are dropped, the process stays.
	for _, method := range []string{"one", "two", "three", "four", "five"} {
		first.incoming <- result{msg: &jsonrpc.Request{Method: method}}
	}
	waitFor(t, ctx, func() bool { return dropped() == 2 })
	if n := queued(); n != 3 || s.Status().State != control.MCPRunning {
		t.Fatalf("queued %d, state %s", n, s.Status().State)
	}
	// Attached and not reading: the next message stops the process.
	c := openConn(t, ctx, s)
	first.incoming <- result{msg: &jsonrpc.Request{Method: "six"}}
	receive(t, ctx, first.closed)
	// The unread backlog goes with the process; the reader learns why.
	readError(t, ctx, c, ErrBacklog)
	status := waitState(t, ctx, s, control.MCPBackoff)
	if !strings.Contains(status.LastError, "backlog limit") || status.Restarts != 1 {
		t.Fatalf("status = %+v", status)
	}

	// The byte budget counts too: one large result over it stops the next
	// process even with room in the queue.
	second := receive(t, ctx, u.opened)
	waitState(t, ctx, s, control.MCPRunning)
	id, _ := jsonrpc.MakeID(float64(1))
	big := &jsonrpc.Response{ID: id, Result: []byte(`"` + strings.Repeat("x", 2<<10) + `"`)}
	second.incoming <- result{msg: big}
	receive(t, ctx, second.closed)
	if status := waitState(t, ctx, s, control.MCPBackoff); !strings.Contains(status.LastError, "backlog limit") {
		t.Fatalf("status after large result = %+v", status)
	}
}
