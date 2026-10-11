import type { Tool } from '@/lib/types'

export interface ToolFilter {
  q: string
  node: string
  mcp: string
  // pending: only tools that clients can't see yet.
  pending?: boolean
}

export function filterTools(tools: readonly Tool[], f: ToolFilter): Tool[] {
  const q = f.q.trim().toLowerCase()
  return tools.filter((t) => {
    if (f.node && t.node !== f.node) return false
    if (f.mcp && t.mcp !== f.mcp) return false
    if (f.pending && t.approval === 'approved') return false
    if (!q) return true
    return [t.name, t.generated_name, t.original_name, t.description, t.description_override, t.node, t.mcp].some((s) => s.toLowerCase().includes(q))
  })
}

// mcpOptions lists the MCP names to offer: the chosen node's, or every node's
// (de-duplicated) when no node is chosen.
export function mcpOptions(nodes: readonly { readonly name: string; readonly mcps: readonly { readonly name: string }[] }[], tools: readonly Tool[], node: string): string[] {
  const names = new Set<string>()
  for (const n of nodes) if (!node || n.name === node) for (const m of n.mcps) names.add(m.name)
  for (const t of tools) if (!node || t.node === node) names.add(t.mcp)
  return [...names].sort()
}
