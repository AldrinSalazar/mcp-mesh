// Package pairing lets a node without a key ask to join the hub. The node
// connects to proto.PairPath, which needs no credentials, and waits; the
// request shows in the dashboard with a code the node shows too, and an
// admin who approves it has a node key created and sent down the waiting
// connection. Requests live in memory, only as long as their connection: a
// node that disconnects (or a hub that restarts) withdraws its request, and
// the node asks again when it reconnects.
package pairing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/auth"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/AldrinSalazar/mcp-mesh/internal/wire"
)

const (
	// DefaultMaxPending bounds the requests waiting at once, and
	// DefaultMaxPerAddress those from one address: the endpoint is open to
	// anyone who can reach the node port, so it must not fill the
	// dashboard (or memory) without limit.
	DefaultMaxPending    = 20
	DefaultMaxPerAddress = 3

	// maxFrame bounds the pair frame; it carries a few short strings.
	maxFrame = 4 << 10
	// maxField bounds each string the node sends, as shown.
	maxField = 64
)

// ErrNotPending means the request was already approved or declined, or its
// node disconnected.
var ErrNotPending = errors.New("no such pairing request; the machine may have disconnected")

// ErrNotDelivered means the node disconnected while its approval was being
// sent; it did not receive the key.
var ErrNotDelivered = errors.New("the machine disconnected before it received its key")

