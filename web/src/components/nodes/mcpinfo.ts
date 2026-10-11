import { plural } from '@/lib/format'
import type { MCP } from '@/lib/types'

// timeoutLabel writes a call timeout in whole units: 600000 → "10m",
// 90000 → "1m 30s", 1500 → "1.5s". 0 (the hub default) gives "".
export function timeoutLabel(ms: number): string {
  if (!(ms > 0)) return ''
  if (ms < 1000) return `${Math.round(ms)}ms`
  const total = ms / 1000
  if (total < 60) return `${Number.isInteger(total) ? total : total.toFixed(1).replace(/\.0$/, '')}s`
  const s = Math.round(total)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  return [h && `${h}h`, m && `${m}m`, sec && `${sec}s`].filter(Boolean).join(' ')
}

export function isPerClient(m: Pick<MCP, 'sharing'>): boolean {
  return m.sharing === 'per-client'
}

// clientsLabel is the connection note of a per-client MCP.
export function clientsLabel(m: Pick<MCP, 'clients'>): string {
  return m.clients > 0 ? `Per-client · ${plural(m.clients, 'client')}` : 'Per-client · no clients yet'
}

// offersLabel is the compact tools cell: "12 · 2 prompts · 1 resource".
export function offersLabel(m: Pick<MCP, 'tools' | 'prompts' | 'resources'>): string {
  return [String(m.tools), m.prompts > 0 && plural(m.prompts, 'prompt'), m.resources > 0 && plural(m.resources, 'resource')]
    .filter(Boolean)
    .join(' · ')
}

// problemsLabel is the badge text for definitions the hub dropped from an
// MCP's listing; "" when there are none.
export function problemsLabel(m: Pick<MCP, 'problems'>): string {
  const n = m.problems?.length ?? 0
  return n > 0 ? `${plural(n, 'definition')} dropped` : ''
}
