import { describe, expect, it } from 'vitest'
import { filterNodes } from './filter'
import type { MCP, Node } from '@/lib/types'

const mcp = (name: string, over: Partial<MCP> = {}): MCP => ({
  name, transport: 'stdio', sharing: 'shared', start: 'lazy', state: 'ready', error: '',
  tools: 1, in_flight: 0, queued: 0, server: '', prompts: 0, resources: 0, call_timeout_ms: 0, clients: 0, problems: [], ...over,
})
const node = (name: string, over: Partial<Node> = {}): Node => ({
  name, node_version: '0.2.0', os: 'linux', remote_addr: '10.0.0.5:1234', key_id: 'k_1',
  connected_at: '2026-10-10T10:00:00Z', online: true, mcps: [mcp('git')],
  trust: { level: 'trust', explicit: false, approved: true }, pending_tools: 0, ...over,
})

const nodes = [
  node('zeta'),
  node('alpha', { os: 'darwin', remote_addr: '192.168.1.21:5', mcps: [mcp('blender', { state: 'failed', error: 'nothing is listening' })] }),
  node('beta', { online: false, mcps: [] }),
  node('gamma', { mcps: [mcp('fs', { state: 'idle' })] }),
  node('delta', { pending_tools: 2 }),
  node('eps', { trust: { level: 'approve', explicit: true, approved: false } }),
]
const names = (l: Node[]) => l.map((n) => n.name)

describe('filterNodes', () => {
  it('sorts online first, then by name', () => {
    expect(names(filterNodes(nodes, '', ''))).toEqual(['alpha', 'delta', 'eps', 'gamma', 'zeta', 'beta'])
  })

  it.each([
    ['mac', ['alpha']], // OS label
    ['192.168', ['alpha']], // address
    ['blend', ['alpha']], // MCP name
    ['ETA', ['beta', 'zeta']], // node name, case-insensitive
  ])('searches %s', (q, want) => {
    expect(names(filterNodes(nodes, q, '')).sort()).toEqual(want)
  })

  it.each([
    ['online', ['alpha', 'delta', 'eps', 'gamma', 'zeta']],
    ['offline', ['beta']],
    ['attention', ['alpha', 'delta', 'eps']], // idle and running are fine; pending approvals count
  ] as const)('filters by %s', (state, want) => {
    expect(names(filterNodes(nodes, '', state))).toEqual(want)
  })
})
