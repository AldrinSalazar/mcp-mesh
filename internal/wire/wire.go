// Package wire carries [proto.Envelope] frames over a WebSocket. Both the node
// and the hub use it so framing, size limits and heartbeats behave the same on
// each side.
package wire

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// Heartbeat settings. A missed pong within PingTimeout counts as a dead peer;
// two consecutive misses close the connection.
const (
	PingInterval = 20 * time.Second
	PingTimeout  = 10 * time.Second
	maxMissed    = 2
)

// Chunking. A large envelope would hold the single WebSocket for
// its whole transfer, so it is split into binary frames of at most ChunkSize
// payload bytes. Each frame is
//
//	[4 bytes stream id, big-endian][1 byte flags][payload]
//
// and concurrent senders take turns frame by frame. The payloads of one stream
// concatenate to the JSON envelope that would otherwise be a text frame.
const (
	// ChunkSize is the payload size of one binary frame.
	ChunkSize = 64 << 10

	flagFinal = 1 << 0 // payload completes the envelope
	flagAbort = 1 << 1 // drop the partial stream; no payload
	flagMask  = flagFinal | flagAbort

	chunkHeader = 5

	// maxStreams bounds partial streams a peer may keep open. A well-behaved
	// peer has one per concurrent sender, and senders are the node's open
	// channels plus control traffic; 256 is far above that while keeping the
	// worst-case buffering bounded.
	maxStreams = 256
	// maxBuffered bounds all partial streams together, in envelopes of the
	// size limit: concurrent large results interleave, but a peer can't make
	// the receiver hold maxStreams of them at once.
	maxBuffered = 8

	abortTimeout = 5 * time.Second

	// streamIdle drops a partial stream that got no chunk for this long:
	// senders take turns frame by frame, so a live stream advances, and one
	// whose sender gave up without an abort (best effort) would otherwise
	// hold its share of the buffer limit for the connection's lifetime.
	streamIdle = time.Minute
	// sweepEvery bounds how often Recv looks for idle streams.
	sweepEvery = time.Second

	// headroom is what an envelope adds around its MCP message.
	headroom = 64 << 10
)

// ErrTooLarge is returned by Send for an envelope larger than the peer
// accepts (SetPeerLimit); sending it would make the peer close the
// connection.
var ErrTooLarge = errors.New("wire: envelope larger than the peer accepts")

// Conn is an envelope-level WebSocket connection. Send may be called
// concurrently; Recv must be called from a single goroutine.
type Conn struct {
	ws       *websocket.Conn
	limit    int64 // largest envelope accepted, chunked or not
	nextID   atomic.Uint32
	turns    turnQueue
	streams  map[uint32]*stream // Recv's partial streams
	buffered int64              // bytes held in streams
	// dropped holds streams dropped as idle, until their final or abort
	// frame (or another streamIdle), so their late chunks are ignored
	// rather than read as new streams.
	dropped   map[uint32]time.Time
	lastSweep time.Time
	peerLimit atomic.Int64 // 0 = unknown
	now       func() time.Time

	// afterChunk, if set by a test, runs after each chunk write once the
	// write turn has been released.
	afterChunk func(id uint32, sent int)
}

type stream struct {
	buf  []byte
	last time.Time // last chunk
}

// SetPeerLimit records the peer's message size limit (Welcome.MaxMessageSize
// on the node); Send then refuses envelopes the peer would reject.
func (c *Conn) SetPeerLimit(maxSize int64) {
	if maxSize > 0 {
		c.peerLimit.Store(maxSize + headroom)
	}
}

// turnQueue hands out write turns in FIFO order. sync.Mutex makes no fairness
// promise, and fair interleaving is the point of chunking.
type turnQueue struct {
	mu      sync.Mutex
	held    bool
	waiters []chan struct{}
}

func (q *turnQueue) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	q.mu.Lock()
	if !q.held {
		q.held = true
		q.mu.Unlock()
		return nil
	}
	ch := make(chan struct{})
	q.waiters = append(q.waiters, ch)
	q.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		q.mu.Lock()
		for i, w := range q.waiters {
			if w == ch {
				q.waiters = append(q.waiters[:i], q.waiters[i+1:]...)
				q.mu.Unlock()
				return ctx.Err()
			}
		}
		q.mu.Unlock()
		// The turn was handed over as the context ended; pass it on.
		q.release()
		return ctx.Err()
	}
}

