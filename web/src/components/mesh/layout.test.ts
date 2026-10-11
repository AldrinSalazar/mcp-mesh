import { describe, expect, it } from 'vitest'
import { cardH, cardW, hubR, mapLayout } from './layout'

const overlap = (a: { x: number; y: number }, b: { x: number; y: number }) =>
  Math.abs(a.x - b.x) < cardW && Math.abs(a.y - b.y) < cardH

describe('mapLayout', () => {
  it('places up to four nodes in the quadrants, first at top left', () => {
    const l = mapLayout(4, 0)
    expect(l.nodes).toHaveLength(4)
    const [tl, tr, bl, br] = l.nodes
    expect(tl!.cx).toBeLessThan(l.hub.x)
    expect(tl!.cy).toBeLessThan(l.hub.y)
    expect(tr!.cx).toBeGreaterThan(l.hub.x)
    expect(bl!.cy).toBeGreaterThan(l.hub.y)
    expect(br!.cx).toBeGreaterThan(l.hub.x)
  })

  it.each([0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 12])('keeps %i nodes apart and inside the canvas', (n) => {
    const l = mapLayout(n, 2)
    expect(l.nodes).toHaveLength(n)
    for (const [i, a] of l.nodes.entries()) {
      expect(a.x).toBeGreaterThanOrEqual(0)
      expect(a.x + cardW).toBeLessThanOrEqual(l.width)
      expect(a.y).toBeGreaterThanOrEqual(0)
      expect(a.y + cardH).toBeLessThanOrEqual(l.height)
      for (const b of l.nodes.slice(i + 1)) expect(overlap(a, b)).toBe(false)
    }
  })

  it.each([5, 6, 7, 8, 9])('keeps the client stem clear of cards with %i nodes', (n) => {
    const l = mapLayout(n, 2)
    for (const p of l.nodes) {
      const crosses = Math.abs(p.cx - l.hub.x) < cardW / 2 && p.cy > l.hub.y
      expect(crosses).toBe(false)
    }
  })

  it('starts each line on the hub edge', () => {
    const l = mapLayout(6, 0)
    for (const p of l.nodes) {
      expect(Math.hypot(p.line.from.x - l.hub.x, p.line.from.y - l.hub.y)).toBeCloseTo(hubR)
    }
  })

  it('centres clients under the hub', () => {
    expect(mapLayout(2, 0).clients).toEqual([])
    expect(mapLayout(2, 0).stem).toBeNull()
    const one = mapLayout(2, 1)
    expect(one.clients[0]!.x).toBe(one.hub.x)
    expect(one.bus).toBeNull()
    const l = mapLayout(2, 3)
    expect(l.clients.map((c) => c.x - l.hub.x)).toEqual([-180, 0, 180])
    expect(l.bus!.x1).toBe(l.clients[0]!.x)
  })

  it('draws an empty hub', () => {
    const l = mapLayout(0, 0)
    expect(l.nodes).toEqual([])
    expect(l.height).toBeGreaterThan(l.hub.y + hubR)
  })
})
