package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

type fakeUpstream struct {
	gate         <-chan struct{}
	lateOpen     bool
	blockedWrite bool
	failure      error
	configure    func(*mcp.Server)
	started      chan struct{}
	opened       chan *fakeConnection
	mu           sync.Mutex
	sessions     []*mcp.ServerSession
}

type fakeConnection struct {
	mcp.Connection
	session      *mcp.ServerSession
	closed       chan struct{}
	once         sync.Once
	writeStarted chan struct{}
	writeOnce    sync.Once
}

func (c *fakeConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if c.writeStarted != nil {
		c.writeOnce.Do(func() { close(c.writeStarted) })
		<-c.closed
		return io.EOF
	}
	return c.Connection.Write(ctx, msg)
}

func (c *fakeConnection) Close() error {
	err := c.Connection.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}

func newUpstream(t *testing.T) *fakeUpstream {
	t.Helper()
	f := &fakeUpstream{started: make(chan struct{}, 16), opened: make(chan *fakeConnection, 16)}
	t.Cleanup(func() {
		f.mu.Lock()
		sessions := append([]*mcp.ServerSession(nil), f.sessions...)
		f.mu.Unlock()
		for _, session := range sessions {
			session.Close()
			session.Wait()
		}
	})
	return f
}

func (f *fakeUpstream) Transport() proto.Transport { return proto.TransportStdio }

func (f *fakeUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	f.started <- struct{}{}
	if f.gate != nil {
		if f.lateOpen {
			<-f.gate
			ctx = context.WithoutCancel(ctx)
		} else {
			select {
			case <-f.gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if f.failure != nil {
		return nil, f.failure
	}
	serverTransport, nodeTransport := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "fake-mcp", Version: "1.0"}, nil)
	if f.configure != nil {
		f.configure(server)
	}
	session, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		return nil, err
	}
	conn, err := nodeTransport.Connect(ctx)
	if err != nil {
		session.Close()
		return nil, err
	}
	f.mu.Lock()
	f.sessions = append(f.sessions, session)
	f.mu.Unlock()
	c := &fakeConnection{Connection: conn, session: session, closed: make(chan struct{})}
	if f.blockedWrite {
		c.writeStarted = make(chan struct{})
	}
	f.opened <- c
	return c, nil
}

type rig struct {
	t       *testing.T
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan error
	conns   chan *wire.Conn
	errors  chan error
	headers chan http.Header
	delays  chan time.Duration
}

func newRig(t *testing.T, cfg Config, customize func(*options)) *rig {
	t.Helper()
	ctx, timeoutCancel := context.WithTimeout(t.Context(), 10*time.Second)
	runctx, cancel := context.WithCancel(ctx)
	r := &rig{t: t, ctx: ctx, cancel: cancel, done: make(chan error, 1), conns: make(chan *wire.Conn, 16), errors: make(chan error, 16), headers: make(chan http.Header, 16), delays: make(chan time.Duration, 16)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := wire.Accept(w, req, 0)
		if err != nil {
			r.errors <- err
			return
		}
		defer conn.CloseNow()
		r.headers <- req.Header.Clone()
		r.conns <- conn
		<-runctx.Done()
	}))
	cfg.HubURL = "ws" + strings.TrimPrefix(server.URL, "http") + proto.NodePath
	cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	opts := options{minBackoff: time.Second, maxBackoff: 30 * time.Second, resetAfter: time.Minute,
		queueLimit: maxQueuedBytes,
		jitter:     func(d time.Duration) time.Duration { return d },
		wait: func(ctx context.Context, d time.Duration) error {
			select {
			case r.delays <- d:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	if customize != nil {
		customize(&opts)
	}
	go func() { r.done <- run(runctx, cfg, opts); close(r.done) }()
	t.Cleanup(func() {
		cancel()
		// testing cancels t.Context before cleanup functions run.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		select {
		case <-r.done:
		case <-cleanupCtx.Done():
			t.Error("Run did not finish cleanup before the deadline")
		}
		server.Close()
		timeoutCancel()
	})
	return r
}

func receive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatal("waiting for event:", ctx.Err())
		var zero T
		return zero
	}
}

