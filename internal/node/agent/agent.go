// Package agent connects a node to the hub and relays its MCP channels.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

// MCP describes an upstream offered to the hub. Empty Sharing defaults to
// shared and empty Start to eager.
type MCP struct {
	Name     string
	Upstream upstream.Upstream
	Sharing  proto.Sharing
	Start    proto.Start
	// CallTimeout bounds one call on the hub; zero means the hub's default.
	CallTimeout time.Duration
	// MaxClients caps channels open at once on a per-client MCP.
	MaxClients int
	// CachedTools returns the MCP's last known tool list (a JSON array), or
	// nil. It is called for every hello, so it can change between connections.
	CachedTools func() json.RawMessage
}

// ClientUpstream is an Upstream that starts one process per client. A
// per-client MCP's Upstream implements it; the agent passes the open
// envelope's Client (empty for a listing channel) and each channel gets its
// own connection.
type ClientUpstream interface {
	upstream.Upstream
	OpenClient(ctx context.Context, client string) (mcp.Connection, error)
}

// Config describes the node and its hub connection.
type Config struct {
	HubURL         string
	Node           string
	Version        string
	MCPs           []MCP
	Header         http.Header
	MaxMessageSize int64 // Zero uses proto.DefaultMaxMessageSize.
	Logger         *slog.Logger
	// InFlight, if set, counts the calls (tools, prompts, resource reads)
	// the node is relaying that have no response yet, across every channel. The daemon waits for it to
	// drop to zero before a reload reconnects.
	InFlight *atomic.Int64
	// OnConnState, if set, is called with true once the hub has welcomed the
	// node and with false (and the reason) when that connection ends or a
	// connection attempt fails.
	OnConnState func(connected bool, err error)
}

type options struct {
	minBackoff, maxBackoff, resetAfter time.Duration
	jitter                             func(time.Duration) time.Duration
	wait                               func(context.Context, time.Duration) error
	// queueLimit is each channel's maxQueuedBytes; tests lower it.
	queueLimit int
}

// Run serves channels until cancellation, reconnecting after transient failures.
func Run(ctx context.Context, cfg Config) error {
	return run(ctx, cfg, options{
		minBackoff: time.Second,
		maxBackoff: 30 * time.Second,
		resetAfter: time.Minute,
		jitter: func(d time.Duration) time.Duration {
			return d/2 + time.Duration(rand.Int64N(int64(d)+1))
		},
		wait:       wait,
		queueLimit: maxQueuedBytes,
	})
}

