import { describe, expect, it } from 'vitest'
import type { Grant, Node, Tool } from '@/lib/types'
import { buildTree, check, countTools, filterTree, future, minimize, summary, toggle } from './grants'
import { membershipChanges } from './membership'

const g = (node: string, mcp = '*', tool = '*'): Grant => ({ node, mcp, tool })
const node = (name: string, mcps: string[], online = true) =>
  ({ name, online, mcps: mcps.map((m) => ({ name: m })) }) as unknown as Node
const tool = (n: string, m: string, t: string) => ({ node: n, mcp: m, original_name: t, name: `${m}_${t}` }) as unknown as Tool

const nodes = [node('laptop', ['fs', 'git']), node('mac', ['fs'])]
const tools = [tool('laptop', 'fs', 'read'), tool('laptop', 'fs', 'write'), tool('laptop', 'git', 'log'), tool('mac', 'fs', 'read')]
const tree = (grants: Grant[]) => buildTree(nodes, tools, grants)

describe('check', () => {
  it('derives tri-state from wildcards', () => {
    const grants = [g('laptop', 'fs', 'read'), g('mac')]
    const t = tree(grants)
    expect(check(grants, t, [])).toBe('partial')
    expect(check(grants, t, ['laptop'])).toBe('partial')
    expect(check(grants, t, ['laptop', 'fs'])).toBe('partial')
    expect(check(grants, t, ['laptop', 'fs', 'read'])).toBe('on')
    expect(check(grants, t, ['laptop', 'fs', 'write'])).toBe('off')
    expect(check(grants, t, ['mac', 'fs', 'read'])).toBe('on')
    expect(check([g('*')], t, [])).toBe('on')
    expect(check([], t, [])).toBe('off')
  })
})

describe('toggle', () => {
  it('checking the root grants everything', () => {
    expect(toggle([g('laptop')], tree([]), [], true)).toEqual([g('*')])
  })
  it('checking every tool grants those tools, not future ones', () => {
    let grants: Grant[] = []
    grants = toggle(grants, tree(grants), ['laptop', 'fs', 'read'], true)
    grants = toggle(grants, tree(grants), ['laptop', 'fs', 'write'], true)
    expect(grants).toEqual([g('laptop', 'fs', 'read'), g('laptop', 'fs', 'write')])
    const t = tree(grants)
    expect(check(grants, t, ['laptop', 'fs'])).toBe('on')
    expect(future(grants, ['laptop', 'fs'])).toBe(false)
  })
  it('checking an MCP or node row grants it now and later', () => {
    let grants = toggle([], tree([]), ['laptop', 'fs'], true)
    expect(grants).toEqual([g('laptop', 'fs')])
    expect(future(grants, ['laptop', 'fs'])).toBe(true)
    expect(future(grants, ['laptop'])).toBe(false)
    grants = toggle(grants, tree(grants), ['laptop'], true)
    expect(grants).toEqual([g('laptop')])
    expect(future(grants, ['laptop', 'git'])).toBe(true)
  })
  it('drops grants a wider one covers', () => {
    const grants = toggle([g('laptop', 'fs', 'read'), g('laptop', 'git')], tree([]), ['laptop'], true)
    expect(grants).toEqual([g('laptop')])
  })
  it('unchecking inside a wildcard splits it', () => {
    const grants = toggle([g('*')], tree([]), ['laptop', 'fs', 'write'], false)
    expect(grants).toEqual([g('laptop', 'fs', 'read'), g('laptop', 'git'), g('mac')])
  })
  it('unchecking a node removes its narrower grants only', () => {
    const grants = toggle([g('laptop', 'fs', 'read'), g('mac')], tree([]), ['laptop'], false)
    expect(grants).toEqual([g('mac')])
  })
  it('unchecking everything empties the list', () => {
    expect(toggle([g('*')], tree([]), [], false)).toEqual([])
  })
  it('keeps grants for things that are not connected', () => {
    const grants = [g('ghost'), g('laptop', 'old', 'x')]
    const t = tree(grants)
    expect(t.find((n) => n.name === 'ghost')?.connected).toBe(false)
    expect(t.find((n) => n.name === 'laptop')?.mcps.find((m) => m.name === 'old')?.tools[0]).toEqual({ name: 'x', connected: false })
    const next = toggle(grants, t, ['mac', 'fs', 'read'], true)
    expect(next).toEqual(expect.arrayContaining(grants))
    const split = toggle([g('*')], tree([g('ghost')]), ['laptop'], false)
    expect(split.map((x) => x.node)).toEqual(['ghost', 'mac'])
  })
})

describe('minimize', () => {
  it('dedupes and orders', () => {
    expect(minimize([g('b'), g('a', 'x', 't'), g('b'), g('a')])).toEqual([g('a'), g('b')])
  })
})

describe('countTools and summary', () => {
  it('counts known tools', () => {
    expect(countTools(tree([]), [g('laptop', 'fs')])).toEqual({ granted: 2, total: 4 })
  })
  it('describes grants', () => {
    expect(summary([])).toBe('No access')
    expect(summary([g('*')])).toBe('Every tool on every node')
    expect(summary([g('ws')])).toBe('All of ws')
    expect(summary([g('a', 'm', 't'), g('b', 'm', 'u'), g('b', 'm', 'v')])).toBe('3 tools on a, b')
  })
})

describe('filterTree', () => {
  it('keeps matching branches', () => {
    const t = tree([])
    expect(filterTree(t, '')).toBe(t)
    expect(filterTree(t, 'git').map((n) => [n.name, n.mcps.map((m) => m.name)])).toEqual([['laptop', ['git']]])
    expect(filterTree(t, 'write')[0]!.mcps[0]!.tools.map((x) => x.name)).toEqual(['write'])
    expect(filterTree(t, 'mac')[0]!.mcps).toHaveLength(1)
  })
})

describe('membershipChanges', () => {
  it('changes only keys whose membership differs', () => {
    const keys = [
      { id: 'k1', kind: 'client', groups: ['g1'] },
      { id: 'k2', kind: 'client', groups: ['g2'] },
      { id: 'k3', kind: 'client', groups: ['g1', 'g2'] },
      { id: 'n', kind: 'node' },
    ] as never
    const ch = membershipChanges(keys, 'g1', new Set(['k2', 'k3']))
    expect(ch.map((c) => [c.key.id, c.groups])).toEqual([['k1', []], ['k2', ['g2', 'g1']]])
  })
})
