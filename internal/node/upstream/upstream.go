// Package upstream reaches one MCP from the node: either a stdio process the
// node starts, or an HTTP or HTTP+SSE endpoint the node connects to. The rest of
// the node only sees an [mcp.Connection] and never knows which one it has.
//
// The node does not speak MCP itself: it relays JSON-RPC messages between the
// hub and the MCP unchanged, including initialize / server/discover.
package upstream

import (
	"context"
	"errors"
	"io"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// Errors a connection's Read may return to say why it ended on purpose.
// The agent turns them into proto close reasons.
var (
	// ErrIdle: the MCP was stopped after its idle timeout.
	ErrIdle = errors.New("mcp stopped after idle timeout")
	// ErrRestarted: the MCP was restarted locally.
	ErrRestarted = errors.New("mcp restarted")
)

// Upstream starts or reaches one MCP.
type Upstream interface {
	// Open starts the MCP (or connects to it) and returns a connection to it.
	// Read returns an error once the MCP is gone (process exit, HTTP failure).
	// Close stops the process or ends the HTTP session; it is safe to call
	// more than once.
	Open(ctx context.Context) (mcp.Connection, error)

	// Transport reports how this MCP is reached.
	Transport() proto.Transport
}

// StdioConfig describes an MCP the node runs as a child process.
type StdioConfig struct {
	Command string
	Args    []string
	// Env is extra KEY=VALUE pairs added to the node's own environment.
	Env []string
	// Dir is the working directory; empty means the node's.
	Dir string
	// MaxMessageSize bounds one newline-delimited JSON-RPC message.
	// Zero means proto.DefaultMaxMessageSize.
	MaxMessageSize int
	// Stderr receives the child's stderr. Nil discards it.
	Stderr io.Writer
}

// ErrUnreachable wraps Open and Read errors when nothing accepts connections
// at an HTTP or SSE MCP's address: usually the app serving it isn't running.
var ErrUnreachable = errors.New("nothing is listening (is the app serving this MCP running?)")

// HTTPConfig describes an MCP someone else runs, reached over Streamable
// HTTP ([NewHTTP]) or the older HTTP+SSE transport ([NewSSE]).
type HTTPConfig struct {
	URL     string
	Headers map[string]string
}
