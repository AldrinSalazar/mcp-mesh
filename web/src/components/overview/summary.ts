// Numbers behind the Overview cards. Kept free of Vue so it can be tested.
import { mcpStatus } from '@/lib/status'
import { percent, plural } from '@/lib/format'
import type { Node, PairingRequest, PendingTool, Stats, Tool } from '@/lib/types'

export interface McpGroups {
  running: number
  idle: number
  starting: number
  waiting: number
  error: number
}

export interface Metrics {
  online: number
  total: number
  mcps: number
  groups: McpGroups
  toolNodes: number
}

// groupMcps counts the MCPs of online nodes by how they look on screen.
// Closed MCPs count as idle: they are not a problem and not running.
export function groupMcps(nodes: readonly Node[]): { total: number; groups: McpGroups } {
  const groups: McpGroups = { running: 0, idle: 0, starting: 0, waiting: 0, error: 0 }
  let total = 0
  for (const n of nodes) {
    if (!n.online) continue
    for (const m of n.mcps) {
      total++
      switch (mcpStatus(m).label) {
        case 'Running': groups.running++; break
        case 'Starting': groups.starting++; break
        case 'Waiting for app': groups.waiting++; break
        case 'Error': groups.error++; break
        default: groups.idle++
      }
    }
  }
  return { total, groups }
}

export function metrics(nodes: readonly Node[], tools: readonly Tool[]): Metrics {
  const { total: mcps, groups } = groupMcps(nodes)
  return {
    online: nodes.filter((n) => n.online).length,
    total: nodes.length,
    mcps,
    groups,
    toolNodes: new Set(tools.map((t) => t.node)).size,
  }
}

// groupsLine is "6 running · 1 idle · 1 waiting", skipping empty groups.
export function groupsLine(g: McpGroups): string {
  const parts: [number, string][] = [
    [g.running, 'running'], [g.idle, 'idle'], [g.starting, 'starting'], [g.waiting, 'waiting'], [g.error, 'in error'],
  ]
  return parts.filter(([n]) => n > 0).map(([n, w]) => `${n} ${w}`).join(' · ')
}

export function nodesLine(m: Pick<Metrics, 'online' | 'total'>): string {
  const off = m.total - m.online
  return off === 0 ? 'All machines online' : `${plural(off, 'machine')} offline`
}

export function callsLine(s: Stats | null): { text: string; bad: boolean } {
  const errors = s?.errors_last_hour ?? 0
  const calls = s?.calls_last_hour ?? 0
  if (errors > 0) return { text: `${plural(errors, 'error')} · ${percent(calls - errors, calls)} successful`, bad: true }
  return { text: 'All successful', bad: false }
}

export interface ApprovalItem {
  key: string
  title: string
  detail: string
  to: { path: string; query?: Record<string, string> }
}

// approvalItems lists what waits for an admin: machines asking to join,
// nodes that are not approved, then one line for the tools waiting (the
// nodes' own tools are part of the node's line, so they are not counted
// twice).
export function approvalItems(nodes: readonly Node[], pending: readonly PendingTool[], pairings: readonly PairingRequest[] = []): ApprovalItem[] {
  const out: ApprovalItem[] = pairings.map((p) => ({
    key: `pairing/${p.id}`,
    title: `${p.node} asks to join`,
    detail: `Code ${p.code} · ${p.ip}`,
    to: { path: '/approvals' },
  }))
  const unapproved = new Set<string>()
  for (const n of nodes) {
    if (n.trust.approved) continue
    unapproved.add(n.name)
    out.push({
      key: `node/${n.name}`,
      title: `${n.name} waits for approval`,
      detail: `${plural(n.pending_tools, 'tool')} not available yet`,
      to: { path: '/approvals' },
    })
  }
  const rest = pending.filter((p) => !unapproved.has(p.node))
  if (rest.length) {
    out.push({
      key: 'tools',
      title: `${plural(rest.length, 'tool')} waiting for approval`,
      detail: [...new Set(rest.map((p) => p.node))].join(', '),
      to: { path: '/approvals' },
    })
  }
  return out
}
