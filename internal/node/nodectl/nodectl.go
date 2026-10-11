// Package nodectl inspects and manages this machine's node: the config file,
// the daemon process and the user service. The CLI and the TUI are both thin
// layers over it, so every action behaves the same in either.
//
// The daemon runs in one of two ways. If the user service is installed, the
// service manager owns it (Start/Stop go through systemd or launchd).
// Otherwise Start launches `mcp-mesh-node run` as a detached background
// process that outlives the caller, logging to LogFile, and Stop asks it to
// exit through the control API.
package nodectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/paths"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
)

var captureLoginEnv = service.CaptureLoginEnv

type Options struct {
	ConfigPath string
	SocketPath string
	Version    string
	// Executable is the mcp-mesh-node binary that Start and Install run;
	// empty means os.Executable with symlinks resolved.
	Executable string
	// StateDir holds LogFile and the captured login environment; empty means
	// paths.StateDir().
	StateDir string
	// Service manages the user service; nil means service.New(). On platforms
	// without one, ServiceState.Supported is false and Install fails.
	Service service.Manager
}

// Snapshot is everything the CLI and TUI show about the node. Inspect never
// fails as a whole: each part reports its own error.
type Snapshot struct {
	Version    string
	ConfigPath string
	SocketPath string
	LogFile    string // the background daemon's (and launchd's) log
	Config     ConfigState
	Daemon     DaemonState
	Service    ServiceState
}

type ConfigState struct {
	Exists bool
	// Err is the load or validation error, with file and line where known.
	// Empty when the file is valid or missing.
	Err  string
	Hub  string // hub URL as written; "" if unknown
	Node string // node name, defaulting to the hostname
	// Key says where the hub key comes from; the key itself is never read here.
	Key config.KeySource
	// Entries lists every MCP in the file, disabled ones included, sorted by
	// name. It is filled whenever the YAML can be read, even if invalid.
	Entries []config.Entry
}

type DaemonState struct {
	Running bool
	Status  control.Status // valid when Running
	// Err explains a daemon that is present but unusable (e.g. not answering).
	Err string
}

type ServiceState struct {
	Supported bool
	Installed bool
	Running   bool
	Detail    string // e.g. "active (running) since ..."
	UnitPath  string
	Err       string
}

type Node struct {
	opts     Options
	client   *control.Client
	actions  chan struct{}
	services chan struct{}
}

func New(opts Options) (*Node, error) {
	var err error
	if opts.Executable == "" {
		opts.Executable, err = os.Executable()
		if err != nil {
			return nil, fmt.Errorf("find executable: %w", err)
		}
		opts.Executable, err = filepath.EvalSymlinks(opts.Executable)
		if err != nil {
			return nil, fmt.Errorf("resolve executable: %w", err)
		}
	}
	if opts.StateDir == "" {
		opts.StateDir, err = paths.StateDir()
		if err != nil {
			return nil, err
		}
	}
	if opts.ConfigPath == "" {
		opts.ConfigPath, err = paths.ConfigFile()
		if err != nil {
			return nil, err
		}
	}
	if opts.SocketPath == "" {
		opts.SocketPath, err = paths.SocketPath()
		if err != nil {
			return nil, err
		}
	}
	for _, field := range []*string{&opts.ConfigPath, &opts.SocketPath, &opts.StateDir, &opts.Executable} {
		*field, err = filepath.Abs(*field)
		if err != nil {
			return nil, fmt.Errorf("resolve node path: %w", err)
		}
	}
	if opts.Service == nil {
		opts.Service, err = service.New()
		if errors.Is(err, errors.ErrUnsupported) {
			opts.Service = nil
		} else if err != nil {
			return nil, err
		}
	}
	return &Node{opts: opts, client: control.NewClient(opts.SocketPath), actions: make(chan struct{}, 1), services: make(chan struct{}, 1)}, nil
}

// Client is the control API client for live data and event streams.
func (n *Node) Client() *control.Client { return n.client }

