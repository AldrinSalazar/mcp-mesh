package wire

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func wsURL(s *httptest.Server) string { return "ws" + strings.TrimPrefix(s.URL, "http") }

// pair returns the two ends of a wire connection.
func pair(t *testing.T, maxSize int64) (client, server *Conn) {
	t.Helper()
	ch := make(chan *Conn, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r, maxSize)
		if err != nil {
			close(ch)
			return
		}
		ch <- c
		<-done
	}))
	client, err := Dial(testCtx(t), wsURL(srv), nil, maxSize)
	if err != nil {
		t.Fatal(err)
	}
	server = <-ch
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		client.CloseNow()
		server.CloseNow()
		close(done)
		srv.Close()
	})
	return client, server
}

// rawServer returns a wire client whose peer is a raw WebSocket.
func rawServer(t *testing.T) (*Conn, *websocket.Conn) {
	t.Helper()
	ch := make(chan *websocket.Conn, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{proto.Subprotocol}})
		if err != nil {
			close(ch)
			return
		}
		ws.SetReadLimit(1 << 30)
		ch <- ws
		<-done
	}))
	client, err := Dial(testCtx(t), wsURL(srv), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	raw := <-ch
	if raw == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		client.CloseNow()
		raw.CloseNow()
		close(done)
		srv.Close()
	})
	return client, raw
}

// rawClient returns a wire server connection whose peer is a raw WebSocket.
func rawClient(t *testing.T, maxSize int64) (*websocket.Conn, *Conn) {
	t.Helper()
	ch := make(chan *Conn, 1)
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := Accept(w, r, maxSize)
		if err != nil {
			close(ch)
			return
		}
		ch <- c
		<-done
	}))
	raw, _, err := websocket.Dial(testCtx(t), wsURL(srv), &websocket.DialOptions{Subprotocols: []string{proto.Subprotocol}})
	if err != nil {
		t.Fatal(err)
	}
	server := <-ch
	if server == nil {
		t.Fatal("accept failed")
	}
	t.Cleanup(func() {
		raw.CloseNow()
		server.CloseNow()
		close(done)
		srv.Close()
	})
	return raw, server
}

func bigEnvelope(ch string, n int) *proto.Envelope {
	msg := `"` + strings.Repeat("x", n) + `"`
	return &proto.Envelope{T: proto.TMCP, MCP: "m", Ch: ch, Msg: json.RawMessage(msg)}
}

func frame(id uint32, flags byte, payload []byte) []byte {
	f := binary.BigEndian.AppendUint32(nil, id)
	f = append(f, flags)
	return append(f, payload...)
}

func TestRoundTripChunked(t *testing.T) {
	ctx := testCtx(t)
	for _, size := range []int{ChunkSize - 100, ChunkSize, 3*ChunkSize + 100, 5 * ChunkSize} {
		a, b := pair(t, 0)
		want := bigEnvelope("c1", size)
		if err := a.Send(ctx, want); err != nil {
			t.Fatal(err)
		}
		got, err := b.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if got.Ch != "c1" || !bytes.Equal(got.Msg, want.Msg) {
			t.Fatalf("size %d: envelope changed in transit", size)
		}
	}
}

func TestFrameTypes(t *testing.T) {
	big := bigEnvelope("c", 3*ChunkSize)
	small := &proto.Envelope{T: proto.TClose, Ch: "c"}
	tests := []struct {
		name   string
		env    *proto.Envelope
		typ    websocket.MessageType
		frames int
	}{
		{"big", big, websocket.MessageBinary, 4},
		{"small", small, websocket.MessageText, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testCtx(t)
			c, raw := rawServer(t)
			errc := make(chan error, 1)
			go func() { errc <- c.Send(ctx, tt.env) }()
			var total []byte
			for i := 0; i < tt.frames; i++ {
				typ, b, err := raw.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if typ != tt.typ {
					t.Fatalf("frame %d type = %v, want %v", i, typ, tt.typ)
				}
				if typ == websocket.MessageBinary {
					if len(b)-chunkHeader > ChunkSize {
						t.Fatalf("chunk payload %d > ChunkSize", len(b)-chunkHeader)
					}
					total = append(total, b[chunkHeader:]...)
				} else {
					total = b
				}
			}
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
			var e proto.Envelope
			if err := json.Unmarshal(total, &e); err != nil || e.Ch != tt.env.Ch {
				t.Fatalf("reassembled envelope: %v, %+v", err, e)
			}
		})
	}
}

