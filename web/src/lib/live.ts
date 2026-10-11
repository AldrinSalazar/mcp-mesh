// Live hub state. One EventSource on /api/v1/events keeps nodes, tools,
// calls, stats, keys and admins current; every screen reads from here and
// never polls. The session (and whether auth is on) lives here too.
import { reactive, ref, onScopeDispose, getCurrentScope } from 'vue'
import { api, setCSRF, setUnauthorizedHandler } from './api'
import type { Admin, Call, Group, Info, Key, Node, PendingTool, Prompt, ResourceItem, Settings, Stats, Tool, ToolOverride } from './types'

export type Connection = 'connecting' | 'live' | 'reconnecting'

type Kind = 'nodes' | 'tools' | 'prompts' | 'resources' | 'calls' | 'stats' | 'keys' | 'admins' | 'groups' | 'approvals' | 'settings' | 'overrides'

const state = reactive({
  info: null as Info | null,
  // username is the signed-in admin; empty with auth off or signed out.
  username: '',
  authOff: false,
  signedIn: false,
  // expired is set when a request needed a session and had none.
  expired: false,
  connection: 'connecting' as Connection,
  nodes: [] as Node[],
  tools: [] as Tool[],
  prompts: [] as Prompt[],
  resources: [] as ResourceItem[],
  calls: [] as Call[],
  stats: null as Stats | null,
  keys: [] as Key[],
  admins: [] as Admin[],
  groups: [] as Group[],
  approvals: [] as PendingTool[],
  settings: null as Settings | null,
  overrides: [] as ToolOverride[],
  // loaded says which lists have arrived at least once, for loading states.
  loaded: {
    nodes: false, tools: false, prompts: false, resources: false, calls: false, stats: false, keys: false, admins: false,
    groups: false, approvals: false, settings: false, overrides: false,
  } as Record<Kind, boolean>,
})

// live is the state for screens to read. Its fields are readonly at the type
// level (change them only through the functions here); the lists keep plain
// array types so they pass to helpers without casts.
export const live: Readonly<typeof state> = state

let source: EventSource | null = null
let retry: ReturnType<typeof setTimeout> | undefined
// failures counts reconnects since the stream last opened, for the backoff.
let failures = 0

// lifecycle changes on every sign-in, sign-out and expiry. A request that
// started under an older one belongs to a session that is gone, so its
// response is dropped.
let lifecycle = 0

// Per-list ordering. ticket numbers every REST request and SSE write; a REST
// response is applied only if no SSE write (newer[k]) and no later REST
// response (applied[k]) has landed since its request started.
let ticket = 0
const newer = {} as Record<Kind, number>
const applied = {} as Record<Kind, number>

setUnauthorizedHandler(() => {
  if (state.signedIn && !state.authOff) expire()
})

function expire() {
  lifecycle++
  state.expired = true
  state.signedIn = false
  state.username = ''
  stop()
  // Nothing of the old session may show to whoever signs in next.
  setCSRF('')
  clearData()
}

// bootstrap loads hub info and the current session. It never throws; a hub
// that can't be reached leaves info null.
export async function bootstrap(): Promise<void> {
  try {
    state.info = await api.info()
  } catch {
    state.info = null
  }
  try {
    const s = await api.session()
    if (s && 'auth' in s) {
      state.authOff = true
      state.signedIn = true
      state.username = ''
    } else if (s) {
      setCSRF(s.csrf)
      state.signedIn = true
      state.username = s.username
    } else {
      state.signedIn = false
    }
  } catch {
    state.signedIn = false
  }
}

export async function signIn(username: string, password: string): Promise<void> {
  const s = await api.signIn(username, password)
  lifecycle++
  setCSRF(s.csrf)
  state.username = s.username
  state.signedIn = true
  state.expired = false
}

export async function signOut(): Promise<void> {
  lifecycle++
  stop()
  try {
    await api.signOut()
  } finally {
    setCSRF('')
    state.signedIn = false
    state.username = ''
    clearData()
  }
}

// signedOut records that the session ended on the hub's side (after changing
// your own password or deleting yourself) without calling the API.
export function signedOut(): void {
  lifecycle++
  stop()
  setCSRF('')
  state.signedIn = false
  state.username = ''
  clearData()
}

