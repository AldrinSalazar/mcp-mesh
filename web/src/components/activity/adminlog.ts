// How the admin log reads on screen: action codes become sentences, targets
// resolve key IDs to names.
import type { AdminLogEntry, Key } from '@/lib/types'

const actions: Record<string, string> = {
  sign_in: 'Signed in',
  sign_in_failed: 'Failed sign-in',
  sign_out: 'Signed out',
  'key.create': 'Created a key',
  'key.revoke': 'Revoked a key',
  'key.groups': "Changed a key's groups",
  'admin.create': 'Added an admin',
  'admin.delete': 'Removed an admin',
  'admin.password': 'Changed a password',
  'group.create': 'Created a group',
  'group.update': 'Edited a group',
  'group.delete': 'Deleted a group',
  'trust.level': 'Changed a trust level',
  'node.approve': 'Approved a node',
  'node.forget': 'Forgot a node',
  'tool.approve': 'Approved tools',
  'oauth.authorize': 'Authorized an app',
  'oauth.denied': 'Denied an app',
  'pairing.approve': 'Approved a machine',
  'pairing.deny': 'Declined a machine',
}

export function actionLabel(action: string): string {
  return actions[action] ?? action
}

export function actorLabel(actor: string): string {
  if (actor === 'cli') return 'command line'
  return actor || '—'
}

export function isFailure(e: Pick<AdminLogEntry, 'action'>): boolean {
  return e.action === 'sign_in_failed'
}

export interface TargetView { text: string; mono: boolean }

// targetView resolves the target for display. Key IDs become key names when
// the Keys list knows them (revoked keys keep their ID, which stays mono).
export function targetView(
  e: Pick<AdminLogEntry, 'action' | 'target' | 'detail'>,
  keys: readonly Pick<Key, 'id' | 'name'>[],
): TargetView {
  const detail = e.detail
  // An approved pairing's target is the key it created.
  if (e.action.startsWith('key.') || e.action === 'pairing.approve') {
    const k = keys.find((x) => x.id === e.target)
    if (k) return { text: [k.name, detail].filter(Boolean).join(' · '), mono: false }
    return { text: [e.target, detail].filter(Boolean).join(' · '), mono: true }
  }
  if (!e.target) return { text: detail || '—', mono: false }
  return { text: detail ? `${e.target} · ${detail}` : e.target, mono: false }
}