func wait(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func run(ctx context.Context, cfg Config, opts options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.InFlight == nil {
		cfg.InFlight = new(atomic.Int64)
	}
	mcps := make(map[string]MCP, len(cfg.MCPs))
	for _, m := range cfg.MCPs {
		if m.Name == "" || m.Upstream == nil {
			return fmt.Errorf("MCP %q requires a name and upstream", m.Name)
		}
		if _, exists := mcps[m.Name]; exists {
			return fmt.Errorf("duplicate MCP %q", m.Name)
		}
		if m.Sharing == "" {
			m.Sharing = proto.SharingShared
		}
		switch m.Sharing {
		case proto.SharingShared:
		case proto.SharingPerClient:
			if _, ok := m.Upstream.(ClientUpstream); !ok {
				return fmt.Errorf("MCP %q: per-client sharing needs an upstream that opens per client", m.Name)
			}
		default:
			return fmt.Errorf("MCP %q: unsupported sharing %q", m.Name, m.Sharing)
		}
		if m.Start == "" {
			m.Start = proto.StartEager
		}
		if m.Start != proto.StartEager && m.Start != proto.StartLazy {
			return fmt.Errorf("MCP %q: unsupported start %q", m.Name, m.Start)
		}
		mcps[m.Name] = m
	}
	newHello := func() *proto.Hello {
		h := &proto.Hello{Version: proto.Version, Node: cfg.Node, NodeVersion: cfg.Version, OS: runtime.GOOS, MCPs: make([]proto.MCP, 0, len(cfg.MCPs))}
		for _, c := range cfg.MCPs {
			m := mcps[c.Name]
			d := proto.MCP{Name: m.Name, Transport: m.Upstream.Transport(), Sharing: m.Sharing, Start: m.Start,
				CallTimeoutMs: m.CallTimeout.Milliseconds(), MaxClients: m.MaxClients}
			if m.CachedTools != nil {
				d.Tools = m.CachedTools()
			}
			h.MCPs = append(h.MCPs, d)
		}
		return h
	}
	backoff := opts.minBackoff
	rejectionLogged := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		conn, err := wire.Dial(ctx, cfg.HubURL, cfg.Header, cfg.MaxMessageSize)
		if err == nil {
			started := time.Now()
			err = serve(ctx, conn, newHello(), mcps, cfg.Logger, cfg.OnConnState, opts.queueLimit, cfg.InFlight)
			if time.Since(started) >= opts.resetAfter {
				backoff = opts.minBackoff
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rejected := false
		if reason, ok := keyRejection(err); ok {
			rejected = true
			err = &KeyRejectedError{Reason: reason, Node: cfg.Node, HasKey: cfg.Header.Get("Authorization") != ""}
		}
		if cfg.OnConnState != nil {
			cfg.OnConnState(false, err)
		}
		code := wire.CloseStatus(err)
		if code == proto.CloseUnsupportedVersion || code == proto.CloseNodeNameTaken {
			return fmt.Errorf("hub rejected node: %w", err)
		}
		if errors.As(err, new(errUnsupportedVersion)) {
			return err
		}
		delay := min(opts.maxBackoff, max(opts.minBackoff, opts.jitter(backoff)))
		if rejected {
			// Only the hub admin can fix this, perhaps while we wait; do not
			// hammer the hub, and say it loudly once rather than every try.
			delay = opts.maxBackoff
			level := slog.LevelDebug
			if !rejectionLogged {
				level = slog.LevelWarn
			}
			rejectionLogged = true
			cfg.Logger.Log(ctx, level, "hub rejected the node key; retrying", "error", err, "delay", delay)
		} else {
			rejectionLogged = false
			cfg.Logger.Warn("hub connection lost; reconnecting", "error", err, "delay", delay)
		}
		if err := opts.wait(ctx, delay); err != nil {
			return err
		}
		backoff = min(opts.maxBackoff, backoff*2)
	}
}

// KeyRejectedError means the hub refused the node's key (or the lack of one):
// an HTTP 401 at the upgrade or a close with proto.CloseInvalidKey. A 403 is
// not a key problem: the hub refuses plain HTTP from non-local networks that
// way, and its message says so.
type KeyRejectedError struct {
	// Reason is the hub's explanation, if it gave one.
	Reason string
	Node   string
	// HasKey is false when the node sent no key at all.
	HasKey bool
}

func (e *KeyRejectedError) Error() string {
	if !e.HasKey {
		return fmt.Sprintf("hub requires a node key; create one on the hub with mcp-mesh-hub key create node %s, then mcp-mesh-node key set <key>", e.Node)
	}
	reason := e.Reason
	if reason == "" {
		reason = "unauthorized"
	}
	return fmt.Sprintf("hub rejected the node key: %s; set a key created on the hub for node %q with mcp-mesh-node key set", reason, e.Node)
}

// Short is the reason without the advice, for a status card.
func (e *KeyRejectedError) Short() string {
	switch {
	case !e.HasKey:
		return "no key set"
	case e.Reason == "":
		return "unauthorized"
	}
	return e.Reason
}

// keyRejection recognizes the two ways the hub says no to a key.
func keyRejection(err error) (reason string, ok bool) {
	if err == nil {
		return "", false
	}
	var httpErr *wire.HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == http.StatusUnauthorized {
		return httpReason(httpErr.Body), true
	}
	if wire.CloseStatus(err) == proto.CloseInvalidKey {
		return wire.CloseReason(err), true
	}
	return "", false
}

// httpReason pulls a short message out of a plain-text or JSON error body.
func httpReason(body string) string {
	if strings.HasPrefix(body, "{") {
		var parsed struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal([]byte(body), &parsed) == nil {
			body = parsed.Error + parsed.Message
		}
	}
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	if len(line) > 200 {
		line = line[:200]
	}
	return line
}

// errUnsupportedVersion means the hub chose a protocol version this node
// does not speak; retrying will not help.
type errUnsupportedVersion struct{ version int }

func (e errUnsupportedVersion) Error() string {
	return fmt.Sprintf("hub chose protocol version %d; this node speaks %d to %d", e.version, proto.MinVersion, proto.Version)
}

// closeReason tells the hub whether a channel ended on purpose.
func closeReason(err error) proto.CloseReason {
	switch {
	case errors.Is(err, upstream.ErrIdle):
		return proto.CloseIdle
	case errors.Is(err, upstream.ErrRestarted):
		return proto.CloseRestart
	}
	return ""
}

type received struct {
	env *proto.Envelope
	err error
}

type event struct {
	channel *channel
	opened  bool
	err     error
}

func serve(parent context.Context, conn *wire.Conn, hello *proto.Hello, mcps map[string]MCP, logger *slog.Logger, onState func(bool, error), queueLimit int, inFlight *atomic.Int64) error {
	ctx, cancel := context.WithCancel(parent)
	var workers sync.WaitGroup
	channels := make(map[string]*channel)
	defer func() {
		cancel()
		conn.CloseNow()
		workers.Wait()
	}()
	workers.Go(func() { conn.Heartbeat(ctx) })
	if err := conn.Send(ctx, &proto.Envelope{T: proto.THello, Hello: hello}); err != nil {
		return fmt.Errorf("send hello: %w", err)
	}
	welcome, err := conn.Recv(ctx)
	if err != nil {
		return fmt.Errorf("receive welcome: %w", err)
	}
	if welcome.T != proto.TWelcome || welcome.Welcome == nil {
		return fmt.Errorf("expected welcome, received %q", welcome.T)
	}
	if v := welcome.Welcome.Version; v < proto.MinVersion || v > proto.Version {
		return errUnsupportedVersion{v}
	}
	conn.SetPeerLimit(welcome.Welcome.MaxMessageSize)
	if onState != nil {
		onState(true, nil)
	}
	incoming := make(chan received)
	events := make(chan event)
	workers.Go(func() {
		for {
			env, err := conn.Recv(ctx)
			select {
			case incoming <- received{env, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	})
	send := func(e *proto.Envelope) error {
		if err := conn.Send(ctx, e); err != nil {
			return fmt.Errorf("send %s: %w", e.T, err)
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev := <-events:
			ch := ev.channel
			// A close may have retired this worker while Open was finishing.
			if channels[ch.id] != ch {
				continue
			}
			typ := proto.TClose
			if ev.opened {
				typ = proto.TOpened
			}
			e := &proto.Envelope{T: typ, MCP: ch.mcp, Ch: ch.id}
			if ev.err != nil {
				e.Error = ev.err.Error()
			}
			if !ev.opened {
				e.Reason = closeReason(ev.err)
			}
			if !ev.opened || ev.err != nil {
				delete(channels, ch.id)
				ch.cancel()
			}
			if err := send(e); err != nil {
				return err
			}
			if ev.opened && ev.err == nil {
				close(ch.ready)
			}
		case in := <-incoming:
			if in.err != nil {
				return fmt.Errorf("receive hub frame: %w", in.err)
			}
			e := in.env
			switch e.T {
			case proto.TOpen:
				m, exists := mcps[e.MCP]
				var reason string
				if !exists {
					reason = fmt.Sprintf("unknown MCP %q", e.MCP)
				} else if _, exists := channels[e.Ch]; exists {
					reason = fmt.Sprintf("channel %q already exists", e.Ch)
				} else {
					for _, ch := range channels {
						// Per-client MCPs cap their channels themselves (max_clients).
						if ch.mcp == e.MCP && m.Sharing == proto.SharingShared {
							reason = fmt.Sprintf("shared MCP %q already has a channel", e.MCP)
							break
						}
					}
				}
				if reason != "" {
					if err := send(&proto.Envelope{T: proto.TOpened, MCP: e.MCP, Ch: e.Ch, Error: reason}); err != nil {
						return err
					}
					continue
				}
				chctx, chcancel := context.WithCancel(ctx)
				ch := &channel{id: e.Ch, mcp: e.MCP, ctx: chctx, cancel: chcancel, ready: make(chan struct{}), wake: make(chan struct{}, 1), queueLimit: queueLimit, logger: logger,
					inFlight: inFlight, pending: make(map[jsonrpc.ID]struct{})}
				channels[e.Ch] = ch
				open := m.Upstream.Open
				if cu, ok := m.Upstream.(ClientUpstream); ok && m.Sharing == proto.SharingPerClient {
					open = func(ctx context.Context) (mcp.Connection, error) { return cu.OpenClient(ctx, e.Client) }
				}
				workers.Go(func() { ch.run(open, conn, events) })
			case proto.TClose, proto.TMCP:
				ch := channels[e.Ch]
				if ch == nil {
					logger.Debug("frame for unknown channel", "type", e.T, "channel", e.Ch)
					continue
				}
				if e.T == proto.TClose {
					delete(channels, e.Ch)
					ch.cancel()
					continue
				}
				msg, err := jsonrpc.DecodeMessage(e.Msg)
				if err != nil {
					logger.Warn("invalid MCP message; dropping frame", "channel", e.Ch, "error", err)
					continue
				}
				ch.fromHub(msg)
				if err := ch.enqueue(msg, len(e.Msg)); err != nil {
					logger.Warn("closing channel", "mcp", ch.mcp, "channel", ch.id, "error", err)
					delete(channels, ch.id)
					ch.cancel()
					if err := send(&proto.Envelope{T: proto.TClose, MCP: ch.mcp, Ch: ch.id, Error: err.Error()}); err != nil {
						return err
					}
				}
			default:
				logger.Debug("ignoring hub frame", "type", e.T)
			}
		}
	}
}

type channel struct {
	id, mcp string
	ctx     context.Context
	cancel  context.CancelFunc
	ready   chan struct{}
	wake    chan struct{}
	mu      sync.Mutex
	queue   []queued
	// queuedBytes counts messages not yet written, including the ones the
	// writer has taken but is still writing.
	queuedBytes int
	queueLimit  int
	logger      *slog.Logger
	// pending holds the IDs of the hub's requests on this channel that have
	// no response yet; each is also counted in inFlight. Guarded by mu.
	pending  map[jsonrpc.ID]struct{}
	inFlight *atomic.Int64
}

// calls are the requests a reload waits for: what a client asked an MCP to
// do. Others are quick (listings) or open on purpose for as long as the
// session lives (subscriptions/listen), and would hold a reload forever.
var calls = map[string]bool{"tools/call": true, "prompts/get": true, "resources/read": true}

// fromHub counts a call the hub sends, and stops counting one the hub
// cancels: an MCP need not answer a cancelled request.
func (ch *channel) fromHub(msg jsonrpc.Message) {
	req, ok := msg.(*jsonrpc.Request)
	if !ok {
		return
	}
	switch {
	case req.ID.IsValid():
		if calls[req.Method] {
			ch.track(req.ID)
		}
	case req.Method == "notifications/cancelled":
		if id, ok := cancelledID(req.Params); ok {
			ch.answered(id)
		}
	}
}

// cancelledID is the request a notifications/cancelled names.
func cancelledID(params json.RawMessage) (jsonrpc.ID, bool) {
	var p struct {
		RequestID any `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil || p.RequestID == nil {
		return jsonrpc.ID{}, false
	}
	id, err := jsonrpc.MakeID(p.RequestID)
	return id, err == nil
}

// track counts a request from the hub until its response goes back.
func (ch *channel) track(id jsonrpc.ID) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.pending == nil {
		return // the channel has ended
	}
	if _, dup := ch.pending[id]; !dup {
		ch.pending[id] = struct{}{}
		ch.inFlight.Add(1)
	}
}

// answered stops counting the request a response is for.
func (ch *channel) answered(id jsonrpc.ID) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if _, ok := ch.pending[id]; ok {
		delete(ch.pending, id)
		ch.inFlight.Add(-1)
	}
}

// release stops counting everything still pending: the channel has ended,
// so none of it will be answered here.
func (ch *channel) release() {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.inFlight.Add(-int64(len(ch.pending)))
	ch.pending = nil
}

type queued struct {
	msg  jsonrpc.Message
	size int
}

// maxQueuedBytes bounds what one channel holds for an upstream that isn't
// reading (a stuck process, a slow HTTP server). One message is always
// accepted into an empty queue, so a single large request still goes
// through.
const maxQueuedBytes = 32 << 20

func (ch *channel) emit(events chan<- event, opened bool, err error) {
	select {
	case events <- event{ch, opened, err}:
	case <-ch.ctx.Done():
	}
}

// enqueue queues msg (size bytes on the wire) for the upstream. It fails
// when the upstream has fallen too far behind; the caller closes the channel.
func (ch *channel) enqueue(msg jsonrpc.Message, size int) error {
	// Keep writes ordered without letting a slow upstream stall hub control frames.
	ch.mu.Lock()
	if ch.queuedBytes > 0 && ch.queuedBytes+size > ch.queueLimit {
		pending := ch.queuedBytes
		ch.mu.Unlock()
		return fmt.Errorf("%s is not reading its input: %d bytes waiting", ch.mcp, pending)
	}
	ch.queue = append(ch.queue, queued{msg, size})
	ch.queuedBytes += size
	ch.mu.Unlock()
	select {
	case ch.wake <- struct{}{}:
	default:
	}
	return nil
}

func (ch *channel) run(open func(context.Context) (mcp.Connection, error), hub *wire.Conn, events chan<- event) {
	defer ch.release()
	defer ch.cancel()
	conn, err := open(ch.ctx)
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		// The hub names the MCP itself.
		ch.emit(events, true, err)
		return
	}
	var workers sync.WaitGroup
	// Close must interrupt Read even for transports that ignore its context.
	workers.Go(func() {
		<-ch.ctx.Done()
		conn.Close()
	})
	defer func() {
		ch.cancel()
		workers.Wait()
	}()
	ch.emit(events, true, nil)
	select {
	case <-ch.ctx.Done():
		return
	case <-ch.ready:
	}
	workers.Go(func() {
		for {
			select {
			case <-ch.ctx.Done():
				return
			case <-ch.wake:
			}
			ch.mu.Lock()
			messages := ch.queue
			ch.queue = nil
			ch.mu.Unlock()
			for _, q := range messages {
				if err := conn.Write(ch.ctx, q.msg); err != nil {
					ch.emit(events, false, fmt.Errorf("write %s: %w", ch.mcp, err))
					ch.cancel()
					return
				}
				ch.mu.Lock()
				ch.queuedBytes -= q.size
				ch.mu.Unlock()
			}
		}
	})
	for {
		msg, err := conn.Read(ch.ctx)
		if err != nil {
			ch.emit(events, false, fmt.Errorf("read %s: %w", ch.mcp, err))
			return
		}
		encoded, err := jsonrpc.EncodeMessage(msg)
		if err == nil {
			err = hub.Send(ch.ctx, &proto.Envelope{T: proto.TMCP, MCP: ch.mcp, Ch: ch.id, Msg: encoded})
			if errors.Is(err, wire.ErrTooLarge) {
				err = ch.tooLarge(hub, msg, err)
			}
		}
		if err != nil {
			ch.emit(events, false, fmt.Errorf("relay %s: %w", ch.mcp, err))
			return
		}
		if resp, ok := msg.(*jsonrpc.Response); ok {
			ch.answered(resp.ID)
		}
	}
}

// tooLarge handles an MCP message bigger than the hub accepts, which the
// hub would answer by closing the whole connection. A response becomes an
// error response to the same request; anything else is dropped.
func (ch *channel) tooLarge(hub *wire.Conn, msg jsonrpc.Message, sendErr error) error {
	resp, ok := msg.(*jsonrpc.Response)
	if !ok {
		ch.logger.Warn("dropping MCP message too large for the hub", "mcp", ch.mcp, "channel", ch.id, "error", sendErr)
		return nil
	}
	encoded, err := jsonrpc.EncodeMessage(&jsonrpc.Response{ID: resp.ID, Error: &jsonrpc.Error{
		Code:    jsonrpc.CodeInternalError,
		Message: fmt.Sprintf("mcp-mesh: %s's result is too large to relay (%v)", ch.mcp, sendErr),
	}})
	if err != nil {
		return err
	}
	return hub.Send(ch.ctx, &proto.Envelope{T: proto.TMCP, MCP: ch.mcp, Ch: ch.id, Msg: encoded})
}