func (r *rig) accept() *wire.Conn {
	r.t.Helper()
	select {
	case conn := <-r.conns:
		return conn
	case err := <-r.errors:
		r.t.Fatal("accept:", err)
	case <-r.ctx.Done():
		r.t.Fatal("waiting for hub connection:", r.ctx.Err())
	}
	return nil
}

func (r *rig) recv(conn *wire.Conn, typ proto.Type, ch string) *proto.Envelope {
	r.t.Helper()
	e, err := conn.Recv(r.ctx)
	if err != nil {
		r.t.Fatal("receive:", err)
	}
	if e.T != typ || e.Ch != ch {
		r.t.Fatalf("received %+v; want type %s channel %q", e, typ, ch)
	}
	return e
}

func (r *rig) send(conn *wire.Conn, e *proto.Envelope) {
	r.t.Helper()
	if err := conn.Send(r.ctx, e); err != nil {
		r.t.Fatal("send:", err)
	}
}

func (r *rig) welcome(conn *wire.Conn) *proto.Hello {
	r.t.Helper()
	hello := r.recv(conn, proto.THello, "").Hello
	r.send(conn, &proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{Version: proto.Version, NodeID: "node-id", MaxMessageSize: proto.DefaultMaxMessageSize, HubVersion: "v9.9.9"}})
	return hello
}

func (r *rig) open(conn *wire.Conn, name, ch string, up *fakeUpstream) *fakeConnection {
	r.t.Helper()
	r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: name, Ch: ch})
	e := r.recv(conn, proto.TOpened, ch)
	if e.Error != "" || e.MCP != name {
		r.t.Fatalf("open failed: %+v", e)
	}
	return receive(r.t, r.ctx, up.opened)
}

func TestWelcomeCarriesHubVersion(t *testing.T) {
	welcomes := make(chan proto.Welcome, 1)
	connected := make(chan bool, 1)
	r := newRig(t, Config{Node: "desk", MCPs: []MCP{{Name: "tools", Upstream: newUpstream(t)}},
		OnWelcome: func(w proto.Welcome) {
			select {
			case <-connected:
				t.Error("OnWelcome after OnConnState")
			default:
			}
			welcomes <- w
		},
		OnConnState: func(c bool, _ error) {
			if c {
				connected <- c
			}
		}}, nil)
	r.welcome(r.accept())
	if w := receive(t, r.ctx, welcomes); w.HubVersion != "v9.9.9" {
		t.Fatalf("hub version = %q", w.HubVersion)
	}
	receive(t, r.ctx, connected)
}

