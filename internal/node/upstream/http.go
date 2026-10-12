package upstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type httpUpstream struct{ cfg HTTPConfig }

// NewHTTP creates a relay to an existing Streamable HTTP endpoint.
func NewHTTP(cfg HTTPConfig) Upstream {
	headers := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		headers[k] = v
	}
	cfg.Headers = headers
	return &httpUpstream{cfg: cfg}
}
func (*httpUpstream) Transport() proto.Transport { return proto.TransportHTTP }
func (u *httpUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(u.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("open HTTP endpoint: %w", err)
	}
	if (endpoint.Scheme != "http" && endpoint.Scheme != "https") || endpoint.Host == "" {
		return nil, fmt.Errorf("open HTTP: invalid endpoint %q", u.cfg.URL)
	}
	connCtx, cancel := context.WithCancel(ctx)
	return &httpConn{cfg: u.cfg, client: &http.Client{Transport: NewHTTPTransport()}, ctx: connCtx, cancel: cancel,
		incoming: make(chan jsonrpc.Message, 16), failed: make(chan struct{}), initializing: make(map[jsonrpc.ID]bool), calls: make(map[jsonrpc.ID]context.CancelFunc)}, nil
}

type httpConn struct {
	cfg                 HTTPConfig
	client              *http.Client
	ctx                 context.Context
	cancel              context.CancelFunc
	incoming            chan jsonrpc.Message
	failed              chan struct{}
	mu                  sync.Mutex
	sessionID, version  string
	initializing        map[jsonrpc.ID]bool
	calls               map[jsonrpc.ID]context.CancelFunc // outstanding call POSTs, by request ID
	getStarted, closing bool
	failure             error
	wg                  sync.WaitGroup
	closeOnce           sync.Once
	closeErr            error
}

func (c *httpConn) SessionID() string { c.mu.Lock(); defer c.mu.Unlock(); return c.sessionID }
func (c *httpConn) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure == nil {
		c.failure = err
		close(c.failed)
		c.cancel()
	}
}
func (c *httpConn) begin() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return mcp.ErrConnectionClosed
	}
	if c.failure != nil {
		return c.failure
	}
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.wg.Add(1)
	return nil
}
func (c *httpConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.failed:
		c.mu.Lock()
		defer c.mu.Unlock()
		return nil, c.failure
	case <-c.ctx.Done():
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.failure != nil {
			return nil, c.failure
		}
		return nil, io.EOF
	case msg := <-c.incoming:
		return msg, nil
	}
}

// request sends one HTTP request; msg is the JSON-RPC request a POST carries
// (nil otherwise), for the headers that repeat it.
func (c *httpConn) request(ctx context.Context, method string, body io.Reader, version string, msg *jsonrpc.Request) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL, body)
	if err != nil {
		return nil, fmt.Errorf("create HTTP %s: %w", method, err)
	}
	for k, v := range c.cfg.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if method == http.MethodGet {
		req.Header.Set("Accept", "text/event-stream")
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	c.mu.Lock()
	if version == "" {
		version = c.version
	}
	sessionID := c.sessionID
	c.mu.Unlock()
	if version != "" {
		req.Header.Set("Mcp-Protocol-Version", version)
	}
	if msg != nil && version >= "2026-07-28" {
		setStandardHeaders(req.Header, msg)
	}
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, connectError(c.cfg.URL, fmt.Errorf("HTTP %s: %w", method, err))
	}
	return resp, nil
}
func mediaType(resp *http.Response) string {
	typ, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	return typ
}
func (c *httpConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if err := c.begin(); err != nil {
		return err
	}
	raw, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		c.wg.Done()
		return fmt.Errorf("encode HTTP message: %w", err)
	}
	var version string
	var call *jsonrpc.Request
	var std *jsonrpc.Request // for the standard headers
	if req, ok := msg.(*jsonrpc.Request); ok {
		std = req
		if req.IsCall() {
			call = req
		}
		// SEP-2575 carries the version per call, including server/discover.
		var params struct {
			Meta struct {
				Version string `json:"io.modelcontextprotocol/protocolVersion"`
			} `json:"_meta"`
		}
		_ = json.Unmarshal(req.Params, &params)
		version = params.Meta.Version
		if req.Method == "notifications/cancelled" {
			// A stateless server treats every POST as its own session, so the
			// notification itself can't reach the running call: end its POST.
			c.cancelCall(req.Params)
		}
		if req.Method == "initialize" && req.IsCall() {
			c.mu.Lock()
			c.initializing[req.ID] = true
			c.mu.Unlock()
		}
	}
	if call == nil {
		defer c.wg.Done()
		return c.post(ctx, raw, version, std, nil)
	}
	// A server answering with plain JSON sends no headers until the call is
	// done, so a call's POST can last as long as the tool runs. Send calls in
	// the background so later messages (other calls, cancellations) don't wait
	// behind it. Failures end the connection through c.fail.
	callCtx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	c.calls[call.ID] = cancel
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		// post returns once the response is being relayed; its cleanup, not this
		// goroutine, ends the call's context (see endCall).
		c.post(callCtx, raw, version, std, call)
	}()
	return nil
}

