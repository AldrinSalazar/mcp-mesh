import { describe, expect, it } from 'vitest'
import { clientsLabel, isPerClient, offersLabel, timeoutLabel } from './mcpinfo'

describe('timeoutLabel', () => {
  it.each([
    [0, ''], [-5, ''], [250, '250ms'], [1000, '1s'], [1500, '1.5s'], [30000, '30s'], [60000, '1m'],
    [90000, '1m 30s'], [600000, '10m'], [3600000, '1h'], [5400000, '1h 30m'],
  ])('%d → %j', (ms, want) => expect(timeoutLabel(ms)).toBe(want))
})

describe('per-client labels', () => {
  it('describes clients', () => {
    expect(clientsLabel({ clients: 0 })).toBe('Per-client · no clients yet')
    expect(clientsLabel({ clients: 1 })).toBe('Per-client · 1 client')
    expect(clientsLabel({ clients: 2 })).toBe('Per-client · 2 clients')
    expect(isPerClient({ sharing: 'per-client' })).toBe(true)
    expect(isPerClient({ sharing: 'shared' })).toBe(false)
  })
})

describe('offersLabel', () => {
  it('adds prompts and resources only when present', () => {
    expect(offersLabel({ tools: 12, prompts: 0, resources: 0 })).toBe('12')
    expect(offersLabel({ tools: 12, prompts: 2, resources: 1 })).toBe('12 · 2 prompts · 1 resource')
    expect(offersLabel({ tools: 0, prompts: 1, resources: 0 })).toBe('0 · 1 prompt')
  })
})