func (q *turnQueue) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiters) == 0 {
		q.held = false
		return
	}
	w := q.waiters[0]
	q.waiters = q.waiters[1:]
	close(w) // ownership moves to w; held stays true
}

// Dial connects to a hub node endpoint (ws:// or wss://).
func Dial(ctx context.Context, url string, header http.Header, maxSize int64) (*Conn, error) {
	ws, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader:   header,
		Subprotocols: []string{proto.Subprotocol},
	})
	if err != nil {
		// A refused upgrade carries the hub's answer (e.g. 401 and why).
		if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
			var body []byte
			if resp.Body != nil {
				body, _ = io.ReadAll(io.LimitReader(resp.Body, 1024))
			}
			return nil, &HTTPError{Status: resp.StatusCode, Body: strings.TrimSpace(string(body)), err: err}
		}
		return nil, err
	}
	return newConn(ws, maxSize)
}

// HTTPError is a WebSocket upgrade the peer answered with an HTTP error.
type HTTPError struct {
	Status int
	// Body is the start of the response body, trimmed.
	Body string
	err  error
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return e.err.Error()
	}
	return fmt.Sprintf("hub answered %d: %s", e.Status, e.Body)
}
func (e *HTTPError) Unwrap() error { return e.err }

// Accept upgrades a hub-side HTTP request to a node connection.
func Accept(w http.ResponseWriter, r *http.Request, maxSize int64) (*Conn, error) {
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{proto.Subprotocol},
	})
	if err != nil {
		return nil, err
	}
	return newConn(ws, maxSize)
}

func newConn(ws *websocket.Conn, maxSize int64) (*Conn, error) {
	if ws.Subprotocol() != proto.Subprotocol {
		ws.Close(websocket.StatusPolicyViolation, "subprotocol "+proto.Subprotocol+" required")
		return nil, fmt.Errorf("peer did not negotiate %s", proto.Subprotocol)
	}
	if maxSize <= 0 {
		maxSize = proto.DefaultMaxMessageSize
	}
	// The envelope adds a little around Msg; leave headroom so a max-size MCP
	// message still fits. Text frames from v1 peers are bounded by it per
	// frame; chunked streams by it in total.
	limit := maxSize + headroom
	ws.SetReadLimit(limit)
	return &Conn{ws: ws, limit: limit, streams: make(map[uint32]*stream), dropped: make(map[uint32]time.Time), now: time.Now}, nil
}

// writeTurn writes one frame while holding a write turn.
func (c *Conn) writeTurn(ctx context.Context, typ websocket.MessageType, b []byte) error {
	if err := c.turns.acquire(ctx); err != nil {
		return err
	}
	defer c.turns.release()
	return c.ws.Write(ctx, typ, b)
}

// Send writes one envelope. Over a v2 connection an envelope larger than
// ChunkSize goes out as binary chunks that interleave with other senders.
func (c *Conn) Send(ctx context.Context, e *proto.Envelope) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if limit := c.peerLimit.Load(); limit > 0 && int64(len(b)) > limit {
		return fmt.Errorf("%w: %d bytes, limit %d", ErrTooLarge, len(b), limit)
	}
	if len(b) <= ChunkSize {
		return c.writeTurn(ctx, websocket.MessageText, b)
	}
	id := c.nextID.Add(1)
	frame := make([]byte, chunkHeader+ChunkSize)
	binary.BigEndian.PutUint32(frame, id)
	for sent := 0; len(b) > 0; sent++ {
		n := min(len(b), ChunkSize)
		frame[4] = 0
		if n == len(b) {
			frame[4] = flagFinal
		}
		copy(frame[chunkHeader:], b[:n])
		if err := c.writeTurn(ctx, websocket.MessageBinary, frame[:chunkHeader+n]); err != nil {
			if sent > 0 {
				c.sendAbort(id)
			}
			return err
		}
		b = b[n:]
		if c.afterChunk != nil {
			c.afterChunk(id, sent+1)
		}
	}
	return nil
}

// sendAbort tells the receiver to free a partial stream. It is best effort and
// outlives the caller's context, which is usually why the stream stopped.
func (c *Conn) sendAbort(id uint32) {
	ctx, cancel := context.WithTimeout(context.Background(), abortTimeout)
	defer cancel()
	var f [chunkHeader]byte
	binary.BigEndian.PutUint32(f[:], id)
	f[4] = flagAbort
	c.writeTurn(ctx, websocket.MessageBinary, f[:])
}