func TestHelloAndRoundTrip(t *testing.T) {
	up := newUpstream(t)
	r := newRig(t, Config{Node: "desk", Version: "v0.1", Header: http.Header{"Authorization": {"Bearer test"}}, MCPs: []MCP{{Name: "tools", Upstream: up, Sharing: proto.SharingShared}}}, nil)
	conn := r.accept()
	hello := r.welcome(conn)
	want := &proto.Hello{Version: proto.Version, Node: "desk", NodeVersion: "v0.1", OS: runtime.GOOS, MCPs: []proto.MCP{{Name: "tools", Transport: proto.TransportStdio, Sharing: proto.SharingShared, Start: proto.StartEager}}}
	if !reflect.DeepEqual(hello, want) {
		t.Fatalf("hello = %+v, want %+v", hello, want)
	}
	if got := receive(t, r.ctx, r.headers).Get("Authorization"); got != "Bearer test" {
		t.Fatalf("Authorization = %q", got)
	}
	r.open(conn, "tools", "ch1", up)
	// These frames must leave the channel usable and produce no responses.
	r.send(conn, &proto.Envelope{T: proto.TMCP, Ch: "missing", Msg: json.RawMessage(`{}`)})
	r.send(conn, &proto.Envelope{T: proto.TClose, Ch: "missing"})
	r.send(conn, &proto.Envelope{T: proto.TMCP, Ch: "ch1", Msg: json.RawMessage(`{"jsonrpc":"wrong","method":"initialize"}`)})
	r.send(conn, &proto.Envelope{T: proto.TMCP, MCP: "tools", Ch: "ch1", Msg: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"hub-test","version":"1"},"capabilities":{}}}`)})
	e := r.recv(conn, proto.TMCP, "ch1")
	if e.MCP != "tools" {
		t.Fatalf("response MCP = %q", e.MCP)
	}
	message, err := jsonrpc.DecodeMessage(e.Msg)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.Error != nil || response.ID.Raw() != int64(1) {
		t.Fatalf("initialize response = %+v", message)
	}
	var result mcp.InitializeResult
	if err := json.Unmarshal(response.Result, &result); err != nil {
		t.Fatal(err)
	}
	if result.ServerInfo.Name != "fake-mcp" || result.ProtocolVersion != "2025-11-25" {
		t.Fatalf("initialize result = %+v", result)
	}
}

func TestOpenErrors(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(map[bool]string{false: "open", true: "pending"}[pending], func(t *testing.T) {
			up := newUpstream(t)
			gate := make(chan struct{})
			if pending {
				up.gate = gate
			}
			r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
			conn := r.accept()
			r.welcome(conn)
			r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "missing", Ch: "bad"})
			if e := r.recv(conn, proto.TOpened, "bad"); !strings.Contains(e.Error, "unknown MCP") {
				t.Fatalf("unknown MCP: %+v", e)
			}
			r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: "first"})
			receive(t, r.ctx, up.started)
			if !pending {
				if e := r.recv(conn, proto.TOpened, "first"); e.Error != "" {
					t.Fatal(e.Error)
				}
			}
			r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: "second"})
			if e := r.recv(conn, proto.TOpened, "second"); !strings.Contains(e.Error, "already has a channel") {
				t.Fatalf("second shared channel: %+v", e)
			}
			if pending {
				close(gate)
				if e := r.recv(conn, proto.TOpened, "first"); e.Error != "" {
					t.Fatal(e.Error)
				}
			}
		})
	}
	t.Run("upstream failure releases reservation", func(t *testing.T) {
		up := newUpstream(t)
		up.failure = errors.New("start failed")
		r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
		conn := r.accept()
		r.welcome(conn)
		for _, id := range []string{"first", "retry"} {
			r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: id})
			if e := r.recv(conn, proto.TOpened, id); !strings.Contains(e.Error, "start failed") {
				t.Fatalf("Open failure: %+v", e)
			}
		}
	})
}

func TestChannelClose(t *testing.T) {
	for _, byHub := range []bool{false, true} {
		t.Run(map[bool]string{false: "upstream dies", true: "hub closes"}[byHub], func(t *testing.T) {
			up := newUpstream(t)
			r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
			conn := r.accept()
			r.welcome(conn)
			opened := r.open(conn, "tools", "ch1", up)
			if byHub {
				r.send(conn, &proto.Envelope{T: proto.TClose, Ch: "ch1"})
			} else {
				if err := opened.session.Close(); err != nil {
					t.Fatal(err)
				}
				if e := r.recv(conn, proto.TClose, "ch1"); e.Error == "" || e.MCP != "tools" {
					t.Fatalf("upstream close: %+v", e)
				}
			}
			receive(t, r.ctx, opened.closed)
			// A retired channel must release the shared MCP reservation.
			r.open(conn, "tools", "ch2", up)
		})
	}
}

func TestReconnectClosesAllChannels(t *testing.T) {
	a, b := newUpstream(t), newUpstream(t)
	r := newRig(t, Config{Node: "desk", MCPs: []MCP{{Name: "a", Upstream: a}, {Name: "b", Upstream: b}}}, nil)
	conn := r.accept()
	hello := r.welcome(conn)
	first, second := r.open(conn, "a", "ch1", a), r.open(conn, "b", "ch2", b)
	conn.CloseNow()
	conn = r.accept()
	// Reconnection cannot start until all old channel workers have stopped.
	for _, c := range []*fakeConnection{first, second} {
		select {
		case <-c.closed:
		default:
			t.Fatal("upstream still open after reconnect")
		}
	}
	if got := r.welcome(conn); !reflect.DeepEqual(got, hello) {
		t.Fatalf("reconnect hello = %+v, want %+v", got, hello)
	}
	r.open(conn, "a", "ch1", a)
}

func TestPermanentClose(t *testing.T) {
	for _, code := range []int{proto.CloseNodeNameTaken, proto.CloseUnsupportedVersion} {
		for _, welcomed := range []bool{false, true} {
			t.Run(strconv.Itoa(code)+"/"+map[bool]string{false: "handshake", true: "established"}[welcomed], func(t *testing.T) {
				r := newRig(t, Config{}, nil)
				conn := r.accept()
				if welcomed {
					r.welcome(conn)
				} else {
					r.recv(conn, proto.THello, "")
				}
				if code == proto.CloseNodeNameTaken {
					conn.Close(proto.CloseNodeNameTaken, "rejected")
				} else {
					conn.Close(proto.CloseUnsupportedVersion, "rejected")
				}
				err := receive(t, r.ctx, r.done)
				if err == nil || int(wire.CloseStatus(err)) != code {
					t.Fatalf("Run = %v, want close code %d", err, code)
				}
				select {
				case <-r.delays:
					t.Fatal("permanent rejection scheduled a reconnect")
				default:
				}
			})
		}
	}
}

func TestUnsupportedWelcomeVersion(t *testing.T) {
	r := newRig(t, Config{}, nil)
	conn := r.accept()
	r.recv(conn, proto.THello, "")
	r.send(conn, &proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{Version: proto.Version + 1, NodeID: "node-id"}})
	err := receive(t, r.ctx, r.done)
	if !errors.As(err, new(errUnsupportedVersion)) {
		t.Fatalf("Run = %v, want errUnsupportedVersion", err)
	}
	select {
	case <-r.delays:
		t.Fatal("unsupported version scheduled a reconnect")
	default:
	}
}

func TestCancel(t *testing.T) {
	for _, phase := range []string{"welcome", "open", "relay"} {
		t.Run(phase, func(t *testing.T) {
			up := newUpstream(t)
			if phase == "open" {
				up.gate = make(chan struct{})
			}
			r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
			conn := r.accept()
			var opened *fakeConnection
			if phase == "welcome" {
				r.recv(conn, proto.THello, "")
			} else {
				r.welcome(conn)
				if phase == "open" {
					r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: "ch1"})
					receive(t, r.ctx, up.started)
				} else {
					opened = r.open(conn, "tools", "ch1", up)
				}
			}
			r.cancel()
			if err := receive(t, r.ctx, r.done); !errors.Is(err, context.Canceled) {
				t.Fatalf("Run = %v, want context.Canceled", err)
			}
			if opened != nil {
				select {
				case <-opened.closed:
				default:
					t.Fatal("Run returned with an open upstream")
				}
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "exponential and bounded", true: "reset after long connection"}[reset], func(t *testing.T) {
			r := newRig(t, Config{}, func(opts *options) {
				opts.maxBackoff = 4 * time.Second
				if reset {
					// Every connection exceeds this threshold without a test sleep.
					opts.resetAfter = 0
				}
			})
			for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second} {
				conn := r.accept()
				r.welcome(conn)
				conn.CloseNow()
				if reset {
					want = time.Second
				}
				if got := receive(t, r.ctx, r.delays); got != want {
					t.Fatalf("backoff = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestCancelDuringBackoff(t *testing.T) {
	waiting := make(chan struct{})
	r := newRig(t, Config{}, func(opts *options) {
		opts.wait = func(ctx context.Context, _ time.Duration) error {
			close(waiting)
			<-ctx.Done()
			return ctx.Err()
		}
	})
	conn := r.accept()
	r.recv(conn, proto.THello, "")
	conn.CloseNow()
	receive(t, r.ctx, waiting)
	r.cancel()
	if err := receive(t, r.ctx, r.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

func TestCancelClosesLateOpen(t *testing.T) {
	up := newUpstream(t)
	gate := make(chan struct{})
	up.gate, up.lateOpen = gate, true
	r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
	var release sync.Once
	t.Cleanup(func() { release.Do(func() { close(gate) }) })
	conn := r.accept()
	r.welcome(conn)
	r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: "ch1"})
	receive(t, r.ctx, up.started)
	r.cancel()
	// Model an upstream that finishes opening just as cancellation arrives.
	release.Do(func() { close(gate) })
	if err := receive(t, r.ctx, r.done); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	opened := receive(t, r.ctx, up.opened)
	select {
	case <-opened.closed:
	default:
		t.Fatal("Run returned without closing the late upstream")
	}
}

func TestHubCloseInterruptsBlockedWrite(t *testing.T) {
	up := newUpstream(t)
	up.blockedWrite = true
	r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
	conn := r.accept()
	r.welcome(conn)
	opened := r.open(conn, "tools", "ch1", up)
	r.send(conn, &proto.Envelope{T: proto.TMCP, Ch: "ch1", Msg: json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)})
	receive(t, r.ctx, opened.writeStarted)
	r.send(conn, &proto.Envelope{T: proto.TClose, Ch: "ch1"})
	receive(t, r.ctx, opened.closed)
	r.open(conn, "tools", "ch2", up)
}

// An upstream that stops reading must not make the node hold the hub's
// messages without end: past the byte budget the channel closes, with a
// reason, and other channels carry on.
func TestStuckUpstreamClosesChannel(t *testing.T) {
	up := newUpstream(t)
	up.blockedWrite = true
	r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, func(o *options) { o.queueLimit = 100 })
	conn := r.accept()
	r.welcome(conn)
	opened := r.open(conn, "tools", "ch1", up)
	ping := func(id int) *proto.Envelope {
		return &proto.Envelope{T: proto.TMCP, Ch: "ch1", Msg: json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"ping"}`, id))}
	}
	// The first write blocks in the upstream; what follows waits in the queue.
	r.send(conn, ping(1))
	receive(t, r.ctx, opened.writeStarted)
	r.send(conn, ping(2))
	r.send(conn, ping(3)) // 3 × ~40 bytes > 100
	e := r.recv(conn, proto.TClose, "ch1")
	if !strings.Contains(e.Error, "not reading its input") {
		t.Fatalf("close = %+v", e)
	}
	receive(t, r.ctx, opened.closed)
	r.open(conn, "tools", "ch2", up)
}

