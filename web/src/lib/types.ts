// Shapes of the hub's /api/v1 responses (docs/api.md). Times are RFC 3339
// strings; optional fields are omitted when empty.

export interface Info {
  version: string
  auth: boolean
  node_url: string
  client_url: string
  discovery_name: string
  // oauth: apps can connect through OAuth (the hub is its own authorization server).
  oauth?: boolean
  // started_at: when the hub started; its nodes are reconnecting for a while after.
  started_at?: string
}

// UpdateInfo is GET /update: whether a newer mcp-mesh release is out.
export interface UpdateInfo {
  // enabled: false for a build without a release version (dev), which never checks.
  enabled: boolean
  version: string
  // latest: the newest release; empty until a check succeeds.
  latest: string
  update_available: boolean
  release_url: string
  checked_at?: string
}

export interface Session {
  username: string
  csrf: string
  expires: string
}

export type MCPState = 'opening' | 'ready' | 'idle' | 'failed' | 'closed'

export interface MCP {
  name: string
  transport: string // stdio | http | sse
  sharing: string // shared | per-client
  start: string // eager | lazy
  state: MCPState
  error: string
  tools: number
  in_flight: number
  queued: number
  server: string
  prompts: number
  resources: number // resources and templates
  call_timeout_ms: number // 0 = the hub default
  // clients: for per-client MCPs, how many clients have a process now.
  clients: number
  // problems: definitions the hub dropped from the MCP's last listing, each
  // with the reason. Empty when none.
  problems: string[]
}

export interface PromptArgument {
  name: string
  required?: boolean
  description?: string
}

export interface Prompt {
  name: string // exposed
  node: string
  mcp: string
  original_name: string
  description: string
  arguments?: PromptArgument[]
}

export interface ResourceItem {
  uri: string // exposed (mcp-mesh://<node>/<mcp>/<original>)
  template: boolean
  node: string
  mcp: string
  original_uri: string
  name: string
  description: string
  mime_type?: string
}

export interface Node {
  name: string
  node_version: string
  os: string
  remote_addr: string
  key_id: string
  connected_at: string
  online: boolean
  disconnected_at?: string
  mcps: MCP[]
  trust: NodeTrust
  pending_tools: number
  // outdated: the node runs an older version than the latest release (or,
  // before the hub knows one, than the hub).
  outdated?: boolean
}

// trust: every tool visible; review: changed tools wait for approval;
// approve: the node, then every new or changed tool, waits for approval.
export type TrustLevel = 'trust' | 'review' | 'approve'

export interface NodeTrust {
  level: TrustLevel
  explicit: boolean // false: follows the hub default
  approved: boolean // false only for an approve-level node not yet approved
}

export type Approval = 'approved' | 'new' | 'changed' | 'node_pending'

export interface PendingTool {
  node: string
  mcp: string
  tool: string // original name
  name: string // exposed name
  state: Exclude<Approval, 'approved'>
  description: string
  input_schema: unknown
  // hash identifies the definition shown; approving sends it back so the hub
  // can refuse when the tool changed meanwhile.
  hash: string
  // definition is the whole tool as the MCP sent it (original name).
  definition: Record<string, unknown>
  previous?: { description: string; input_schema: unknown; definition: Record<string, unknown>; approved_at: string; approved_by: string }
  since: string
}

export interface ApprovalRef {
  node: string
  mcp: string
  tool: string // original name
  hash: string
}

export interface Grant {
  node: string // "*" = every node
  mcp: string // "*" = every MCP of the node
  tool: string // original tool name, or "*"
}

export interface Group {
  id: string
  name: string
  description: string
  created: string
  grants: Grant[]
  keys: string[]
}

export interface Settings {
  trust_default: TrustLevel
  oauth: { enabled: boolean; issuer?: string; resource?: string }
  // pairing: machines without a key may ask to join (the hub's --pairing).
  pairing: boolean
}

// A machine without a key asking to join. Approving it creates a node key
// and sends it to the machine, which waits with the same code on screen.
export interface PairingRequest {
  id: string
  node: string // the name it asked for
  hostname: string
  os: string
  node_version: string
  ip: string
  code: string
  requested: string
  // name_taken: a node key is bound to that name already; approving under
  // it replaces that key.
  name_taken: boolean
}

export interface AdminLogEntry {
  id: number
  time: string
  actor: string // username, "cli", or "" for a failed sign-in
  action: string
  target: string
  detail: string
  ip: string
}

export interface Tool {
  name: string
  node: string
  mcp: string
  original_name: string
  description: string
  input_schema: unknown
  approval: Approval
  // alias says name is an admin-set alias; generated_name is
  // <node>__<mcp>__<tool>. description stays the MCP's own, and
  // description_override (empty if none) is what models see instead.
  alias: boolean
  generated_name: string
  description_override: string
}

// ToolOverride is a stored alias / description for one tool (which may be
// offline); tool is the original name.
export interface ToolOverride {
  node: string
  mcp: string
  tool: string
  alias: string
  description: string
  updated: string
  updated_by: string
}

export type Outcome = 'ok' | 'tool_error' | 'error'

export interface Call {
  time: string
  client_id: string
  client: string
  /** What the client asked for: a tool call, a prompt, or a resource read. */
  kind: 'tool' | 'prompt' | 'resource'
  /** The tool or prompt name, or the resource URI. */
  tool: string
  node: string
  mcp: string
  duration_ms: number
  outcome: Outcome
  error: string
}

export interface AuditEntry extends Call {
  id: number
}

export interface AuditPage {
  entries: AuditEntry[]
  next_before_id: number
}

export interface AuditQuery {
  since?: string
  until?: string
  client_id?: string
  node?: string
  mcp?: string
  tool?: string
  outcome?: Outcome | ''
  before_id?: number
  limit?: number
}

export interface Stats {
  calls_last_hour: number
  errors_last_hour: number
}

export type KeyKind = 'node' | 'client'

export interface Key {
  id: string
  kind: KeyKind
  name: string
  created: string
  last_used?: string
  last_ip?: string
  groups?: string[] // client keys
  oauth_client?: { id: string; name: string } // the key behind an OAuth authorization
}

export interface NodeSetup {
  hub_url: string
  init_command: string // sets up a new machine: hub, node name and key
  command: string // replaces the key on a machine that is already set up
}

export interface ClientSetup {
  mcp_url: string
  header: string
  claude_code: string
}

export interface CreatedKey {
  key: Key
  secret: string
  setup: NodeSetup | ClientSetup
}

export interface Admin {
  username: string
  created: string
  password_changed: string
  last_login?: string
}
