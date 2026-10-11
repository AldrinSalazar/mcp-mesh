import { describe, expect, it } from 'vitest'
import { buildQuery, defaultFilter, isFiltered, sinceFor } from './query'

const now = Date.UTC(2026, 9, 10, 12, 0, 0)

describe('sinceFor', () => {
  it('maps ranges', () => {
    expect(sinceFor('1h', now)).toBe('2026-10-10T11:00:00.000Z')
    expect(sinceFor('24h', now)).toBe('2026-10-09T12:00:00.000Z')
    expect(sinceFor('7d', now)).toBe('2026-10-03T12:00:00.000Z')
    expect(sinceFor('30d', now)).toBe('2026-09-10T12:00:00.000Z')
  })
})

describe('buildQuery', () => {
  it('omits empty filters', () => {
    expect(buildQuery(defaultFilter, 0, now)).toEqual({ since: '2026-10-09T12:00:00.000Z', limit: 100 })
  })
  it('includes set filters and the page cursor', () => {
    const f = { ...defaultFilter, client: 'k_1', node: 'n', mcp: 'm', tool: ' x__y ', outcome: 'error' as const }
    expect(buildQuery(f, 42, now)).toEqual({
      since: '2026-10-09T12:00:00.000Z', limit: 100, client_id: 'k_1', node: 'n', mcp: 'm', tool: 'x__y', outcome: 'error', before_id: 42,
    })
    expect(isFiltered(f)).toBe(true)
    expect(isFiltered(defaultFilter)).toBe(false)
  })
})
