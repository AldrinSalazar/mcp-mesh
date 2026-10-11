import { describe, expect, it } from 'vitest'
import { paramLine, parameters } from './schema'

describe('parameters', () => {
  it('handles missing or odd schemas', () => {
    for (const s of [undefined, null, 'x', 3, [], {}, { properties: 4 }]) expect(parameters(s)).toEqual([])
  })
  it('reads types, required, enum, default', () => {
    const ps = parameters({
      type: 'object',
      required: ['path'],
      properties: {
        path: { type: 'string', description: 'Where.' },
        format: { enum: ['PNG', 'JPEG'], default: 'PNG' },
        opts: { type: 'object', properties: {} },
        tags: { type: 'array', items: { type: 'string' } },
        any: { type: 'array' },
        n: { type: ['integer', 'null'] },
        u: { anyOf: [{ type: 'string' }, { type: 'number' }] },
        bad: 5,
      },
    })
    expect(ps.map((p) => [p.name, p.type, p.required])).toEqual([
      ['path', 'string', true],
      ['format', 'string', false],
      ['opts', 'object', false],
      ['tags', 'array of string', false],
      ['any', 'array', false],
      ['n', 'integer | null', false],
      ['u', 'string | number', false],
      ['bad', 'any', false],
    ])
    expect(ps[1]!.default).toBe('PNG')
  })
})

describe('paramLine', () => {
  it('joins type, description, enum and default', () => {
    expect(paramLine({ name: 'a', type: 'string', required: false, description: 'Where.', enumValues: [], default: undefined })).toBe('string · Where.')
    expect(paramLine({ name: 'f', type: 'string', required: false, description: '', enumValues: ['PNG', 'JPEG'], default: 'PNG' })).toBe('string · PNG or JPEG. Default: PNG.')
    expect(paramLine({ name: 'f', type: 'string', required: false, description: 'PNG or JPEG.', enumValues: ['PNG', 'JPEG'], default: 'PNG' })).toBe('string · PNG or JPEG. Default: PNG.')
    expect(paramLine({ name: 'f', type: 'object', required: false, description: '', enumValues: [], default: undefined })).toBe('object')
  })
})
