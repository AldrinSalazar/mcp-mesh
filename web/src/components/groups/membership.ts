// Which client keys' group lists change when a group's members are edited.
import type { Key } from '@/lib/types'

export interface MembershipChange { key: Key; groups: string[] }

// membershipChanges returns, for every client key whose membership of group
// `id` differs from `members`, its new full group list (each key's other
// groups are kept).
export function membershipChanges(keys: Key[], id: string, members: ReadonlySet<string>): MembershipChange[] {
  const out: MembershipChange[] = []
  for (const key of keys) {
    if (key.kind !== 'client') continue
    const groups = key.groups ?? []
    const has = groups.includes(id)
    const want = members.has(key.id)
    if (has === want) continue
    out.push({ key, groups: want ? [...groups, id] : groups.filter((g) => g !== id) })
  }
  return out
}

// toggled returns groups with `id` added or removed.
export function toggled(groups: string[], id: string, on: boolean): string[] {
  const rest = groups.filter((g) => g !== id)
  return on ? [...rest, id] : rest
}
