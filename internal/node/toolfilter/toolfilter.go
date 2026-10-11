// Package toolfilter restricts the tools exposed through a node connection and
// observes complete tool listings without interpreting other MCP traffic.
package toolfilter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Budget for one collection (one listing pass). The hub applies the same
// values to its own accumulation.
const (
	MaxListPages = 1000
	MaxListItems = 10000
	MaxListBytes = 16 << 20
)

type Options struct {
	// Logger receives a warning when a listing exceeds its budget. Nil means
	// slog.Default().
	Logger *slog.Logger
	// MaxPages, MaxItems and MaxBytes override the MaxList* defaults when
	// positive (tests use small values).
	MaxPages, MaxItems, MaxBytes int
	// Allow lists the tool names the hub may see and call. Nil allows all
	// tools; a non-nil empty slice allows none.
	Allow []string
	// OnToolList receives the filtered tools from all pages of a full listing.
	OnToolList func(tools json.RawMessage)
}

// Wrap applies opts to conn. The returned connection owns conn until Close.
func Wrap(conn mcp.Connection, opts Options) mcp.Connection {
	if opts.Allow == nil && opts.OnToolList == nil {
		return conn
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &connection{
		conn: conn, onToolList: opts.OnToolList, cancel: cancel,
		wake: make(chan struct{}, 1), consumed: make(chan struct{}, 1),
		done: ctx.Done(), readerDone: make(chan struct{}),
		pending:  make(map[jsonrpc.ID]listRequest),
		log:      opts.Logger,
		maxPages: pick(opts.MaxPages, MaxListPages), maxItems: pick(opts.MaxItems, MaxListItems),
		maxBytes: pick(opts.MaxBytes, MaxListBytes),
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if opts.Allow != nil {
		c.allow = make(map[string]bool, len(opts.Allow))
		for _, name := range opts.Allow {
			c.allow[name] = true
		}
	}
	go c.readLoop(ctx)
	return c
}

func pick(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

type event struct {
	msg      jsonrpc.Message
	err      error
	upstream bool
}

type listRequest struct {
	generation uint64
	cursor     string
}

type connection struct {
	conn       mcp.Connection
	allow      map[string]bool
	onToolList func(json.RawMessage)
	cancel     context.CancelFunc
	done       <-chan struct{}
	readerDone chan struct{}
	closeOnce  sync.Once
	closeErr   error
	wake       chan struct{}
	consumed   chan struct{}
	readMu     sync.Mutex
	mu         sync.Mutex
	queue      []event
	readErr    error
	pending    map[jsonrpc.ID]listRequest
	generation uint64
	collecting bool
	cursor     string
	tools      []json.RawMessage
	log        *slog.Logger
	maxPages   int
	maxItems   int
	maxBytes   int
	pages      int
	bytes      int
	seen       map[string]bool
}

func (c *connection) allowed(name string) bool { return c.allow == nil || c.allow[name] }

func (c *connection) enqueue(e event) {
	c.mu.Lock()
	c.queue = append(c.queue, e)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *connection) readLoop(ctx context.Context) {
	defer close(c.readerDone)
	for {
		msg, err := c.conn.Read(ctx)
		c.enqueue(event{msg: msg, err: err, upstream: true})
		if err != nil {
			return
		}
		// Keep at most one upstream message buffered while the hub is idle.
		select {
		case <-c.consumed:
		case <-ctx.Done():
			return
		}
	}
}

func (c *connection) Read(ctx context.Context) (jsonrpc.Message, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c.mu.Lock()
		if len(c.queue) > 0 {
			e := c.queue[0]
			c.queue[0] = event{}
			c.queue = c.queue[1:]
			if e.err != nil {
				c.readErr = e.err
			}
			c.mu.Unlock()
			if e.upstream && e.err == nil {
				c.consumed <- struct{}{}
			}
			if e.err != nil {
				return e.msg, e.err
			}
			return c.filter(e.msg), nil
		}
		err := c.readErr
		c.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case <-c.wake:
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.done:
			return nil, io.EOF
		}
	}
}

func (c *connection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return mcp.ErrConnectionClosed
	default:
	}
	req, ok := msg.(*jsonrpc.Request)
	if !ok || !req.IsCall() {
		return c.conn.Write(ctx, msg)
	}
	if req.Method == "tools/call" && c.allow != nil {
		var params struct {
			Name *string `json:"name"`
		}
		// Fail closed: a call whose tool name can't be read is rejected too.
		if json.Unmarshal(req.Params, &params) != nil || params.Name == nil || !c.allowed(*params.Name) {
			name := "<invalid>"
			if params.Name != nil {
				name = *params.Name
			}
			c.enqueue(event{msg: &jsonrpc.Response{
				ID: req.ID, Error: &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "Unknown tool: " + name},
			}})
			return nil
		}
	}
	if req.Method != "tools/list" {
		return c.conn.Write(ctx, msg)
	}
	var params struct {
		Cursor string `json:"cursor"`
	}
	if len(req.Params) != 0 && json.Unmarshal(req.Params, &params) != nil {
		return c.conn.Write(ctx, msg)
	}
	// Register before forwarding: a response may arrive before Write returns.
	c.mu.Lock()
	if params.Cursor == "" {
		c.generation++
		c.collecting = c.onToolList != nil
		c.cursor, c.tools = "", nil
		c.pages, c.bytes, c.seen = 0, 0, nil
	}
	tracked := listRequest{c.generation, params.Cursor}
	c.pending[req.ID] = tracked
	c.mu.Unlock()
	err := c.conn.Write(ctx, msg)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, req.ID)
		if tracked.generation == c.generation {
			c.collecting, c.tools = false, nil
		}
		c.mu.Unlock()
	}
	return err
}

