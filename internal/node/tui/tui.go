// Package tui provides the node's home dashboard.
package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/discovery"
	"github.com/AldrinSalazar/mcp-mesh/internal/proto"

	tea "charm.land/bubbletea/v2"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/config"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/control"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/nodectl"
	"github.com/AldrinSalazar/mcp-mesh/internal/node/service"
)

// Node is what the TUI drives; *nodectl.Node implements it.
type Node interface {
	Inspect(context.Context) nodectl.Snapshot
	Discover(context.Context, time.Duration) ([]discovery.Hub, error)
	DetectTransport(context.Context, string, map[string]string) (proto.Transport, error)
	InitConfig(hubURL string, force bool) error
	Start(context.Context) error
	Stop(context.Context) error
	Install(context.Context) (service.State, error)
	Uninstall(context.Context) error
	LogsHint() string
	AddMCP(context.Context, config.Entry) (bool, error)
	RemoveMCP(context.Context, string) (bool, error)
	SetDisabled(context.Context, string, bool) (bool, error)
	SetKey(context.Context, string) (nodectl.KeyResult, error)
	Pair(ctx context.Context, hubURL, name string) (nodectl.PairResult, error)
	Reload(context.Context) error
	Restart(context.Context, string) error
	Logs(context.Context, string, int) ([]byte, error)
	Tools(context.Context, string) ([]control.Tool, error)
	Events(context.Context) (<-chan control.Event, error)
	EditorCommand() (*exec.Cmd, error)
}

var _ Node = (*nodectl.Node)(nil)

// Run shows the dashboard until the user quits or ctx ends. The daemon is
// independent of the terminal and keeps running after the dashboard closes.
func Run(ctx context.Context, node Node) error {
	if node == nil {
		return errors.New("run node TUI: nil node")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, node)
	m.mouse = os.Getenv("MCPMESH_NO_MOUSE") != "1"
	defer m.closeConnection()
	defer m.cancelDiscovery()
	_, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil {
		return fmt.Errorf("run node TUI: %w", err)
	}
	return nil
}
