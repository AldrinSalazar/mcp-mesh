// Package control is the node daemon's local API. The daemon serves it on a
// Unix socket (a named pipe on Windows, later); `mcp-mesh-node status`,
// `logs`, `restart`, `reload` and the TUI are its clients. The daemon owns the
// MCP processes, so closing a client never affects them.
//
// HTTP over the socket, JSON bodies, all under /v1:
//
//	GET  /v1/status                  Status
//	GET  /v1/mcps/{name}/logs?max=N  stderr tail as text/plain (last N bytes, default 64 KiB)
//	GET  /v1/mcps/{name}/tools       JSON []Tool; 404 for an unknown MCP
//	POST /v1/mcps/{name}/restart     204; 404 for an unknown MCP
//	POST /v1/reload                  204; 400 with the config error if the new config is invalid
//	POST /v1/shutdown                202; the daemon then stops as if it got SIGTERM
//	GET  /v1/events                  text/event-stream of Event (one JSON object per "data:" line)
//
// Errors are JSON {"error": "..."} with a 4xx/5xx status.
package control

import (
	"context"
	"errors"
	"time"
)

// ErrUnknownMCP is returned by Backend methods for an MCP name that is not
// configured; the server maps it to 404.
var ErrUnknownMCP = errors.New("unknown MCP")

// Status is a snapshot of the daemon.
type Status struct {
	Node       string    `json:"node"`
	Version    string    `json:"version"`
	PID        int       `json:"pid"`
	StartedAt  time.Time `json:"startedAt"`
	ConfigPath string    `json:"configPath"`
	// ConfigChanged: node.yaml changed after the daemon loaded it. MCP
	// changes are saved, not applied; `reload` applies them.
	ConfigChanged bool        `json:"configChanged,omitempty"`
	Hub           HubStatus   `json:"hub"`
	MCPs          []MCPStatus `json:"mcps"` // sorted by Name
}

// HubStatus is the daemon's connection to the hub.
type HubStatus struct {
	URL       string    `json:"url"`
	Connected bool      `json:"connected"`
	Since     time.Time `json:"since"` // when Connected last changed
	LastError string    `json:"lastError,omitempty"`
	// KeyRejected: the hub refused the node's key (or wants one); LastError
	// says what to do. UIs show it apart from an unreachable hub.
	KeyRejected bool `json:"keyRejected,omitempty"`
	// KeyReason is the short why for a card ("key revoked", "no key set").
	KeyReason string `json:"keyReason,omitempty"`
}

// MCPState is where an MCP's process is in its lifecycle.
type MCPState string

const (
	MCPStopped  MCPState = "stopped"  // not running (lazy and not yet used, or idle-stopped)
	MCPStarting MCPState = "starting" // process launched, not yet attached or ready
	MCPRunning  MCPState = "running"  // process up (attached to a hub channel or pre-warmed)
	MCPBackoff  MCPState = "backoff"  // crashed; waiting until NextRetry before starting again
	MCPFailed   MCPState = "failed"   // could not start (e.g. command not found); see LastError
)

// MCPStatus is one configured MCP.
type MCPStatus struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"` // "stdio", "http" or "sse"
	Start     string   `json:"start"`     // "eager" or "lazy"
	State     MCPState `json:"state"`
	// Attached is true while a hub channel uses the process.
	Attached     bool      `json:"attached"`
	PID          int       `json:"pid,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitzero"`
	LastActivity time.Time `json:"lastActivity,omitzero"`
	Restarts     int       `json:"restarts"`
	LastError    string    `json:"lastError,omitempty"`
	// Unreachable: the last attempt found nothing listening at the MCP's URL
	// (upstream.ErrUnreachable). UIs show it as waiting for the app, not as
	// a crash.
	Unreachable bool      `json:"unreachable,omitempty"`
	NextRetry   time.Time `json:"nextRetry,omitzero"`
	// Tools is the size of the last tool list seen (after AllowTools).
	Tools      int      `json:"tools"`
	AllowTools []string `json:"allowTools,omitempty"` // nil = all tools allowed
	// Sharing is "shared" or "per-client". A per-client MCP has no single
	// process: State, StartedAt and LastActivity summarize Clients, PID is 0.
	Sharing    string `json:"sharing,omitempty"`
	MaxClients int    `json:"maxClients,omitempty"`
	// Clients lists a per-client MCP's processes, one per open channel.
	Clients []ClientStatus `json:"clients,omitempty"`
}

// ClientStatus is one process of a per-client MCP.
type ClientStatus struct {
	// Client is the hub client the process serves; empty for the hub's own
	// tool-listing channel.
	Client       string    `json:"client,omitempty"`
	State        MCPState  `json:"state"`
	PID          int       `json:"pid,omitempty"`
	StartedAt    time.Time `json:"startedAt,omitzero"`
	LastActivity time.Time `json:"lastActivity,omitzero"`
}

// Tool is one tool an MCP exposes after allow_tools, from the daemon's last
// tool list for it (live or cached).
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// EventKind says what an Event carries.
type EventKind string

const (
	// EventStatus: something in Status changed; clients refetch /v1/status.
	EventStatus EventKind = "status"
	// EventLog: MCP wrote Data to stderr.
	EventLog EventKind = "log"
)

// Event is one item on /v1/events.
type Event struct {
	Time time.Time `json:"time"`
	Kind EventKind `json:"kind"`
	MCP  string    `json:"mcp,omitempty"`
	Data string    `json:"data,omitempty"` // log text for EventLog
}

// Backend is what the daemon implements for the server.
type Backend interface {
	Status() Status
	// Logs returns up to maxBytes of the MCP's most recent stderr.
	Logs(mcp string, maxBytes int) ([]byte, error)
	// Tools returns the last observed or cached list, or an empty non-nil slice.
	// Unknown names return ErrUnknownMCP.
	Tools(mcp string) ([]Tool, error)
	Restart(ctx context.Context, mcp string) error
	// Reload re-reads the config file and applies it.
	Reload(ctx context.Context) error
	// Shutdown must return promptly; the server writes the response before
	// calling it, and the daemon stops asynchronously.
	Shutdown()
	// Subscribe delivers events until ctx ends. Slow subscribers may miss
	// events rather than block the daemon.
	Subscribe(ctx context.Context) <-chan Event
}
