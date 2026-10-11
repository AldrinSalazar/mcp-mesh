package upstream

import (
	"context"
	"net/http"
	"sync"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewSSE creates a raw relay to an MCP served over the older HTTP+SSE transport.
func NewSSE(cfg HTTPConfig) Upstream {
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	cfg.Headers = headers
	return &sseUpstream{cfg: cfg}
}

type sseUpstream struct{ cfg HTTPConfig }

func (*sseUpstream) Transport() proto.Transport { return proto.TransportSSE }

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	return t.base.RoundTrip(req)
}
func (t *headerTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (u *sseUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	connCtx, cancel := context.WithCancel(ctx)
	client := &http.Client{Transport: &headerTransport{base: NewHTTPTransport(), headers: u.cfg.Headers}}
	raw, err := (&mcp.SSEClientTransport{Endpoint: u.cfg.URL, HTTPClient: client, MaxEventSize: proto.DefaultMaxMessageSize}).Connect(connCtx)
	if err != nil {
		cancel()
		client.CloseIdleConnections()
		return nil, connectError(u.cfg.URL, err)
	}
	return &sseConn{Connection: raw, client: client, endpoint: u.cfg.URL, ctx: connCtx, cancel: cancel}, nil
}

// The SDK ends Read with EOF when its event scanner exits. This wrapper owns
// POST lifetimes too, so Close also joins writes whose callers still have a
// live context.
type sseConn struct {
	mcp.Connection
	client    *http.Client
	endpoint  string
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closing   bool
	failure   error
	writes    sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

func (c *sseConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	msg, err := c.Connection.Read(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	return msg, err
}
func (c *sseConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	c.mu.Lock()
	if c.closing {
		c.mu.Unlock()
		return mcp.ErrConnectionClosed
	}
	c.writes.Add(1)
	c.mu.Unlock()
	defer c.writes.Done()
	postCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	defer func() { stop(); cancel() }()
	err := connectError(c.endpoint, c.Connection.Write(postCtx, msg))
	if err != nil {
		c.mu.Lock()
		if c.failure == nil {
			c.failure = err
		}
		c.mu.Unlock()
		c.cancel()
		c.Connection.Close()
	}
	return err
}
func (c *sseConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		c.cancel()
		c.closeErr = c.Connection.Close()
		c.writes.Wait()
		c.client.CloseIdleConnections()
	})
	return c.closeErr
}
