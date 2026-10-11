// Groups the pending tools for the Approvals screen: node, then MCP.
import type { PendingTool } from '@/lib/types'

export interface McpGroup { mcp: string; tools: PendingTool[] }
export interface NodeGroup { node: string; mcps: McpGroup[]; count: number }

export function groupApprovals(list: readonly PendingTool[]): NodeGroup[] {
  const nodes = new Map<string, Map<string, PendingTool[]>>()
  for (const p of list) {
    const mcps = nodes.get(p.node) ?? new Map<string, PendingTool[]>()
    const tools = mcps.get(p.mcp) ?? []
    tools.push(p)
    mcps.set(p.mcp, tools)
    nodes.set(p.node, mcps)
  }
  return [...nodes]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([node, mcps]) => {
      const groups = [...mcps]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([mcp, tools]) => ({ mcp, tools: [...tools].sort((a, b) => a.name.localeCompare(b.name)) }))
      return { node, mcps: groups, count: groups.reduce((s, g) => s + g.tools.length, 0) }
    })
}
