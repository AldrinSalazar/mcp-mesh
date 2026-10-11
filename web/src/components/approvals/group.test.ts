import { describe, expect, it } from 'vitest'
import { groupApprovals } from './group'
import type { PendingTool } from '@/lib/types'

const p = (node: string, mcp: string, tool: string): PendingTool => ({
  node, mcp, tool, name: `${node}__${mcp}__${tool}`, state: 'new', description: '', input_schema: {}, hash: 'h', definition: {}, since: '',
})

describe('groupApprovals', () => {
  it('groups by node then MCP, sorted', () => {
    const g = groupApprovals([p('nas', 'fs', 'b'), p('mac', 'notes', 'x'), p('nas', 'fs', 'a'), p('nas', 'db', 'q')])
    expect(g.map((n) => [n.node, n.count])).toEqual([['mac', 1], ['nas', 3]])
    expect(g[1]!.mcps.map((m) => m.mcp)).toEqual(['db', 'fs'])
    expect(g[1]!.mcps[1]!.tools.map((t) => t.tool)).toEqual(['a', 'b'])
  })
  it('is empty for nothing', () => {
    expect(groupApprovals([])).toEqual([])
  })
})
