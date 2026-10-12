// Package daemon owns the node's supervisors, hub connection and local API.
package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/agent"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/supervisor"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/toolfilter"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/upstream"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	ConfigPath   string
	SocketPath   string
	EnvFile      string
	ToolCacheDir string
	Version      string
	Logger       *slog.Logger
	// DrainTimeout bounds how long a reload that reconnects to the hub waits
	// for calls in progress to finish; zero means a minute.
	DrainTimeout time.Duration
}

type runtime struct {
	agentCancel, supervisorsCancel context.CancelFunc
	agentDone                      chan struct{}
	supervisors                    sync.WaitGroup
}

type toolList struct {
	raw   json.RawMessage
	tools []control.Tool
}

func decodeToolList(raw json.RawMessage) (toolList, error) {
	list := toolList{tools: make([]control.Tool, 0)}
	if len(raw) == 0 {
		return list, nil
	}
	if err := json.Unmarshal(raw, &list.tools); err != nil {
		return toolList{}, fmt.Errorf("decode MCP tools: %w", err)
	}
	list.raw = append(json.RawMessage(nil), raw...)
	return list, nil
}

// runner is what the daemon needs of an MCP's process owner: a Supervisor for
// shared MCPs, a Pool for per-client ones.
type runner interface {
	Run(ctx context.Context)
	Status() control.MCPStatus
	Logs(maxBytes int) []byte
	Restart() error
	SetToolCount(n int)
	Transport() proto.Transport
}

type backend struct {
	opts    Options
	ctx     context.Context
	logger  *slog.Logger
	level   *slog.LevelVar
	started time.Time
	// Lifecycle operations never hold mu while joining workers: their callbacks
	// publish events and update the hub snapshot under mu.
	lifecycle     sync.Mutex
	closing       bool
	current       *runtime
	mu            sync.Mutex
	cfg           *config.Config
	hub           control.HubStatus
	supervisors   map[string]runner
	tools         map[string]toolList
	subscribers   map[chan control.Event]struct{}
	subscriptions sync.WaitGroup
	failed        chan error
	shutdown      chan struct{}
	// inFlight counts the hub's requests being relayed (agent.Config.InFlight).
	inFlight atomic.Int64
	// loadedHash is the SHA-256 of node.yaml as last loaded; guarded by mu.
	loadedHash [32]byte
}

// Run runs the daemon until ctx ends or the control API requests shutdown.
func Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	env, err := service.ReadEnvFile(opts.EnvFile)
	if err != nil {
		return err
	}
	if err := service.ApplyEnv(env); err != nil {
		return fmt.Errorf("apply login environment: %w", err)
	}
	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return err
	}
	listener, err := control.Listen(opts.SocketPath)
	if err != nil {
		if errors.Is(err, control.ErrDaemonRunning) {
			return fmt.Errorf("the node daemon is already running on %s: %w", opts.SocketPath, err)
		}
		return fmt.Errorf("listen on %s: %w", opts.SocketPath, err)
	}
	ownerCtx, cancelOwner := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelOwner()
	level := new(slog.LevelVar)
	level.Set(cfg.LogLevel)
	logger := opts.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	}
	d := &backend{opts: opts, ctx: ownerCtx, cfg: cfg, level: level, logger: logger, started: time.Now(), subscribers: make(map[chan control.Event]struct{}), failed: make(chan error, 1), shutdown: make(chan struct{}, 1)}
	hup, stopSignals := reloadSignals()
	defer stopSignals()
	d.start(cfg)
	d.noteLoaded()
	serverCtx, cancelServer := context.WithCancel(ownerCtx)
	serverDone := make(chan error, 1)
	go func() { serverDone <- control.Serve(serverCtx, listener, d) }()
	logger.Info("node daemon started", "config", opts.ConfigPath, "socket", opts.SocketPath)
	var runErr error
	var serverStopped bool
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-d.shutdown:
			break loop
		case runErr = <-d.failed:
			break loop
		case runErr = <-serverDone:
			serverStopped = true
			break loop
		case <-hup:
			if err := d.Reload(ctx); err != nil {
				logger.Error("reload config", "error", err)
			}
		}
	}
	d.lifecycle.Lock()
	d.closing = true
	d.stop()
	d.lifecycle.Unlock()
	cancelServer()
	cancelOwner()
	if !serverStopped {
		runErr = errors.Join(runErr, <-serverDone)
	}
	d.subscriptions.Wait()
	return runErr
}

