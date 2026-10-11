package nodes

import (
	"errors"
	"testing"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

var testPolicy = reopenPolicy{initial: time.Second, maxAuto: 8 * time.Second, maxDemand: 3 * time.Second}

// want is the part of a step a transition test checks; err is compared by
// presence (and identity for errIdleBeforeReady).
type want struct {
	next     next
	delay    time.Duration
	state    MCPState
	err      error
	onDemand bool
	log      string
	repeat   bool
}

func TestLifecycleTransitions(t *testing.T) {
	crash := errors.New("process crashed")
	closed := errors.New("app closed")
	failed := func(err error) outcome { return outcome{err: err} }
	remote := func(err error) outcome { return outcome{remote: true, err: err} }
	readyThen := func(o outcome) outcome { o.ready = true; return o }
	idle := outcome{remote: true, reason: proto.CloseIdle, ready: true}
	restart := outcome{remote: true, reason: proto.CloseRestart, ready: true}
	for _, tt := range []struct {
		name         string
		lazy, cached bool
		first        next
		outcomes     []outcome
		want         []want
	}{
		{
			name: "eager failures back off to the auto cap", first: openNow,
			outcomes: []outcome{failed(crash), failed(crash), failed(crash), failed(crash), failed(crash)},
			want: []want{
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: crash, log: logReopening},
				{next: waitDelay, delay: 2 * time.Second, state: MCPFailed, err: crash, log: logReopening, repeat: true},
				{next: waitDelay, delay: 4 * time.Second, state: MCPFailed, err: crash, log: logReopening, repeat: true},
				{next: waitDelay, delay: 8 * time.Second, state: MCPFailed, err: crash, log: logReopening, repeat: true},
				{next: waitDelay, delay: 8 * time.Second, state: MCPFailed, err: crash, log: logReopening, repeat: true},
			},
		},
		{
			name: "eager session that got ready resets backoff and warns again", first: openNow,
			outcomes: []outcome{failed(crash), failed(crash), readyThen(remote(crash))},
			want: []want{
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: crash, log: logReopening},
				{next: waitDelay, delay: 2 * time.Second, state: MCPFailed, err: crash, log: logReopening, repeat: true},
				{next: waitDelay, delay: time.Second, state: MCPClosed, err: crash, log: logReopening},
			},
		},
		{
			name: "a different error is not a repeat", first: openNow,
			outcomes: []outcome{failed(crash), failed(closed)},
			want: []want{
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: crash, log: logReopening},
				{next: waitDelay, delay: 2 * time.Second, state: MCPFailed, err: closed, log: logReopening},
			},
		},
		{
			name: "eager idle becomes demand-driven until ready", first: openNow,
			outcomes: []outcome{idle, failed(closed), readyThen(failed(crash))},
			want: []want{
				{next: waitDemand, state: MCPIdle, onDemand: true, log: logIdle},
				// Tools known from the earlier session: a call drives the retry.
				{next: waitDemand, delay: time.Second, state: MCPIdle, err: closed, onDemand: true, log: logDemand},
				// Ready again: back to self-driven recovery.
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: crash, log: logReopening},
			},
		},
		{
			name: "restart reopens at once and resets backoff", first: openNow,
			outcomes: []outcome{failed(crash), failed(crash), restart, failed(crash)},
			want: []want{
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: crash, log: logReopening},
				{next: waitDelay, delay: 2 * time.Second, state: MCPFailed, err: crash, log: logReopening, repeat: true},
				{next: openNow, state: MCPOpening},
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: crash, log: logReopening},
			},
		},
		{
			name: "idle before ready reports why", first: openNow,
			outcomes: []outcome{{remote: true, reason: proto.CloseIdle}},
			want:     []want{{next: waitDemand, state: MCPIdle, err: errIdleBeforeReady, onDemand: true, log: logIdle}},
		},
		{
			name: "lazy with cache waits for a call; failures refuse calls up to the demand cap", lazy: true, cached: true, first: waitDemand,
			outcomes: []outcome{failed(closed), failed(closed), failed(closed), failed(closed)},
			want: []want{
				{next: waitDemand, delay: time.Second, state: MCPIdle, err: closed, onDemand: true, log: logDemand},
				{next: waitDemand, delay: 2 * time.Second, state: MCPIdle, err: closed, onDemand: true, log: logDemand, repeat: true},
				{next: waitDemand, delay: 3 * time.Second, state: MCPIdle, err: closed, onDemand: true, log: logDemand, repeat: true},
				{next: waitDemand, delay: 3 * time.Second, state: MCPIdle, err: closed, onDemand: true, log: logDemand, repeat: true},
			},
		},
		{
			name: "lazy attached failure is failed, not idle", lazy: true, cached: true, first: waitDemand,
			outcomes: []outcome{readyThen(remote(crash)), idle},
			want: []want{
				{next: waitDemand, delay: time.Second, state: MCPFailed, err: crash, onDemand: true, log: logDemand},
				{next: waitDemand, state: MCPIdle, onDemand: true, log: logIdle},
			},
		},
		{
			// Nothing can call an MCP whose tools aren't listed, so it must
			// keep retrying by itself (this once never retried).
			name: "lazy without tools retries on its own until it gets them", lazy: true, first: openNow,
			outcomes: []outcome{failed(closed), failed(closed), failed(closed), readyThen(remote(crash))},
			want: []want{
				{next: waitDelay, delay: time.Second, state: MCPFailed, err: closed, onDemand: true, log: logReopening},
				{next: waitDelay, delay: 2 * time.Second, state: MCPFailed, err: closed, onDemand: true, log: logReopening, repeat: true},
				{next: waitDelay, delay: 3 * time.Second, state: MCPFailed, err: closed, onDemand: true, log: logReopening, repeat: true},
				// Tools listed now: back to demand-driven.
				{next: waitDemand, delay: time.Second, state: MCPFailed, err: crash, onDemand: true, log: logDemand},
			},
		},
		{
			name: "lazy restart stays demand-driven", lazy: true, cached: true, first: waitDemand,
			outcomes: []outcome{restart},
			want:     []want{{next: openNow, state: MCPOpening, onDemand: true}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lc := newLifecycle(tt.lazy, tt.cached, testPolicy)
			if got := lc.first(); got != tt.first {
				t.Fatalf("first = %v, want %v", got, tt.first)
			}
			for i, o := range tt.outcomes {
				s := lc.ended(o)
				got := want{next: s.next, delay: s.delay, state: s.state, err: s.err, onDemand: s.onDemand, log: s.log, repeat: s.repeat}
				if got != tt.want[i] {
					t.Fatalf("outcome %d: got %+v\nwant %+v", i, got, tt.want[i])
				}
			}
		})
	}
}

