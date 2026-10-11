// Which machines the Nodes screen shows, and in what order.
import { host, osLabel } from '@/lib/format'
import { mcpStatus } from '@/lib/status'
import type { Node } from '@/lib/types'

export type StateFilter = '' | 'online' | 'offline' | 'attention'

// needsAttention: a node waiting for approval or with tools waiting, or an
// online node with an MCP that is neither Running nor Idle.
export function needsAttention(n: Node): boolean {
  if (n.pending_tools > 0 || !n.trust.approved) return true
  if (!n.online) return false
  return n.mcps.some((m) => {
    const label = mcpStatus(m).label
    return label !== 'Running' && label !== 'Idle'
  })
}

export function filterNodes(nodes: Node[], query: string, state: StateFilter): Node[] {
  const q = query.trim().toLowerCase()
  const match = (n: Node) =>
    !q ||
    [n.name, osLabel(n.os), host(n.remote_addr), ...n.mcps.map((m) => m.name)].some((s) => s.toLowerCase().includes(q))
  const inState = (n: Node) => {
    switch (state) {
      case 'online': return n.online
      case 'offline': return !n.online
      case 'attention': return needsAttention(n)
      default: return true
    }
  }
  return nodes
    .filter((n) => match(n) && inState(n))
    .sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name))
}
