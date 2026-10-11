package nodes

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func TestRegistryMCPGraceExpires(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 500 * time.Millisecond})
	r.reopenBackoff, r.maxReopenBackoff = 20*time.Millisecond, 40*time.Millisecond
	f := startFake(t, url, hello("machine"), fakeOptions{failAfterFirst: true})
	first := nextOpen(t, f)
	await(t, func() bool { return len(cat.List()) == 2 && r.Nodes()[0].MCPs[0].State == MCPReady })
	tool, _ := cat.Lookup("machine__tools__echo")
	if err := f.conn.Send(t.Context(), &proto.Envelope{T: proto.TClose, Ch: first.Ch, Error: "process crashed"}); err != nil {
		t.Fatal(err)
	}
	nextOpen(t, f)
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPFailed })
	if len(cat.List()) != 2 || r.Nodes()[0].MCPs[0].Tools != 2 {
		t.Fatal("failed reopen removed tools before grace expired")
	}
	// Callers learn why an eager MCP is down, not just that it is.
	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "process failed to start") {
		t.Fatalf("offline call: %v, want ErrUnavailable with the node's reason", err)
	}
	// Repeated failed opens must not restart the grace timer.
	await(t, func() bool { return len(cat.List()) == 0 && r.Nodes()[0].MCPs[0].Tools == 0 })
	if !r.Nodes()[0].Online {
		t.Fatal("MCP crash marked its node offline")
	}
}

func TestRegistryReconnectReusesHandles(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 750 * time.Millisecond})
	h := hello("machine")
	h.MCPs = append(h.MCPs, proto.MCP{Name: "removed", Transport: proto.TransportStdio})
	f := startFake(t, url, h, fakeOptions{})
	await(t, func() bool { return len(cat.List()) == 4 })
	tool, _ := cat.Lookup("machine__tools__echo")
	removed, _ := cat.Lookup("machine__removed__echo")
	f.disconnect()
	await(t, func() bool { return !r.Nodes()[0].Online })
	disconnected := r.Nodes()[0].DisconnectedAt
	permits := make(chan struct{}, 1)
	replacement := startFake(t, url, hello("machine"), fakeOptions{permits: permits})
	nextOpen(t, replacement)
	info := r.Nodes()[0]
	if !info.Online || !info.DisconnectedAt.IsZero() || len(info.MCPs) != 1 || len(cat.List()) != 2 {
		t.Fatalf("reconnected snapshot: %+v; catalog: %+v", info, cat.List())
	}
	for _, old := range []*mcpHandle{tool.Caller.(*mcpHandle), removed.Caller.(*mcpHandle)} {
		if _, err := old.CallTool(t.Context(), &mcp.CallToolParams{Name: "echo"}); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("call while replacement initializes: %v, want ErrUnavailable", err)
		}
	}
	permits <- struct{}{}
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
	fresh, _ := cat.Lookup(tool.Name)
	if fresh.Caller != tool.Caller {
		t.Fatal("node reconnect replaced routing handle")
	}
	if _, err := tool.Call(t.Context(), nil); err != nil {
		t.Fatalf("old caller after node reconnect: %v", err)
	}
	// Crossing the original deadline exercises cancellation of the node timer.
	await(t, func() bool { return time.Since(disconnected) > r.opts.OfflineGrace+20*time.Millisecond })
	if len(cat.List()) != 2 || !r.Nodes()[0].Online {
		t.Fatal("old node timer removed replacement")
	}
	if _, err := tool.Call(t.Context(), nil); err != nil {
		t.Fatalf("old caller after original grace deadline: %v", err)
	}
}

