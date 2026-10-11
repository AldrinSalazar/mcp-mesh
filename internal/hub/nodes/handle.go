package nodes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/hub/catalog"
	"github.com/AldrinSalazar/mcp-mesh/internal/hub/identity"
)

// ErrUnavailable means the MCP has no live session to route a call to.
var ErrUnavailable = errors.New("MCP unavailable")

// ErrBusy means the MCP's call queue is full. It also matches
// ErrUnavailable.
var ErrBusy = errors.New("MCP busy")

// queueFactor bounds the calls waiting for a slot on one MCP, as a multiple
// of its in-flight limit. Past that a call fails at once: each waiter holds a
// client request open, and a stuck MCP shouldn't collect them without end.
// Calls waiting for the MCP to start (or for their per-client process to
// open) count too: with the calls in flight and queued they may be at most
// what the slots and the queue hold together.
const queueFactor = 4

// unavailableError is an ErrUnavailable whose text is only the reason: the
// gateway already says which MCP is unavailable, so the node and MCP names
// aren't repeated.
type unavailableError struct {
	reason string
	cause  error
}

func (e *unavailableError) Error() string { return e.reason }
func (e *unavailableError) Unwrap() []error {
	if e.cause == nil {
		return []error{ErrUnavailable}
	}
	return []error{ErrUnavailable, e.cause}
}

// A handle outlives channels and node connections, so catalog callers remain
// usable after reconnects. The registry owns its session; calls own their slots.
type mcpHandle struct {
	node, name string
	limit      int
	mu         sync.Mutex
	session    *mcp.ClientSession
	sessionCtx context.Context
	state      string
	inFlight   int
	queue      []*callWaiter
	starting   int  // calls waiting for the MCP to start or their process to open
	onDemand   bool // also true for eager MCPs waking from idle
	lazy       bool
	demand     chan struct{}
	nodeCtx    context.Context
	opening    *openAttempt
	retryAt    time.Time
	lastErr    error
	content    catalog.Content // last listed; see setContent
	listed     bool            // content was set at least once

	// Per-client MCPs: each client's calls go to a session of its own, opened
	// on its first call through openClient (set for each node connection).
	perClient     bool
	openClient    func(ctx context.Context, c identity.Client) (*clientSession, error)
	clients       map[string]*clientSession
	clientOpening map[string]*openAttempt
	// isRevoked reports a client key the registry was told is revoked
	// (Registry.CloseClient); nil means none is. It takes the registry's
	// lock, so it is never called with h.mu held.
	isRevoked func(clientID string) bool
	counters  *counters // the registry's; nil in tests that build handles alone
}

// clientSession is one client's session on a per-client MCP.
type clientSession struct {
	session *mcp.ClientSession
	done    <-chan struct{} // closed when its channel ends
	// tools is what its process listed, by upstream name. The catalog holds
	// one definition per tool, but each process lists its own, so a call is
	// admitted only when they match (catalog.Listed.Check).
	tools map[string]*mcp.Tool
}

func (cs *clientSession) listed(name string) (*mcp.Tool, bool) {
	t, ok := cs.tools[name]
	return t, ok
}

type openAttempt struct {
	done chan struct{}
	err  error
}

type callWaiter struct {
	ready   chan struct{}
	granted bool
	err     error
}

var (
	_ catalog.Caller         = (*mcpHandle)(nil)
	_ catalog.PromptGetter   = (*mcpHandle)(nil)
	_ catalog.ResourceReader = (*mcpHandle)(nil)
)

func newMCPHandle(node, name string, limit int) *mcpHandle {
	return &mcpHandle{node: node, name: name, limit: limit, state: "starting"}
}

func (h *mcpHandle) unavailableLocked() error {
	if h.session != nil && h.sessionCtx.Err() == nil {
		return nil
	}
	return &unavailableError{reason: h.state}
}

func (h *mcpHandle) setSession(session *mcp.ClientSession, ctx context.Context, state string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.session, h.sessionCtx, h.state = session, ctx, state
	if session != nil {
		h.onDemand = h.lazy
		h.finishOpenLocked(nil)
		h.retryAt = time.Time{}
		h.lastErr = nil
	}
	if session == nil {
		// Offline calls should fail promptly even if old calls still own slots.
		for _, waiter := range h.queue {
			waiter.err = h.unavailableLocked()
			close(waiter.ready)
		}
		h.queue = nil
	}
}