func (n *Node) Inspect(ctx context.Context) Snapshot {
	result := Snapshot{Version: n.opts.Version, ConfigPath: n.opts.ConfigPath, SocketPath: n.opts.SocketPath, LogFile: n.logFile()}
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		done := make(chan ConfigState, 1)
		go func() { done <- n.configState() }()
		select {
		case result.Config = <-done:
		case <-ctx.Done():
			result.Config.Node, _ = os.Hostname()
			result.Config.Err = ctx.Err().Error()
		}
	}()
	go func() {
		defer wg.Done()
		status, err := n.daemonStatus(ctx)
		if err == nil {
			result.Daemon = DaemonState{Running: true, Status: status}
		} else if !errors.Is(err, control.ErrNotRunning) {
			result.Daemon.Err = err.Error()
		}
	}()
	go func() {
		defer wg.Done()
		result.Service.Supported = n.opts.Service != nil
		if n.opts.Service == nil {
			return
		}
		state, err := n.serviceStatus(ctx)
		result.Service.Installed, result.Service.Running = state.Installed, state.Running
		result.Service.Detail, result.Service.UnitPath = state.Detail, state.UnitPath
		if err != nil {
			result.Service.Err = err.Error()
		}
	}()
	wg.Wait()
	return result
}

// InitConfig writes a starter config pointing at hubURL ("" keeps the example URL).
func (n *Node) InitConfig(hubURL string, force bool) error {
	n.actions <- struct{}{}
	defer func() { <-n.actions }()
	return config.Create(n.opts.ConfigPath, hubURL, force)
}

// SetNodeName sets this machine's name in the mesh (node.name) and reloads
// a running daemon. The hub binds node keys to names, so it must match the
// name the key was created for.
func (n *Node) SetNodeName(ctx context.Context, name string) (reloaded bool, err error) {
	return n.edit(ctx, func() error { return config.SetNodeName(n.opts.ConfigPath, name) })
}

// AtBoot reports whether the installed service starts when the machine
// boots rather than at login; supported is false where the service manager
// has no such choice (launchd agents and Windows start at login).
func (n *Node) AtBoot(ctx context.Context) (on, supported bool, err error) {
	b, ok := n.opts.Service.(service.BootStarter)
	if !ok {
		return false, false, nil
	}
	on, err = b.StartsAtBoot(ctx)
	return on, true, err
}

// EnableAtBoot makes the service start at boot, without a login.
func (n *Node) EnableAtBoot(ctx context.Context) error {
	b, ok := n.opts.Service.(service.BootStarter)
	if !ok {
		return fmt.Errorf("starting at boot: %w", errors.ErrUnsupported)
	}
	return b.EnableAtBoot(ctx)
}

// Start starts the daemon: through the service if installed, else as a
// detached background process. It returns once the daemon answers on its
// socket, or with an error that includes the tail of its log.
func (n *Node) Start(ctx context.Context) error {
	if err := acquire(ctx, n.actions); err != nil {
		return err
	}
	defer func() { <-n.actions }()
	if _, err := n.daemonStatus(ctx); err == nil {
		return nil
	} else if !errors.Is(err, control.ErrNotRunning) {
		return err
	}
	if _, err := config.Load(n.opts.ConfigPath); err != nil {
		return err
	}
	state, err := n.serviceStatus(ctx)
	if err != nil {
		return err
	}
	if state.Installed {
		if err := n.serviceCall(ctx, n.opts.Service.Start); err != nil {
			return err
		}
		return n.waitService(ctx)
	}
	return n.startBackground(ctx)
}

// Stop stops the daemon and returns once its socket stops answering.
func (n *Node) Stop(ctx context.Context) error {
	if err := acquire(ctx, n.actions); err != nil {
		return err
	}
	defer func() { <-n.actions }()
	return n.stop(ctx)
}

// Install validates the config, captures the login environment and installs
// and starts the user service, stopping a background daemon first.
func (n *Node) Install(ctx context.Context) (service.State, error) {
	if err := acquire(ctx, n.actions); err != nil {
		return service.State{}, err
	}
	defer func() { <-n.actions }()
	if n.opts.Service == nil {
		return service.State{}, errors.ErrUnsupported
	}
	if _, err := config.Load(n.opts.ConfigPath); err != nil {
		return service.State{}, err
	}
	env, err := captureLoginEnv(ctx)
	if err != nil {
		return service.State{}, err
	}
	if err := service.WriteEnvFile(filepath.Join(n.opts.StateDir, "login.env"), env); err != nil {
		return service.State{}, err
	}
	state, err := n.serviceStatus(ctx)
	if err != nil {
		return state, err
	}
	if !state.Running {
		if err := n.stop(ctx); err != nil {
			return state, err
		}
	}
	spec := service.Spec{Executable: n.opts.Executable, Args: n.runArgs(), LogFile: n.logFile()}
	if err := n.serviceCall(ctx, func(ctx context.Context) error { return n.opts.Service.Install(ctx, spec) }); err != nil {
		return state, err
	}
	if err := n.waitService(ctx); err != nil {
		return state, err
	}
	return n.serviceStatus(ctx)
}

