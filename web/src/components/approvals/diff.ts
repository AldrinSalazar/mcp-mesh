// Pure diffs for the Approvals screen: words for descriptions, lines for
// pretty-printed schemas. Both use a longest-common-subsequence table; the
// inputs are a few hundred tokens at most, so O(n*m) is fine.

export type Op = 'same' | 'add' | 'del'
export interface Part { op: Op; text: string }

// lcs returns the diff as (op, index into a or b) steps.
function lcs(a: string[], b: string[]): { op: Op; i: number; j: number }[] {
  const n = a.length
  const m = b.length
  const t: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0))
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      t[i]![j] = a[i] === b[j] ? t[i + 1]![j + 1]! + 1 : Math.max(t[i + 1]![j]!, t[i]![j + 1]!)
    }
  }
  const out: { op: Op; i: number; j: number }[] = []
  let i = 0
  let j = 0
  while (i < n && j < m) {
    if (a[i] === b[j]) { out.push({ op: 'same', i, j }); i++; j++ }
    else if (t[i + 1]![j]! >= t[i]![j + 1]!) { out.push({ op: 'del', i, j }); i++ }
    else { out.push({ op: 'add', i, j }); j++ }
  }
  while (i < n) { out.push({ op: 'del', i, j }); i++ }
  while (j < m) { out.push({ op: 'add', i, j }); j++ }
  return out
}

// Words keep their trailing whitespace so joining the parts restores the text.
function words(s: string): string[] {
  return s.match(/\S+\s*|\s+/g) ?? []
}

function merge(parts: Part[]): Part[] {
  const out: Part[] = []
  for (const p of parts) {
    const last = out[out.length - 1]
    if (last && last.op === p.op) last.text += p.text
    else out.push({ ...p })
  }
  return out
}

// diffWords compares by word ignoring whitespace differences, so a reflowed
// sentence is not reported as changed.
export function diffWords(before: string, after: string): Part[] {
  const a = words(before)
  const b = words(after)
  const steps = lcs(a.map((w) => w.trim()), b.map((w) => w.trim()))
  return merge(
    steps.map((s) => (s.op === 'del' ? { op: s.op, text: a[s.i]! } : { op: s.op, text: b[s.j]! })),
  )
}

export function diffLines(before: string, after: string): Part[] {
  const split = (s: string) => (s === '' ? [] : s.split('\n'))
  const a = split(before)
  const b = split(after)
  return lcs(a, b).map((s) => (s.op === 'del' ? { op: s.op, text: a[s.i]! } : { op: s.op, text: b[s.j]! }))
}

export function pretty(v: unknown): string {
  return JSON.stringify(v ?? {}, null, 2)
}

export function changed(parts: readonly { op: Op }[]): boolean {
  return parts.some((p) => p.op !== 'same')
}

export function diffSchema(before: unknown, after: unknown): Part[] {
  return diffLines(pretty(before), pretty(after))
}

// Fields shown on their own (or that identify the tool), so the generic
// field diff skips them.
const handledFields = new Set(['name', 'description', 'inputSchema'])

// canonical pretty-prints with sorted keys so a reordered object is not a change.
function canonical(v: unknown): string {
  const sorted = (x: unknown): unknown => {
    if (Array.isArray(x)) return x.map(sorted)
    if (x && typeof x === 'object') {
      return Object.fromEntries(Object.keys(x).sort().map((k) => [k, sorted((x as Record<string, unknown>)[k])]))
    }
    return x
  }
  return JSON.stringify(sorted(v), null, 2)
}

// Labels for the fields MCP defines; anything else shows its key.
const fieldLabels: Record<string, string> = {
  title: 'Title', annotations: 'Annotations', outputSchema: 'Output schema', icons: 'Icons', _meta: 'Metadata (_meta)', execution: 'Execution',
}

// fieldLabel names a definition field for a heading.
export function fieldLabel(key: string): string {
  return fieldLabels[key] ?? key
}

export interface FieldDiff {
  key: string
  status: 'added' | 'removed' | 'changed'
  lines: Part[]
}

// diffFields compares every field of two tool definitions except name,
// description and inputSchema. Unchanged fields are left out.
export function diffFields(before: Record<string, unknown> | undefined, after: Record<string, unknown> | undefined): FieldDiff[] {
  const a = before ?? {}
  const b = after ?? {}
  const keys = [...new Set([...Object.keys(a), ...Object.keys(b)])].filter((k) => !handledFields.has(k)).sort()
  const out: FieldDiff[] = []
  for (const key of keys) {
    const inA = a[key] !== undefined
    const inB = b[key] !== undefined
    if (!inA && !inB) continue
    const x = inA ? canonical(a[key]) : ''
    const y = inB ? canonical(b[key]) : ''
    if (inA && inB && x === y) continue
    out.push({ key, status: !inA ? 'added' : !inB ? 'removed' : 'changed', lines: diffLines(x, y) })
  }
  return out
}

export interface FieldValue { key: string; text: string }

// otherFields lists every field except name, description and inputSchema, for
// a tool that has nothing to be compared with.
export function otherFields(def: Record<string, unknown> | undefined): FieldValue[] {
  const d = def ?? {}
  return Object.keys(d)
    .filter((k) => !handledFields.has(k) && d[k] !== undefined)
    .sort()
    .map((key) => ({ key, text: canonical(d[key]) }))
}