// A result larger than the hub accepts would make the hub close the whole
// connection; the node answers the request with an error instead.
func TestTooLargeResultBecomesError(t *testing.T) {
	up := newUpstream(t)
	up.configure = func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "big", InputSchema: map[string]any{"type": "object"}},
			func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: strings.Repeat("x", 100<<10)}}}, nil
			})
	}
	r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}}, nil)
	conn := r.accept()
	r.recv(conn, proto.THello, "")
	// The hub accepts envelopes of 1 KiB + 64 KiB headroom.
	r.send(conn, &proto.Envelope{T: proto.TWelcome, Welcome: &proto.Welcome{Version: proto.Version, NodeID: "n", MaxMessageSize: 1 << 10}})
	r.open(conn, "tools", "ch1", up)
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"hub-test","version":"1"},"capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"big","arguments":{}}}`,
	} {
		r.send(conn, &proto.Envelope{T: proto.TMCP, Ch: "ch1", Msg: json.RawMessage(msg)})
	}
	r.recv(conn, proto.TMCP, "ch1") // initialize result
	e := r.recv(conn, proto.TMCP, "ch1")
	message, err := jsonrpc.DecodeMessage(e.Msg)
	if err != nil {
		t.Fatal(err)
	}
	resp, ok := message.(*jsonrpc.Response)
	if !ok || resp.ID.Raw() != int64(2) || resp.Error == nil || !strings.Contains(resp.Error.Error(), "too large to relay") {
		t.Fatalf("tools/call answer = %+v", message)
	}
	// The channel still works.
	r.send(conn, &proto.Envelope{T: proto.TMCP, Ch: "ch1", Msg: json.RawMessage(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)})
	if e := r.recv(conn, proto.TMCP, "ch1"); !strings.Contains(string(e.Msg), `"id":3`) {
		t.Fatalf("ping answer = %s", e.Msg)
	}
}