func (h *mcpHandle) finishOpenLocked(err error) {
	if h.opening != nil {
		h.opening.err = err
		close(h.opening.done)
		h.opening = nil
	}
}

func (h *mcpHandle) configure(ctx context.Context, demand chan struct{}, onDemand, openNow bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.finishOpenLocked(h.unavailableLocked())
	h.nodeCtx, h.demand, h.onDemand = ctx, demand, onDemand
	h.lazy = onDemand
	h.retryAt = time.Time{}
	if onDemand && openNow {
		h.opening = &openAttempt{done: make(chan struct{})}
	}
}

// configureClients sets how this connection opens per-client sessions;
// open is nil for shared MCPs.
func (h *mcpHandle) configureClients(open func(ctx context.Context, c identity.Client) (*clientSession, error)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.perClient, h.openClient = open != nil, open
	if h.clients == nil {
		h.clients, h.clientOpening = map[string]*clientSession{}, map[string]*openAttempt{}
	}
}

// clientSession returns the calling client's session on a per-client MCP,
// opening it on first use. Concurrent first calls of one client share one
// open.
func (h *mcpHandle) clientSession(ctx context.Context) (*clientSession, error) {
	c := identity.From(ctx)
	for {
		h.mu.Lock()
		if h.nodeCtx == nil || h.nodeCtx.Err() != nil || h.openClient == nil {
			h.mu.Unlock()
			return nil, &unavailableError{reason: "offline"}
		}
		if cs := h.clients[c.ID]; cs != nil {
			select {
			case <-cs.done:
				delete(h.clients, c.ID)
			default:
				h.mu.Unlock()
				return cs, nil
			}
		}
		if a := h.clientOpening[c.ID]; a != nil {
			h.mu.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-a.done:
			}
			if a.err != nil {
				return nil, a.err
			}
			continue
		}
		a := &openAttempt{done: make(chan struct{})}
		h.clientOpening[c.ID] = a
		open := h.openClient
		h.mu.Unlock()

		var cs *clientSession
		err := h.revokedError(c.ID)
		if err == nil {
			cs, err = open(ctx, c)
		}
		h.mu.Lock()
		delete(h.clientOpening, c.ID)
		if err == nil {
			h.clients[c.ID] = cs
		}
		a.err = err
		close(a.done)
		h.mu.Unlock()
		if err != nil {
			return nil, err
		}
		// The key may have been revoked while the process started. Checked
		// after publishing: a revocation that came later finds the session
		// in h.clients (closeClient), one that came earlier is seen here.
		if err := h.revokedError(c.ID); err != nil {
			h.dropClient(c.ID, cs)
			cs.session.Close()
			return nil, err
		}
		return cs, nil
	}
}

// errClientRevoked answers a call whose key was revoked while it waited.
var errClientRevoked = errors.New("mcp-mesh: client key revoked")

func (h *mcpHandle) revokedError(clientID string) error {
	if h.isRevoked != nil && h.isRevoked(clientID) {
		return errClientRevoked
	}
	return nil
}

// dropClient forgets a client's session once its channel has ended.
func (h *mcpHandle) dropClient(id string, cs *clientSession) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.clients[id] == cs {
		delete(h.clients, id)
	}
}

// closeClient ends a client's session (its key was revoked); its process on
// the node stops with the channel.
func (h *mcpHandle) closeClient(id string) bool {
	h.mu.Lock()
	cs := h.clients[id]
	delete(h.clients, id)
	h.mu.Unlock()
	if cs == nil {
		return false
	}
	cs.session.Close()
	return true
}

// clientCount is how many clients have a session open.
func (h *mcpHandle) clientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, cs := range h.clients {
		select {
		case <-cs.done:
		default:
			n++
		}
	}
	return n
}

func (h *mcpHandle) demandMode() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.onDemand
}

func (h *mcpHandle) beginOpen(onDemand bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onDemand, h.state = onDemand, "starting"
	if onDemand && h.opening == nil {
		h.opening = &openAttempt{done: make(chan struct{})}
	}
	// An automatic restart may also have been requested by a waiting call.
	select {
	case <-h.demand:
	default:
	}
}

