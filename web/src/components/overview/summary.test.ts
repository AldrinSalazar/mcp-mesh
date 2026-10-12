import { describe, expect, it } from 'vitest'
import { approvalItems, callsLine, groupMcps, groupsLine, metrics, nodesLine } from './summary'
import type { MCP, Node, PairingRequest, PendingTool, Tool } from '@/lib/types'

const mcp = (over: Partial<MCP>): MCP => ({
  name: 'x', transport: 'stdio', sharing: 'shared', start: 'lazy', state: 'ready', error: '',
  tools: 1, in_flight: 0, queued: 0, server: '', prompts: 0, resources: 0, call_timeout_ms: 0, clients: 0, problems: [], ...over,
})
const node = (name: string, online: boolean, mcps: MCP[]): Node => ({
  name, node_version: '', os: '', remote_addr: '', key_id: '', connected_at: '', online, mcps,
  trust: { level: 'trust', explicit: false, approved: true }, pending_tools: 0,
})
const waiting = 'nothing is listening'

const nodes = [
  node('a', true, [mcp({}), mcp({}), mcp({ state: 'idle' }), mcp({ state: 'failed', error: waiting })]),
  node('b', false, [mcp({})]),
]

describe('groupMcps', () => {
  it('counts only online nodes and groups by label', () => {
    expect(groupMcps(nodes)).toEqual({ total: 4, groups: { running: 2, idle: 1, starting: 0, waiting: 1, error: 0 } })
  })
})

describe('lines', () => {
  it('skips empty groups', () => {
    expect(groupsLine(groupMcps(nodes).groups)).toBe('2 running · 1 idle · 1 waiting')
  })
  it('nodes', () => {
    expect(nodesLine({ online: 1, total: 2 })).toBe('1 machine offline')
    expect(nodesLine({ online: 2, total: 2 })).toBe('All machines online')
  })
  it('calls', () => {
    expect(callsLine({ calls_last_hour: 128, errors_last_hour: 3 })).toEqual({ text: '3 errors · 97.7% successful', bad: true })
    expect(callsLine({ calls_last_hour: 5, errors_last_hour: 0 })).toEqual({ text: 'All successful', bad: false })
    expect(callsLine(null).bad).toBe(false)
  })
})

describe('metrics', () => {
  it('counts distinct tool nodes', () => {
    const tools = ['a', 'a', 'c'].map((n) => ({ node: n }) as Tool)
    expect(metrics(nodes, tools)).toMatchObject({ online: 1, total: 2, mcps: 4, toolNodes: 2 })
  })
})

describe('approvalItems', () => {
  const pend = (n: string): PendingTool => ({ node: n, mcp: 'm', tool: 't', name: `${n}__m__t`, state: 'new', description: '', input_schema: {}, hash: 'h', definition: {}, since: '' })
  it('is empty with nothing waiting', () => {
    expect(approvalItems(nodes, [])).toEqual([])
  })
  it('lists unapproved nodes and a tools line without double counting', () => {
    const nas = { ...node('nas', true, []), pending_tools: 3, trust: { level: 'approve' as const, explicit: true, approved: false } }
    const items = approvalItems([...nodes, nas], [pend('nas'), pend('nas'), pend('nas'), pend('mac'), pend('mac')])
    expect(items.map((i) => i.title)).toEqual(['nas waits for approval', '2 tools waiting for approval'])
    expect(items[1]!.detail).toBe('mac')
  })
  it('puts machines asking to join first', () => {
    const p: PairingRequest = { id: 'p_1', node: 'render-box', hostname: '', os: 'linux', node_version: '', ip: '10.0.0.9', code: 'K7Q-4MX', requested: '', name_taken: false }
    const items = approvalItems(nodes, [pend('mac')], [p])
    expect(items.map((i) => i.title)).toEqual(['render-box asks to join', '1 tool waiting for approval'])
    expect(items[0]).toMatchObject({ key: 'pairing/p_1', detail: 'Code K7Q-4MX · 10.0.0.9', to: { path: '/approvals' } })
  })
})