func TestJitterBounds(t *testing.T) {
	for _, jitter := range []time.Duration{0, time.Hour} {
		t.Run(jitter.String(), func(t *testing.T) {
			r := newRig(t, Config{}, func(opts *options) {
				opts.jitter = func(time.Duration) time.Duration { return jitter }
			})
			conn := r.accept()
			r.welcome(conn)
			conn.CloseNow()
			want := time.Second
			if jitter == time.Hour {
				want = 30 * time.Second
			}
			if got := receive(t, r.ctx, r.delays); got != want {
				t.Fatalf("jittered backoff = %v, want %v", got, want)
			}
		})
	}
}

func TestRunCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Run(ctx, Config{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
}

func TestHelloCarriesStartAndCachedTools(t *testing.T) {
	up := newUpstream(t)
	calls := 0
	cached := func() json.RawMessage {
		calls++
		return json.RawMessage(fmt.Sprintf(`[{"name":"t%d"}]`, calls))
	}
	r := newRig(t, Config{Node: "desk", MCPs: []MCP{{Name: "tools", Upstream: up, Start: proto.StartLazy, CachedTools: cached}}}, nil)
	conn := r.accept()
	hello := r.welcome(conn)
	if m := hello.MCPs[0]; m.Start != proto.StartLazy || string(m.Tools) != `[{"name":"t1"}]` {
		t.Fatalf("first hello MCP = %+v (tools %s)", m, m.Tools)
	}
	// The cache is read again on reconnect.
	conn.CloseNow()
	conn = r.accept()
	if m := r.welcome(conn).MCPs[0]; string(m.Tools) != `[{"name":"t2"}]` {
		t.Fatalf("second hello tools = %s", m.Tools)
	}
}

func TestCloseReason(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want proto.CloseReason
	}{
		{fmt.Errorf("read fs: %w", upstream.ErrIdle), proto.CloseIdle},
		{fmt.Errorf("read fs: %w", upstream.ErrRestarted), proto.CloseRestart},
		{errors.New("exit status 1"), ""},
		{nil, ""},
	} {
		if got := closeReason(tt.err); got != tt.want {
			t.Errorf("closeReason(%v) = %q, want %q", tt.err, got, tt.want)
		}
	}
}

