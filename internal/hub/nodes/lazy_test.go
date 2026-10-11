package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

func lazyHello(t *testing.T, cached bool) *proto.Hello {
	t.Helper()
	h := hello("machine")
	h.MCPs[0].Start = proto.StartLazy
	if cached {
		tools, err := json.Marshal([]*mcp.Tool{{Name: "echo", Description: "cached", InputSchema: map[string]any{"type": "object"}}})
		if err != nil {
			t.Fatal(err)
		}
		h.MCPs[0].Tools = tools
	}
	return h
}

// Seeing Done proves a call entered its startup wait before the fake is released.
type startupContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (ctx *startupContext) Done() <-chan struct{} {
	ctx.once.Do(func() { close(ctx.waiting) })
	return ctx.Context.Done()
}

func startWaitingCall(t *testing.T, workers *sync.WaitGroup, ctx context.Context, tool *catalog.Tool) <-chan error {
	t.Helper()
	waitingCtx := &startupContext{Context: ctx, waiting: make(chan struct{})}
	result := make(chan error, 1)
	workers.Go(func() {
		_, err := tool.Call(waitingCtx, nil)
		result <- err
	})
	select {
	case <-waitingCtx.waiting:
	case <-ctx.Done():
		t.Fatal("call did not wait for startup")
	}
	return result
}

func callResult(t *testing.T, result <-chan error, want error) error {
	t.Helper()
	select {
	case err := <-result:
		if !errors.Is(err, want) {
			t.Fatalf("call result: %v, want %v", err, want)
		}
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("call did not finish")
		return nil
	}
}

func assertNoMoreOpens(t *testing.T, f *fakeNode, count int, duration time.Duration) {
	t.Helper()
	deadline := time.Now().Add(duration)
	await(t, func() bool {
		if got := f.countOpens(); got != count {
			t.Fatalf("opens = %d, want %d", got, count)
		}
		return time.Now().After(deadline)
	})
}

func TestLazyCachedToolsAndIdle(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond, MaxInFlight: 1})
	permits := make(chan struct{}, 2)
	f := startFake(t, url, lazyHello(t, true), fakeOptions{permits: permits})
	await(t, func() bool { return len(cat.List()) == 1 && r.Nodes()[0].MCPs[0].State == MCPIdle })
	if info := r.Nodes()[0].MCPs[0]; info.Start != proto.StartLazy || info.Tools != 1 || info.Error != "" {
		t.Fatalf("cached snapshot: %+v", info)
	}
	assertNoMoreOpens(t, f, 0, 20*time.Millisecond)
	tool, _ := cat.Lookup("machine__tools__echo")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	var results []<-chan error
	// One fewer than a slot and a full queue hold (TestStartupWaitersAreBounded),
	// leaving room for the cancelled and expired calls below.
	for range queueFactor {
		results = append(results, startWaitingCall(t, &workers, ctx, tool))
	}
	first := nextOpen(t, f)
	if info := r.Nodes()[0].MCPs[0]; info.InFlight != 0 || info.Queued != 0 {
		t.Fatalf("startup consumed call slots: %+v", info)
	}
	if f.countOpens() != 1 {
		t.Fatal("concurrent calls did not share startup")
	}
	cancelCtx, cancelCall := context.WithCancel(ctx)
	cancelled := startWaitingCall(t, &workers, cancelCtx, tool)
	cancelCall()
	callResult(t, cancelled, context.Canceled)
	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer deadlineCancel()
	expired := startWaitingCall(t, &workers, deadlineCtx, tool)
	callResult(t, expired, context.DeadlineExceeded)
	permits <- struct{}{}
	for _, result := range results {
		callResult(t, result, nil)
	}
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady && len(cat.List()) == 2 })
	fresh, _ := cat.Lookup(tool.Name)
	if fresh.Upstream.Description == "cached" || fresh.Caller != tool.Caller {
		t.Fatal("fresh list did not replace cached tools with the stable handle")
	}
	addTool(f.server, "third")
	await(t, func() bool { return len(cat.List()) == 3 })
	f.closeChannel(t, first.Ch, proto.CloseIdle)
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPIdle })
	assertNoMoreOpens(t, f, 1, r.opts.OfflineGrace+20*time.Millisecond)
	if info := r.Nodes()[0].MCPs[0]; info.Tools != 3 || len(cat.List()) != 3 || info.Error != "" {
		t.Fatalf("idle tools expired: %+v", info)
	}
	next := startWaitingCall(t, &workers, ctx, tool)
	second := nextOpen(t, f)
	if second.Ch == first.Ch {
		t.Fatal("idle reopen reused channel")
	}
	permits <- struct{}{}
	callResult(t, next, nil)
	if f.countOpens() != 2 {
		t.Fatal("next call did not open exactly one channel")
	}
}

