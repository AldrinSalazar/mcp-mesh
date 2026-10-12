// Approving a machine that asked to join: the name it gets, and whether
// that name already belongs to a node key.
import type { Key } from '@/lib/types'

// nameError applies the hub's rules for a node key's name, so a bad name
// is caught before the request is sent.
export function nameError(name: string): string {
  if (!name) return 'Enter a name.'
  if (new TextEncoder().encode(name).length > 64) return 'Use at most 64 characters.'
  if (/[\s\p{Cc}]/u.test(name)) return 'Use a name without spaces.'
  return ''
}

// nameTaken says whether a node key is bound to name: approving under it
// replaces that key and disconnects its node.
export function nameTaken(name: string, keys: readonly Pick<Key, 'kind' | 'name'>[]): boolean {
  return keys.some((k) => k.kind === 'node' && k.name === name)
}
