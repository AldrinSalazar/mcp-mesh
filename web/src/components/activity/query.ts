import type { AuditQuery, Outcome } from '@/lib/types'

export type Range = '1h' | '24h' | '7d' | '30d'

export const rangeOptions: { value: Range; label: string }[] = [
  { value: '1h', label: 'Last hour' },
  { value: '24h', label: 'Last 24 hours' },
  { value: '7d', label: 'Last 7 days' },
  { value: '30d', label: 'Last 30 days' },
]

const spans: Record<Range, number> = { '1h': 3_600_000, '24h': 86_400_000, '7d': 7 * 86_400_000, '30d': 30 * 86_400_000 }

export function sinceFor(range: Range, now: number = Date.now()): string {
  return new Date(now - spans[range]).toISOString()
}

export interface AuditFilter {
  range: Range
  client: string
  node: string
  mcp: string
  outcome: Outcome | ''
  tool: string
}

export const defaultFilter: AuditFilter = { range: '24h', client: '', node: '', mcp: '', outcome: '', tool: '' }

export const pageSize = 100

export function isFiltered(f: AuditFilter): boolean {
  return !!(f.client || f.node || f.mcp || f.outcome || f.tool)
}

// buildQuery maps the filter row to the API; empty fields are left out so the
// hub doesn't filter on them.
export function buildQuery(f: AuditFilter, beforeID = 0, now: number = Date.now()): AuditQuery {
  const q: AuditQuery = { since: sinceFor(f.range, now), limit: pageSize }
  if (f.client) q.client_id = f.client
  if (f.node) q.node = f.node
  if (f.mcp) q.mcp = f.mcp
  if (f.tool.trim()) q.tool = f.tool.trim()
  if (f.outcome) q.outcome = f.outcome
  if (beforeID > 0) q.before_id = beforeID
  return q
}
