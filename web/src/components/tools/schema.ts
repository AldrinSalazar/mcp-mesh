// Reads a tool's JSON Schema defensively: input_schema comes from third-party
// MCPs, so anything may be missing or oddly shaped.
export interface Param {
  name: string
  type: string
  required: boolean
  description: string
  enumValues: string[]
  default?: string
}

const isObj = (v: unknown): v is Record<string, unknown> => typeof v === 'object' && v !== null && !Array.isArray(v)

function typeOf(s: Record<string, unknown>): string {
  const t = s.type
  if (typeof t === 'string') {
    if (t === 'array') {
      const items = isObj(s.items) ? s.items : {}
      const it = typeof items.type === 'string' ? items.type : ''
      return it ? `array of ${it}` : 'array'
    }
    return t
  }
  if (Array.isArray(t)) {
    const names = t.filter((x): x is string => typeof x === 'string')
    if (names.length) return names.join(' | ')
  }
  for (const k of ['anyOf', 'oneOf'] as const) {
    const alts = s[k]
    if (Array.isArray(alts)) {
      const names = alts.filter(isObj).map(typeOf).filter((x) => x !== 'any')
      if (names.length) return [...new Set(names)].join(' | ')
    }
  }
  if (Array.isArray(s.enum)) return 'string'
  if (isObj(s.properties)) return 'object'
  return 'any'
}

function show(v: unknown): string {
  return typeof v === 'string' ? v : JSON.stringify(v)
}

export function parameters(schema: unknown): Param[] {
  if (!isObj(schema) || !isObj(schema.properties)) return []
  const required = new Set(Array.isArray(schema.required) ? schema.required.filter((x): x is string => typeof x === 'string') : [])
  return Object.entries(schema.properties).map(([name, raw]) => {
    const s = isObj(raw) ? raw : {}
    return {
      name,
      type: typeOf(s),
      required: required.has(name),
      description: typeof s.description === 'string' ? s.description : '',
      enumValues: Array.isArray(s.enum) ? s.enum.map(show) : [],
      default: 'default' in s ? show(s.default) : undefined,
    }
  })
}

// paramLine (enum values only when the description doesn't already say them) builds "string · PNG or JPEG. Default: PNG." from a parameter.
export function paramLine(p: Param): string {
  const parts = [p.type]
  const text: string[] = []
  if (p.description) text.push(p.description.replace(/\.$/, ''))
  if (p.enumValues.length && !p.description) text.push(p.enumValues.join(' or '))
  if (p.default !== undefined) text.push(`Default: ${p.default}`)
  if (text.length) parts.push(text.join('. ') + '.')
  return parts.join(' · ')
}