func (d *backend) start(cfg *config.Config) {
	// Separate contexts let the agent close its channels before supervisors stop.
	supCtx, supCancel := context.WithCancel(d.ctx)
	agentCtx, agentCancel := context.WithCancel(d.ctx)
	rt := &runtime{agentCancel: agentCancel, supervisorsCancel: supCancel, agentDone: make(chan struct{})}
	sups := make(map[string]runner, len(cfg.MCPs))
	lists := make(map[string]toolList, len(cfg.MCPs))
	mcps := make([]agent.MCP, 0, len(cfg.MCPs))
	for _, def := range cfg.MCPs {
		var maxBackoff time.Duration
		if def.Transport == proto.TransportHTTP || def.Transport == proto.TransportSSE {
			maxBackoff = 5 * time.Second
		}
		supOpts := supervisor.Options{Name: def.Name, Start: def.Start, MaxBackoff: maxBackoff, IdleTimeout: def.IdleTimeout, AllowTools: def.AllowTools, Logger: d.logger,
			New: func(stderr io.Writer) upstream.Upstream {
				if def.Transport == proto.TransportSSE {
					return upstream.NewSSE(upstream.HTTPConfig{URL: def.URL, Headers: def.Headers})
				}
				if def.Transport == proto.TransportHTTP {
					return upstream.NewHTTP(upstream.HTTPConfig{URL: def.URL, Headers: def.Headers})
				}
				return upstream.NewStdio(upstream.StdioConfig{Command: def.Command, Args: def.Args, Env: def.Env, Dir: def.Dir, Stderr: stderr})
			},
			OnChange: func() { d.publish(control.Event{Kind: control.EventStatus, MCP: def.Name}) },
			OnLog: func(chunk []byte) {
				d.publish(control.Event{Kind: control.EventLog, MCP: def.Name, Data: string(chunk)})
			},
		}
		var sup runner
		var pool *supervisor.Pool
		if def.Sharing == proto.SharingPerClient {
			pool = supervisor.NewPool(supervisor.PoolOptions{Options: supOpts, MaxClients: def.MaxClients})
			sup = pool
		} else {
			sup = supervisor.New(supOpts)
		}
		cache := toolCache{dir: d.opts.ToolCacheDir, definition: def}
		list, err := decodeToolList(cache.read())
		if err != nil {
			d.logger.Warn("load tool cache", "mcp", def.Name, "error", err)
			list, _ = decodeToolList(nil)
		}
		lists[def.Name] = list
		sup.SetToolCount(len(list.tools))
		cached := func() json.RawMessage {
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.tools[def.Name].raw
		}
		// Every process of a per-client MCP feeds the same cache.
		filtered := &filteredUpstream{sup: sup, pool: pool, allow: def.AllowTools, logger: d.logger.With("mcp", def.Name), observe: func(tools json.RawMessage) {
			list, err := decodeToolList(tools)
			if err != nil {
				d.logger.Warn("observe tool list", "mcp", def.Name, "error", err)
				return
			}
			d.mu.Lock()
			d.tools[def.Name] = list
			d.mu.Unlock()
			if err := cache.write(tools); err != nil {
				d.logger.Warn("save tool cache", "mcp", def.Name, "error", err)
			}
			sup.SetToolCount(len(list.tools))
		}}
		sups[def.Name] = sup
		mcps = append(mcps, agent.MCP{Name: def.Name, Upstream: filtered, Sharing: def.Sharing, Start: def.Start, CallTimeout: def.CallTimeout, MaxClients: def.MaxClients, CachedTools: cached})
	}
	d.mu.Lock()
	d.cfg, d.supervisors, d.tools = cfg, sups, lists
	d.hub = control.HubStatus{URL: cfg.Hub.URL, Since: time.Now()}
	d.mu.Unlock()
	d.current = rt
	for _, sup := range sups {
		rt.supervisors.Go(func() { sup.Run(supCtx) })
	}
	go func() {
		defer close(rt.agentDone)
		hostname, _ := os.Hostname()
		pairing := &agent.Pairing{Hostname: hostname, OnCode: d.pairCode, Save: d.savePaired, Refused: cfg.Hub.CheckKeyTransport()}
		err := agent.Run(agentCtx, agent.Config{HubURL: cfg.Hub.URL, Node: cfg.NodeName, Version: d.opts.Version, Header: cfg.Hub.Header, MCPs: mcps, Logger: d.logger, OnConnState: d.connectionState, InFlight: &d.inFlight, Pairing: pairing})
		d.connectionState(false, err)
		if err != nil && agentCtx.Err() == nil {
			select {
			case d.failed <- err:
			default:
			}
		}
	}()
	d.publish(control.Event{Kind: control.EventStatus})
}

