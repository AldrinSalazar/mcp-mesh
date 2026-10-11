package nodes

import (
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
)

// NodeInfo is a snapshot of a node, including offline nodes within the grace period.
type NodeInfo struct {
	Name           string
	NodeVersion    string
	OS             string
	RemoteAddr     string
	KeyID          string // key the node connected with; "" when auth is off
	ConnectedAt    time.Time
	Online         bool
	DisconnectedAt time.Time
	MCPs           []MCPInfo // sorted by Name
}

// MCPState is the hub's view of one MCP's channel.
type MCPState string

const (
	MCPOpening MCPState = "opening" // open sent, waiting for opened + initialize
	MCPReady   MCPState = "ready"   // tools listed and in the catalog
	MCPIdle    MCPState = "idle"    // tools listed; channel opens on demand
	MCPFailed  MCPState = "failed"  // open, initialize or tools/list failed; see Error
	MCPClosed  MCPState = "closed"  // node closed the channel (e.g. process crashed)
)

// MCPInfo is a snapshot of one MCP on a node.
type MCPInfo struct {
	Name      string
	Transport proto.Transport
	Sharing   proto.Sharing
	// Clients is how many clients have their own session open on a
	// per-client MCP.
	Clients int
	// CallTimeout is the MCP's own call timeout; zero means the hub default.
	CallTimeout time.Duration
	Start       proto.Start
	State       MCPState
	Error       string
	Tools       int
	// Problems are the definitions the hub dropped from the MCP's last
	// listing, and why (see catalog.Sanitize). They describe that listing, so
	// they stay while the MCP is idle or offline and clear when a later
	// listing is clean.
	Problems []string
	// Tools, Prompts and Resources count what was kept of the listing (resources include
	// templates). They are zero until the MCP has been opened once.
	Prompts   int
	Resources int
	InFlight  int
	Queued    int
	// Starting counts calls waiting for the MCP to start or for their
	// per-client process to open.
	Starting int
	// Server is the upstream server's reported name and version, if known.
	Server string
}