function clearData() {
  // Responses to requests already in flight must not refill the lists.
  for (const k of kinds) newer[k] = ++ticket
  state.nodes = []
  state.tools = []
  state.prompts = []
  state.resources = []
  state.calls = []
  state.stats = null
  state.keys = []
  state.admins = []
  state.groups = []
  state.approvals = []
  state.settings = null
  state.overrides = []
  for (const k of Object.keys(state.loaded) as Kind[]) state.loaded[k] = false
}

const kinds: Kind[] = ['nodes', 'tools', 'prompts', 'resources', 'calls', 'stats', 'keys', 'admins', 'groups', 'approvals', 'settings', 'overrides']

// start opens the events stream. Safe to call again; it keeps one stream.
export function start(): void {
  if (source || !state.signedIn) return
  clearTimeout(retry)
  state.connection = state.connection === 'live' ? 'reconnecting' : state.connection
  const es = new EventSource('/api/v1/events', { withCredentials: true })
  source = es
  es.onopen = () => {
    failures = 0
    state.connection = 'live'
  }
  for (const k of kinds) {
    es.addEventListener(k, (ev) => {
      try {
        const data = JSON.parse((ev as MessageEvent<string>).data)
        ;(state as Record<Kind, unknown>)[k] = data
        newer[k] = ++ticket
        state.loaded[k] = true
      } catch {
        // A malformed event is dropped; the next snapshot replaces it.
      }
    })
  }
  es.onerror = () => {
    if (source !== es) return
    state.connection = 'reconnecting'
    // EventSource retries by itself after network errors, but gives up
    // (CLOSED) on an HTTP error such as 401. Find out which.
    if (es.readyState === EventSource.CLOSED) {
      es.close()
      source = null
      void recover()
    }
  }
}

async function recover() {
  const at = lifecycle
  try {
    const s = await api.session()
    if (!s) {
      if (at === lifecycle) expire()
      return
    }
  } catch {
    // Hub unreachable: keep trying.
  }
  // Signed out or expired while asking: nothing to reconnect.
  if (at !== lifecycle) return
  // Back off up to 30s so a hub that is down isn't hammered. The stream
  // sends fresh snapshots on every connect, so nothing is refetched here.
  const wait = Math.min(1000 * 2 ** failures, 30000)
  failures++
  retry = setTimeout(start, wait)
}

export function stop(): void {
  clearTimeout(retry)
  source?.close()
  source = null
  failures = 0
  state.connection = 'connecting'
}

// refresh refetches one list now, for screens that just changed it and
// shouldn't wait for the next event.
export async function refresh(kind: 'keys' | 'admins' | 'prompts' | 'resources' | 'groups' | 'approvals' | 'nodes' | 'settings' | 'tools' | 'overrides'): Promise<void> {
  const at = lifecycle
  const mine = ++ticket
  try {
    const data = await fetchKind(kind)
    // Drop the response if the session changed, or the stream (or a newer
    // refresh) already delivered something newer than this request saw.
    if (at !== lifecycle || mine < newer[kind] || mine < applied[kind]) return
    applied[kind] = mine
    ;(state as Record<Kind, unknown>)[kind] = data
    state.loaded[kind] = true
  } catch {
    // The stream will catch up.
  }
}

function fetchKind(kind: Kind): Promise<unknown> {
  switch (kind) {
    case 'keys': return api.keys()
    case 'admins': return api.admins()
    case 'groups': return api.groups()
    case 'approvals': return api.approvals()
    case 'nodes': return api.nodes()
    case 'tools': return api.tools()
    case 'prompts': return api.prompts()
    case 'resources': return api.resources()
    case 'overrides': return api.overrides()
    case 'settings': return api.settings()
    default: return Promise.reject(new Error(`no REST list for ${kind}`))
  }
}

// useNow is a clock that ticks every second while the calling component is
// mounted, for relative times ("3m ago") that must stay current.
export function useNow(every = 1000) {
  const now = ref(Date.now())
  const t = setInterval(() => (now.value = Date.now()), every)
  if (getCurrentScope()) onScopeDispose(() => clearInterval(t))
  return now
}

// hubName is how the hub is called on screen: its network name, else host.
export function hubName(): string {
  return state.info?.discovery_name || location.hostname
}
