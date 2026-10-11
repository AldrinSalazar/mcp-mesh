// Dialogs that more than one screen opens (create a key from Overview, Nodes,
// Mesh map or Keys; change your password from the account menu or Admins).
// AppShell mounts them once; screens only call these functions.
import { reactive } from 'vue'
import type { KeyKind } from './types'

export const dialogs = reactive({
  // createKey is open when kind is set; the kind is preselected.
  createKey: null as null | { kind: KeyKind },
  // password is open for the named admin; your own needs the current one.
  password: null as null | { username: string },
})

export function openCreateKey(kind: KeyKind = 'node'): void {
  dialogs.createKey = { kind }
}

export function openPassword(username: string): void {
  dialogs.password = { username }
}
