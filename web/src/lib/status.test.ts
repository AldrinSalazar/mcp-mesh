import { describe, expect, it } from 'vitest'
import { activeClients, keyConnection, mcpStatus, problems } from './status'
import type { Key, MCP, Node } from './types'

const mcp = (over: Partial<MCP>): MCP => ({
  name: 'fs', transport: 'stdio', sharing: 'shared', start: 'lazy', state: 'ready', error: '',
  tools: 1, in_flight: 0, queued: 0, server: '', prompts: 0, resources: 0, call_timeout_ms: 0, clients: 0, problems: [], ...over,
})
const unreachable = 'nothing is listening (is the app serving this MCP running?)'

describe('mcpStatus', () => {
  it.each([
    [mcp({ state: 'ready' }), 'Running', 'ok'],
    [mcp({ state: 'idle' }), 'Idle', 'idle'],
    [mcp({ state: 'opening' }), 'Starting', 'warn'],
    [mcp({ state: 'failed', error: 'exec: not found' }), 'Error', 'err'],
    [mcp({ state: 'failed', error: unreachable }), 'Waiting for app', 'warn'],
    [mcp({ state: 'idle', error: unreachable }), 'Waiting for app', 'warn'],
    [mcp({ state: 'closed' }), 'Closed', 'idle'],
  ])('%o → %s', (m, label, tone) => {
    expect(mcpStatus(m)).toMatchObject({ label, tone })
  })
  it('offline node', () => expect(mcpStatus(mcp({}), false).label).toBe('Offline'))
})

describe('problems', () => {
  it('lists offline nodes and failing MCPs', () => {
    const nodes: Node[] = [
      { name: 'ws', node_version: '', os: '', remote_addr: '', key_id: 'k1', connected_at: '', online: true, trust: { level: 'trust', explicit: false, approved: true }, pending_tools: 0,
        mcps: [mcp({ name: 'blender', state: 'failed', error: unreachable }), mcp({ name: 'git' })] },
      { name: 'studio', node_version: '', os: '', remote_addr: '', key_id: '', connected_at: '', online: false, mcps: [], trust: { level: 'trust', explicit: false, approved: true }, pending_tools: 0 },
    ]
    expect(problems(nodes).map((p) => p.title)).toEqual(['Blender is waiting for the app', 'studio is offline'])
  })
})

describe('keys', () => {
  const now = Date.parse('2026-10-10T12:00:00Z')
  const key = (over: Partial<Key>): Key => ({ id: 'k1', kind: 'client', name: 'c', created: '', ...over })
  it('client key states', () => {
    expect(keyConnection(key({}), [], now).label).toBe('Unused')
    expect(keyConnection(key({ last_used: '2026-10-10T11:58:00Z' }), [], now).label).toBe('In use')
    expect(keyConnection(key({ last_used: '2026-10-09T11:58:00Z' }), [], now).label).toBe('Idle')
  })
  it('node key connected when a node uses it', () => {
    const n = { name: 'ws', key_id: 'k1', online: true } as Node
    expect(keyConnection(key({ kind: 'node' }), [n], now).label).toBe('Connected')
    expect(keyConnection(key({ kind: 'node', last_used: '2026-10-09T00:00:00Z' }), [], now).label).toBe('Offline')
  })
  it('activeClients merges keys and calls', () => {
    const got = activeClients(
      [key({ id: 'k1', name: 'Claude Code', last_used: '2026-10-10T11:58:00Z' }), key({ id: 'k2', name: 'old' })],
      [{ time: '2026-10-10T11:59:00Z', client_id: 'anonymous', client: '' } as never],
      now,
    )
    expect(got).toEqual([{ id: 'k1', name: 'Claude Code' }, { id: 'anonymous', name: 'anonymous' }])
  })
})