// Request is one node waiting for an admin.
type Request struct {
	ID string `json:"id"`
	// Node is the name the node asked for.
	Node        string    `json:"node"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	NodeVersion string    `json:"node_version"`
	IP          string    `json:"ip"`
	Code        string    `json:"code"`
	Requested   time.Time `json:"requested"`
}

// Grant is an approval: the name the node joins as and its new key.
type Grant struct {
	Node string
	Key  string
}

type entry struct {
	req   Request
	grant chan Grant    // receives the approval, once
	deny  chan struct{} // closed when an admin declines
	sent  chan error    // the result of sending the grant
	done  chan struct{} // closed when the connection ends
}

// Options configure a Manager.
type Options struct {
	Logger *slog.Logger
	// ClientIP names a request's address (after trusted proxies); nil uses
	// RemoteAddr's host.
	ClientIP      func(*http.Request) string
	MaxPending    int // 0 = DefaultMaxPending
	MaxPerAddress int // 0 = DefaultMaxPerAddress
}

// Manager holds the waiting requests and serves proto.PairPath.
type Manager struct {
	opts    Options
	mu      sync.Mutex
	pending map[string]*entry
	// helloTimeout bounds the wait for the pair frame.
	helloTimeout time.Duration
}

func New(opts Options) *Manager {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.ClientIP == nil {
		opts.ClientIP = func(r *http.Request) string {
			host, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				return r.RemoteAddr
			}
			return host
		}
	}
	if opts.MaxPending <= 0 {
		opts.MaxPending = DefaultMaxPending
	}
	if opts.MaxPerAddress <= 0 {
		opts.MaxPerAddress = DefaultMaxPerAddress
	}
	return &Manager{opts: opts, pending: map[string]*entry{}, helloTimeout: 10 * time.Second}
}

// Pending lists the waiting requests, oldest first.
func (m *Manager) Pending() []Request {
	m.mu.Lock()
	out := make([]Request, 0, len(m.pending))
	for _, e := range m.pending {
		out = append(out, e.req)
	}
	m.mu.Unlock()
	slices.SortFunc(out, func(a, b Request) int {
		if c := a.Requested.Compare(b.Requested); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Get returns a waiting request.
func (m *Manager) Get(id string) (Request, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.pending[id]
	if !ok {
		return Request{}, false
	}
	return e.req, true
}

// take removes a waiting request, so only one admin action applies to it.
func (m *Manager) take(id string) *entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.pending[id]
	delete(m.pending, id)
	return e
}

// Deliver approves a request: it sends the grant to the waiting node and
// returns once the node has it. ErrNotPending: the request is gone (another
// admin acted on it, or the node left); ErrNotDelivered: the node left while
// it was being sent. Either way the node has no key, so the caller revokes
// the one it created. Once handed over, the send isn't cancelled with the
// caller: a node that received a key the caller then revoked would be stuck
// with it. The connection's heartbeat bounds the wait.
func (m *Manager) Deliver(id string, g Grant) error {
	e := m.take(id)
	if e == nil {
		return ErrNotPending
	}
	e.grant <- g
	select {
	case err := <-e.sent:
		if err != nil {
			return ErrNotDelivered
		}
		return nil
	case <-e.done:
		// The connection may have ended right after the send succeeded.
		select {
		case err := <-e.sent:
			if err == nil {
				return nil
			}
		default:
		}
		return ErrNotDelivered
	}
}

// Deny declines a request; its node is told and stops asking.
func (m *Manager) Deny(id string) (Request, bool) {
	e := m.take(id)
	if e == nil {
		return Request{}, false
	}
	close(e.deny)
	return e.req, true
}

// refusal is why a pair request is turned away, as a WebSocket close.
type refusal struct {
	code   websocket.StatusCode
	reason string
}

// add registers a request unless the limits are reached.
func (m *Manager) add(e *entry) *refusal {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) >= m.opts.MaxPending {
		return &refusal{websocket.StatusTryAgainLater, "too many machines are waiting for approval; try again later"}
	}
	from := 0
	for _, other := range m.pending {
		if other.req.IP == e.req.IP {
			from++
		}
	}
	if from >= m.opts.MaxPerAddress {
		return &refusal{websocket.StatusTryAgainLater, "too many pairing requests from this address; try again later"}
	}
	m.pending[e.req.ID] = e
	return nil
}

func (m *Manager) remove(e *entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending[e.req.ID] == e {
		delete(m.pending, e.req.ID)
	}
}

// checkPair validates the first frame.
func checkPair(e *proto.Envelope) *refusal {
	if e == nil || e.T != proto.TPair || e.Pair == nil {
		return &refusal{websocket.StatusPolicyViolation, "first frame must be pair"}
	}
	if e.Pair.Version < proto.MinVersion {
		return &refusal{proto.CloseUnsupportedVersion, "unsupported protocol version"}
	}
	if err := auth.ValidName(e.Pair.Node); err != nil {
		return &refusal{websocket.StatusPolicyViolation, "node name: " + err.Error()}
	}
	return nil
}

// clean makes a string the node sent safe to show: printable, one line,
// at most maxField bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > maxField {
		// Cutting may split a rune; ToValidUTF8 drops the piece.
		s = s[:maxField]
	}
	return strings.ToValidUTF8(s, "")
}

// ServeHTTP is one node's pairing request, from its first frame until an
// admin decides or the node leaves.
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := wire.Accept(w, r, maxFrame)
	if err != nil {
		m.opts.Logger.Debug("accept pairing", "error", err)
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, m.helloTimeout)
	first, err := conn.Recv(hctx)
	hcancel()
	if err != nil {
		first = nil
	}
	if refused := checkPair(first); refused != nil {
		conn.Close(refused.code, refused.reason)
		return
	}
	p := first.Pair
	e := &entry{
		req: Request{
			ID: newID(), Node: p.Node, Hostname: clean(p.Hostname), OS: clean(p.OS), NodeVersion: clean(p.NodeVersion),
			IP: m.opts.ClientIP(r), Code: newCode(), Requested: time.Now().UTC(),
		},
		grant: make(chan Grant, 1), deny: make(chan struct{}), sent: make(chan error, 1), done: make(chan struct{}),
	}
	if refused := m.add(e); refused != nil {
		m.opts.Logger.Warn("pairing request refused", "node", e.req.Node, "addr", e.req.IP, "reason", refused.reason)
		conn.Close(refused.code, refused.reason)
		return
	}
	defer m.remove(e)
	defer close(e.done)
	m.opts.Logger.Info("pairing requested; approve or decline it in the dashboard", "node", e.req.Node, "hostname", e.req.Hostname, "addr", e.req.IP)
	if err := conn.Send(ctx, &proto.Envelope{T: proto.TPairing, Pairing: &proto.Pairing{Code: e.req.Code}}); err != nil {
		return
	}

	// The node sends nothing more; reading keeps the heartbeat's pongs
	// flowing and notices when it leaves.
	left := make(chan struct{})
	var workers sync.WaitGroup
	defer workers.Wait()
	defer cancel()
	workers.Go(func() { conn.Heartbeat(ctx) })
	workers.Go(func() {
		defer close(left)
		for {
			if _, err := conn.Recv(ctx); err != nil {
				return
			}
		}
	})
	select {
	case g := <-e.grant:
		err := conn.Send(ctx, &proto.Envelope{T: proto.TPaired, Paired: &proto.Paired{Node: g.Node, Key: g.Key}})
		e.sent <- err
		if err == nil {
			m.opts.Logger.Info("pairing approved", "node", g.Node, "addr", e.req.IP)
			// The node closes once it has the key; wait a moment for that so
			// the frame isn't cut off by our close.
			select {
			case <-left:
			case <-time.After(5 * time.Second):
			}
			conn.Close(websocket.StatusNormalClosure, "paired")
		}
	case <-e.deny:
		m.opts.Logger.Info("pairing declined", "node", e.req.Node, "addr", e.req.IP)
		conn.Close(proto.CloseDenied, "an admin declined this machine")
	case <-left:
		m.opts.Logger.Info("pairing request withdrawn: the machine disconnected", "node", e.req.Node, "addr", e.req.IP)
	case <-ctx.Done():
	}
}

func newID() string {
	var b [8]byte
	rand.Read(b[:])
	return "p_" + hex.EncodeToString(b[:])
}

// codeAlphabet has no look-alikes (0/O, 1/I/L), so a code read off one
// screen is typed or compared without doubt.
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// newCode returns a code such as "K7Q-4MX".
func newCode() string {
	out := make([]byte, 0, 7)
	limit := big.NewInt(int64(len(codeAlphabet)))
	for i := range 6 {
		if i == 3 {
			out = append(out, '-')
		}
		v, _ := rand.Int(rand.Reader, limit)
		out = append(out, codeAlphabet[v.Int64()])
	}
	return string(out)
}
