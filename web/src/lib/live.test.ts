import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApprovalRef, PendingTool } from './types'

// FakeEventSource stands in for the browser's: tests push events and errors.
class FakeEventSource {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSED = 2
  static all: FakeEventSource[] = []
  readyState = FakeEventSource.CONNECTING
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  closed = false
  private listeners = new Map<string, ((ev: MessageEvent<string>) => void)[]>()
  constructor(readonly url: string) {
    FakeEventSource.all.push(this)
  }
  addEventListener(type: string, fn: (ev: MessageEvent<string>) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn])
  }
  close() {
    this.closed = true
    this.readyState = FakeEventSource.CLOSED
  }
  open() {
    this.readyState = FakeEventSource.OPEN
    this.onopen?.()
  }
  emit(type: string, data: unknown) {
    for (const fn of this.listeners.get(type) ?? []) fn(new MessageEvent(type, { data: JSON.stringify(data) }))
  }
  // fail: a network error (the browser retries itself) or, with
  // closed=true, an HTTP error after which it gives up.
  fail(closed: boolean) {
    this.readyState = closed ? FakeEventSource.CLOSED : FakeEventSource.CONNECTING
    this.onerror?.()
  }
}

// json answers with a body that can be read any number of times, so one
// mocked response can serve repeated calls.
const json = (body: unknown, status = 200) =>
  ({ ok: status < 400, status, statusText: '', headers: new Headers(), json: async () => body }) as Response

// deferred makes a fetch response the test completes by hand.
function deferred() {
  let resolve!: (r: Response) => void
  const promise = new Promise<Response>((r) => (resolve = r))
  return { promise, resolve }
}

const session = { username: 'ann', csrf: 'tok-1' }

// Each test gets fresh module state (live.ts and api.ts keep module-level
// session data).
async function load() {
  vi.resetModules()
  const live = await import('./live')
  const api = await import('./api')
  return { ...live, ...api }
}

const csrfSent = (fetchMock: ReturnType<typeof vi.fn>) =>
  (fetchMock.mock.calls.at(-1)![1].headers as Record<string, string>)['X-CSRF-Token']

beforeEach(() => {
  FakeEventSource.all = []
  vi.stubGlobal('EventSource', FakeEventSource)
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('event stream reconnect', () => {
  it('goes live on open, reconnecting on a network error, live again when the browser retries', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    m.start() // a second call keeps one stream
    expect(FakeEventSource.all).toHaveLength(1)
    const es = FakeEventSource.all[0]!
    es.open()
    expect(m.live.connection).toBe('live')
    es.fail(false)
    expect(m.live.connection).toBe('reconnecting')
    expect(es.closed).toBe(false)
    es.open()
    expect(m.live.connection).toBe('live')
    m.stop()
  })

  it('after the browser gives up it checks the session, then reconnects with backoff', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    FakeEventSource.all[0]!.fail(true)
    await vi.advanceTimersByTimeAsync(0)
    expect(m.live.connection).toBe('reconnecting')
    expect(FakeEventSource.all).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(999)
    expect(FakeEventSource.all).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeEventSource.all).toHaveLength(2)
    // Fails again without ever opening: the wait doubles.
    FakeEventSource.all[1]!.fail(true)
    await vi.advanceTimersByTimeAsync(1999)
    expect(FakeEventSource.all).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    expect(FakeEventSource.all).toHaveLength(3)
    // Once open, the backoff starts over.
    FakeEventSource.all[2]!.open()
    FakeEventSource.all[2]!.fail(true)
    await vi.advanceTimersByTimeAsync(1000)
    expect(FakeEventSource.all).toHaveLength(4)
    m.stop()
  })

  it('a reconnect whose session is gone expires instead of retrying', async () => {
    vi.useFakeTimers()
    const fetchMock = vi.fn().mockResolvedValueOnce(json(session)).mockResolvedValue(json({ error: 'no' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    FakeEventSource.all[0]!.emit('keys', [{ id: 'k1' }])
    FakeEventSource.all[0]!.fail(true)
    await vi.advanceTimersByTimeAsync(60000)
    expect(m.live.expired).toBe(true)
    expect(m.live.keys).toEqual([])
    expect(FakeEventSource.all).toHaveLength(1)
  })

  it('signing out while the session check is in flight does not reconnect', async () => {
    vi.useFakeTimers()
    const check = deferred()
    const fetchMock = vi.fn().mockResolvedValueOnce(json(session)).mockReturnValueOnce(check.promise)
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    FakeEventSource.all[0]!.fail(true)
    m.signedOut()
    check.resolve(json(session))
    await vi.advanceTimersByTimeAsync(60000)
    expect(FakeEventSource.all).toHaveLength(1)
  })
})

describe('session expiry', () => {
  it('clears cached lists and the CSRF token', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    const es = FakeEventSource.all[0]!
    es.emit('nodes', [{ name: 'n1' }])
    es.emit('keys', [{ id: 'k1' }])
    es.emit('stats', { calls: 1 })
    expect(m.live.nodes).toHaveLength(1)
    expect(m.live.loaded.nodes).toBe(true)

    // Any request that gets 401 while signed in expires the session.
    fetchMock.mockResolvedValue(json({ error: 'expired' }, 401))
    await m.api.nodes().catch(() => {})

    expect(m.live.expired).toBe(true)
    expect(m.live.signedIn).toBe(false)
    expect(m.live.username).toBe('')
    expect(m.live.nodes).toEqual([])
    expect(m.live.keys).toEqual([])
    expect(m.live.stats).toBeNull()
    expect(Object.values(m.live.loaded).every((v) => !v)).toBe(true)
    expect(es.closed).toBe(true)

    fetchMock.mockResolvedValue(json({}))
    await m.api.createGroup({ name: 'g', description: '', grants: [] })
    expect(csrfSent(fetchMock)).toBeUndefined()
  })

  it('signing out clears the lists and the CSRF token', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    FakeEventSource.all[0]!.emit('groups', [{ id: 'g1' }])
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await m.signOut()
    expect(m.live.groups).toEqual([])
    expect(m.live.loaded.groups).toBe(false)
    fetchMock.mockResolvedValue(json({}))
    await m.api.createGroup({ name: 'g', description: '', grants: [] })
    expect(csrfSent(fetchMock)).toBeUndefined()
  })
})

