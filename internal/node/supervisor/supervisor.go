// Package supervisor owns one MCP's lifecycle, including prewarming, crash
// backoff, idle shutdown and stderr history. Each process serves one channel.
package supervisor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	Name string
	// New is called once, with the supervisor's stderr writer.
	New func(stderr io.Writer) upstream.Upstream
	// Empty Start means eager.
	Start proto.Start
	// IdleTimeout applies only to lazy MCPs; zero means ten minutes.
	IdleTimeout time.Duration
	// MaxBackoff caps retry delays; zero means one minute.
	MaxBackoff time.Duration
	// AllowTools is reported in Status; filtering belongs to the relay.
	AllowTools []string
	// LogSize is the stderr history in bytes; zero means one MiB.
	LogSize int
	// Callbacks run without supervisor or stderr buffer locks held.
	OnChange func()
	OnLog    func(chunk []byte)
	Logger   *slog.Logger
}

// Supervisor is an upstream that never reuses an initialized process.
// Run must be called once; Open may wait for Run to begin.
type Supervisor struct {
	opts   Options
	up     upstream.Upstream
	logs   *logBuffer
	logger *slog.Logger

	mu                                sync.Mutex
	status                            control.MCPStatus
	changed                           chan struct{}
	wake                              chan struct{}
	ran, shutdown, opening, eagerNeed bool
	ctx                               context.Context
	p                                 *process
	startCancel                       context.CancelFunc
	startAborted                      bool
	restartStart                      bool
	startErr                          error
	failures                          uint64
	unreachableRetries                int
	delay                             time.Duration
	// Tests shorten policy timings without changing the public options.
	backoffMin, backoffMax, healthyAfter time.Duration
	// Tests lower the backlog limits the same way.
	backlog, backlogBytes int
}

type process struct {
	raw              mcp.Connection
	ctx              context.Context
	cancel           context.CancelFunc
	started, ended   time.Time
	readerDone, done chan struct{}
	changed          chan struct{}
	queue            []jsonrpc.Message
	queuedBytes      int // payloadSize of queue
	dropped          int // notifications dropped while unattached and full
	err, closeErr    error
	forced           bool
	outstanding      map[jsonrpc.ID]bool
	writing          int
}

var _ upstream.Upstream = (*Supervisor)(nil)

// Backlog limits: what a process may have written that nobody has read yet.
// Past either, unread notifications of an unattached process are dropped
// (nobody would see them), and anything else stops the process as a failure.
const (
	maxBacklog      = 4096
	maxBacklogBytes = 64 << 20
)

// ErrBacklog stops a process whose output isn't being read.
var ErrBacklog = errors.New("MCP output not read: backlog limit reached")

// payloadSize is the size of a message's raw JSON payload, which is most of
// it; the rest is bounded by the transport.
func payloadSize(msg jsonrpc.Message) int {
	switch m := msg.(type) {
	case *jsonrpc.Request:
		return len(m.Params) + len(m.Method)
	case *jsonrpc.Response:
		n := len(m.Result)
		if m.Error != nil {
			n += len(m.Error.Error())
		}
		return n
	}
	return 0
}

func New(opts Options) *Supervisor {
	if opts.Start == "" {
		opts.Start = proto.StartEager
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 10 * time.Minute
	}
	if opts.LogSize <= 0 {
		opts.LogSize = 1 << 20
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = time.Minute
	}
	opts.AllowTools = cloneTools(opts.AllowTools)
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	s := &Supervisor{opts: opts, logger: logger, logs: newLogBuffer(opts.LogSize, opts.OnLog),
		changed: make(chan struct{}), wake: make(chan struct{}, 1), eagerNeed: opts.Start == proto.StartEager,
		backoffMin: time.Second, backoffMax: opts.MaxBackoff, healthyAfter: time.Minute,
		backlog: maxBacklog, backlogBytes: maxBacklogBytes}
	s.up = opts.New(s.logs)
	s.status = control.MCPStatus{Name: opts.Name, Transport: string(s.up.Transport()), Start: string(opts.Start),
		State: control.MCPStopped, AllowTools: opts.AllowTools}
	return s
}

func cloneTools(tools []string) []string {
	if tools == nil {
		return nil
	}
	return append([]string{}, tools...)
}

func (s *Supervisor) Transport() proto.Transport { return s.up.Transport() }
func (s *Supervisor) Logs(maxBytes int) []byte   { return s.logs.tail(maxBytes) }
func (s *Supervisor) Status() control.MCPStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	status := s.status
	status.AllowTools = cloneTools(status.AllowTools)
	return status
}
func (s *Supervisor) SetToolCount(n int) {
	s.mu.Lock()
	changed := s.status.Tools != n
	s.status.Tools = n
	s.mu.Unlock()
	if changed {
		s.onChange()
	}
}
func (s *Supervisor) onChange() {
	if s.opts.OnChange != nil {
		s.opts.OnChange()
	}
}

// signalLocked wakes both lifecycle reconciliation and waiting Open calls.
func (s *Supervisor) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
func (p *process) signalLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}
func (s *Supervisor) stoppedLocked() bool {
	return s.shutdown || (s.ctx != nil && s.ctx.Err() != nil)
}

