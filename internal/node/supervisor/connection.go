package supervisor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connection struct {
	s *Supervisor
	p *process
}

var _ mcp.Connection = (*connection)(nil)

func (c *connection) SessionID() string { return c.p.raw.SessionID() }
func (c *connection) Read(ctx context.Context) (jsonrpc.Message, error) {
	s, p := c.s, c.p
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		s.mu.Lock()
		if len(p.queue) != 0 {
			msg := p.queue[0]
			p.queue[0] = nil
			p.queue = p.queue[1:]
			p.queuedBytes -= payloadSize(msg)
			if len(p.queue) == 0 {
				p.queue = nil
			}
			if resp, ok := msg.(*jsonrpc.Response); ok {
				delete(p.outstanding, resp.ID)
			}
			if s.p == p && p.err == nil {
				s.status.LastActivity = time.Now()
				s.signalLocked()
			}
			s.mu.Unlock()
			return msg, nil
		}
		if p.err != nil {
			err := p.err
			s.mu.Unlock()
			return nil, err
		}
		changed := p.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
	}
}

func (c *connection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, p := c.s, c.p
	s.mu.Lock()
	if p.err != nil {
		s.mu.Unlock()
		return mcp.ErrConnectionClosed
	}
	// Register before Write: an in-memory peer can respond before Write
	// returns. Also keep blocked notification writes out of the idle window.
	call, isCall := msg.(*jsonrpc.Request)
	if isCall && !call.IsCall() && call.Method == "notifications/cancelled" {
		// Servers don't answer cancelled requests, so stop waiting for them;
		// otherwise a cancelled call would keep a lazy MCP from going idle.
		if id, ok := cancelledID(call.Params); ok {
			delete(p.outstanding, id)
		}
	}
	// subscriptions/listen stays open for notifications for the whole session.
	// Counting it as unfinished work would prevent every modern lazy MCP
	// with list-change notifications from ever reaching its idle timeout.
	isCall = isCall && call.IsCall() && call.Method != "subscriptions/listen"
	if isCall {
		p.outstanding[call.ID] = true
	}
	p.writing++
	s.status.LastActivity = time.Now()
	s.signalLocked()
	s.mu.Unlock()
	err := p.raw.Write(ctx, msg)
	s.mu.Lock()
	p.writing--
	if err != nil && isCall {
		delete(p.outstanding, call.ID)
	}
	if s.p == p {
		s.status.LastActivity = time.Now()
	}
	s.signalLocked()
	s.mu.Unlock()
	return err
}

func cancelledID(params json.RawMessage) (jsonrpc.ID, bool) {
	var p struct {
		RequestID any `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil || p.RequestID == nil {
		return jsonrpc.ID{}, false
	}
	// MakeID takes the default JSON decoding: float64 or string.
	id, err := jsonrpc.MakeID(p.RequestID)
	return id, err == nil
}

func (c *connection) Close() error {
	s, p := c.s, c.p
	s.mu.Lock()
	s.stopLocked(p, mcp.ErrConnectionClosed)
	s.mu.Unlock()
	<-p.done
	return p.closeErr
}
