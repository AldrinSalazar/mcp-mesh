package nodes

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// The node read loop never waits on an SDK reader. A stalled channel is closed
// when its bounded inbox fills, so other MCPs can still make progress. The SDK
// reads continuously, so the inbox only needs to absorb bursts (e.g. progress
// notifications).
const inboxSize = 1024

type channel struct {
	node   *node
	id     string
	name   string
	client string // per-client channels: the client it serves (for the node's logs)
	server string // owned by the MCP worker
	ctx    context.Context
	cancel context.CancelFunc
	opened chan string
	inbox  chan json.RawMessage
	mu     sync.Mutex
	err    error
	remote bool
	reason proto.CloseReason
	close  sync.Once
}

var _ mcp.Transport = (*channel)(nil)
var _ mcp.Connection = (*channel)(nil)

func newChannel(n *node, name string) *channel {
	ctx, cancel := context.WithCancel(n.ctx)
	return &channel{
		node: n, id: randomID("c_"), name: name, ctx: ctx, cancel: cancel,
		opened: make(chan string, 1), inbox: make(chan json.RawMessage, inboxSize),
	}
}

// openError is the node's reason for refusing to open a channel (the MCP
// couldn't start, or its app isn't running). It's shown to callers as is.
type openError struct{ reason string }

func (e *openError) Error() string { return e.reason }

func (ch *channel) Connect(ctx context.Context) (mcp.Connection, error) {
	if err := ch.node.conn.Send(ctx, &proto.Envelope{T: proto.TOpen, MCP: ch.name, Ch: ch.id, Client: ch.client}); err != nil {
		return nil, fmt.Errorf("open %s: %w", ch.name, err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch.ctx.Done():
		return nil, ch.failure()
	case reason := <-ch.opened:
		if reason != "" {
			return nil, &openError{reason}
		}
		return ch, nil
	}
}

func (ch *channel) dispatch(e *proto.Envelope) {
	switch e.T {
	case proto.TOpened:
		select {
		case ch.opened <- e.Error:
		default:
		}
	case proto.TMCP:
		if int64(len(e.Msg)) > ch.node.r.opts.MaxMessageSize {
			ch.fail(fmt.Errorf("MCP message exceeds size limit"), false)
			return
		}
		select {
		case ch.inbox <- e.Msg:
		default:
			ch.fail(fmt.Errorf("MCP channel inbox full"), false)
		}
	case proto.TClose:
		err := error(mcp.ErrConnectionClosed)
		if e.Error != "" {
			err = fmt.Errorf("node closed %s: %s", ch.name, e.Error)
		}
		ch.failWithReason(err, true, e.Reason)
	}
}

func (ch *channel) fail(err error, remote bool) {
	ch.failWithReason(err, remote, "")
}

func (ch *channel) failWithReason(err error, remote bool, reason proto.CloseReason) {
	ch.mu.Lock()
	if ch.err == nil {
		ch.err, ch.remote = err, remote
		ch.reason = reason
		ch.cancel()
	}
	ch.mu.Unlock()
}

func (ch *channel) closeReason() proto.CloseReason {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.reason
}

func (ch *channel) failure() error {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	if ch.err != nil {
		return ch.err
	}
	return ch.ctx.Err()
}

func (ch *channel) remoteClosed() bool {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return ch.remote
}

func (ch *channel) Read(ctx context.Context) (jsonrpc.Message, error) {
	if ch.ctx.Err() != nil {
		return nil, ch.failure()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ch.ctx.Done():
		return nil, ch.failure()
	case raw := <-ch.inbox:
		msg, err := jsonrpc.DecodeMessage(raw)
		if err != nil {
			return nil, fmt.Errorf("decode MCP message: %w", err)
		}
		ch.expectProgress(msg)
		return msg, nil
	}
}

// expectProgress tells the progress router about a progress notification
// before the SDK reads the next message, so a call's result (read after it)
// can wait for it; see progress.Settle.
func (ch *channel) expectProgress(msg jsonrpc.Message) {
	req, ok := msg.(*jsonrpc.Request)
	router := ch.node.r.opts.Progress
	if !ok || req.ID.IsValid() || req.Method != "notifications/progress" || router == nil {
		return
	}
	var p struct {
		Token any `json:"progressToken"`
	}
	if json.Unmarshal(req.Params, &p) == nil {
		router.Expect(p.Token)
	}
}

func (ch *channel) Write(ctx context.Context, msg jsonrpc.Message) error {
	if ch.ctx.Err() != nil {
		return ch.failure()
	}
	raw, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("encode MCP message: %w", err)
	}
	if int64(len(raw)) > ch.node.r.opts.MaxMessageSize {
		return fmt.Errorf("MCP message exceeds size limit")
	}
	if err := ch.node.conn.Send(ctx, &proto.Envelope{T: proto.TMCP, Ch: ch.id, Msg: raw}); err != nil {
		return fmt.Errorf("send MCP message: %w", err)
	}
	return nil
}

func (ch *channel) Close() error {
	ch.fail(mcp.ErrConnectionClosed, false)
	var err error
	ch.close.Do(func() {
		if ch.node.ctx.Err() == nil && !ch.remoteClosed() {
			ctx, cancel := context.WithTimeout(ch.node.ctx, 5*time.Second)
			defer cancel()
			err = ch.node.conn.Send(ctx, &proto.Envelope{T: proto.TClose, Ch: ch.id})
		}
	})
	return err
}

func (ch *channel) SessionID() string { return "" }
