package nodes

import (
	"errors"
	"fmt"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// lifecycle is one MCP's reopen policy. It holds every piece of state that
// decides what the worker does after a channel ends, and nothing else: no
// locks, timers or I/O, so each transition is a plain function call that
// tests can drive directly. The MCP's worker goroutine owns it.
//
//	          first()
//	             │
//	 ┌───────────┼──────────────┐
//	 ▼           ▼              ▼
//	waitDemand  openNow     waitDelay ──▶ open
//	 │ (call)                              │
//	 └────────────────▶ open ──▶ ended(outcome) ──▶ step
//
// An MCP is either self-driven (eager, until it goes idle) or demand-driven
// (lazy, or eager woken from idle): demand-driven MCPs open only when a call
// asks, and a failure refuses calls for a short backoff instead of reopening
// by itself. The exception is a demand-driven MCP whose tools aren't known
// yet: no call can name it, so it keeps retrying on its own.
type lifecycle struct {
	lazy bool
	// onDemand: the next open waits for a call. Lazy MCPs always are; eager
	// ones are after an idle close until a session gets ready again.
	onDemand bool
	// known: tools are listed, from the node's cache or a session that got
	// ready, so calls can ask for this MCP.
	known   bool
	backoff time.Duration // delay for the next failure
	lastErr string        // the previous outcome's error, to quiet repeats
	policy  reopenPolicy
}

type reopenPolicy struct {
	initial   time.Duration // first delay after a failure
	maxAuto   time.Duration // cap while the MCP reopens by itself
	maxDemand time.Duration // cap while calls drive reopening
}

// next is what the worker waits for before opening a channel again.
type next int

const (
	openNow    next = iota
	waitDelay       // a timer of step.delay, then open
	waitDemand      // a call; calls before step.delay has passed are refused
)

func (n next) String() string {
	return [...]string{"openNow", "waitDelay", "waitDemand"}[n]
}

// outcome is how one channel ended.
type outcome struct {
	ready  bool              // its session got ready (tools listed)
	reason proto.CloseReason // the node's close reason, if it closed it
	remote bool              // the node closed it (rather than a hub-side error)
	err    error
}

// step is the worker's next move after an outcome.
type step struct {
	next  next
	delay time.Duration
	state MCPState
	// err is the reason the MCP is unavailable, for callers and MCPInfo.
	// Nil for idle and restart closes, which aren't failures.
	err error
	// onDemand is the demand mode the handle should advertise to callers.
	onDemand bool
	// log is the message to log, if any; repeat says it's the same failure as
	// last time, which logs at debug so an app that stays closed isn't noisy.
	log    string
	repeat bool
}

const (
	logIdle      = "MCP idle"
	logDemand    = "MCP unavailable; waiting for demand"
	logReopening = "MCP unavailable; reopening"
)

var errIdleBeforeReady = errors.New("went idle before its session was ready")

func newLifecycle(lazy, cached bool, policy reopenPolicy) *lifecycle {
	return &lifecycle{lazy: lazy, onDemand: lazy, known: cached, backoff: policy.initial, policy: policy}
}

// first is how the worker starts: a lazy MCP whose tools the node had
// cached waits for a call; everything else opens right away to list tools.
func (l *lifecycle) first() next {
	if l.lazy && l.known {
		return waitDemand
	}
	return openNow
}

// expiresTools says whether a channel ending this way starts the offline
// grace period after which the MCP's tools leave the catalog. Demand-driven
// MCPs keep their tools: they're expected to be closed most of the time.
func (l *lifecycle) expiresTools(reason proto.CloseReason) bool {
	return !l.onDemand && reason != proto.CloseIdle && reason != proto.CloseRestart
}

func (l *lifecycle) ended(o outcome) step {
	if o.ready {
		l.backoff, l.onDemand, l.known, l.lastErr = l.policy.initial, l.lazy, true, ""
	}
	text := fmt.Sprint(o.err)
	repeat := text == l.lastErr
	l.lastErr = text
	switch o.reason {
	case proto.CloseIdle:
		l.onDemand, l.backoff = true, l.policy.initial
		s := step{next: waitDemand, state: MCPIdle, onDemand: true, log: logIdle}
		if !o.ready {
			s.err = errIdleBeforeReady
		}
		return s
	case proto.CloseRestart:
		l.backoff = l.policy.initial
		return step{next: openNow, state: MCPOpening, onDemand: l.onDemand}
	}
	state := MCPFailed
	if o.remote {
		state = MCPClosed
	}
	if l.onDemand && l.known {
		// Each attempt needs a call, so user traffic already bounds hammering.
		delay := min(l.backoff, l.policy.maxDemand)
		l.backoff = min(delay*2, l.policy.maxDemand)
		state = MCPFailed
		if !o.ready {
			state = MCPIdle
		}
		return step{next: waitDemand, delay: delay, state: state, err: o.err, onDemand: true, log: logDemand, repeat: repeat}
	}
	delay, limit := l.backoff, l.policy.maxAuto
	if l.onDemand {
		// Tools not known yet: stay demand-driven so a call made once they're
		// listed opens it, but retry promptly meanwhile.
		delay, limit = min(delay, l.policy.maxDemand), l.policy.maxDemand
	}
	l.backoff = min(delay*2, limit)
	return step{next: waitDelay, delay: delay, state: state, err: o.err, onDemand: l.onDemand, log: logReopening, repeat: repeat}
}