func TestLazyWithoutUsableCacheOpensImmediately(t *testing.T) {
	for _, cache := range []string{"", `"bad"`, `null`, `[null]`, `{}`} {
		t.Run(cache, func(t *testing.T) {
			r, cat, url := newTestRegistry(t)
			h := lazyHello(t, false)
			h.MCPs[0].Tools = json.RawMessage(cache)
			f := startFake(t, url, h, fakeOptions{})
			nextOpen(t, f)
			await(t, func() bool { return len(cat.List()) == 2 && r.Nodes()[0].MCPs[0].State == MCPReady })
			if f.countOpens() != 1 {
				t.Fatal("cache discovery opened more than once")
			}
		})
	}
}

func TestLazyOpenFailureAndBackoff(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond})
	r.maxDemandBackoff = 225 * time.Millisecond
	permits := make(chan struct{}, 3)
	f := startFake(t, url, lazyHello(t, true), fakeOptions{failOpens: 5, permits: permits})
	tool, _ := cat.Lookup("machine__tools__echo")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var workers sync.WaitGroup
	t.Cleanup(func() { cancel(); workers.Wait() })
	for attempt := range 5 {
		var results []<-chan error
		for range 4 {
			results = append(results, startWaitingCall(t, &workers, ctx, tool))
		}
		nextOpen(t, f)
		permits <- struct{}{}
		for _, result := range results {
			err := callResult(t, result, ErrUnavailable)
			if !strings.Contains(err.Error(), "process failed to start") {
				t.Fatalf("startup failure lost its cause: %v", err)
			}
		}
		await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPIdle })
		if _, err := tool.Call(ctx, nil); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "process failed to start") || !strings.Contains(err.Error(), "retrying in 1s") {
			t.Fatalf("call during backoff: %v", err)
		}
		handle := tool.Caller.(*mcpHandle)
		handle.mu.Lock()
		retryAt := handle.retryAt
		handle.mu.Unlock()
		wantBackoff := min(r.reopenBackoff*time.Duration(1<<attempt), r.maxDemandBackoff)
		if remaining := time.Until(retryAt); remaining > wantBackoff || remaining < wantBackoff/2 {
			t.Fatalf("backoff remaining %s, want approximately %s", remaining, wantBackoff)
		}
		assertNoMoreOpens(t, f, attempt+1, time.Until(retryAt)+20*time.Millisecond)
		if len(cat.List()) != 1 {
			t.Fatal("lazy failure expired cached tools")
		}
	}
	next := startWaitingCall(t, &workers, ctx, tool)
	nextOpen(t, f)
	permits <- struct{}{}
	callResult(t, next, nil)
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
	handle := tool.Caller.(*mcpHandle)
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if !handle.retryAt.IsZero() {
		t.Fatal("successful open did not reset retry deadline")
	}
	if handle.lastErr != nil {
		t.Fatalf("successful open did not clear last error: %v", handle.lastErr)
	}
}

func TestLazyAttachedFailure(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond})
	f := startFake(t, url, lazyHello(t, false), fakeOptions{})
	first := nextOpen(t, f)
	await(t, func() bool { return len(cat.List()) == 2 && r.Nodes()[0].MCPs[0].State == MCPReady })
	tool, _ := cat.Lookup("machine__tools__echo")
	f.closeChannel(t, first.Ch, "")
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPFailed })
	if r.Nodes()[0].MCPs[0].Error == "" {
		t.Fatal("attached failure was not recorded")
	}
	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("call during failure backoff: %v", err)
	}
	assertNoMoreOpens(t, f, 1, r.maxReopenBackoff+20*time.Millisecond)
	if len(cat.List()) != 2 {
		t.Fatal("lazy failure removed tools")
	}
	if _, err := tool.Call(t.Context(), nil); err != nil {
		t.Fatalf("call after backoff: %v", err)
	}
	if f.countOpens() != 2 {
		t.Fatal("call after failure did not reopen")
	}
}

func TestRegistryRestartClose(t *testing.T) {
	for _, lazy := range []bool{false, true} {
		t.Run(map[bool]string{false: "eager", true: "lazy"}[lazy], func(t *testing.T) {
			r, cat, url := newTestRegistry(t)
			r.reopenBackoff, r.maxReopenBackoff = 2*time.Second, 4*time.Second
			permits := make(chan struct{}, 3)
			permits <- struct{}{}
			h := hello("machine")
			if lazy {
				h.MCPs[0].Start = proto.StartLazy
			}
			f := startFake(t, url, h, fakeOptions{permits: permits})
			first := nextOpen(t, f)
			await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
			f.closeChannel(t, first.Ch, proto.CloseRestart)
			deadline, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
			defer cancel()
			var second proto.Envelope
			select {
			case second = <-f.opens:
			case <-deadline.Done():
				t.Fatal("restart used failure backoff")
			}
			if len(cat.List()) != 2 || r.Nodes()[0].MCPs[0].Error != "" {
				t.Fatal("restart removed tools or recorded failure")
			}
			assertNoMoreOpens(t, f, 2, r.opts.OfflineGrace+20*time.Millisecond)
			if len(cat.List()) != 2 {
				t.Fatal("restart started tool expiry")
			}
			permits <- struct{}{}
			await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
			// A restart must not leave an on-demand signal that opens after idle.
			f.closeChannel(t, second.Ch, proto.CloseIdle)
			await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPIdle })
			assertNoMoreOpens(t, f, 2, 30*time.Millisecond)
		})
	}
}

