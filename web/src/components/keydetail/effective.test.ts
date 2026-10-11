import { describe, expect, it } from 'vitest'
import type { Group, Tool } from '@/lib/types'
import { emptyReason, groupTools } from './effective'

const t = (node: string, mcp: string, name: string, description = ''): Tool =>
  ({ node, mcp, name, original_name: name, description, input_schema: null, approval: 'approved', alias: false, generated_name: name, description_override: '' })

describe('groupTools', () => {
  const tools = [t('b', 'fs', 'z'), t('a', 'git', 'log', 'History'), t('a', 'fs', 'read'), t('a', 'fs', 'list')]
  it('groups and sorts', () => {
    const g = groupTools(tools, '')
    expect(g.map((n) => [n.node, n.count, n.mcps.map((m) => m.mcp)])).toEqual([['a', 3, ['fs', 'git']], ['b', 1, ['fs']]])
    expect(g[0]!.mcps[0]!.tools.map((x) => x.name)).toEqual(['list', 'read'])
  })
  it('filters by name or description', () => {
    expect(groupTools(tools, 'history')[0]!.count).toBe(1)
    expect(groupTools(tools, 'nope')).toEqual([])
  })
})

describe('emptyReason', () => {
  const grp = (grants: Group['grants']): Group => ({ id: 'g', name: 'g', description: '', created: '', grants, keys: [] })
  it('explains', () => {
    expect(emptyReason([]).text).toMatch(/no group/)
    expect(emptyReason([grp([])]).text).toMatch(/don’t grant/)
    expect(emptyReason([grp([{ node: '*', mcp: '*', tool: '*' }])]).title).toMatch(/right now/)
  })
})
