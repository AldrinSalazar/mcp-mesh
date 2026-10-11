import { describe, expect, it } from 'vitest'
import { ago, duration, host, percent, shortID, span } from './format'

describe('format', () => {
  it.each([
    [48, '48 ms'],
    [999, '999 ms'],
    [1200, '1.2 s'],
    [12_400, '12 s'],
    [125_000, '2m 05s'],
  ])('duration(%d) = %s', (ms, want) => expect(duration(ms)).toBe(want))

  it.each([
    [18_000, '18s'],
    [42 * 60_000, '42m'],
    [(3 * 60 + 24) * 60_000, '3h 24m'],
    [(2 * 24 + 5) * 3_600_000, '2d 5h'],
  ])('span(%d) = %s', (ms, want) => expect(span(ms)).toBe(want))

  it('ago', () => {
    const now = Date.parse('2026-10-10T12:00:00Z')
    expect(ago('2026-10-10T11:59:30Z', now)).toBe('Just now')
    expect(ago('2026-10-10T11:58:00Z', now)).toBe('2m ago')
    expect(ago('2026-10-10T08:00:00Z', now)).toBe('4h ago')
  })

  it.each([
    ['10.0.0.5:51234', '10.0.0.5'],
    ['[fe80::1]:7000', 'fe80::1'],
    ['fe80::1', 'fe80::1'],
    ['', ''],
  ])('host(%s) = %s', (addr, want) => expect(host(addr)).toBe(want))

  it('shortID and percent', () => {
    expect(shortID('k_n8d2a1b3')).toBe('k_n8d2…')
    expect(percent(125, 128)).toBe('97.7%')
    expect(percent(0, 0)).toBe('100%')
  })
})
