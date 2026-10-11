// What the Approvals screen sends and how it reacts when the hub refuses.
// Approvals carry the hash of the definition the admin was shown, so only
// tools as currently rendered are ever listed.
import type { ApprovalRef, PendingTool } from '@/lib/types'
import { ApiError } from '@/lib/api'
import type { NodeGroup } from './group'

export const refOf = (t: PendingTool): ApprovalRef => ({ node: t.node, mcp: t.mcp, tool: t.tool, hash: t.hash })

export const mcpRefs = (g: NodeGroup, mcp: string): ApprovalRef[] =>
  (g.mcps.find((m) => m.mcp === mcp)?.tools ?? []).map(refOf)

export const nodeRefs = (g: NodeGroup): ApprovalRef[] => g.mcps.flatMap((m) => m.tools.map(refOf))

export interface ReportDeps {
  toast: (title: string, body?: string, tone?: 'ok' | 'err', ms?: number) => void
  refresh: (kind: 'approvals') => Promise<void>
}

// reportApproveError tells the admin why an approval failed. After a 409 it
// also reloads the approvals (the events feed usually has already), so the
// list shows the new definitions.
export async function reportApproveError(e: unknown, deps: ReportDeps): Promise<void> {
  const stale = staleToast(e)
  if (stale) {
    deps.toast(stale.title, stale.body, 'err', 8000)
    await deps.refresh('approvals').catch(() => {})
    return
  }
  deps.toast("Couldn't approve", e instanceof Error ? e.message : undefined, 'err')
}

// staleToast describes a 409: the tools changed after the admin loaded them.
// It returns null for any other error.
export function staleToast(e: unknown): { title: string; body: string } | null {
  if (!(e instanceof ApiError) || e.status !== 409) return null
  const names = e.stale.map((s) => `${s.mcp}/${s.tool}`)
  const what = names.length === 0 ? 'Some tools' : names.length <= 3 ? names.join(', ') : `${names.length} tools`
  return {
    title: 'Tools changed since you loaded them',
    body: `${what} changed before the approval went through. Review them again.`,
  }
}
