// Package progress routes progress notifications from upstream MCPs back to
// the client call that asked for them.
//
// A client's progress token only means something on its own session, and
// two clients may pick the same token, so the gateway gives each upstream
// call a token of its own (Register) and the node registry's MCP clients
// hand every notifications/progress they receive to Dispatch, which
// forwards it under the client's original token.
//
// Ordering: the SDK delivers a call's response at once but queues incoming
// notifications for their handler, so a result can overtake the progress
// the MCP sent before it, and MCP doesn't allow progress after the result.
// The hub's channel reads frames in order and calls Expect for each progress
// notification before the SDK sees the next frame; Settle then waits until
// every expected notification has been dispatched. That is a barrier, not a
// timing guess: by the time the result is read, all progress sent before it
// has been counted.
package progress

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Notify delivers a notification to the client that registered the token;
// p.ProgressToken is already the client's own.
type Notify func(ctx context.Context, p *mcp.ProgressNotificationParams) error

// Router is safe for concurrent use. The zero value is not usable; call New.
type Router struct {
	mu   sync.Mutex
	subs map[string]*sub
}

type sub struct {
	notify Notify
	// pending counts notifications read off the wire (Expect) and not yet
	// dispatched. done is closed and replaced whenever pending drops to 0.
	pending int
	idle    chan struct{}
}

func New() *Router { return &Router{subs: map[string]*sub{}} }

// settleMax bounds Settle in case an expected notification never reaches
// Dispatch (the SDK drops one it can't decode).
const settleMax = time.Second

// Register starts routing for one upstream call. clientToken is the token
// the client sent; notify reaches the client. It returns the token to send
// upstream and a release func the caller must call when the call ends, after
// which late notifications are dropped.
func (r *Router) Register(clientToken any, notify Notify) (token string, release func()) {
	var b [12]byte
	rand.Read(b[:])
	token = "mm-" + hex.EncodeToString(b[:])
	r.mu.Lock()
	r.subs[token] = &sub{idle: closed(), notify: func(ctx context.Context, p *mcp.ProgressNotificationParams) error {
		q := *p
		q.ProgressToken = clientToken
		return notify(ctx, &q)
	}}
	r.mu.Unlock()
	return token, func() {
		r.mu.Lock()
		delete(r.subs, token)
		r.mu.Unlock()
	}
}

func closed() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// Expect records that a progress notification for token was read from an
// upstream connection and will reach Dispatch. Call it in transport order,
// before the next message is read. Tokens without a call are ignored.
func (r *Router) Expect(token any) {
	t, ok := token.(string)
	if !ok {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.subs[t]; s != nil {
		if s.pending == 0 {
			s.idle = make(chan struct{})
		}
		s.pending++
	}
}

// Dispatch forwards an upstream notification. Unknown tokens (a call that
// already ended, or a token the hub didn't issue) are dropped; it reports
// whether the notification was delivered.
func (r *Router) Dispatch(ctx context.Context, p *mcp.ProgressNotificationParams) bool {
	token, ok := p.ProgressToken.(string)
	if !ok {
		return false
	}
	r.mu.Lock()
	s := r.subs[token]
	r.mu.Unlock()
	if s == nil {
		return false
	}
	err := s.notify(ctx, p)
	r.mu.Lock()
	if s.pending > 0 {
		if s.pending--; s.pending == 0 {
			close(s.idle)
		}
	}
	r.mu.Unlock()
	return err == nil
}

// Settle waits until every notification Expect counted for token has been
// dispatched, so they reach the client before the call's result. It also
// returns when ctx ends or after settleMax.
func (r *Router) Settle(ctx context.Context, token string) {
	r.mu.Lock()
	s := r.subs[token]
	var idle chan struct{}
	if s != nil {
		idle = s.idle
	}
	r.mu.Unlock()
	if idle == nil {
		return
	}
	timer := time.NewTimer(settleMax)
	defer timer.Stop()
	select {
	case <-idle:
	case <-ctx.Done():
	case <-timer.C:
	}
}

// Len is the number of calls being routed (for tests and metrics).
func (r *Router) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.subs)
}