// endCall forgets a finished call's POST so a late cancellation is a no-op and
// the context is released.
func (c *httpConn) endCall(id jsonrpc.ID) {
	c.mu.Lock()
	cancel := c.calls[id]
	delete(c.calls, id)
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// cancelCall ends the POST of the call a notifications/cancelled refers to.
// Unknown or finished calls are ignored.
func (c *httpConn) cancelCall(params json.RawMessage) {
	var p struct {
		RequestID any `json:"requestId"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	id, err := jsonrpc.MakeID(p.RequestID)
	if err != nil || !id.IsValid() {
		return
	}
	c.mu.Lock()
	cancel := c.calls[id]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// post sends one message and starts relaying its response body, if any.
func (c *httpConn) post(ctx context.Context, raw []byte, version string, req, call *jsonrpc.Request) error {
	// Both the caller and connection own a POST's lifetime, including its body.
	postCtx, cancel := context.WithCancel(ctx)
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(c.ctx, func() { cancel(); close(cancelDone) })
	cleanup := func() {
		if !stop() {
			<-cancelDone
		}
		cancel()
		if call != nil {
			c.endCall(call.ID)
		}
	}
	resp, err := c.request(postCtx, http.MethodPost, bytes.NewReader(raw), version, req)
	if err != nil {
		// Checked before cleanup, which ends the call's context itself.
		cancelled := call != nil && ctx.Err() != nil && c.ctx.Err() == nil
		cleanup()
		// A cancelled call ends only its own POST; the connection is still fine.
		if cancelled {
			return err
		}
		c.fail(err)
		return err
	}
	c.mu.Lock()
	sid := resp.Header.Get("Mcp-Session-Id")
	if sid != "" && c.sessionID != "" && sid != c.sessionID {
		c.mu.Unlock()
		resp.Body.Close()
		cleanup()
		err := errors.New("HTTP response changed the session ID")
		c.fail(err)
		return err
	}
	if sid != "" {
		c.sessionID = sid
	}
	c.mu.Unlock()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		resp.Body.Close()
		cleanup()
		return nil
	}
	typ := mediaType(resp)
	// Transport-level errors carrying JSON-RPC must reach the hub, which owns
	// discovery fallback and protocol negotiation.
	if resp.StatusCode != http.StatusOK && !(typ == "application/json" && (resp.StatusCode == 400 || resp.StatusCode == 404)) {
		resp.Body.Close()
		cleanup()
		err := fmt.Errorf("HTTP POST: %s", resp.Status)
		c.fail(err)
		return err
	}
	if typ != "application/json" && typ != "text/event-stream" {
		resp.Body.Close()
		cleanup()
		err := fmt.Errorf("HTTP POST: unsupported content type %q", typ)
		c.fail(err)
		return err
	}
	// The caller's wg slot keeps Close from entering Wait until this Add completes.
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cleanup()
		defer resp.Body.Close()
		var err error
		if typ == "application/json" {
			err = c.readJSON(resp, call)
		} else {
			err = c.readSSE(resp.Body, call)
		}
		if err != nil && postCtx.Err() == nil {
			c.fail(fmt.Errorf("read HTTP POST: %w", err))
		}
	}()
	return nil
}
func (c *httpConn) deliver(raw []byte) (jsonrpc.Message, error) {
	msg, err := jsonrpc.DecodeMessage(raw)
	if err != nil {
		return nil, fmt.Errorf("decode HTTP message: %w", err)
	}
	if resp, ok := msg.(*jsonrpc.Response); ok {
		c.mu.Lock()
		initializing := c.initializing[resp.ID]
		delete(c.initializing, resp.ID)
		if initializing && resp.Error == nil {
			// Observe only transport metadata; forward the result without modifying it.
			var result struct {
				Version string `json:"protocolVersion"`
			}
			if json.Unmarshal(resp.Result, &result) == nil {
				c.version = result.Version
			}
		}
		startGET := initializing && resp.Error == nil && c.version != "" && c.version < "2026-07-28" && !c.getStarted && !c.closing
		if startGET {
			c.getStarted = true
			c.wg.Add(1)
		}
		c.mu.Unlock()
		if startGET {
			go func() { defer c.wg.Done(); c.listen() }()
		}
	}
	select {
	case c.incoming <- msg:
		return msg, nil
	case <-c.ctx.Done():
		return nil, c.ctx.Err()
	}
}
func (c *httpConn) readJSON(resp *http.Response, call *jsonrpc.Request) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, proto.DefaultMaxMessageSize+1))
	if err != nil {
		return err
	}
	if len(raw) > proto.DefaultMaxMessageSize {
		return errors.New("HTTP message exceeds size limit")
	}
	if resp.StatusCode != http.StatusOK && call != nil {
		if _, err := jsonrpc.DecodeMessage(raw); err != nil {
			// Some servers reject a call with a plain JSON body, not JSON-RPC
			// (Fusion answers server/discover with {"error":"Missing
			// MCP-Session-Id header"}). Answer the call with it so the hub can
			// fall back to initialize, as it does for a JSON-RPC error.
			if raw, err = jsonrpc.EncodeMessage(&jsonrpc.Response{ID: call.ID, Error: rejectedCall(resp.Status, raw)}); err != nil {
				return err
			}
		}
	}
	_, err = c.deliver(raw)
	return err
}

// rejectedCall is the JSON-RPC error for a call the server refused with a
// body that isn't JSON-RPC.
func rejectedCall(status string, body []byte) *jsonrpc.Error {
	const limit = 512
	text := strings.ToValidUTF8(strings.TrimSpace(string(body)), "�")
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "") + "…"
	}
	return &jsonrpc.Error{Code: jsonrpc.CodeInvalidRequest, Message: fmt.Sprintf("HTTP POST: %s: %s", status, text)}
}
func (c *httpConn) readSSE(r io.Reader, call *jsonrpc.Request) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), proto.DefaultMaxMessageSize+2)
	var data []byte
	var event string
	dispatch := func() (bool, error) {
		if len(data) == 0 || (event != "" && event != "message") {
			return false, nil
		}
		msg, err := c.deliver(bytes.TrimSuffix(data, []byte{'\n'}))
		if err != nil {
			return false, err
		}
		resp, ok := msg.(*jsonrpc.Response)
		return ok && call != nil && resp.ID == call.ID, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := dispatch()
			if done || err != nil {
				return err
			}
			data = nil
			event = ""
			continue
		}
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch key {
		case "event":
			event = value
		case "data":
			if len(data)+len(value)+1 > proto.DefaultMaxMessageSize {
				return errors.New("SSE event exceeds size limit")
			}
			data = append(data, value...)
			data = append(data, '\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	// A call stream ending without its response cannot be answered by the relay.
	return io.ErrUnexpectedEOF
}
func (c *httpConn) listen() {
	resp, err := c.request(c.ctx, http.MethodGet, nil, "", nil)
	if err != nil {
		if c.ctx.Err() == nil {
			c.fail(err)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return
	}
	if resp.StatusCode != http.StatusOK || mediaType(resp) != "text/event-stream" {
		c.fail(fmt.Errorf("HTTP GET: %s (content type %q)", resp.Status, mediaType(resp)))
		return
	}
	if err := c.readSSE(resp.Body, nil); err != nil && c.ctx.Err() == nil {
		c.fail(fmt.Errorf("read HTTP GET: %w", err))
	}
}
func (c *httpConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closing = true
		c.mu.Unlock()
		c.cancel()
		c.wg.Wait()
		if c.SessionID() != "" {
			ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 5*time.Second)
			defer cancel()
			resp, err := c.request(ctx, http.MethodDelete, nil, "", nil)
			if err != nil {
				c.closeErr = err
			} else {
				resp.Body.Close()
				if resp.StatusCode != 200 && resp.StatusCode != 204 && resp.StatusCode != 405 && resp.StatusCode != 404 {
					c.closeErr = fmt.Errorf("HTTP DELETE: %s", strconv.Itoa(resp.StatusCode))
				}
			}
		}
		c.client.CloseIdleConnections()
	})
	return c.closeErr
}

// setStandardHeaders repeats a request's method and the name it acts on in
// headers, as 2026-07-28 servers require (they refuse a request without
// them). Tools' x-mcp-header parameters (Mcp-Param-*) are not sent: the
// relay doesn't keep tool definitions.
func setStandardHeaders(h http.Header, msg *jsonrpc.Request) {
	h.Set("Mcp-Method", msg.Method)
	var p struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	switch msg.Method {
	case "tools/call", "prompts/get":
		if json.Unmarshal(msg.Params, &p) == nil {
			h.Set("Mcp-Name", p.Name)
		}
	case "resources/read":
		if json.Unmarshal(msg.Params, &p) == nil {
			h.Set("Mcp-Name", p.URI)
		}
	}
}
