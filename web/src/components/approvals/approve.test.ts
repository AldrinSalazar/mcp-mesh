import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import type { PendingTool } from '@/lib/types'
import { groupApprovals } from './group'
import { mcpRefs, nodeRefs, refOf, reportApproveError, staleToast } from './approve'

function pt(node: string, mcp: string, tool: string, hash = `h-${tool}`): PendingTool {
  return { node, mcp, tool, name: `${node}__${mcp}__${tool}`, state: 'new', description: '', input_schema: {}, hash, definition: {}, since: '' }
}

describe('approval refs', () => {
  // Groups sort by node name: m, then n.
  const g = groupApprovals([pt('n', 'a', 't1'), pt('n', 'a', 't2', 'x2'), pt('n', 'b', 't3'), pt('m', 'a', 't9')])[1]!

  it('refOf carries the hash the row was rendered with', () => {
    expect(refOf(pt('n', 'a', 't2', 'x2'))).toEqual({ node: 'n', mcp: 'a', tool: 't2', hash: 'x2' })
  })

  it("an MCP approval lists only that MCP's rendered tools", () => {
    expect(g.node).toBe('n')
    expect(mcpRefs(g, 'a')).toEqual([
      { node: 'n', mcp: 'a', tool: 't1', hash: 'h-t1' },
      { node: 'n', mcp: 'a', tool: 't2', hash: 'x2' },
    ])
    expect(mcpRefs(g, 'nope')).toEqual([])
  })

  it("a node approval lists all of that node's rendered tools", () => {
    expect(nodeRefs(g).map((r) => r.tool)).toEqual(['t1', 't2', 't3'])
  })
})

describe('reportApproveError', () => {
  it('on 409 shows the stale toast and reloads the approvals', async () => {
    const toast = vi.fn()
    const refresh = vi.fn().mockResolvedValue(undefined)
    const stale = [{ node: 'n', mcp: 'a', tool: 't1', hash: 'h' }]
    await reportApproveError(new ApiError(409, 'changed', undefined, stale), { toast, refresh })
    expect(toast).toHaveBeenCalledOnce()
    expect(toast.mock.calls[0]![0]).toMatch(/changed since/)
    expect(toast.mock.calls[0]![1]).toContain('a/t1')
    expect(toast.mock.calls[0]![2]).toBe('err')
    expect(refresh).toHaveBeenCalledWith('approvals')
  })

  it('other errors show the message and do not reload', async () => {
    const toast = vi.fn()
    const refresh = vi.fn()
    await reportApproveError(new ApiError(500, 'boom'), { toast, refresh })
    expect(toast).toHaveBeenCalledWith("Couldn't approve", 'boom', 'err')
    expect(refresh).not.toHaveBeenCalled()
  })
})

describe('staleToast', () => {
  const ref = (tool: string) => ({ node: 'n', mcp: 'a', tool, hash: 'h' })

  it('names a few stale tools', () => {
    const t = staleToast(new ApiError(409, 'changed', undefined, [ref('t1'), ref('t2')]))
    expect(t?.body).toContain('a/t1, a/t2')
    expect(t?.body).toContain('Review them again')
  })

  it('counts many', () => {
    expect(staleToast(new ApiError(409, 'changed', undefined, [ref('1'), ref('2'), ref('3'), ref('4')]))?.body).toContain('4 tools')
  })

  it('handles a 409 without a list', () => {
    expect(staleToast(new ApiError(409, 'changed'))?.body).toContain('Some tools')
  })

  it('ignores other errors', () => {
    expect(staleToast(new ApiError(500, 'boom'))).toBeNull()
    expect(staleToast(new Error('x'))).toBeNull()
  })
})