func TestInterleaving(t *testing.T) {
	ctx := testCtx(t)
	a, b := pair(t, 0)
	sentSmall := false
	a.afterChunk = func(id uint32, sent int) {
		if sent != 1 || sentSmall {
			return
		}
		sentSmall = true
		// The turn is free again: a small envelope goes out right now,
		// between chunks of the big one.
		if err := a.Send(ctx, &proto.Envelope{T: proto.TClose, Ch: "small"}); err != nil {
			t.Error(err)
		}
	}
	if err := a.Send(ctx, bigEnvelope("big", 4*ChunkSize)); err != nil {
		t.Fatal(err)
	}
	var order []string
	for range 2 {
		e, err := b.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		order = append(order, e.Ch)
	}
	if order[0] != "small" || order[1] != "big" {
		t.Fatalf("arrival order = %v, want small before big", order)
	}
}

func TestConcurrentBigSends(t *testing.T) {
	ctx := testCtx(t)
	a, b := pair(t, 0)
	sizes := map[string]int{"a": 4*ChunkSize + 7, "b": 6*ChunkSize + 11, "c": 3 * ChunkSize}
	var wg sync.WaitGroup
	for ch, n := range sizes {
		wg.Go(func() {
			if err := a.Send(ctx, bigEnvelope(ch, n)); err != nil {
				t.Error(err)
			}
		})
	}
	for range sizes {
		e, err := b.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if want := bigEnvelope(e.Ch, sizes[e.Ch]); !bytes.Equal(e.Msg, want.Msg) {
			t.Fatalf("channel %s corrupted", e.Ch)
		}
		delete(sizes, e.Ch)
	}
	wg.Wait()
}

func TestAbort(t *testing.T) {
	ctx := testCtx(t)
	a, b := pair(t, 0)
	sendCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.afterChunk = func(id uint32, sent int) {
		if sent == 2 {
			cancel()
		}
	}
	if err := a.Send(sendCtx, bigEnvelope("dropped", 5*ChunkSize)); err == nil {
		t.Fatal("Send succeeded after cancel")
	}
	a.afterChunk = nil
	if err := a.Send(ctx, &proto.Envelope{T: proto.TClose, Ch: "after"}); err != nil {
		t.Fatal(err)
	}
	e, err := b.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if e.Ch != "after" {
		t.Fatalf("got %q, want the later message only", e.Ch)
	}
	if len(b.streams) != 0 || b.buffered != 0 {
		t.Fatalf("%d partial streams (%d bytes) left after abort", len(b.streams), b.buffered)
	}
}

func TestRecvRejects(t *testing.T) {
	const maxSize = 1024 // read limit 1024 + 64 KiB
	chunk := func(n int) []byte { return bytes.Repeat([]byte("x"), n) }
	tests := []struct {
		name   string
		frames [][]byte
	}{
		{"short frame", [][]byte{{0, 0, 0, 1}}},
		{"unknown flags", [][]byte{frame(1, 0x04, nil)}},
		{"unknown flags with final", [][]byte{frame(1, 0x81, nil)}},
		{"oversize stream", [][]byte{frame(1, 0, chunk(40000)), frame(1, 0, chunk(40000))}},
		{"too much buffered across streams", func() [][]byte {
			var fs [][]byte
			for i := range maxBuffered + 1 {
				fs = append(fs, frame(uint32(i), 0, chunk(60000)))
			}
			return fs
		}()},
		{"too many streams", func() [][]byte {
			var fs [][]byte
			for i := range maxStreams + 1 {
				fs = append(fs, frame(uint32(i), 0, []byte("{")))
			}
			return fs
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := testCtx(t)
			raw, srv := rawClient(t, maxSize)
			for _, f := range tt.frames {
				if err := raw.Write(ctx, websocket.MessageBinary, f); err != nil {
					t.Fatal(err)
				}
			}
			if e, err := srv.Recv(ctx); err == nil {
				t.Fatalf("Recv returned %+v, want error", e)
			}
		})
	}
}