func (n *Node) Uninstall(ctx context.Context) error {
	if n.opts.Service == nil {
		return errors.ErrUnsupported
	}
	if err := acquire(ctx, n.actions); err != nil {
		return err
	}
	defer func() { <-n.actions }()
	return n.serviceCall(ctx, n.opts.Service.Uninstall)
}

// LogsHint says how to follow the daemon's log, e.g. "journalctl --user -u mcp-mesh-node -f".
func (n *Node) LogsHint() string {
	if runtime.GOOS == "linux" {
		state, _ := n.serviceStatus(context.Background())
		if state.Installed {
			return "journalctl --user -u mcp-mesh-node -f"
		}
	}
	return "tail -f " + shellWord(n.logFile())
}

// AddMCP, RemoveMCP and SetDisabled edit the config file (keeping comments)
// and reload the daemon if it is running; reloaded reports whether it was.
// AddMCP, RemoveMCP and SetDisabled change node.yaml only. A running node
// keeps its MCPs until the user applies the change (Reload, which restarts
// them), like an MCP client that reads its config at start; pending reports
// that a running node still uses the previous config.
func (n *Node) AddMCP(ctx context.Context, e config.Entry) (pending bool, err error) {
	return n.save(ctx, func() error { return config.AddMCP(n.opts.ConfigPath, e) })
}
func (n *Node) RemoveMCP(ctx context.Context, name string) (pending bool, err error) {
	return n.save(ctx, func() error { return config.RemoveMCP(n.opts.ConfigPath, name) })
}
func (n *Node) SetDisabled(ctx context.Context, name string, disabled bool) (pending bool, err error) {
	return n.save(ctx, func() error { return config.SetDisabled(n.opts.ConfigPath, name, disabled) })
}

// Reload, Restart, Logs, Tools and Events forward to the running daemon and
// return control.ErrNotRunning when there is none.
func (n *Node) Reload(ctx context.Context) error              { return n.client.Reload(ctx) }
func (n *Node) Restart(ctx context.Context, mcp string) error { return n.client.Restart(ctx, mcp) }
func (n *Node) Logs(ctx context.Context, mcp string, maxBytes int) ([]byte, error) {
	return n.client.Logs(ctx, mcp, maxBytes)
}
func (n *Node) Tools(ctx context.Context, mcp string) ([]control.Tool, error) {
	return n.client.Tools(ctx, mcp)
}
func (n *Node) Events(ctx context.Context) (<-chan control.Event, error) { return n.client.Events(ctx) }

// EditorCommand returns a command that opens the config in the user's
// editor ($VISUAL, $EDITOR, then nano, vi), with stdio left for the caller.
func (n *Node) EditorCommand() (*exec.Cmd, error) {
	for _, line := range []string{os.Getenv("VISUAL"), os.Getenv("EDITOR"), "nano", "vi"} {
		if line == "" {
			continue
		}
		words, err := SplitCommand(line)
		if err != nil {
			return nil, fmt.Errorf("parse editor command: %w", err)
		}
		if len(words) == 0 {
			continue
		}
		executable, err := exec.LookPath(words[0])
		if err != nil {
			continue
		}
		return exec.Command(executable, append(words[1:], n.opts.ConfigPath)...), nil
	}
	return nil, errors.New("no editor found; set VISUAL or EDITOR, or install nano or vi")
}

// Discover lists hubs announcing themselves on the local network. The user
// picks one; nothing connects to a hub just because it was found.
func (n *Node) Discover(ctx context.Context, timeout time.Duration) ([]discovery.Hub, error) {
	return discovery.Browse(ctx, timeout, nil)
}
