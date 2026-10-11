package supervisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// PoolOptions configure a per-client MCP. The embedded Options are the
// template for each process's supervisor; Start is always lazy.
type PoolOptions struct {
	Options
	// MaxClients caps the processes alive at once; zero means 8.
	MaxClients int
}

// Pool runs one Supervisor per open channel of a per-client MCP. A process is
// never shared between channels: closing the channel discards its supervisor.
type Pool struct {
	opts      PoolOptions
	transport proto.Transport
	logs      *logBuffer
	logger    *slog.Logger
	ready     chan struct{}

	mu      sync.Mutex
	ctx     context.Context
	closed  bool // Run is waiting for the processes; add admits no more
	members map[*member]struct{}
	nextID  uint64
	tools   int
	wg      sync.WaitGroup

	// A process that fails to start would otherwise be tried again by every
	// call, each with a fresh supervisor (whose own backoff starts over), so
	// the pool keeps one backoff for the MCP.
	delay      time.Duration
	retryAt    time.Time
	lastErr    error
	backoffMin time.Duration // tests shorten these
	backoffMax time.Duration
	now        func() time.Time
}

type member struct {
	id     uint64
	client string
	sup    *Supervisor
	cancel context.CancelFunc
	stderr *prefixWriter
}

// NewPool builds a pool; Run must be called once.
func NewPool(opts PoolOptions) *Pool {
	if opts.MaxClients <= 0 {
		opts.MaxClients = 8
	}
	if opts.LogSize <= 0 {
		opts.LogSize = 1 << 20
	}
	opts.Start = proto.StartLazy
	opts.AllowTools = cloneTools(opts.AllowTools)
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	opts.Logger = logger
	// The template upstream is only asked for its transport; each process
	// builds its own with its own stderr.
	backoffMax := opts.MaxBackoff
	if backoffMax <= 0 {
		backoffMax = time.Minute
	}
	return &Pool{opts: opts, transport: opts.New(io.Discard).Transport(), logger: logger,
		logs: newLogBuffer(opts.LogSize, opts.OnLog), ready: make(chan struct{}), members: make(map[*member]struct{}),
		backoffMin: time.Second, backoffMax: backoffMax, now: time.Now}
}

func (p *Pool) Transport() proto.Transport { return p.transport }
func (p *Pool) Logs(maxBytes int) []byte   { return p.logs.tail(maxBytes) }

func (p *Pool) SetToolCount(n int) {
	p.mu.Lock()
	changed := p.tools != n
	p.tools = n
	p.mu.Unlock()
	if changed {
		p.onChange()
	}
}

func (p *Pool) onChange() {
	if p.opts.OnChange != nil {
		p.opts.OnChange()
	}
}

// Run returns once ctx ends and every process has stopped.
func (p *Pool) Run(ctx context.Context) {
	p.mu.Lock()
	if p.ctx != nil {
		p.mu.Unlock()
		return
	}
	p.ctx = ctx
	p.mu.Unlock()
	close(p.ready)
	<-ctx.Done()
	// Closed under the lock add holds for wg.Go, so no Go can race the Wait.
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.wg.Wait()
}