func TestRecvAcceptsMaxStreamsAndAbort(t *testing.T) {
	ctx := testCtx(t)
	raw, srv := rawClient(t, 0)
	for i := range maxStreams {
		if err := raw.Write(ctx, websocket.MessageBinary, frame(uint32(i), 0, []byte(`{"t":`))); err != nil {
			t.Fatal(err)
		}
	}
	// Abort of an unknown stream is ignored; abort of a known one frees a slot.
	if err := raw.Write(ctx, websocket.MessageBinary, frame(9999, flagAbort, nil)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Write(ctx, websocket.MessageBinary, frame(0, flagAbort, nil)); err != nil {
		t.Fatal(err)
	}
	if err := raw.Write(ctx, websocket.MessageBinary, frame(5000, flagFinal, []byte(`{"t":"close","ch":"ok"}`))); err != nil {
		t.Fatal(err)
	}
	e, err := srv.Recv(ctx)
	if err != nil || e.Ch != "ok" {
		t.Fatalf("Recv = %+v, %v", e, err)
	}
	if len(srv.streams) != maxStreams-1 {
		t.Fatalf("streams = %d, want %d", len(srv.streams), maxStreams-1)
	}
	if want := int64(maxStreams-1) * int64(len(`{"t":`)); srv.buffered != want {
		t.Fatalf("buffered = %d, want %d", srv.buffered, want)
	}
}

func TestTurnQueueFIFO(t *testing.T) {
	ctx := testCtx(t)
	var q turnQueue
	if err := q.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	const n = 8
	order := make(chan int, n)
	for i := range n {
		go func() {
			if q.acquire(ctx) == nil {
				order <- i
				q.release()
			}
		}()
		// Wait until goroutine i is queued so arrival order is known.
		for {
			q.mu.Lock()
			queued := len(q.waiters)
			q.mu.Unlock()
			if queued == i+1 {
				break
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			time.Sleep(time.Millisecond)
		}
	}
	q.release()
	for want := range n {
		if got := <-order; got != want {
			t.Fatalf("turn %d went to waiter %d", want, got)
		}
	}
}

func TestTurnQueueCancel(t *testing.T) {
	var q turnQueue
	if err := q.acquire(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	go func() { errc <- q.acquire(ctx) }()
	for {
		q.mu.Lock()
		queued := len(q.waiters)
		q.mu.Unlock()
		if queued == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-errc; err == nil {
		t.Fatal("canceled acquire succeeded")
	}
	q.release()
	if err := q.acquire(t.Context()); err != nil {
		t.Fatal("queue wedged after canceled waiter:", err)
	}
}

// A sender that stops mid-envelope without an abort must not hold its share
// of the buffer limit for good: its stream is dropped once idle, and its late
// chunks are ignored rather than read as a new envelope.
func TestIdlePartialStreamsExpire(t *testing.T) {
	ctx := testCtx(t)
	raw, srv := rawClient(t, 0)
	now := time.Unix(1000, 0)
	srv.now = func() time.Time { return now }
	send := func(f []byte) {
		t.Helper()
		if err := raw.Write(ctx, websocket.MessageBinary, f); err != nil {
			t.Fatal(err)
		}
	}
	recv := func(want string) {
		t.Helper()
		e, err := srv.Recv(ctx)
		if err != nil || e.Ch != want {
			t.Fatalf("Recv = %+v, %v; want channel %q", e, err, want)
		}
	}
	send(frame(1, 0, []byte(`{"t":"close",`))) // abandoned
	send(frame(2, 0, []byte(`{"t":"close",`))) // slow but alive
	send(frame(9, flagFinal, []byte(`{"t":"close","ch":"a"}`)))
	recv("a")
	if len(srv.streams) != 2 {
		t.Fatalf("streams = %d, want 2", len(srv.streams))
	}

	// Recv reads frames only when called, so each step ends with a whole
	// envelope that makes it process them at that step's time.
	now = now.Add(streamIdle / 2)
	send(frame(2, 0, []byte(`"ch":`)))
	send(frame(8, flagFinal, []byte(`{"t":"close","ch":"c"}`)))
	recv("c")
	now = now.Add(streamIdle / 2)
	// Stream 1 has been idle for streamIdle; stream 2 for half of it.
	send(frame(1, 0, []byte(`"ch":"late"`)))
	send(frame(1, flagFinal, []byte(`}`)))
	send(frame(2, flagFinal, []byte(`"b"}`)))
	recv("b")
	if len(srv.streams) != 0 || srv.buffered != 0 || len(srv.dropped) != 0 {
		t.Fatalf("left: %d streams, %d bytes, %d dropped", len(srv.streams), srv.buffered, len(srv.dropped))
	}
}

func TestSendRespectsPeerLimit(t *testing.T) {
	ctx := testCtx(t)
	a, b := pair(t, 0)
	a.SetPeerLimit(1024)
	if err := a.Send(ctx, bigEnvelope("big", 1024+headroom)); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Send = %v, want ErrTooLarge", err)
	}
	// The connection is still fine.
	if err := a.Send(ctx, bigEnvelope("ok", 1024)); err != nil {
		t.Fatal(err)
	}
	if e, err := b.Recv(ctx); err != nil || e.Ch != "ok" {
		t.Fatalf("Recv = %+v, %v", e, err)
	}
}
