package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type logWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (w *logWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}
func (w *logWriter) text() string { w.mu.Lock(); defer w.mu.Unlock(); return w.buffer.String() }

func TestUnreachableStatusAndLogging(t *testing.T) {
	ctx := testContext(t)
	writer := &logWriter{}
	logger := slog.New(slog.NewTextHandler(writer, &slog.HandlerOptions{Level: slog.LevelDebug}))
	u := newFakeUpstream()
	ordinary := errors.New("unauthorized")
	u.open = func(context.Context) (mcp.Connection, error) {
		switch u.calls.Load() {
		case 1, 2:
			return nil, fmt.Errorf("connect app: %w", upstream.ErrUnreachable)
		case 3:
			return nil, ordinary
		}
		c := newFakeConn()
		u.opened <- c
		return c, nil
	}
	s := fakeSupervisor(t, u, Options{Start: proto.StartLazy, Logger: logger})
	for _, want := range []error{upstream.ErrUnreachable, upstream.ErrUnreachable, ordinary} {
		if _, err := s.Open(ctx); !errors.Is(err, want) {
			t.Fatalf("open: %v", err)
		}
		if s.Status().Unreachable != errors.Is(want, upstream.ErrUnreachable) {
			t.Fatalf("status: %+v", s.Status())
		}
	}
	logs := writer.text()
	if strings.Count(logs, "level=WARN") != 2 || strings.Count(logs, "level=DEBUG") != 1 {
		t.Fatalf("logs: %s", logs)
	}
	conn := openConn(t, ctx, s)
	if s.Status().Unreachable {
		t.Fatal("success did not clear unreachable")
	}
	raw := receive(t, ctx, u.opened)
	raw.incoming <- result{err: upstream.ErrUnreachable}
	readError(t, ctx, conn, upstream.ErrUnreachable)
	waitState(t, ctx, s, control.MCPBackoff)
	if !s.Status().Unreachable {
		t.Fatal("run failure not unreachable")
	}
	conn = openConn(t, ctx, s)
	raw = receive(t, ctx, u.opened)
	raw.incoming <- result{err: ordinary}
	readError(t, ctx, conn, ordinary)
	waitState(t, ctx, s, control.MCPBackoff)
	if s.Status().Unreachable {
		t.Fatal("other run failure did not clear unreachable")
	}
}

func TestMaxBackoff(t *testing.T) {
	for _, cap := range []time.Duration{0, 5 * time.Second, 100 * time.Millisecond} {
		s := New(Options{MaxBackoff: cap, New: func(io.Writer) upstream.Upstream { return newFakeUpstream() }})
		want := cap
		if want == 0 {
			want = time.Minute
		}
		for range 10 {
			s.backoffLocked()
			if s.delay > want {
				t.Fatalf("delay %v > cap %v", s.delay, want)
			}
		}
		if s.delay != want {
			t.Fatalf("cap: %v, want %v", s.delay, want)
		}
	}
}

func TestEagerRetriesUnreachable(t *testing.T) {
	ctx := testContext(t)
	u := newFakeUpstream()
	u.open = func(context.Context) (mcp.Connection, error) {
		if u.calls.Load() < 3 {
			return nil, upstream.ErrUnreachable
		}
		c := newFakeConn()
		u.opened <- c
		return c, nil
	}
	s := fakeSupervisor(t, u, Options{})
	receive(t, ctx, u.opened)
	waitState(t, ctx, s, control.MCPRunning)
	if u.calls.Load() != 3 || s.Status().Unreachable {
		t.Fatalf("recovery: %+v (%d attempts)", s.Status(), u.calls.Load())
	}
}
