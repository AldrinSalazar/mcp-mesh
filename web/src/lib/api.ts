// The hub's JSON API. Every call goes through request(), which adds the CSRF
// token to state-changing requests and turns error bodies into ApiError.
import type {
  Admin, AdminLogEntry, ApprovalRef, AuditPage, AuditQuery, Call, CreatedKey, Grant, Group, Info, Key, KeyKind, Node,
  PairingRequest, PendingTool, Prompt, ResourceItem, Session, Settings, Stats, Tool, ToolOverride, TrustLevel,
} from './types'

const base = '/api/v1'

export class ApiError extends Error {
  readonly status: number
  // retryAfter is the Retry-After header in seconds (sign-in rate limiting).
  readonly retryAfter?: number
  // stale: on a 409 from an approval, the tools whose definition changed
  // since the admin saw them.
  readonly stale: ApprovalRef[]
  constructor(status: number, message: string, retryAfter?: number, stale: ApprovalRef[] = []) {
    super(message)
    this.status = status
    this.retryAfter = retryAfter
    this.stale = stale
  }
}

let csrf = ''
let onUnauthorized: (() => void) | null = null

export function setCSRF(token: string) {
  csrf = token
}

// setUnauthorizedHandler is called when a request that needed a session gets
// 401 (expired, signed out elsewhere, or the password changed).
export function setUnauthorizedHandler(fn: () => void) {
  onUnauthorized = fn
}

async function request<T>(method: string, path: string, body?: unknown, opts: { quiet401?: boolean } = {}): Promise<T> {
  const headers: Record<string, string> = { Accept: 'application/json' }
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf
  let res: Response
  try {
    res = await fetch(base + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      credentials: 'same-origin',
    })
  } catch {
    throw new ApiError(0, "Can't reach the hub.")
  }
  if (res.status === 204) return undefined as T
  let data: unknown = null
  try {
    data = await res.json()
  } catch {
    // Not JSON (a proxy error page, for example); handled below.
  }
  if (!res.ok) {
    const msg = (data as { error?: string } | null)?.error ?? `${res.status} ${res.statusText}`
    if (res.status === 401 && !opts.quiet401) onUnauthorized?.()
    const ra = Number(res.headers.get('Retry-After'))
    const stale = res.status === 409 ? (data as { stale?: ApprovalRef[] } | null)?.stale : undefined
    throw new ApiError(res.status, msg, Number.isFinite(ra) && ra > 0 ? ra : undefined, Array.isArray(stale) ? stale : [])
  }
  return data as T
}

function overridePath(t: { node: string; mcp: string; original_name: string }): string {
  return `/overrides/${[t.node, t.mcp, t.original_name].map(encodeURIComponent).join('/')}`
}

function query(q: object): string {
  const p = new URLSearchParams()
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== '' && v !== null) p.set(k, String(v))
  }
  const s = p.toString()
  return s ? `?${s}` : ''
}

