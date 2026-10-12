package nodectl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
)

func acquire(ctx context.Context, gate chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Node) logFile() string { return filepath.Join(n.opts.StateDir, "node.log") }
func (n *Node) runArgs() []string {
	return []string{"run", "--config", n.opts.ConfigPath, "--socket", n.opts.SocketPath}
}

func (n *Node) configState() ConfigState {
	var state ConfigState
	state.Node, _ = os.Hostname()
	_, err := os.Stat(n.opts.ConfigPath)
	if errors.Is(err, os.ErrNotExist) {
		return state
	}
	if err != nil {
		state.Err = fmt.Errorf("stat %s: %w", n.opts.ConfigPath, err).Error()
		return state
	}
	state.Exists = true
	cfg, err := config.Load(n.opts.ConfigPath)
	if err != nil {
		state.Err = err.Error()
	} else {
		state.Hub, state.Node = cfg.Hub.URL, cfg.NodeName
	}
	// Read apart from validation so a bad key file still shows as configured.
	state.Key, _ = config.HubKey(n.opts.ConfigPath)
	entries, err := config.Entries(n.opts.ConfigPath)
	state.Entries = entries
	if err != nil && state.Err == "" {
		state.Err = err.Error()
	}
	return state
}

func (n *Node) daemonStatus(ctx context.Context) (control.Status, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return n.client.Status(ctx)
}

func (n *Node) serviceStatus(ctx context.Context) (service.State, error) {
	if n.opts.Service == nil {
		return service.State{}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := acquire(ctx, n.services); err != nil {
		return service.State{}, err
	}
	defer func() { <-n.services }()
	return n.opts.Service.Status(ctx)
}

func (n *Node) serviceCall(ctx context.Context, call func(context.Context) error) error {
	if err := acquire(ctx, n.services); err != nil {
		return err
	}
	defer func() { <-n.services }()
	return call(ctx)
}

func (n *Node) stop(ctx context.Context) error {
	if _, err := n.daemonStatus(ctx); errors.Is(err, control.ErrNotRunning) {
		return nil
	} else if err != nil {
		return err
	}
	state, err := n.serviceStatus(ctx)
	if err != nil {
		return err
	}
	if state.Running {
		err = n.serviceCall(ctx, n.opts.Service.Stop)
	} else {
		err = n.client.Shutdown(ctx)
	}
	if err != nil {
		return fmt.Errorf("stop daemon: %w", err)
	}
	return n.waitDaemon(ctx, false, nil)
}

func (n *Node) waitService(ctx context.Context) error {
	if err := n.waitDaemon(ctx, true, nil); err != nil {
		return fmt.Errorf("wait for service daemon: %w; logs: %s", err, n.LogsHint())
	}
	return nil
}

func (n *Node) waitDaemon(ctx context.Context, running bool, exited <-chan error) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		select {
		case err := <-exited:
			return childExit(err)
		default:
		}
		_, err := n.daemonStatus(ctx)
		if running && err == nil {
			select {
			case err := <-exited:
				return childExit(err)
			default:
				return nil
			}
		}
		if !running && errors.Is(err, control.ErrNotRunning) {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return fmt.Errorf("wait for daemon (last status: %v): %w", lastErr, ctx.Err())
			}
			return fmt.Errorf("wait for daemon: %w", ctx.Err())
		case err := <-exited:
			return childExit(err)
		case <-ticker.C:
		}
	}
}

func childExit(err error) error {
	if err != nil {
		return fmt.Errorf("daemon exited before answering: %w", err)
	}
	return errors.New("daemon exited before answering")
}

// save writes a config change without applying it and reports whether a
// running daemon is left on the previous config.
func (n *Node) save(ctx context.Context, write func() error) (pending bool, err error) {
	if err := acquire(ctx, n.actions); err != nil {
		return false, err
	}
	defer func() { <-n.actions }()
	if err := write(); err != nil {
		return false, err
	}
	if _, err := n.daemonStatus(ctx); err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return false, nil
		}
		return false, fmt.Errorf("config file was saved, but the node's state is unknown: %w", err)
	}
	return true, nil
}

// edit writes a config change and applies it to a running daemon.
func (n *Node) edit(ctx context.Context, save func() error) (bool, error) {
	if err := acquire(ctx, n.actions); err != nil {
		return false, err
	}
	defer func() { <-n.actions }()
	if err := save(); err != nil {
		return false, err
	}
	if _, err := n.daemonStatus(ctx); err != nil {
		if errors.Is(err, control.ErrNotRunning) {
			return false, nil
		}
		return false, fmt.Errorf("config file was saved but the daemon could not reload: %w", err)
	}
	if err := n.client.Reload(ctx); err != nil {
		return false, fmt.Errorf("config file was saved but the daemon could not reload: %w", err)
	}
	return true, nil
}

func shellWord(word string) string {
	if !strings.ContainsAny(word, " \t\n'\"\\$`;&|<>()*?[]{}!~") {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", "'\\''") + "'"
}