func TestRegistryCallQueue(t *testing.T) {
	for _, limit := range []int{1, 2} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond, MaxInFlight: limit})
			entered := make(chan string, 10)
			release := make(chan struct{})
			startFake(t, url, hello("machine"), fakeOptions{configure: func(server *mcp.Server) {
				server.AddTool(&mcp.Tool{Name: "block", InputSchema: map[string]any{"type": "object"}},
					func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
						entered <- string(req.Params.Arguments)
						select {
						case <-release:
							return &mcp.CallToolResult{}, nil
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					})
			}})
			await(t, func() bool { return len(cat.List()) == 3 })
			tool, _ := cat.Lookup("machine__tools__block")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var workers sync.WaitGroup
			t.Cleanup(func() { cancel(); workers.Wait() })
			call := func(callCtx context.Context, id string) <-chan error {
				result := make(chan error, 1)
				workers.Go(func() {
					_, err := tool.Call(callCtx, map[string]any{"id": id})
					result <- err
				})
				return result
			}
			waitResult := func(result <-chan error, want error) {
				t.Helper()
				select {
				case err := <-result:
					if !errors.Is(err, want) {
						t.Fatalf("call result: %v, want %v", err, want)
					}
				case <-ctx.Done():
					t.Fatal("call did not finish")
				}
			}
			waitEntered := func() string {
				t.Helper()
				select {
				case id := <-entered:
					return id
				case <-ctx.Done():
					t.Fatal("call did not reach upstream")
					return ""
				}
			}
			var active []<-chan error
			for range limit {
				active = append(active, call(ctx, "active"))
				waitEntered()
			}
			queuedCtx, queuedCancel := context.WithCancel(ctx)
			defer queuedCancel()
			cancelled := call(queuedCtx, "cancelled")
			await(t, func() bool { return r.Nodes()[0].MCPs[0].Queued == 1 })
			first := call(ctx, "first")
			await(t, func() bool { return r.Nodes()[0].MCPs[0].Queued == 2 })
			second := call(ctx, "second")
			await(t, func() bool { return r.Nodes()[0].MCPs[0].Queued == 3 })
			if info := r.Nodes()[0].MCPs[0]; info.InFlight != limit {
				t.Fatalf("in-flight calls: %d, want %d", info.InFlight, limit)
			}
			queuedCancel()
			waitResult(cancelled, context.Canceled)
			await(t, func() bool { return r.Nodes()[0].MCPs[0].Queued == 2 })
			select {
			case id := <-entered:
				t.Fatalf("queued call reached upstream early: %s", id)
			default:
			}
			// Release one slot at a time so upstream arrival proves FIFO order.
			release <- struct{}{}
			if id := waitEntered(); id != `{"id":"first"}` {
				t.Fatalf("first queued call: %s", id)
			}
			release <- struct{}{}
			if id := waitEntered(); id != `{"id":"second"}` {
				t.Fatalf("second queued call: %s", id)
			}
			close(release)
			for _, result := range append(active, first, second) {
				waitResult(result, nil)
			}
			await(t, func() bool {
				info := r.Nodes()[0].MCPs[0]
				return info.InFlight == 0 && info.Queued == 0
			})
		})
	}
}

func TestRegistryQueuedCallUnavailable(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 200 * time.Millisecond, MaxInFlight: 1})
	entered := make(chan struct{}, 1)
	f := startFake(t, url, hello("machine"), fakeOptions{configure: func(server *mcp.Server) {
		server.AddTool(&mcp.Tool{Name: "block", InputSchema: map[string]any{"type": "object"}},
			func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			})
	}})
	await(t, func() bool { return len(cat.List()) == 3 })
	tool, _ := cat.Lookup("machine__tools__block")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	active := make(chan error, 1)
	workers.Go(func() {
		_, err := tool.Call(ctx, nil)
		active <- err
	})
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("active call did not reach upstream")
	}
	queued := make(chan error, 1)
	workers.Go(func() {
		_, err := tool.Call(ctx, nil)
		queued <- err
	})
	await(t, func() bool { return r.Nodes()[0].MCPs[0].Queued == 1 })
	f.disconnect()
	select {
	case err := <-queued:
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("queued call after disconnect: %v, want ErrUnavailable", err)
		}
	case <-ctx.Done():
		t.Fatal("queued call did not fail after disconnect")
	}
	select {
	case err := <-active:
		if err == nil {
			t.Fatal("active call succeeded after disconnect")
		}
	case <-ctx.Done():
		t.Fatal("active call did not fail after disconnect")
	}
	await(t, func() bool {
		info := r.Nodes()[0]
		return !info.Online && info.MCPs[0].InFlight == 0 && info.MCPs[0].Queued == 0
	})
}

// A stuck MCP must not collect waiting calls without end: past
// queueFactor × the in-flight limit a call fails at once.
func TestCallQueueIsBounded(t *testing.T) {
	h := newMCPHandle("machine", "tools", 2)
	h.perClient = true // skips the shared-session check; slots work the same
	ctx, cancel := context.WithCancel(t.Context())
	var waiters sync.WaitGroup
	t.Cleanup(func() { cancel(); waiters.Wait() })
	for range 2 {
		if err := h.acquire(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for range queueFactor * 2 {
		waiters.Go(func() { h.acquire(ctx) })
	}
	await(t, func() bool { _, q, _ := h.counts(); return q == queueFactor*2 })
	err := h.acquire(ctx)
	if !errors.Is(err, ErrBusy) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("acquire with a full queue = %v, want ErrBusy", err)
	}
	if _, q, _ := h.counts(); q != queueFactor*2 {
		t.Fatalf("queued = %d after a refused call", q)
	}
	// A freed slot goes to a waiter, which makes room in the queue again.
	h.mu.Lock()
	h.releaseLocked()
	h.mu.Unlock()
	await(t, func() bool { _, q, _ := h.counts(); return q == queueFactor*2-1 })
	done := make(chan error, 1)
	waiters.Go(func() { done <- h.acquire(ctx) })
	await(t, func() bool { _, q, _ := h.counts(); return q == queueFactor*2 })
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued call = %v", err)
	}
}