func (h *mcpHandle) endOpen(state string, err error, retryAt time.Time, onDemand bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.state, h.retryAt, h.onDemand = state, retryAt, onDemand
	h.lastErr = err
	if err != nil {
		err = &unavailableError{reason: err.Error(), cause: err}
		h.finishOpenLocked(err)
		// A call may have requested another open during session teardown.
		select {
		case <-h.demand:
		default:
		}
	}
}

// Startup waiting is separate from the call queue: it must not consume slots.
func (h *mcpHandle) ensureReady(ctx context.Context) error {
	h.mu.Lock()
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		return err
	}
	err := h.unavailableLocked()
	if err == nil || !h.onDemand || h.nodeCtx == nil || h.nodeCtx.Err() != nil {
		h.mu.Unlock()
		return err
	}
	if h.opening == nil {
		if remaining := time.Until(h.retryAt); remaining > 0 {
			retryIn := max(remaining.Round(time.Second), time.Second)
			if h.lastErr != nil {
				err = &unavailableError{reason: fmt.Sprintf("%v (retrying in %s)", h.lastErr, retryIn), cause: h.lastErr}
			} else {
				err = &unavailableError{reason: fmt.Sprintf("retrying in %s", retryIn)}
			}
			h.mu.Unlock()
			return err
		}
		h.opening = &openAttempt{done: make(chan struct{})}
		select {
		case h.demand <- struct{}{}:
		default:
		}
	}
	attempt, nodeCtx := h.opening, h.nodeCtx
	h.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-nodeCtx.Done():
		return &unavailableError{reason: "offline"}
	case <-attempt.done:
		return attempt.err
	}
}

// counts is the MCP's load: calls in flight, waiting for a slot, and
// waiting for the MCP to start or their process to open.
func (h *mcpHandle) counts() (inFlight, queued, starting int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inFlight, len(h.queue), h.starting
}

// busyLocked is the error for a call refused because too many wait.
func (h *mcpHandle) busyLocked() error {
	if h.counters != nil {
		h.counters.busy.Add(1)
	}
	return &unavailableError{reason: fmt.Sprintf("busy: %d calls in flight and %d waiting, try again later", h.inFlight, len(h.queue)+h.starting), cause: ErrBusy}
}

// waitLimit bounds the calls in flight, queued and starting together.
func (h *mcpHandle) waitLimit() int { return (1 + queueFactor) * h.limit }

// waitStart admits a call to wait for the MCP to start or its process to
// open, within the same bound as the slot queue. done ends the wait.
func (h *mcpHandle) waitStart() (done func(), err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight+len(h.queue)+h.starting >= h.waitLimit() {
		return nil, h.busyLocked()
	}
	h.starting++
	var once sync.Once
	return func() {
		once.Do(func() {
			h.mu.Lock()
			h.starting--
			h.mu.Unlock()
		})
	}, nil
}

func (h *mcpHandle) acquire(ctx context.Context) error {
	h.mu.Lock()
	if err := ctx.Err(); err != nil {
		h.mu.Unlock()
		return err
	}
	// A per-client MCP's shared channel is only for listing; its state says
	// nothing about the clients' sessions.
	if err := h.unavailableLocked(); err != nil && !h.perClient {
		h.mu.Unlock()
		return err
	}
	if h.inFlight < h.limit && len(h.queue) == 0 {
		h.inFlight++
		h.mu.Unlock()
		return nil
	}
	if len(h.queue) >= queueFactor*h.limit || h.inFlight+len(h.queue)+h.starting >= h.waitLimit() {
		err := h.busyLocked()
		h.mu.Unlock()
		return err
	}
	w := &callWaiter{ready: make(chan struct{})}
	h.queue = append(h.queue, w)
	h.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		if w.granted {
			h.releaseLocked()
		} else if i := slices.Index(h.queue, w); i >= 0 {
			h.queue = slices.Delete(h.queue, i, i+1)
		}
		return err
	}
	return w.err
}