func TestOnConnState(t *testing.T) {
	states := make(chan bool, 4)
	r := newRig(t, Config{OnConnState: func(connected bool, _ error) { states <- connected }}, nil)
	conn := r.accept()
	r.welcome(conn)
	if !receive(t, r.ctx, states) {
		t.Fatal("first state should be connected")
	}
	conn.CloseNow()
	if receive(t, r.ctx, states) {
		t.Fatal("second state should be disconnected")
	}
}

type fakeClientUpstream struct {
	*fakeUpstream
	clients chan string
	max     int
	mu      sync.Mutex
	live    int
}

func (f *fakeClientUpstream) OpenClient(ctx context.Context, client string) (mcp.Connection, error) {
	f.mu.Lock()
	if f.live >= f.max {
		f.mu.Unlock()
		return nil, errors.New("too many clients for tools (max_clients 2)")
	}
	f.live++
	f.mu.Unlock()
	f.clients <- client
	conn, err := f.fakeUpstream.Open(ctx)
	if err != nil {
		return nil, err
	}
	return &releasingConn{Connection: conn, release: func() { f.mu.Lock(); f.live--; f.mu.Unlock() }}, nil
}

type releasingConn struct {
	mcp.Connection
	once    sync.Once
	release func()
}

func (c *releasingConn) Close() error {
	err := c.Connection.Close()
	c.once.Do(c.release)
	return err
}