func (s *Supervisor) Open(ctx context.Context) (mcp.Connection, error) {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	if s.stoppedLocked() {
		s.mu.Unlock()
		return nil, fmt.Errorf("open %s: supervisor stopped", s.opts.Name)
	}
	if s.opening || s.status.Attached {
		s.mu.Unlock()
		return nil, fmt.Errorf("open %s: a channel is already attached or opening", s.opts.Name)
	}
	s.opening = true
	failures := s.failures
	s.signalLocked()
	for {
		if err := ctx.Err(); err != nil {
			s.opening = false
			if s.opts.Start == proto.StartLazy {
				if s.startCancel != nil {
					s.startAborted = true
					s.startCancel()
				}
				if s.p != nil && s.p.err == nil {
					s.stopLocked(s.p, io.EOF)
				}
			}
			s.signalLocked()
			s.mu.Unlock()
			return nil, err
		}
		if s.stoppedLocked() {
			s.opening = false
			s.mu.Unlock()
			return nil, fmt.Errorf("open %s: supervisor stopped", s.opts.Name)
		}
		if s.failures != failures {
			s.opening = false
			err := s.startErr
			s.signalLocked()
			s.mu.Unlock()
			return nil, err
		}
		if s.p != nil && s.p.err == nil {
			p := s.p
			s.opening = false
			s.status.Attached = true
			s.status.LastActivity = time.Now()
			s.signalLocked()
			s.mu.Unlock()
			s.onChange()
			return &connection{s: s, p: p}, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		}
		s.mu.Lock()
	}
}

// Restart stops the current process without waiting for its replacement.
func (s *Supervisor) Restart() error {
	s.mu.Lock()
	if s.stoppedLocked() {
		s.mu.Unlock()
		return fmt.Errorf("restart %s: supervisor stopped", s.opts.Name)
	}
	if s.p != nil && s.p.err == nil {
		p := s.p
		s.stopLocked(p, upstream.ErrRestarted)
		s.mu.Unlock()
		// The connection contract permits concurrent, idempotent Close. Run
		// also closes it and joins the reader before starting a replacement.
		if err := p.raw.Close(); err != nil {
			return fmt.Errorf("restart %s: %w", s.opts.Name, err)
		}
		return nil
	} else if s.startCancel != nil {
		s.restartStart = true
		s.startAborted = true
		s.startCancel()
	} else if s.opts.Start == proto.StartEager {
		s.eagerNeed = true
		s.status.NextRetry = time.Time{}
		s.delay = 0
	}
	s.signalLocked()
	s.mu.Unlock()
	return nil
}

func (s *Supervisor) stopLocked(p *process, reason error) {
	if p.err == nil {
		p.err, p.forced = reason, true
		p.ended = time.Now()
	}
	// A deliberate stop takes precedence over queued messages so Close always
	// unblocks Read, even if the consumer has stopped draining stdout.
	p.queue, p.queuedBytes = nil, 0
	p.signalLocked()
	s.signalLocked()
}