func (h *mcpHandle) releaseLocked() {
	h.inFlight--
	if len(h.queue) > 0 {
		w := h.queue[0]
		h.queue = slices.Delete(h.queue, 0, 1)
		h.inFlight++
		w.granted = true
		close(w.ready)
	}
}

// errSessionDiffers answers a call whose client's process still lists
// another definition of the tool than the catalog after one reopen: another
// client's process (listing the catalog's) republished in between.
var errSessionDiffers = &unavailableError{
	reason: "this client's process lists another definition of the tool than the catalog; try again",
	cause:  catalog.ErrSessionDiffers,
}

// withSession runs fn on the MCP's session holding one in-flight slot, after
// waiting for the MCP to be ready. Every kind of request shares the limit.
// tool is the upstream name of the tool a call is for ("" for prompts and
// reads).
func (h *mcpHandle) withSession(ctx context.Context, tool string, fn func(*mcp.ClientSession) error) error {
	h.mu.Lock()
	perClient := h.perClient
	h.mu.Unlock()
	if perClient {
		// A process whose listing differs from the catalog's is closed and
		// reopened once: opening lists again and republishes, so the check
		// then judges what that process offers.
		for reopened := false; ; reopened = true {
			done, err := h.waitStart()
			if err != nil {
				return err
			}
			cs, err := h.clientSession(ctx)
			done()
			if err != nil {
				return err
			}
			var listed catalog.Listed
			if tool != "" {
				listed = cs.listed
			}
			err = h.withSlot(ctx, func() error {
				// Last gate, for the process this call goes to.
				if err := catalog.RunCheck(ctx, listed); err != nil {
					return err
				}
				return fn(cs.session)
			})
			if !errors.Is(err, catalog.ErrSessionDiffers) {
				return err
			}
			h.dropClient(identity.From(ctx).ID, cs)
			cs.session.Close()
			if reopened {
				return errSessionDiffers
			}
		}
	}
	done, err := h.waitStart()
	if err != nil {
		return err
	}
	err = h.ensureReady(ctx)
	done()
	if err != nil {
		return err
	}
	return h.withSlot(ctx, func() error {
		h.mu.Lock()
		session, err := h.session, h.unavailableLocked()
		h.mu.Unlock()
		if err != nil {
			return err
		}
		// Last gate: waiting for a start or a slot can outlast the gateway's
		// own checks. The catalog is this session's listing.
		if err := catalog.RunCheck(ctx, nil); err != nil {
			return err
		}
		return fn(session)
	})
}

// withSlot runs fn holding one in-flight slot.
func (h *mcpHandle) withSlot(ctx context.Context, fn func() error) error {
	if err := h.acquire(ctx); err != nil {
		return err
	}
	defer func() {
		h.mu.Lock()
		h.releaseLocked()
		h.mu.Unlock()
	}()
	return fn()
}

func (h *mcpHandle) CallTool(ctx context.Context, params *mcp.CallToolParams) (result *mcp.CallToolResult, err error) {
	err = h.withSession(ctx, params.Name, func(s *mcp.ClientSession) (err error) {
		result, err = s.CallTool(ctx, params)
		return err
	})
	return result, err
}

func (h *mcpHandle) GetPrompt(ctx context.Context, params *mcp.GetPromptParams) (result *mcp.GetPromptResult, err error) {
	err = h.withSession(ctx, "", func(s *mcp.ClientSession) (err error) {
		result, err = s.GetPrompt(ctx, params)
		return err
	})
	return result, err
}

func (h *mcpHandle) ReadResource(ctx context.Context, params *mcp.ReadResourceParams) (result *mcp.ReadResourceResult, err error) {
	err = h.withSession(ctx, "", func(s *mcp.ClientSession) (err error) {
		result, err = s.ReadResource(ctx, params)
		return err
	})
	return result, err
}

// setContent remembers what was last listed, for a reconnecting node whose
// lazy MCP is not opened yet.
func (h *mcpHandle) setContent(c catalog.Content) {
	h.mu.Lock()
	h.content, h.listed = c, true
	h.mu.Unlock()
}

// listedContent is the last listing, if the MCP has ever been listed.
func (h *mcpHandle) listedContent() (catalog.Content, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.content, h.listed
}

func (h *mcpHandle) lastContent() catalog.Content {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.content
}