func (d *backend) stop() {
	d.current.agentCancel()
	<-d.current.agentDone
	d.current.supervisorsCancel()
	d.current.supervisors.Wait()
}

func (d *backend) connectionState(connected bool, err error) {
	d.mu.Lock()
	changed := d.hub.Connected != connected
	if changed {
		d.hub.Since = time.Now()
	}
	d.hub.Connected = connected
	d.hub.LastError, d.hub.KeyRejected, d.hub.KeyReason = "", false, ""
	if connected {
		d.hub.PairCode = ""
	}
	var rejected *agent.KeyRejectedError
	if errors.As(err, &rejected) {
		d.hub.KeyRejected, d.hub.KeyReason = true, rejected.Short()
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		d.hub.LastError = err.Error()
	}
	d.mu.Unlock()
	if connected {
		d.logger.Info("hub connected")
	} else {
		d.logger.Info("hub disconnected", "error", err)
	}
	d.publish(control.Event{Kind: control.EventStatus})
}

// pairCode shows the code of the pairing request waiting on the hub ("" once
// it ends).
func (d *backend) pairCode(code string) {
	d.mu.Lock()
	d.hub.PairCode = code
	d.mu.Unlock()
	d.publish(control.Event{Kind: control.EventStatus})
}

// savePaired writes the key a pairing brought, and the node name the hub
// bound it to, into node.yaml. The agent already uses both, so the daemon
// records them as what it runs with: status doesn't ask for a reload, unless
// other changes were already waiting.
func (d *backend) savePaired(node, key string) error {
	path := d.opts.ConfigPath
	d.mu.Lock()
	current, loaded := d.cfg, d.loadedHash
	d.mu.Unlock()
	before, err := fileHash(path)
	if err != nil {
		return err
	}
	if node != current.NodeName {
		if err := config.SetNodeName(path, node); err != nil {
			return err
		}
	}
	if _, err := config.StoreKey(path, key); err != nil {
		return err
	}
	next := *current
	next.NodeName = node
	next.Hub.Header = current.Hub.Header.Clone()
	if next.Hub.Header == nil {
		next.Hub.Header = make(http.Header)
	}
	next.Hub.Header.Set("Authorization", "Bearer "+key)
	d.mu.Lock()
	d.cfg = &next
	d.mu.Unlock()
	if before == loaded {
		d.noteLoaded()
	}
	d.publish(control.Event{Kind: control.EventStatus})
	return nil
}

func (d *backend) Status() control.Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	status := control.Status{Node: d.cfg.NodeName, Version: d.opts.Version, PID: os.Getpid(), StartedAt: d.started, ConfigPath: d.cfg.Path, Hub: d.hub, MCPs: make([]control.MCPStatus, 0, len(d.supervisors))}
	if current, err := fileHash(d.opts.ConfigPath); err == nil {
		status.ConfigChanged = current != d.loadedHash
	}
	for _, sup := range d.supervisors {
		status.MCPs = append(status.MCPs, sup.Status())
	}
	sort.Slice(status.MCPs, func(i, j int) bool { return status.MCPs[i].Name < status.MCPs[j].Name })
	return status
}

