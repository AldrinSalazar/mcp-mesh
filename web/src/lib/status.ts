// How hub states are named and coloured on screen. Every screen shows status
// through these so the same thing always looks the same.
import type { Call, Key, MCP, Node, Outcome } from './types'

export type Tone = 'ok' | 'idle' | 'warn' | 'err' | 'info' | 'neutral'

export interface Status {
  label: string
  tone: Tone
  // detail is the reason to show under or next to the badge, if any.
  detail?: string
}

// waitingForApp recognises the node's "app isn't running" failure
// (upstream.ErrUnreachable) in an MCP's error text.
export function waitingForApp(m: Pick<MCP, 'error'>): boolean {
  return /nothing is listening|is the app|waiting for app/i.test(m.error)
}

export function mcpStatus(m: MCP, nodeOnline = true): Status {
  if (!nodeOnline) return { label: 'Offline', tone: 'err' }
  switch (m.state) {
    case 'ready':
      return { label: 'Running', tone: 'ok' }
    case 'idle':
      // Idle with an error: an on-demand MCP whose last open failed.
      if (m.error && waitingForApp(m)) return { label: 'Waiting for app', tone: 'warn', detail: m.error }
      return { label: 'Idle', tone: 'idle', detail: m.error || undefined }
    case 'opening':
      return { label: 'Starting', tone: 'warn', detail: m.error || undefined }
    case 'failed':
      if (waitingForApp(m)) return { label: 'Waiting for app', tone: 'warn', detail: m.error }
      return { label: 'Error', tone: 'err', detail: m.error || undefined }
    case 'closed':
      return { label: 'Closed', tone: 'idle', detail: m.error || undefined }
  }
  return { label: m.state, tone: 'neutral' }
}

export function nodeStatus(n: Node): Status {
  return n.online ? { label: 'Online', tone: 'ok' } : { label: 'Offline', tone: 'err' }
}

export function outcomeStatus(o: Outcome): Status {
  switch (o) {
    case 'ok': return { label: 'OK', tone: 'ok' }
    case 'tool_error': return { label: 'Tool error', tone: 'err' }
    default: return { label: 'Error', tone: 'err' }
  }
}

export function transportLabel(m: MCP): string {
  switch (m.transport) {
    case 'stdio': return 'stdio / launched command'
    case 'http': return 'HTTP / app connection'
    case 'sse': return 'SSE / app connection'
    default: return m.transport
  }
}

export function startLabel(m: MCP): string {
  return m.start === 'eager' ? 'Always on' : 'On demand'
}

// A client key counts as active when it was used this recently. The hub
// writes last-used at most once a minute, so this can't be much shorter.
export const clientActiveWindow = 15 * 60_000

export function keyConnection(k: Key, nodes: Node[], now: number = Date.now()): Status {
  if (k.kind === 'node') {
    if (nodes.some((n) => n.online && n.key_id === k.id)) return { label: 'Connected', tone: 'ok' }
    return k.last_used ? { label: 'Offline', tone: 'idle' } : { label: 'Unused', tone: 'idle' }
  }
  if (!k.last_used) return { label: 'Unused', tone: 'idle' }
  if (now - new Date(k.last_used).getTime() < clientActiveWindow) return { label: 'In use', tone: 'ok' }
  return { label: 'Idle', tone: 'idle' }
}

// activeClients are the clients seen recently: client keys used within the
// window, plus (with auth off, where there are no keys) callers in recent calls.
export function activeClients(keys: Key[], calls: Call[], now: number = Date.now()): { id: string; name: string }[] {
  const out = new Map<string, string>()
  for (const k of keys) {
    if (k.kind === 'client' && k.last_used && now - new Date(k.last_used).getTime() < clientActiveWindow) out.set(k.id, k.name)
  }
  for (const c of calls) {
    if (now - new Date(c.time).getTime() < clientActiveWindow && !out.has(c.client_id)) out.set(c.client_id, c.client || c.client_id)
  }
  return [...out].map(([id, name]) => ({ id, name }))
}

export interface Problem {
  kind: 'node' | 'mcp'
  node: string
  mcp?: string
  title: string
  detail: string
  tone: Tone
  since?: string // for offline nodes: when it went offline
}

// problems lists what needs attention: offline nodes and MCPs that are
// failing or waiting for their app.
export function problems(nodes: Node[]): Problem[] {
  const out: Problem[] = []
  for (const n of nodes) {
    if (!n.online) {
      out.push({ kind: 'node', node: n.name, title: `${n.name} is offline`, detail: '', tone: 'err', since: n.disconnected_at })
      continue
    }
    for (const m of n.mcps) {
      const s = mcpStatus(m)
      if (s.label === 'Waiting for app') {
        out.push({ kind: 'mcp', node: n.name, mcp: m.name, title: `${cap(m.name)} is waiting for the app`, detail: `${n.name} / ${m.name}`, tone: 'warn' })
      } else if (s.tone === 'err') {
        out.push({ kind: 'mcp', node: n.name, mcp: m.name, title: `${cap(m.name)} has an error`, detail: m.error || `${n.name} / ${m.name}`, tone: 'err' })
      }
    }
  }
  return out
}

function cap(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s
}