func TestEagerIdleClose(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 100 * time.Millisecond})
	f := startFake(t, url, hello("machine"), fakeOptions{})
	first := nextOpen(t, f)
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
	if r.Nodes()[0].MCPs[0].Start != proto.StartEager {
		t.Fatal("empty start did not default to eager")
	}
	f.closeChannel(t, first.Ch, proto.CloseIdle)
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPIdle })
	assertNoMoreOpens(t, f, 1, r.maxReopenBackoff+20*time.Millisecond)
	if len(cat.List()) != 2 {
		t.Fatal("eager idle close expired tools")
	}
	tool, _ := cat.Lookup("machine__tools__echo")
	if _, err := tool.Call(t.Context(), nil); err != nil {
		t.Fatalf("call after eager idle: %v", err)
	}
	if f.countOpens() != 2 {
		t.Fatal("eager idle call did not reopen")
	}
	second := nextOpen(t, f)
	f.closeChannel(t, second.Ch, "")
	// Once ready again, an eager MCP resumes automatic failure recovery.
	nextOpen(t, f)
	await(t, func() bool { return r.Nodes()[0].MCPs[0].State == MCPReady })
}

func TestLazyReconnectWithCache(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 200 * time.Millisecond})
	h := lazyHello(t, true)
	f := startFake(t, url, h, fakeOptions{})
	tool, _ := cat.Lookup("machine__tools__echo")
	f.disconnect()
	await(t, func() bool { return !r.Nodes()[0].Online })
	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("offline lazy call: %v", err)
	}
	replacement := startFake(t, url, h, fakeOptions{})
	if info := r.Nodes()[0]; !info.Online || info.MCPs[0].State != MCPIdle {
		t.Fatalf("reconnected lazy node: %+v", info)
	}
	fresh, _ := cat.Lookup(tool.Name)
	if fresh.Caller != tool.Caller {
		t.Fatal("lazy reconnect replaced handle")
	}
	assertNoMoreOpens(t, replacement, 0, r.opts.OfflineGrace+20*time.Millisecond)
	if len(cat.List()) != 1 {
		t.Fatal("reconnected idle tools expired")
	}
	if _, err := tool.Call(t.Context(), nil); err != nil {
		t.Fatalf("old caller after lazy reconnect: %v", err)
	}
	if replacement.countOpens() != 1 {
		t.Fatal("reconnected lazy call did not open")
	}
	replacement.disconnect()
	await(t, func() bool { return len(r.Nodes()) == 0 && len(cat.List()) == 0 })
}

// A lazy MCP added while its app was closed has no tools to call, so nothing
// would ever demand it: the hub must keep retrying until it gets them.
func TestLazyWithoutToolsRetriesWithoutDemand(t *testing.T) {
	r, cat, url := newTestRegistry(t)
	r.reopenBackoff, r.maxDemandBackoff = 10*time.Millisecond, 20*time.Millisecond
	f := startFake(t, url, lazyHello(t, false), fakeOptions{failOpens: 3})
	for range 4 {
		nextOpen(t, f)
	}
	await(t, func() bool {
		_, ok := cat.Lookup("machine__tools__echo")
		return ok
	})
}

// Calls waiting for a lazy MCP to start are bounded like calls waiting for a
// slot: past what the slots and the queue hold a call is refused at once.
func TestStartupWaitersAreBounded(t *testing.T) {
	r, cat, url := newTestRegistryOptions(t, Options{OfflineGrace: 500 * time.Millisecond, MaxInFlight: 1})
	permits := make(chan struct{})
	startFake(t, url, lazyHello(t, true), fakeOptions{permits: permits})
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].State == MCPIdle })
	tool, _ := cat.Lookup("machine__tools__echo")

	var workers sync.WaitGroup
	t.Cleanup(workers.Wait)
	var waiting []<-chan error
	for range 1 + queueFactor {
		waiting = append(waiting, startWaitingCall(t, &workers, t.Context(), tool))
	}
	await(t, func() bool { return infoOf(r, "machine").MCPs[0].Starting == 1+queueFactor })
	if _, err := tool.Call(t.Context(), nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("call past the bound: %v, want %v", err, ErrBusy)
	}
	close(permits)
	for _, result := range waiting {
		callResult(t, result, nil)
	}
	if info := infoOf(r, "machine").MCPs[0]; info.Queued != 0 || info.Starting != 0 {
		t.Errorf("after start: %+v", info)
	}
}