func (c *connection) filter(msg jsonrpc.Message) jsonrpc.Message {
	resp, ok := msg.(*jsonrpc.Response)
	if !ok {
		return msg
	}
	c.mu.Lock()
	tracked, ok := c.pending[resp.ID]
	delete(c.pending, resp.ID)
	c.mu.Unlock()
	if !ok {
		return msg
	}
	tools, cursor, start, end, valid := listPage(resp.Result)
	if resp.Error != nil || !valid {
		c.mu.Lock()
		if tracked.generation == c.generation {
			c.collecting, c.tools = false, nil
		}
		c.mu.Unlock()
		if resp.Error == nil && c.allow != nil {
			// Fail closed: a result we can't filter could expose hidden tools
			// (e.g. a duplicate "tools" key that the hub's parser reads differently).
			return &jsonrpc.Response{ID: resp.ID, Error: &jsonrpc.Error{
				Code: jsonrpc.CodeInternalError, Message: "mcp-mesh: tools/list result could not be filtered",
			}}
		}
		return msg
	}
	if c.allow != nil {
		filtered := make([]json.RawMessage, 0, len(tools))
		for _, tool := range tools {
			var header struct {
				Name string `json:"name"`
			}
			if json.Unmarshal(tool, &header) == nil && c.allowed(header.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
		replacement := joinTools(tools)
		copyResp := *resp
		copyResp.Result = make(json.RawMessage, 0, len(resp.Result)+len(replacement))
		copyResp.Result = append(copyResp.Result, resp.Result[:start]...)
		copyResp.Result = append(copyResp.Result, replacement...)
		copyResp.Result = append(copyResp.Result, resp.Result[end:]...)
		msg = &copyResp
	}
	var complete json.RawMessage
	reason := ""
	c.mu.Lock()
	if c.collecting && tracked.generation == c.generation && tracked.cursor == c.cursor {
		c.pages++
		for _, tool := range tools {
			c.bytes += len(tool)
		}
		switch {
		case c.pages > c.maxPages:
			reason = "too many pages"
		case len(c.tools)+len(tools) > c.maxItems:
			reason = "too many tools"
		case c.bytes > c.maxBytes:
			reason = "tool list too large"
		case cursor != "" && c.seen[cursor]:
			reason = "repeated cursor"
		}
		if reason != "" {
			c.collecting, c.tools, c.seen = false, nil, nil
		} else {
			c.tools = append(c.tools, tools...)
			c.cursor = cursor
			if cursor == "" {
				complete = joinTools(c.tools)
				c.collecting, c.tools, c.seen = false, nil, nil
			} else {
				if c.seen == nil {
					c.seen = map[string]bool{}
				}
				c.seen[cursor] = true
			}
		}
	}
	c.mu.Unlock()
	if reason != "" {
		c.log.Warn("tools/list collection abandoned: "+reason, "max_pages", c.maxPages, "max_items", c.maxItems, "max_bytes", c.maxBytes)
	}
	if complete != nil {
		// User callbacks may write or close the connection; never hold mu here.
		c.onToolList(complete)
	}
	return msg
}

// Locate just the tools value so unknown fields, spelling, and whitespace in
// the surrounding result survive filtering. Ambiguous or malformed pages are
// reported as invalid; with an allowlist the caller rejects them.
func listPage(raw json.RawMessage) (tools []json.RawMessage, cursor string, start, end int, valid bool) {
	if !json.Valid(raw) {
		return
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, _ := d.Token()
	if token != json.Delim('{') {
		return
	}
	seenTools := false
	for d.More() {
		key, _ := d.Token()
		name := key.(string)
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return
		}
		switch name {
		case "tools":
			if seenTools || len(value) == 0 || value[0] != '[' || json.Unmarshal(value, &tools) != nil {
				return
			}
			seenTools = true
			end = int(d.InputOffset())
			start = end - len(value)
		case "nextCursor":
			if json.Unmarshal(value, &cursor) != nil {
				return
			}
		}
	}
	valid = seenTools
	return
}

func joinTools(tools []json.RawMessage) json.RawMessage {
	joined := json.RawMessage{'['}
	for i, tool := range tools {
		if i > 0 {
			joined = append(joined, ',')
		}
		joined = append(joined, tool...)
	}
	return append(joined, ']')
}

func (c *connection) Close() error {
	c.closeOnce.Do(func() {
		c.cancel()
		c.closeErr = c.conn.Close()
		<-c.readerDone
	})
	return c.closeErr
}

func (c *connection) SessionID() string { return c.conn.SessionID() }