// Recv reads the next envelope. It returns an error when the connection closes
// or the peer breaks the chunking rules; the caller should then close.
func (c *Conn) Recv(ctx context.Context) (*proto.Envelope, error) {
	for {
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			return nil, err
		}
		if typ == websocket.MessageBinary {
			var done bool
			if b, done, err = c.chunk(b); err != nil {
				return nil, err
			}
			if !done {
				continue
			}
		}
		var e proto.Envelope
		if err := json.Unmarshal(b, &e); err != nil {
			return nil, fmt.Errorf("wire: bad envelope: %w", err)
		}
		return &e, nil
	}
}

// chunk consumes one binary frame. It returns the whole envelope bytes and
// true when the frame completes a stream.
func (c *Conn) chunk(f []byte) ([]byte, bool, error) {
	if len(f) < chunkHeader {
		return nil, false, errors.New("wire: binary frame shorter than header")
	}
	id, flags, payload := binary.BigEndian.Uint32(f), f[4], f[chunkHeader:]
	if flags&^flagMask != 0 {
		return nil, false, fmt.Errorf("wire: unknown chunk flags %#x", flags)
	}
	now := c.now()
	c.sweep(now)
	if _, gone := c.dropped[id]; gone {
		if flags&(flagFinal|flagAbort) != 0 {
			delete(c.dropped, id)
		}
		return nil, false, nil
	}
	st := c.streams[id]
	if flags&flagAbort != 0 {
		if st != nil {
			c.buffered -= int64(len(st.buf))
			delete(c.streams, id)
		}
		return nil, false, nil
	}
	if st == nil && len(c.streams) >= maxStreams {
		return nil, false, fmt.Errorf("wire: more than %d partial streams", maxStreams)
	}
	var held int
	if st != nil {
		held = len(st.buf)
	}
	if int64(held)+int64(len(payload)) > c.limit {
		return nil, false, errors.New("wire: chunked envelope exceeds size limit")
	}
	if c.buffered+int64(len(payload)) > maxBuffered*c.limit {
		return nil, false, errors.New("wire: chunked envelopes in progress exceed buffer limit")
	}
	if flags&flagFinal != 0 {
		if st == nil {
			return payload, true, nil
		}
		c.buffered -= int64(held)
		delete(c.streams, id)
		return append(st.buf, payload...), true, nil
	}
	if st == nil {
		st = &stream{}
		c.streams[id] = st
	}
	st.buf = append(st.buf, payload...)
	st.last = now
	c.buffered += int64(len(payload))
	return nil, false, nil
}

// sweep drops partial streams idle for streamIdle, and forgets dropped ones
// after as long again.
func (c *Conn) sweep(now time.Time) {
	if now.Sub(c.lastSweep) < sweepEvery {
		return
	}
	c.lastSweep = now
	for id, st := range c.streams {
		if now.Sub(st.last) >= streamIdle {
			c.buffered -= int64(len(st.buf))
			delete(c.streams, id)
			c.dropped[id] = now
		}
	}
	for id, at := range c.dropped {
		if now.Sub(at) >= streamIdle {
			delete(c.dropped, id)
		}
	}
}

// Heartbeat pings the peer every PingInterval until ctx ends or the peer stops
// answering, in which case it closes the connection and returns. Pongs are
// only processed while Recv is running, so run Heartbeat alongside a read loop.
func (c *Conn) Heartbeat(ctx context.Context) {
	t := time.NewTicker(PingInterval)
	defer t.Stop()
	missed := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, PingTimeout)
		err := c.ws.Ping(pctx)
		cancel()
		if err == nil {
			missed = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if missed++; missed >= maxMissed {
			c.ws.Close(websocket.StatusGoingAway, "heartbeat timeout")
			return
		}
	}
}

// Close closes the connection with a status code and reason.
func (c *Conn) Close(code websocket.StatusCode, reason string) error {
	return c.ws.Close(code, reason)
}

// CloseNow closes the connection without the closing handshake.
func (c *Conn) CloseNow() error { return c.ws.CloseNow() }

// CloseStatus extracts the WebSocket close code from an error returned by
// Recv or Send, or -1 if there is none.
func CloseStatus(err error) websocket.StatusCode { return websocket.CloseStatus(err) }

// CloseReason extracts the reason text the peer gave when it closed, or "".
func CloseReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}
