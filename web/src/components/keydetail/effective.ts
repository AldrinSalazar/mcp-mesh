// Presentation of a key's effective tool list: grouping, search, and why it
// may be empty.
import type { Group, Tool } from '@/lib/types'

export interface McpTools { mcp: string; tools: Tool[] }
export interface NodeTools { node: string; mcps: McpTools[]; count: number }

// groupTools groups tools by node, then MCP, keeping those whose exposed
// name, original name or description contains q.
export function groupTools(tools: Tool[], q: string): NodeTools[] {
  const s = q.trim().toLowerCase()
  const nodes = new Map<string, Map<string, Tool[]>>()
  for (const t of tools) {
    if (s && !`${t.name} ${t.original_name} ${t.description}`.toLowerCase().includes(s)) continue
    const mcps = nodes.get(t.node) ?? new Map<string, Tool[]>()
    mcps.set(t.mcp, [...(mcps.get(t.mcp) ?? []), t])
    nodes.set(t.node, mcps)
  }
  return [...nodes.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([node, mcps]) => {
      const list = [...mcps.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([mcp, ts]) => ({ mcp, tools: ts.slice().sort((a, b) => a.name.localeCompare(b.name)) }))
      return { node, mcps: list, count: list.reduce((n, m) => n + m.tools.length, 0) }
    })
}

// emptyReason explains an empty effective list from the key's groups.
export function emptyReason(keyGroups: Group[]): { title: string; text: string } {
  if (keyGroups.length === 0) {
    return { title: 'This key sees no tools', text: 'It is in no group. Add it to a group to give it access.' }
  }
  if (keyGroups.every((g) => g.grants.length === 0)) {
    return { title: 'This key sees no tools', text: 'Its groups don’t grant anything yet. Edit a group to choose what it can use.' }
  }
  return {
    title: 'Nothing is available right now',
    text: 'Its groups grant tools that aren’t connected at the moment, or that haven’t been approved yet.',
  }
}