// OpenClient starts a process for client and returns its connection. Closing
// the connection stops the process and frees its slot.
func (p *Pool) OpenClient(ctx context.Context, client string) (mcp.Connection, error) {
	select {
	case <-p.ready:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m, err := p.add(client)
	if err != nil {
		return nil, err
	}
	conn, err := m.sup.Open(ctx)
	if err != nil {
		p.release(m)
		if ctx.Err() == nil {
			p.failed(err)
		}
		return nil, err
	}
	p.mu.Lock()
	p.delay, p.retryAt, p.lastErr = 0, time.Time{}, nil
	p.mu.Unlock()
	return &clientConn{Connection: conn, release: func() { p.release(m) }}, nil
}

// failed starts or extends the pool's backoff after a process failed to
// start.
func (p *Pool) failed(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.delay == 0 {
		p.delay = min(p.backoffMin, p.backoffMax)
	} else {
		p.delay = min(2*p.delay, p.backoffMax)
	}
	p.retryAt, p.lastErr = p.now().Add(p.delay), err
}

// Open serves the Upstream interface for callers that carry no client.
func (p *Pool) Open(ctx context.Context) (mcp.Connection, error) { return p.OpenClient(ctx, "") }

func (p *Pool) add(client string) (*member, error) {
	p.mu.Lock()
	if p.closed || p.ctx.Err() != nil {
		p.mu.Unlock()
		return nil, fmt.Errorf("open %s: supervisor stopped", p.opts.Name)
	}
	if wait := p.retryAt.Sub(p.now()); wait > 0 {
		err := fmt.Errorf("%s failed to start: %w (retrying in %s)", p.opts.Name, p.lastErr, max(wait.Round(time.Second), time.Second))
		p.mu.Unlock()
		return nil, err
	}
	if len(p.members) >= p.opts.MaxClients {
		p.mu.Unlock()
		return nil, fmt.Errorf("too many clients for %s (max_clients %d)", p.opts.Name, p.opts.MaxClients)
	}
	label := client
	if label == "" {
		label = "listing"
	}
	m := &member{id: p.nextID, client: client, stderr: &prefixWriter{prefix: "[" + label + "] ", dst: p.logs}}
	p.nextID++
	o := p.opts.Options
	o.OnLog = nil
	newUpstream := o.New
	o.New = func(io.Writer) upstream.Upstream { return newUpstream(m.stderr) }
	m.sup = New(o)
	var ctx context.Context
	ctx, m.cancel = context.WithCancel(p.ctx)
	p.members[m] = struct{}{}
	p.wg.Go(func() { m.sup.Run(ctx) })
	p.mu.Unlock()
	p.onChange()
	return m, nil
}

func (p *Pool) release(m *member) {
	p.mu.Lock()
	_, present := p.members[m]
	delete(p.members, m)
	p.mu.Unlock()
	if !present {
		return
	}
	m.cancel()
	m.stderr.flush()
	p.onChange()
}

// Restart stops every process; their channels end with upstream.ErrRestarted.
func (p *Pool) Restart() error {
	p.mu.Lock()
	members := make([]*member, 0, len(p.members))
	for m := range p.members {
		members = append(members, m)
	}
	p.mu.Unlock()
	var errs []error
	for _, m := range members {
		errs = append(errs, m.sup.Restart())
	}
	return errors.Join(errs...)
}

// Status summarizes the processes; Clients has one entry per process.
func (p *Pool) Status() control.MCPStatus {
	p.mu.Lock()
	members := make([]*member, 0, len(p.members))
	for m := range p.members {
		members = append(members, m)
	}
	st := control.MCPStatus{Name: p.opts.Name, Transport: string(p.transport), Start: string(proto.StartLazy), State: control.MCPStopped,
		Tools: p.tools, AllowTools: cloneTools(p.opts.AllowTools), Sharing: string(proto.SharingPerClient), MaxClients: p.opts.MaxClients}
	p.mu.Unlock()
	sort.Slice(members, func(i, j int) bool { return members[i].id < members[j].id })
	for _, m := range members {
		ms := m.sup.Status()
		st.Clients = append(st.Clients, control.ClientStatus{Client: m.client, State: ms.State, PID: ms.PID, StartedAt: ms.StartedAt, LastActivity: ms.LastActivity})
		st.Attached = st.Attached || ms.Attached
		if ms.LastActivity.After(st.LastActivity) {
			st.LastActivity = ms.LastActivity
		}
		if ms.LastError != "" {
			st.LastError = ms.LastError
		}
		switch ms.State {
		case control.MCPRunning:
			st.State = control.MCPRunning
			if st.StartedAt.IsZero() || ms.StartedAt.Before(st.StartedAt) {
				st.StartedAt = ms.StartedAt
			}
		case control.MCPStarting:
			if st.State != control.MCPRunning {
				st.State = control.MCPStarting
			}
		}
	}
	return st
}

// clientConn frees the pool slot when the channel ends.
type clientConn struct {
	mcp.Connection
	once    sync.Once
	release func()
}

func (c *clientConn) Close() error {
	err := c.Connection.Close()
	c.once.Do(c.release)
	return err
}

// prefixWriter tags each stderr line with its client before it joins the
// MCP's shared log.
type prefixWriter struct {
	mu      sync.Mutex
	prefix  string
	partial []byte
	dst     io.Writer
}

const maxPartialLine = 64 << 10

func (w *prefixWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	data := append(w.partial, b...)
	var out []byte
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		out = append(out, w.prefix...)
		out = append(out, data[:i+1]...)
		data = data[i+1:]
	}
	if len(data) > maxPartialLine {
		out = append(append(append(out, w.prefix...), data...), '\n')
		data = nil
	}
	w.partial = append([]byte(nil), data...)
	w.mu.Unlock()
	if len(out) > 0 {
		_, _ = w.dst.Write(out)
	}
	return len(b), nil
}

func (w *prefixWriter) flush() {
	w.mu.Lock()
	rest := w.partial
	w.partial = nil
	w.mu.Unlock()
	if len(rest) > 0 {
		_, _ = w.dst.Write(append(append([]byte(w.prefix), rest...), '\n'))
	}
}