func TestPerClientChannels(t *testing.T) {
	up := &fakeClientUpstream{fakeUpstream: newUpstream(t), clients: make(chan string, 8), max: 2}
	r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up, Sharing: proto.SharingPerClient, Start: proto.StartLazy}}}, nil)
	conn := r.accept()
	r.welcome(conn)
	open := func(ch, client string) *proto.Envelope {
		r.send(conn, &proto.Envelope{T: proto.TOpen, MCP: "tools", Ch: ch, Client: client})
		return r.recv(conn, proto.TOpened, ch)
	}
	if e := open("a", "alice"); e.Error != "" {
		t.Fatal(e.Error)
	}
	if e := open("b", "bob"); e.Error != "" {
		t.Fatal(e.Error)
	}
	if got := []string{receive(t, r.ctx, up.clients), receive(t, r.ctx, up.clients)}; got[0] != "alice" || got[1] != "bob" {
		t.Fatalf("clients = %v", got)
	}
	ca, cb := receive(t, r.ctx, up.opened), receive(t, r.ctx, up.opened)
	if ca == cb {
		t.Fatal("channels share a connection")
	}
	if e := open("c", "carol"); !strings.Contains(e.Error, "too many clients") {
		t.Fatalf("third channel: %+v", e)
	}
	// Closing one channel frees its slot and leaves the other open.
	r.send(conn, &proto.Envelope{T: proto.TClose, MCP: "tools", Ch: "a"})
	receive(t, r.ctx, ca.closed)
	select {
	case <-cb.closed:
		t.Fatal("closing channel a closed channel b")
	default:
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if e := open("d", "dave"); e.Error == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot not freed")
		}
		time.Sleep(time.Millisecond)
	}
}

// InFlight counts the hub's requests until their responses go back; a
// cancellation or the channel's end stops counting them too.
func TestInFlightCounting(t *testing.T) {
	// Each call waits for one release token, or its cancellation.
	release := make(chan struct{}, 1)
	up := newUpstream(t)
	up.configure = func(s *mcp.Server) {
		s.AddTool(&mcp.Tool{Name: "wait", InputSchema: json.RawMessage(`{"type":"object"}`)},
			func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return &mcp.CallToolResult{}, nil
			})
	}
	var inFlight atomic.Int64
	r := newRig(t, Config{MCPs: []MCP{{Name: "tools", Upstream: up}}, InFlight: &inFlight}, nil)
	conn := r.accept()
	r.welcome(conn)
	r.open(conn, "tools", "ch1", up)
	mcpFrame := func(msg string) {
		r.send(conn, &proto.Envelope{T: proto.TMCP, MCP: "tools", Ch: "ch1", Msg: json.RawMessage(msg)})
	}
	waitCount := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for inFlight.Load() != want {
			if time.Now().After(deadline) {
				t.Fatalf("in flight = %d, want %d", inFlight.Load(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	call := func(id int) {
		mcpFrame(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"wait","arguments":{}}}`, id))
	}

	mcpFrame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","clientInfo":{"name":"hub-test","version":"1"},"capabilities":{}}}`)
	r.recv(conn, proto.TMCP, "ch1")
	waitCount(0)
	mcpFrame(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

	call(2)
	waitCount(1)
	release <- struct{}{}
	r.recv(conn, proto.TMCP, "ch1")
	waitCount(0)

	call(3)
	waitCount(1)
	mcpFrame(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":3}}`)
	waitCount(0)

	call(4)
	waitCount(1)
	r.send(conn, &proto.Envelope{T: proto.TClose, Ch: "ch1"})
	waitCount(0)
}

// An MCP may leave a cancelled request unanswered, so the cancellation
// itself stops counting it.
func TestFromHubCancellation(t *testing.T) {
	var inFlight atomic.Int64
	ch := &channel{inFlight: &inFlight, pending: make(map[jsonrpc.ID]struct{})}
	decode := func(s string) jsonrpc.Message {
		m, err := jsonrpc.DecodeMessage([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	ch.fromHub(decode(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}}`))
	ch.fromHub(decode(`{"jsonrpc":"2.0","id":"s","method":"tools/call","params":{"name":"x"}}`))
	ch.fromHub(decode(`{"jsonrpc":"2.0","method":"notifications/progress","params":{}}`))
	// Long-lived on purpose, and quick listings: not calls.
	ch.fromHub(decode(`{"jsonrpc":"2.0","id":8,"method":"subscriptions/listen","params":{}}`))
	ch.fromHub(decode(`{"jsonrpc":"2.0","id":9,"method":"tools/list","params":{}}`))
	if got := inFlight.Load(); got != 2 {
		t.Fatalf("in flight = %d, want 2", got)
	}
	ch.fromHub(decode(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":7}}`))
	ch.fromHub(decode(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"s"}}`))
	ch.fromHub(decode(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":99}}`))
	if got := inFlight.Load(); got != 0 {
		t.Fatalf("in flight after cancellations = %d, want 0", got)
	}
}