func TestLifecycleExpiresTools(t *testing.T) {
	for _, tt := range []struct {
		name   string
		lazy   bool
		idle   bool // went idle first, so demand-driven
		reason proto.CloseReason
		want   bool
	}{
		{name: "eager failure", want: true},
		{name: "eager idle close", reason: proto.CloseIdle},
		{name: "eager restart", reason: proto.CloseRestart},
		{name: "eager woken from idle", idle: true},
		{name: "lazy failure", lazy: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			lc := newLifecycle(tt.lazy, false, testPolicy)
			if tt.idle {
				lc.ended(outcome{ready: true, remote: true, reason: proto.CloseIdle})
			}
			if got := lc.expiresTools(tt.reason); got != tt.want {
				t.Fatalf("expiresTools(%q) = %v, want %v", tt.reason, got, tt.want)
			}
		})
	}
}

func TestCallerState(t *testing.T) {
	crash := errors.New("node closed tools: process crashed")
	for _, tt := range []struct {
		step step
		want string
	}{
		{step{state: MCPOpening}, "starting"},
		{step{state: MCPIdle, onDemand: true}, "idle"},
		// Eager callers learn why it's down instead of a bare state.
		{step{state: MCPClosed, err: crash}, crash.Error()},
		{step{state: MCPIdle, err: crash, onDemand: true}, "idle"},
	} {
		if got := callerState(tt.step); got != tt.want {
			t.Errorf("callerState(%+v) = %q, want %q", tt.step, got, tt.want)
		}
	}
}