describe('stale responses', () => {
  it('a request that finishes after sign-out is ignored', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    const slow = deferred()
    fetchMock.mockReturnValueOnce(slow.promise)
    const p = m.refresh('keys')
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await m.signOut()
    slow.resolve(json([{ id: 'old' }]))
    await p
    expect(m.live.keys).toEqual([])
    expect(m.live.loaded.keys).toBe(false)
  })

  it('a request that finishes after another sign-in is ignored', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    const slow = deferred()
    fetchMock.mockReturnValueOnce(slow.promise)
    const p = m.refresh('keys')
    m.signedOut()
    fetchMock.mockResolvedValue(json({ username: 'bob', csrf: 'tok-2' }))
    await m.signIn('bob', 'pw')
    slow.resolve(json([{ id: 'ann-key' }]))
    await p
    expect(m.live.keys).toEqual([])
    expect(m.live.username).toBe('bob')
  })

  it('a request that finishes after expiry is ignored', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    const slow = deferred()
    fetchMock.mockReturnValueOnce(slow.promise)
    const p = m.refresh('nodes')
    fetchMock.mockResolvedValue(json({ error: 'expired' }, 401))
    await m.api.tools().catch(() => {})
    slow.resolve(json([{ name: 'old' }]))
    await p
    expect(m.live.expired).toBe(true)
    expect(m.live.nodes).toEqual([])
  })

  it('a REST refresh that lands after a newer stream update does not overwrite it', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    const slow = deferred()
    fetchMock.mockReturnValueOnce(slow.promise)
    const p = m.refresh('keys')
    FakeEventSource.all[0]!.emit('keys', [{ id: 'new' }])
    slow.resolve(json([{ id: 'old' }]))
    await p
    expect(m.live.keys).toEqual([{ id: 'new' }])
    m.stop()
  })

  it('a refresh with no newer write is applied, and a write to another list does not block it', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    m.start()
    const slow = deferred()
    fetchMock.mockReturnValueOnce(slow.promise)
    const p = m.refresh('keys')
    FakeEventSource.all[0]!.emit('nodes', [{ name: 'n' }])
    slow.resolve(json([{ id: 'k' }]))
    await p
    expect(m.live.keys).toEqual([{ id: 'k' }])
    m.stop()
  })

  it('of two refreshes, the one started later wins even if it lands first', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    await m.signIn('ann', 'pw')
    const first = deferred()
    const second = deferred()
    fetchMock.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const p1 = m.refresh('groups')
    const p2 = m.refresh('groups')
    second.resolve(json([{ id: 'second' }]))
    await p2
    first.resolve(json([{ id: 'first' }]))
    await p1
    expect(m.live.groups).toEqual([{ id: 'second' }])
  })
})

describe('approval conflict recovery', () => {
  const pending = (hash: string): PendingTool => ({
    node: 'n', mcp: 'a', tool: 't1', name: 'n__a__t1', state: 'changed', description: '', input_schema: {}, hash, definition: {}, since: '',
  })

  it('a 409 reloads the approvals so the list shows the new definition, and tells the admin', async () => {
    const fetchMock = vi.fn().mockResolvedValue(json(session))
    vi.stubGlobal('fetch', fetchMock)
    const m = await load()
    const { reportApproveError } = await import('@/components/approvals/approve')
    await m.signIn('ann', 'pw')
    m.start()
    FakeEventSource.all[0]!.emit('approvals', [pending('h1')])

    const shown: ApprovalRef = { node: 'n', mcp: 'a', tool: 't1', hash: 'h1' }
    fetchMock.mockResolvedValueOnce(json({ error: 'changed', stale: [shown] }, 409))
    const err = await m.api.approve([shown]).catch((e: unknown) => e)
    expect(JSON.parse(fetchMock.mock.calls.at(-1)![1].body)).toEqual({ tools: [shown] })

    // The hub now has a new definition; the reload fetches it.
    fetchMock.mockResolvedValueOnce(json([pending('h2')]))
    const toast = vi.fn()
    await reportApproveError(err, { toast, refresh: m.refresh })

    expect(toast.mock.calls[0]![0]).toMatch(/changed since/)
    expect(toast.mock.calls[0]![1]).toContain('a/t1')
    expect(m.live.approvals.map((t) => t.hash)).toEqual(['h2'])
    expect(m.live.expired).toBe(false)
    m.stop()
  })
})