func (d *backend) supervisor(name string) (runner, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	sup := d.supervisors[name]
	if sup == nil {
		return nil, fmt.Errorf("%w: %s", control.ErrUnknownMCP, name)
	}
	return sup, nil
}
func (d *backend) Logs(name string, n int) ([]byte, error) {
	sup, err := d.supervisor(name)
	if err != nil {
		return nil, err
	}
	return sup.Logs(n), nil
}
func (d *backend) Tools(name string) ([]control.Tool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	list, ok := d.tools[name]
	if !ok {
		return nil, fmt.Errorf("%w: %s", control.ErrUnknownMCP, name)
	}
	// Callers may modify their snapshot without changing future responses.
	return append(make([]control.Tool, 0, len(list.tools)), list.tools...), nil
}
func (d *backend) Shutdown() {
	select {
	case d.shutdown <- struct{}{}:
	default:
	}
}
func (d *backend) Restart(ctx context.Context, name string) error {
	d.lifecycle.Lock()
	defer d.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	sup, err := d.supervisor(name)
	if err != nil {
		return err
	}
	return sup.Restart()
}
func (d *backend) Reload(ctx context.Context) error {
	d.lifecycle.Lock()
	defer d.lifecycle.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.closing {
		return errors.New("daemon is stopping")
	}
	cfg, err := config.Load(d.opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("%w: %w", control.ErrInvalidConfig, err)
	}
	d.mu.Lock()
	old := *d.cfg
	d.mu.Unlock()
	old.LogLevel = cfg.LogLevel
	if reflect.DeepEqual(&old, cfg) {
		d.mu.Lock()
		d.cfg = cfg
		d.mu.Unlock()
	} else {
		// Applying the change reconnects to the hub and restarts the MCPs,
		// which would cut off calls in progress: let them finish first.
		d.drain()
		d.stop()
		d.start(cfg)
	}
	d.noteLoaded()
	d.logger.Info("config reloaded", "config", cfg.Path)
	d.level.Set(cfg.LogLevel)
	d.publish(control.Event{Kind: control.EventStatus})
	return nil
}

// fileHash is the SHA-256 of a file's content.
func fileHash(path string) ([32]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

// noteLoaded records the content of node.yaml the daemon now runs with.
func (d *backend) noteLoaded() {
	h, err := fileHash(d.opts.ConfigPath)
	if err != nil {
		return
	}
	d.mu.Lock()
	d.loadedHash = h
	d.mu.Unlock()
}

// drain waits until no call is in progress, or DrainTimeout passes (then
// the reload goes ahead and cuts off what is left), or the daemon stops.
func (d *backend) drain() {
	timeout := d.opts.DrainTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	n := d.inFlight.Load()
	if n <= 0 {
		return
	}
	d.logger.Info("reload waits for calls in progress to finish", "calls", n, "at_most", timeout)
	start := time.Now()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for d.inFlight.Load() > 0 {
		select {
		case <-d.ctx.Done():
			return
		case <-deadline.C:
			d.logger.Warn("calls still in progress after the reload's wait; cutting them off", "calls", d.inFlight.Load(), "waited", timeout)
			return
		case <-ticker.C:
		}
	}
	d.logger.Info("calls finished; applying the reload", "waited", time.Since(start).Round(time.Millisecond))
}

func (d *backend) publish(event control.Event) {
	event.Time = time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()
	for ch := range d.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}
func (d *backend) Subscribe(ctx context.Context) <-chan control.Event {
	ch := make(chan control.Event, 64)
	d.mu.Lock()
	d.subscribers[ch] = struct{}{}
	d.mu.Unlock()
	d.subscriptions.Go(func() {
		select {
		case <-ctx.Done():
		case <-d.ctx.Done():
		}
		d.mu.Lock()
		delete(d.subscribers, ch)
		close(ch)
		d.mu.Unlock()
	})
	return ch
}

type filteredUpstream struct {
	sup     runner
	pool    *supervisor.Pool // set for per-client MCPs
	allow   []string
	observe func(json.RawMessage)
	logger  *slog.Logger // nil: slog.Default()
}

func (u *filteredUpstream) Transport() proto.Transport { return u.sup.Transport() }
func (u *filteredUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	if u.pool != nil {
		return u.OpenClient(ctx, "")
	}
	conn, err := u.sup.(upstream.Upstream).Open(ctx)
	if err != nil {
		return nil, err
	}
	return u.wrap(conn), nil
}

// OpenClient opens a process of a per-client MCP for one hub client.
func (u *filteredUpstream) OpenClient(ctx context.Context, client string) (mcp.Connection, error) {
	if u.pool == nil {
		return u.Open(ctx)
	}
	conn, err := u.pool.OpenClient(ctx, client)
	if err != nil {
		return nil, err
	}
	return u.wrap(conn), nil
}

func (u *filteredUpstream) wrap(conn mcp.Connection) mcp.Connection {
	return toolfilter.Wrap(conn, toolfilter.Options{Allow: u.allow, OnToolList: u.observe, Logger: u.logger})
}
