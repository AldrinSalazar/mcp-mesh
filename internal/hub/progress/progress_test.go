package progress

import (
	"context"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRouter(t *testing.T) {
	r := New()
	var got []any
	tokA, releaseA := r.Register(float64(7), func(_ context.Context, p *mcp.ProgressNotificationParams) error {
		got = append(got, p.ProgressToken, p.Progress)
		return nil
	})
	tokB, releaseB := r.Register("7", func(context.Context, *mcp.ProgressNotificationParams) error { return nil })
	if tokA == tokB || r.Len() != 2 {
		t.Fatalf("tokens %q %q, len %d", tokA, tokB, r.Len())
	}
	if !r.Dispatch(t.Context(), &mcp.ProgressNotificationParams{ProgressToken: tokA, Progress: 0.5}) {
		t.Fatal("not delivered")
	}
	if len(got) != 2 || got[0] != float64(7) || got[1] != 0.5 {
		t.Errorf("client saw %v, want its own token 7 and 0.5", got)
	}
	releaseA()
	if r.Dispatch(t.Context(), &mcp.ProgressNotificationParams{ProgressToken: tokA}) {
		t.Error("delivered after release")
	}
	if r.Dispatch(t.Context(), &mcp.ProgressNotificationParams{ProgressToken: 42}) {
		t.Error("non-string token delivered")
	}
	releaseB()
	if r.Len() != 0 {
		t.Errorf("len = %d after release", r.Len())
	}
}

// Settle is a barrier: it returns once every notification Expect counted
// has been dispatched, however long the SDK's queue takes.
func TestSettleWaitsForExpectedNotifications(t *testing.T) {
	r := New()
	tok, release := r.Register(1, func(context.Context, *mcp.ProgressNotificationParams) error { return nil })
	defer release()

	// Nothing expected: nothing to wait for.
	r.Settle(t.Context(), tok)
	r.Settle(t.Context(), "unknown")

	r.Expect(tok)
	r.Expect(tok)
	r.Expect(42)        // not a hub token: ignored
	r.Expect("mm-gone") // no such call: ignored
	settled := make(chan struct{})
	go func() {
		defer close(settled)
		r.Settle(t.Context(), tok)
	}()
	r.Dispatch(t.Context(), &mcp.ProgressNotificationParams{ProgressToken: tok})
	select {
	case <-settled:
		t.Fatal("Settle returned with a notification still queued")
	default:
	}
	r.Dispatch(t.Context(), &mcp.ProgressNotificationParams{ProgressToken: tok})
	select {
	case <-settled:
	case <-time.After(settleMax / 2):
		t.Fatal("Settle didn't return once everything was dispatched")
	}

	// A dispatch nobody expected (no transport saw it) doesn't go negative.
	r.Dispatch(t.Context(), &mcp.ProgressNotificationParams{ProgressToken: tok})
	r.Settle(t.Context(), tok)
}

func TestSettleIsBounded(t *testing.T) {
	r := New()
	tok, release := r.Register(1, func(context.Context, *mcp.ProgressNotificationParams) error { return nil })
	defer release()
	r.Expect(tok) // never dispatched: the SDK dropped it
	start := time.Now()
	r.Settle(t.Context(), tok)
	if d := time.Since(start); d < settleMax || d > settleMax+500*time.Millisecond {
		t.Errorf("Settle took %v, want its cap %v", d, settleMax)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	start = time.Now()
	r.Settle(ctx, tok)
	if d := time.Since(start); d > settleMax/2 {
		t.Errorf("Settle with a done context took %v", d)
	}
}
