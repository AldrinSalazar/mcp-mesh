import type { Prompt, ResourceItem } from '@/lib/types'

export type Kind = 'tools' | 'prompts' | 'resources'

// parseKind reads ?kind=; anything unknown is the tools tab.
export function parseKind(v: string): Kind {
  return v === 'prompts' || v === 'resources' ? v : 'tools'
}

export interface ItemFilter {
  q: string
  node: string
  mcp: string
}

function match(f: ItemFilter, node: string, mcp: string, fields: (string | undefined)[]): boolean {
  if (f.node && node !== f.node) return false
  if (f.mcp && mcp !== f.mcp) return false
  const q = f.q.trim().toLowerCase()
  return !q || [...fields, node, mcp].some((s) => (s ?? '').toLowerCase().includes(q))
}

export function filterPrompts(list: readonly Prompt[], f: ItemFilter): Prompt[] {
  return list.filter((p) => match(f, p.node, p.mcp, [p.name, p.original_name, p.description]))
}

export function filterResources(list: readonly ResourceItem[], f: ItemFilter): ResourceItem[] {
  return list.filter((r) => match(f, r.node, r.mcp, [r.uri, r.original_uri, r.name, r.description, r.mime_type]))
}