export const api = {
  info: () => request<Info>('GET', '/info'),

  // session returns null when not signed in, {auth:false} when auth is off.
  session: async (): Promise<Session | { auth: false } | null> => {
    try {
      return await request<Session | { auth: false }>('GET', '/session', undefined, { quiet401: true })
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return null
      throw e
    }
  },
  signIn: (username: string, password: string) =>
    request<Session>('POST', '/session', { username, password }, { quiet401: true }),
  signOut: () => request<void>('DELETE', '/session', undefined, { quiet401: true }),

  nodes: () => request<Node[]>('GET', '/nodes'),
  tools: () => request<Tool[]>('GET', '/tools'),
  prompts: () => request<Prompt[]>('GET', '/prompts'),
  resources: () => request<ResourceItem[]>('GET', '/resources'),
  overrides: () => request<ToolOverride[]>('GET', '/overrides'),
  // Both fields empty clears the override (the hub answers 204).
  setOverride: (t: { node: string; mcp: string; original_name: string }, alias: string, description: string) =>
    request<ToolOverride | undefined>('PUT', overridePath(t), { alias, description }),
  calls: () => request<Call[]>('GET', '/calls'),
  stats: () => request<Stats>('GET', '/stats'),
  audit: (q: AuditQuery) => request<AuditPage>('GET', '/audit' + query(q)),

  keys: () => request<Key[]>('GET', '/keys'),
  // groups: client keys (omit for the default, Everything); trust: node keys
  // (omit for the hub default).
  createKey: (kind: KeyKind, name: string, opts: { groups?: string[]; trust?: TrustLevel } = {}) =>
    request<CreatedKey>('POST', '/keys', { kind, name, ...opts }),
  setKeyGroups: (id: string, groups: string[]) =>
    request<{ key: Key }>('PUT', `/keys/${encodeURIComponent(id)}/groups`, { groups }),
  keyTools: (id: string) => request<Tool[]>('GET', `/keys/${encodeURIComponent(id)}/tools`),

  groups: () => request<Group[]>('GET', '/groups'),
  createGroup: (g: { name: string; description: string; grants: Grant[] }) => request<Group>('POST', '/groups', g),
  updateGroup: (id: string, g: { name: string; description: string; grants: Grant[] }) =>
    request<Group>('PUT', `/groups/${encodeURIComponent(id)}`, g),
  deleteGroup: (id: string) => request<void>('DELETE', `/groups/${encodeURIComponent(id)}`),

  approvals: () => request<PendingTool[]>('GET', '/approvals'),
  // approve: exactly the listed tools, each with the hash of the definition
  // the admin was shown. 409 (ApiError.stale) when any changed since.
  approve: (tools: ApprovalRef[]) => request<{ approved: number }>('POST', '/approvals', { tools }),
  // setNodeTrust(null) makes the node follow the hub default again.
  setNodeTrust: (node: string, level: TrustLevel | null) =>
    request<Node['trust']>('PUT', `/nodes/${encodeURIComponent(node)}/trust`, { level }),
  // approveNode approves the node and the listed tools; tools not listed
  // (an empty list approves only the node) stay pending.
  approveNode: (node: string, tools: Omit<ApprovalRef, 'node'>[]) =>
    request<Node['trust']>('POST', `/nodes/${encodeURIComponent(node)}/approve`, { tools }),

  pairings: () => request<PairingRequest[]>('GET', '/pairings'),
  // approvePairing creates a node key and hands it to the waiting machine.
  // name: another name than it asked for; replace: revoke the node key bound
  // to that name (409 without it). 410 when the machine left meanwhile.
  approvePairing: (id: string, opts: { name?: string; replace?: boolean } = {}) =>
    request<{ key: Key }>('POST', `/pairings/${encodeURIComponent(id)}/approve`, opts),
  denyPairing: (id: string) => request<void>('DELETE', `/pairings/${encodeURIComponent(id)}`),

  settings: () => request<Settings>('GET', '/settings'),
  setTrustDefault: (level: TrustLevel) => request<Settings>('PUT', '/settings', { trust_default: level }),
  adminLog: (q: { before_id?: number; limit?: number } = {}) =>
    request<{ entries: AdminLogEntry[]; next_before_id: number }>('GET', '/admin-log' + query(q)),
  revokeKey: (id: string) => request<{ key: Key }>('DELETE', `/keys/${encodeURIComponent(id)}`),

  admins: () => request<Admin[]>('GET', '/admins'),
  createAdmin: (username: string, password: string) => request<Admin>('POST', '/admins', { username, password }),
  setPassword: (username: string, password: string, currentPassword?: string) =>
    request<void>('PUT', `/admins/${encodeURIComponent(username)}/password`,
      currentPassword === undefined ? { password } : { password, current_password: currentPassword }),
  deleteAdmin: (username: string) => request<void>('DELETE', `/admins/${encodeURIComponent(username)}`),
}
