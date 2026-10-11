package nodes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestDemandRetryMessage(t *testing.T) {
	for _, tt := range []struct {
		name      string
		remaining time.Duration
		lastErr   error
		want      string
	}{
		{"minimum", 100 * time.Millisecond, errors.New("app closed"), "app closed (retrying in 1s)"},
		{"round down", 2400 * time.Millisecond, errors.New("app closed"), "app closed (retrying in 2s)"},
		{"round up", 2600 * time.Millisecond, errors.New("app closed"), "app closed (retrying in 3s)"},
		{"no cause", 2400 * time.Millisecond, nil, "retrying in 2s"},
	} {
		// The gateway prefixes "node/mcp is unavailable:", so the error is only the reason.
		t.Run(tt.name, func(t *testing.T) {
			h := newMCPHandle("machine", "tools", 1)
			demand := make(chan struct{}, 1)
			h.configure(t.Context(), demand, true, false)
			h.endOpen("idle", tt.lastErr, time.Now().Add(tt.remaining), true)
			err := h.ensureReady(t.Context())
			if !errors.Is(err, ErrUnavailable) || err.Error() != tt.want {
				t.Fatalf("retry error = %v, want %s", err, tt.want)
			}
			select {
			case <-demand:
				t.Fatal("call during backoff requested another open")
			default:
			}
		})
	}
}

func TestLazyNodeErrorReachesCaller(t *testing.T) {
	const reason = "connect http://127.0.0.1:9000/sse: nothing is listening (is the app serving this MCP running?)"
	for _, tt := range []struct {
		name string
		opts fakeOptions
	}{
		{"opened", fakeOptions{failOpens: 1}},
		{"close before opened", fakeOptions{failOpens: 1, closeOnOpen: true}},
		{"close during discovery", fakeOptions{closeOnMethod: "server/discover"}},
		{"close during initialize", fakeOptions{closeOnMethod: "initialize", sessionBased: true}},
		{"close during tool listing", fakeOptions{closeOnMethod: "tools/list"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, cat, url := newTestRegistry(t)
			r.reopenBackoff = time.Second
			r.maxDemandBackoff = time.Second
			opts := tt.opts
			opts.openError = reason
			startFake(t, url, lazyHello(t, true), opts)
			tool, _ := cat.Lookup("machine__tools__echo")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if _, err := tool.Call(ctx, nil); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), reason) {
				t.Fatalf("first call lost node error: %v", err)
			}
			await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPIdle })
			if got := r.Nodes()[0].MCPs[0].Error; !strings.Contains(got, reason) {
				t.Fatalf("MCP status lost node error: %s", got)
			}
			if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), reason) || !strings.Contains(err.Error(), "retrying in 1s") {
				t.Fatalf("retry call lost node error: %v", err)
			}
		})
	}
}

type retryLog struct {
	Level   string
	Message string `json:"msg"`
	Error   string
	RetryIn time.Duration `json:"retry_in"`
}

type retryLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *retryLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *retryLogBuffer) retries(t *testing.T, message string) []retryLog {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var logs []retryLog
	for line := range strings.SplitSeq(strings.TrimSpace(b.buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry retryLog
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry.Message == message {
			logs = append(logs, entry)
		}
	}
	return logs
}

func TestRegistryBackoffAndRepeatedLogs(t *testing.T) {
	for _, tt := range []struct {
		name    string
		demand  bool
		initial time.Duration
	}{
		{"demand", true, 40 * time.Millisecond},
		{"demand caps initial backoff", true, 120 * time.Millisecond},
		{"eager", false, 40 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var buffer retryLogBuffer
			logger := slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug}))
			r, cat, url := newTestRegistryOptions(t, Options{Logger: logger, OfflineGrace: 100 * time.Millisecond})
			r.reopenBackoff = tt.initial
			r.maxDemandBackoff = 60 * time.Millisecond
			r.maxReopenBackoff = 120 * time.Millisecond
			h := hello("machine")
			cap, message := r.maxReopenBackoff, "MCP unavailable; reopening"
			if tt.demand {
				h = lazyHello(t, true)
				cap, message = r.maxDemandBackoff, "MCP unavailable; waiting for demand"
			}
			permits := make(chan struct{})
			f := startFake(t, url, h, fakeOptions{failOpens: 5, permits: permits})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			var workers sync.WaitGroup
			t.Cleanup(func() { cancel(); workers.Wait() })
			for attempt := range 5 {
				var result <-chan error
				if tt.demand {
					tool, _ := cat.Lookup("machine__tools__echo")
					result = startWaitingCall(t, &workers, ctx, tool)
				}
				nextOpen(t, f)
				select {
				case permits <- struct{}{}:
				case <-ctx.Done():
					t.Fatal("fake node did not accept permit")
				}
				if tt.demand {
					callResult(t, result, ErrUnavailable)
				}
				await(t, func() bool { return len(buffer.retries(t, message)) == attempt+1 })
				entry := buffer.retries(t, message)[attempt]
				wantBackoff := min(tt.initial*time.Duration(1<<attempt), cap)
				wantLevel := "WARN"
				// The same failure repeating logs at debug in both modes.
				if attempt > 0 {
					wantLevel = "DEBUG"
				}
				if entry.RetryIn != wantBackoff || entry.Level != wantLevel {
					t.Fatalf("attempt %d log = %+v, want %s with retry_in %s", attempt, entry, wantLevel, wantBackoff)
				}
				if tt.demand {
					tool, _ := cat.Lookup("machine__tools__echo")
					handle := tool.Caller.(*mcpHandle)
					await(t, func() bool {
						handle.mu.Lock()
						defer handle.mu.Unlock()
						return !time.Now().Before(handle.retryAt)
					})
				}
			}
			if tt.demand {
				assertNoMoreOpens(t, f, 5, 20*time.Millisecond)
			}
			// A new outage should warn after a successful session.
			var result <-chan error
			if tt.demand {
				tool, _ := cat.Lookup("machine__tools__echo")
				result = startWaitingCall(t, &workers, ctx, tool)
			}
			opened := nextOpen(t, f)
			permits <- struct{}{}
			if tt.demand {
				callResult(t, result, nil)
			}
			await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
			if tt.demand {
				if err := f.conn.Send(ctx, &proto.Envelope{T: proto.TClose, Ch: opened.Ch, Error: "app exited"}); err != nil {
					t.Fatal(err)
				}
				await(t, func() bool { return len(buffer.retries(t, message)) == 6 })
				if entry := buffer.retries(t, message)[5]; entry.Level != "WARN" {
					t.Fatalf("new outage log = %+v, want WARN", entry)
				}
			}
		})
	}
}
