package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestDrain(t *testing.T) {
	newBackend := func(ctx context.Context, timeout time.Duration) *backend {
		return &backend{ctx: ctx, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), opts: Options{DrainTimeout: timeout}}
	}
	t.Run("nothing in flight", func(t *testing.T) {
		d := newBackend(t.Context(), time.Hour)
		start := time.Now()
		d.drain()
		if time.Since(start) > time.Second {
			t.Fatal("drain waited with nothing in flight")
		}
	})
	t.Run("calls finish", func(t *testing.T) {
		d := newBackend(t.Context(), time.Hour)
		d.inFlight.Store(2)
		done := make(chan struct{})
		go func() { d.drain(); close(done) }()
		select {
		case <-done:
			t.Fatal("drain returned with calls in flight")
		case <-time.After(100 * time.Millisecond):
		}
		d.inFlight.Store(0)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("drain didn't return once the calls finished")
		}
	})
	t.Run("timeout", func(t *testing.T) {
		d := newBackend(t.Context(), 50*time.Millisecond)
		d.inFlight.Store(1)
		done := make(chan struct{})
		go func() { d.drain(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("drain ignored its timeout")
		}
	})
	t.Run("daemon stops", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		d := newBackend(ctx, time.Hour)
		d.inFlight.Store(1)
		done := make(chan struct{})
		go func() { d.drain(); close(done) }()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("drain kept waiting after the daemon stopped")
		}
	})
}