// Run owns starting and reaping processes. It joins the old process and its
// reader before starting a replacement, even when Restart initiates the stop.
func (s *Supervisor) Run(ctx context.Context) {
	s.mu.Lock()
	if s.ran {
		s.mu.Unlock()
		return
	}
	s.ran, s.ctx = true, ctx
	s.signalLocked()
	s.mu.Unlock()
	for {
		s.mu.Lock()
		if ctx.Err() != nil {
			s.shutdown = true
			if s.p != nil {
				s.stopLocked(s.p, io.EOF)
			}
			s.signalLocked()
		}
		p := s.p
		if p != nil && p.err != nil {
			s.mu.Unlock()
			// Close must run outside the lock: it can finish stderr writes and
			// unblock the reader, both of which can call back into this package.
			err := p.raw.Close()
			p.cancel()
			<-p.readerDone
			s.mu.Lock()
			p.closeErr = err
			s.p = nil
			s.status.Attached, s.status.PID = false, 0
			s.status.State = control.MCPStopped
			s.status.NextRetry = time.Time{}
			if p.ended.Sub(p.started) >= s.healthyAfter {
				s.delay = 0
			}
			if !s.shutdown {
				s.eagerNeed = s.opts.Start == proto.StartEager
				if !p.forced {
					s.status.Restarts++
					s.failureLocked(p.err)
					s.status.LastError = p.err.Error()
					s.backoffLocked()
					s.status.State = control.MCPBackoff
				} else if errors.Is(p.err, upstream.ErrRestarted) {
					s.status.Restarts++
					s.delay = 0
				}
			}
			close(p.done)
			s.signalLocked()
			s.mu.Unlock()
			if err != nil {
				s.logger.Warn("stop MCP", "mcp", s.opts.Name, "error", err)
			}
			s.onChange()
			continue
		}
		if s.shutdown {
			s.status.State = control.MCPStopped
			s.status.NextRetry = time.Time{}
			s.signalLocked()
			s.mu.Unlock()
			s.onChange()
			return
		}
		var deadline time.Time
		if p != nil {
			if s.opts.Start == proto.StartLazy && s.status.Attached && len(p.outstanding) == 0 && p.writing == 0 {
				deadline = s.status.LastActivity.Add(s.opts.IdleTimeout)
				if !time.Now().Before(deadline) {
					s.stopLocked(p, upstream.ErrIdle)
					s.mu.Unlock()
					continue
				}
			}
		} else if s.eagerNeed || s.opening {
			deadline = s.status.NextRetry
			if deadline.IsZero() || !time.Now().Before(deadline) {
				s.status.State = control.MCPStarting
				s.status.NextRetry = time.Time{}
				procCtx, cancel := context.WithCancel(ctx)
				s.startCancel = cancel
				s.startAborted = false
				s.mu.Unlock()
				s.onChange()
				raw, err := s.up.Open(procCtx)
				s.mu.Lock()
				s.startCancel = nil
				s.eagerNeed = false
				if err != nil {
					cancel()
					if ctx.Err() != nil || s.startAborted {
						s.status.State = control.MCPStopped
						s.eagerNeed = s.restartStart && s.opts.Start == proto.StartEager
					} else {
						// Status rows and the hub already name the MCP.
						s.startErr = err
						s.failures++
						s.failureLocked(err)
						s.eagerNeed = s.opts.Start == proto.StartEager
						s.status.LastError = s.startErr.Error()
						s.status.State = control.MCPFailed
						s.backoffLocked()
					}
				} else {
					p = &process{raw: raw, ctx: procCtx, cancel: cancel, started: time.Now(),
						readerDone: make(chan struct{}), done: make(chan struct{}), changed: make(chan struct{}),
						outstanding: make(map[jsonrpc.ID]bool)}
					s.p = p
					s.status.State = control.MCPRunning
					s.status.StartedAt, s.status.LastActivity = p.started, p.started
					s.status.PID = 0
					if pid, ok := raw.(interface{ PID() int }); ok {
						s.status.PID = pid.PID()
					}
					s.status.LastError = ""
					s.status.Unreachable = false
					if s.up.Transport() != proto.TransportHTTP {
						s.unreachableRetries = 0
					}
					if s.restartStart {
						s.stopLocked(p, upstream.ErrRestarted)
					} else if s.startAborted || ctx.Err() != nil || (s.opts.Start == proto.StartLazy && !s.opening) {
						s.stopLocked(p, io.EOF)
					}
					go s.readLoop(p)
				}
				s.restartStart = false
				s.startAborted = false
				s.signalLocked()
				s.mu.Unlock()
				s.onChange()
				continue
			}
		}
		s.mu.Unlock()
		var timer *time.Timer
		var tick <-chan time.Time
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			tick = timer.C
		}
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-tick:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *Supervisor) failureLocked(err error) {
	s.status.Unreachable = errors.Is(err, upstream.ErrUnreachable)
	level := slog.LevelWarn
	if s.status.Unreachable {
		if s.unreachableRetries > 0 {
			level = slog.LevelDebug
		}
		s.unreachableRetries++
	} else {
		s.unreachableRetries = 0
	}
	s.logger.Log(s.ctx, level, "MCP unavailable; retrying", "mcp", s.opts.Name, "error", err)
}

func (s *Supervisor) backoffLocked() {
	if s.delay == 0 {
		s.delay = min(s.backoffMin, s.backoffMax)
	} else {
		s.delay = min(2*s.delay, s.backoffMax)
	}
	s.status.NextRetry = time.Now().Add(s.delay)
}

func (s *Supervisor) readLoop(p *process) {
	defer close(p.readerDone)
	for {
		msg, err := p.raw.Read(p.ctx)
		s.mu.Lock()
		if p.err != nil {
			s.mu.Unlock()
			return
		}
		if err != nil {
			p.err = err
			p.ended = time.Now()
			p.signalLocked()
			s.signalLocked()
			s.mu.Unlock()
			return
		}
		s.unreachableRetries = 0
		s.status.Unreachable = false
		// Keep reading before attachment as well: a bounded pump channel could
		// hide a prewarmed process's exit behind undrained messages. The
		// backlog limits bound it instead.
		size := payloadSize(msg)
		if len(p.queue) >= s.backlog || p.queuedBytes+size > s.backlogBytes {
			if req, ok := msg.(*jsonrpc.Request); ok && !req.IsCall() && !s.status.Attached {
				if p.dropped++; p.dropped == 1 {
					s.logger.Warn("MCP output not read; dropping notifications until a channel attaches", "mcp", s.opts.Name)
				}
				s.mu.Unlock()
				continue
			}
			p.err = fmt.Errorf("%w (%d messages, %d bytes)", ErrBacklog, len(p.queue), p.queuedBytes)
			p.ended = time.Now()
			p.queue, p.queuedBytes = nil, 0
			p.signalLocked()
			s.signalLocked()
			s.mu.Unlock()
			return
		}
		p.queue = append(p.queue, msg)
		p.queuedBytes += size
		s.status.LastActivity = time.Now()
		p.signalLocked()
		s.signalLocked()
		s.mu.Unlock()
	}
}
