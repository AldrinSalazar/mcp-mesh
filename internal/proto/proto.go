// Package proto defines the node ↔ hub wire protocol.
//
// A node keeps one WebSocket to the hub and multiplexes channels over it. A
// channel maps to exactly one MCP connection on the node (a stdio process or
// an HTTP client). Every frame is one JSON-encoded [Envelope] in a text
// message. MCP messages travel as raw JSON so neither side re-encodes them.
//
// So that a large envelope does not block the other channels, an envelope
// larger than 64 KiB is sent as consecutive binary messages of the form
// [4-byte big-endian stream id][1 flag byte][payload], flag bit0 = final,
// bit1 = abort, and senders take turns frame by frame. The concatenated
// payloads are the same JSON a text frame would carry.
//
// Flow:
//
//	node → hub  hello    node name and the MCPs it offers
//	hub  → node welcome  accepted; limits
//	hub  → node open     open channel ch on MCP mcp
//	node → hub  opened   channel ready (or Error set)
//	both        mcp      one JSON-RPC message on channel ch
//	both        close    channel ch is gone (Error = reason)
//
// The hub can only open channels on MCPs the node announced. There is no
// message that lets the hub choose a command, arguments or environment.
package proto

import "encoding/json"

// Version is the newest protocol version this build speaks; nodes send it in
// [Hello]. MinVersion is the oldest a hub still accepts. A hub answers with
// the version both sides will use in [Welcome], so nodes and hubs can be
// upgraded independently: add a version by bumping Version, keep handling the
// old behavior while MinVersion still allows it.
const (
	Version    = 1
	MinVersion = 1
)

// Subprotocol is the WebSocket subprotocol both sides must negotiate.
const Subprotocol = "mcpmesh.v1"

// NodePath is the hub's WebSocket endpoint for nodes.
const NodePath = "/node"

// DefaultMaxMessageSize bounds a single frame (32 MiB).
const DefaultMaxMessageSize = 32 << 20

// Type identifies the kind of an [Envelope].
type Type string

const (
	THello   Type = "hello"
	TWelcome Type = "welcome"
	TOpen    Type = "open"
	TOpened  Type = "opened"
	TMCP     Type = "mcp"
	TClose   Type = "close"
)

// Envelope is one frame on the node ↔ hub WebSocket. Which fields are set
// depends on T; unused fields are omitted.
type Envelope struct {
	T Type `json:"t"`

	// MCP is the MCP name on the node (open, opened, mcp, close).
	MCP string `json:"mcp,omitempty"`
	// Ch is the channel id, chosen by the hub in open (opened, mcp, close).
	Ch string `json:"ch,omitempty"`
	// Client names the client a per-client channel is for (open only); for
	// logs and status on the node. Empty for shared channels and listing.
	Client string `json:"client,omitempty"`
	// Msg is one raw JSON-RPC message (mcp).
	Msg json.RawMessage `json:"msg,omitempty"`
	// Error is a failure on opened, or a human-readable reason on close.
	Error string `json:"error,omitempty"`
	// Reason classifies a close so the hub knows whether to reopen (close).
	Reason CloseReason `json:"reason,omitempty"`

	Hello   *Hello   `json:"hello,omitempty"`
	Welcome *Welcome `json:"welcome,omitempty"`
}

// Hello is the first frame a node sends.
type Hello struct {
	Version     int    `json:"version"`
	Node        string `json:"node"`
	NodeVersion string `json:"nodeVersion,omitempty"`
	OS          string `json:"os,omitempty"`
	MCPs        []MCP  `json:"mcps"`
}

// Welcome is the hub's reply to [Hello].
type Welcome struct {
	// Version is the protocol version for this connection:
	// min(hello.Version, hub's Version).
	Version        int    `json:"version"`
	NodeID         string `json:"nodeId"`
	MaxMessageSize int64  `json:"maxMessageSize"`
}

// Transport is how a node reaches an MCP.
type Transport string

const (
	TransportStdio Transport = "stdio"
	TransportHTTP  Transport = "http" // Streamable HTTP
	// TransportSSE is the older HTTP+SSE transport (protocol 2024-11-05),
	// which many app plugins still serve. Hubs only display it.
	TransportSSE Transport = "sse"
)

// Sharing says how many channels the hub may open on an MCP.
type Sharing string

const (
	// SharingShared means one channel (one process) serves every client.
	SharingShared Sharing = "shared"
	// SharingPerClient means each client gets its own channel (its own
	// process or upstream session), opened on its first use and closed after
	// the MCP's idle timeout. The hub may also open a channel with no client
	// to list the MCP's tools.
	SharingPerClient Sharing = "per-client"
)

// Start says when the node runs an MCP's process.
type Start string

const (
	// StartEager keeps the MCP running whenever the node is connected. The
	// hub opens its channel right away. Empty means eager.
	StartEager Start = "eager"
	// StartLazy starts the MCP on its first call and stops it after an idle
	// timeout. The hub lists the cached Tools without opening a channel and
	// opens one on demand.
	StartLazy Start = "lazy"
)

// MCP describes one MCP a node offers.
type MCP struct {
	Name      string    `json:"name"`
	Transport Transport `json:"transport"`
	Sharing   Sharing   `json:"sharing"`
	Start     Start     `json:"start,omitempty"`
	// Tools is the MCP's tool list from its last run (a JSON array of MCP
	// Tool objects, after the node's allowlist), if the node has one cached.
	// It lets the hub list a lazy MCP's tools before starting it.
	Tools json.RawMessage `json:"tools,omitempty"`
	// CallTimeoutMs bounds one call on this MCP (tools/call, prompts/get,
	// resources/read); zero means the hub's default.
	CallTimeoutMs int64 `json:"callTimeoutMs,omitempty"`
	// MaxClients caps the channels open at once on a per-client MCP; zero
	// means the node's default (8).
	MaxClients int `json:"maxClients,omitempty"`
}

// CloseReason says why a channel closed. Empty means a failure: the hub
// reopens with backoff.
type CloseReason string

const (
	// CloseIdle: a lazy MCP was stopped after its idle timeout. Not a
	// failure; the hub reopens on the next call.
	CloseIdle CloseReason = "idle"
	// CloseRestart: the MCP was restarted on the node (e.g. mcp-mesh-node
	// restart). The hub reopens right away, without backoff.
	CloseRestart CloseReason = "restart"
)

// WebSocket close codes used by the protocol.
const (
	CloseUnsupportedVersion = 4001
	// CloseInvalidKey: the node's key is missing, unknown, revoked, or bound
	// to another node name.
	CloseInvalidKey      = 4003
	CloseNodeNameTaken   = 4004
	CloseMessageTooLarge = 4013
)
