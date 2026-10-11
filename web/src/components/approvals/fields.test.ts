import { describe, expect, it } from 'vitest'
import { diffFields, fieldLabel, otherFields } from './diff'

describe('diffFields', () => {
  const base = { name: 'a', description: 'd', inputSchema: { type: 'object' } }

  it('ignores name, description and inputSchema', () => {
    const after = { name: 'b', description: 'other', inputSchema: { type: 'string' } }
    expect(diffFields(base, after)).toEqual([])
  })

  it('reports added, removed and changed fields, sorted by key', () => {
    const before = { ...base, title: 'Old', annotations: { readOnlyHint: true } }
    const after = { ...base, title: 'New', outputSchema: { type: 'object' } }
    const d = diffFields(before, after)
    expect(d.map((f) => [f.key, f.status])).toEqual([['annotations', 'removed'], ['outputSchema', 'added'], ['title', 'changed']])
    expect(d[0]!.lines.every((l) => l.op === 'del')).toBe(true)
    expect(d[1]!.lines.every((l) => l.op === 'add')).toBe(true)
    expect(d[2]!.lines.some((l) => l.op === 'add' && l.text.includes('New'))).toBe(true)
  })

  it('treats reordered object keys as unchanged', () => {
    const a = { annotations: { x: 1, y: { p: 1, q: 2 } } }
    const b = { annotations: { y: { q: 2, p: 1 }, x: 1 } }
    expect(diffFields(a, b)).toEqual([])
  })

  it('handles a missing previous definition', () => {
    expect(diffFields(undefined, { title: 'T' }).map((f) => f.key)).toEqual(['title'])
    expect(diffFields(undefined, undefined)).toEqual([])
  })
})

describe('otherFields', () => {
  it('lists every field except name, description and inputSchema', () => {
    const f = otherFields({ name: 'a', description: 'd', inputSchema: {}, title: 'T', annotations: { readOnlyHint: true } })
    expect(f.map((x) => x.key)).toEqual(['annotations', 'title'])
    expect(f[1]!.text).toBe('"T"')
  })

  it('is empty without a definition', () => {
    expect(otherFields(undefined)).toEqual([])
  })
})

describe('fieldLabel', () => {
  it('names MCP fields and keeps unknown keys', () => {
    expect(fieldLabel('outputSchema')).toBe('Output schema')
    expect(fieldLabel('_meta')).toBe('Metadata (_meta)')
    expect(fieldLabel('x-vendor')).toBe('x-vendor')
  })
})
