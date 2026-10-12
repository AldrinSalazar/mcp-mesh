// A fake /api/v1 for `npm run dev:mock` (MOCK=1 vite): fixture data, a live
// events stream that adds a call every few seconds, and working key, admin
// and session endpoints kept in memory. MOCK=empty starts a fresh hub (no
// nodes, keys or calls); MOCK=authoff runs with auth off.
import type { IncomingMessage, ServerResponse } from 'node:http'
import type { Plugin } from 'vite'
import * as fx from './fixtures.ts'
import type { AdminLogEntry, ApprovalRef, AuditEntry, Group, Key, Node, PairingRequest, PendingTool, Settings, Tool, ToolOverride, TrustLevel } from '../src/lib/types.ts'

export function mockApi(mode: string): Plugin {
  const empty = mode === 'empty'
  const authOn = mode !== 'authoff'
  const st = {
    nodes: (empty ? [] : structuredClone(fx.nodes)) as Node[],
    tools: empty ? [] : structuredClone(fx.tools),
    prompts: empty ? [] : structuredClone(fx.prompts),
    resources: empty ? [] : structuredClone(fx.resources),
    audit: (empty ? [] : structuredClone(fx.audit)) as AuditEntry[],
    keys: (empty ? [] : structuredClone(fx.keys)) as Key[],
    admins: structuredClone(fx.admins),
    groups: (empty ? structuredClone(fx.groups.slice(0, 1)).map((g) => ({ ...g, keys: [] })) : structuredClone(fx.groups)) as Group[],
    approvals: (empty ? [] : structuredClone(fx.approvals)) as PendingTool[],
    pairings: (empty ? [] : structuredClone(fx.pairings)) as PairingRequest[],
    settings: structuredClone(fx.settings) as Settings,
    overrides: (empty ? [] : structuredClone(fx.overrides)) as ToolOverride[],
    adminLog: (empty ? [] : structuredClone(fx.adminLog)) as AdminLogEntry[],
    session: '' as string, // username; empty = signed out
    failures: 0,
  }
  const csrf = 'mock-csrf-token'
  // Up for an hour: its nodes aren't reconnecting from a restart.
  const startedAt = new Date(Date.now() - 3_600_000).toISOString()
  const subs = new Set<(type: string) => void>()
  const emit = (type: string) => subs.forEach((f) => f(type))

  const calls = () => st.audit.slice(0, 50).map(({ id: _id, ...c }) => c)
  const stats = () => {
    const since = Date.now() - 3_600_000
    const recent = st.audit.filter((e) => Date.parse(e.time) >= since)
    return { calls_last_hour: recent.length, errors_last_hour: recent.filter((e) => e.outcome !== 'ok').length }
  }
  // A request's name is taken while a node key is bound to it.
  const pairingViews = () => st.pairings.map((p) => ({ ...p, name_taken: st.keys.some((k) => k.kind === 'node' && k.name === p.node) }))
  const keyViews = () => st.keys.map((k) => (k.kind === 'client' ? { ...k, groups: st.groups.filter((g) => g.keys.includes(k.id)).map((g) => g.id) } : k))
  const snapshot = (type: string): unknown =>
    ({
      nodes: st.nodes, tools: st.tools, prompts: st.prompts, resources: st.resources, calls: calls(), stats: stats(), keys: keyViews(), admins: st.admins,
      groups: st.groups, approvals: st.approvals, settings: st.settings, overrides: st.overrides, pairings: pairingViews(), update: fx.update,
    })[type]
  const log = (action: string, target: string, detail = '') => {
    st.adminLog.unshift({ id: (st.adminLog[0]?.id ?? 0) + 1, time: new Date().toISOString(), actor: st.session || 'admin', action, target, detail, ip: '127.0.0.1' })
  }
  // approved recomputes node and tool approval fields after approvals change.
  const syncApprovals = () => {
    for (const t of st.tools as Tool[]) {
      const p = st.approvals.find((a) => a.node === t.node && a.mcp === t.mcp && a.tool === t.original_name)
      t.approval = p ? p.state : 'approved'
    }
    for (const n of st.nodes) n.pending_tools = st.approvals.filter((a) => a.node === n.name).length
    emit('approvals')
    emit('tools')
    emit('nodes')
  }
  const validGrant = (g: { node?: string; mcp?: string; tool?: string }) =>
    !!g.node && !!g.mcp && !!g.tool && !(g.node === '*' && (g.mcp !== '*' || g.tool !== '*')) && !(g.mcp === '*' && g.tool !== '*')

  function json(res: ServerResponse, status: number, body?: unknown, headers: Record<string, string> = {}) {
    res.writeHead(status, { 'Content-Type': 'application/json', ...headers })
    res.end(body === undefined ? '' : JSON.stringify(body))
  }
  const err = (res: ServerResponse, status: number, msg: string, h?: Record<string, string>) => json(res, status, { error: msg }, h)

  async function body(req: IncomingMessage): Promise<Record<string, string>> {
    let s = ''
    for await (const c of req) s += c
    try {
      return JSON.parse(s || '{}')
    } catch {
      return {}
    }
  }

  return {
    name: 'mock-api',
    configureServer(server) {
      // Live traffic: a new call every 4 s while not empty.
      if (!empty) {
        let i = 0
        const t = setInterval(() => {
          const base = fx.audit[i++ % 5]!
          st.audit.unshift({ ...base, id: (st.audit[0]?.id ?? 0) + 1, time: new Date().toISOString() })
          st.audit.length = Math.min(st.audit.length, 2000)
          emit('calls')
          emit('stats')
        }, 4000)
        server.httpServer?.on('close', () => clearInterval(t))
      }

      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? '/', 'http://mock')
        if (!url.pathname.startsWith('/api/v1/')) return next()
        const path = url.pathname.slice('/api/v1'.length)
        const m = req.method ?? 'GET'
        const signed = !authOn || !!st.session
        await new Promise((r) => setTimeout(r, 120)) // feel like a network

        if (path === '/info') return json(res, 200, { ...fx.info, auth: authOn, started_at: startedAt })
        if (path === '/session') {
          if (!authOn) return json(res, 200, { auth: false })
          if (m === 'GET') return st.session ? json(res, 200, { username: st.session, csrf, expires: new Date(Date.now() + 7 * 864e5).toISOString() }) : err(res, 401, 'sign in required')
          if (m === 'DELETE') {
            st.session = ''
            return json(res, 204)
          }
          const b = await body(req)
          const user = st.admins.find((a) => a.username === b.username)
          if (st.failures >= 5) return err(res, 429, 'too many failed sign-ins', { 'Retry-After': '150' })
          // Any admin with password "password1234" (or anything for "admin", the demo account).
          if (!user || (b.password !== 'password1234' && b.username !== 'admin')) {
            st.failures++
            return err(res, 401, 'wrong username or password')
          }
          st.failures = 0
          st.session = user.username
          return json(res, 200, { username: user.username, csrf, expires: new Date(Date.now() + 7 * 864e5).toISOString() })
        }
        if (!signed) return err(res, 401, 'sign in required')
        if (m !== 'GET' && authOn && req.headers['x-csrf-token'] !== csrf) return err(res, 403, 'missing or wrong CSRF token')

        if (path === '/events') {
          res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' })
          const types = ['nodes', 'tools', 'prompts', 'resources', 'calls', 'stats', 'approvals', 'settings', 'overrides', 'update', ...(authOn ? ['keys', 'admins', 'groups', 'pairings'] : [])]
          const send = (type: string) => {
            if (types.includes(type)) res.write(`event: ${type}\ndata: ${JSON.stringify(snapshot(type))}\n\n`)
          }
          types.forEach(send)
          subs.add(send)
          const ping = setInterval(() => res.write(': ping\n\n'), 25_000)
          req.on('close', () => {
            subs.delete(send)
            clearInterval(ping)
          })
          return
        }
        if (m === 'GET' && ['/nodes', '/tools', '/prompts', '/resources', '/calls', '/stats', '/approvals', '/settings', '/overrides', '/update'].includes(path)) return json(res, 200, snapshot(path.slice(1)))
        const ovPath = path.match(/^\/overrides\/([^/]+)\/([^/]+)\/(.+)$/)
        if (ovPath && (m === 'PUT' || m === 'DELETE')) {
          const [node, mcp, tool] = ovPath.slice(1).map(decodeURIComponent) as [string, string, string]
          const same = (o: ToolOverride) => o.node === node && o.mcp === mcp && o.tool === tool
          const b = m === 'PUT' ? ((await body(req)) as { alias?: string; description?: string }) : {}
          const alias = b.alias ?? ''
          const description = b.description ?? ''
          if (alias && !/^[A-Za-z0-9_-]{1,64}$/.test(alias)) return err(res, 400, 'invalid override: alias must be 1 to 64 letters, digits, _ or -')
          if (description.length > 2000) return err(res, 400, 'invalid override: description is longer than 2000 characters')
          const clash = st.overrides.some((o) => !same(o) && o.alias === alias) || (st.tools as Tool[]).some((t) => t.name === alias && !(t.node === node && t.mcp === mcp && t.original_name === tool))
          if (alias && clash) return err(res, 409, `alias is taken: "${alias}" is already the name of another tool`)
          st.overrides = st.overrides.filter((o) => !same(o))
          if (alias || description) st.overrides.push({ node, mcp, tool, alias, description, updated: new Date().toISOString(), updated_by: st.session || 'admin' })
          st.overrides.sort((x, y) => `${x.node}/${x.mcp}/${x.tool}`.localeCompare(`${y.node}/${y.mcp}/${y.tool}`))
          fx.applyOverrides(st.tools as Tool[], st.overrides)
          log('tool.override', `${node}/${mcp}/${tool}`, alias)
          emit('overrides')
          emit('tools')
          return alias || description ? json(res, 200, st.overrides.find(same)) : json(res, 204)
        }
        if (path === '/settings' && m === 'PUT') {
          const b = (await body(req)) as { trust_default?: TrustLevel }
          if (!['trust', 'review', 'approve'].includes(b.trust_default ?? '')) return err(res, 400, 'trust_default must be "trust", "review" or "approve"')
          st.settings.trust_default = b.trust_default!
          for (const n of st.nodes) if (!n.trust.explicit) n.trust = { ...n.trust, level: b.trust_default! }
          log('trust.level', '', b.trust_default)
          emit('settings')
          emit('nodes')
          return json(res, 200, st.settings)
        }
        if (path === '/approvals' && m === 'POST') {
          const b = (await body(req)) as unknown as { tools?: ApprovalRef[] }
          if (!Array.isArray(b.tools) || !b.tools.length) return err(res, 400, 'tools is required')
          const stale = b.tools.filter((r) => st.approvals.find((a) => a.node === r.node && a.mcp === r.mcp && a.tool === r.tool)?.hash !== r.hash)
          if (stale.length) return json(res, 409, { error: 'tools changed since they were listed', stale })
          const match = (a: PendingTool) => b.tools!.some((r) => a.node === r.node && a.mcp === r.mcp && a.tool === r.tool)
          const n = st.approvals.filter(match).length
          st.approvals = st.approvals.filter((a) => !match(a))
          for (const r of b.tools) {
            const node = st.nodes.find((x) => x.name === r.node)
            if (node) node.trust = { ...node.trust, approved: true }
          }
          // Approving a node's tools approves the node: its other tools become plain "new".
          for (const a of st.approvals) if (b.tools.some((r) => r.node === a.node) && a.state === 'node_pending') a.state = 'new'
          log('tool.approve', b.tools.map((r) => `${r.node}/${r.mcp}/${r.tool}`).join(', '), String(n))
          syncApprovals()
          return json(res, 200, { approved: n })
        }
        const forgetM = path.match(/^\/nodes\/([^/]+)$/)
        if (forgetM && m === 'DELETE') {
          const name = decodeURIComponent(forgetM[1]!)
          const node = st.nodes.find((x) => x.name === name)
          if (!node) return err(res, 404, 'no such node')
          if (node.online) return err(res, 409, `${name} is connected; only an offline node can be forgotten`)
          st.nodes = st.nodes.filter((x) => x !== node)
          log('node.forget', name)
          emit('nodes')
          return json(res, 204)
        }
        const trustM = path.match(/^\/nodes\/([^/]+)\/(trust|approve)$/)
        if (trustM) {
          const name = decodeURIComponent(trustM[1]!)
          let node = st.nodes.find((x) => x.name === name)
          if (trustM[2] === 'approve' && m === 'POST') {
            const b = (await body(req)) as unknown as { tools?: Omit<ApprovalRef, 'node'>[] }
            const listed = b.tools ?? []
            const stale = listed.filter((r) => st.approvals.find((a) => a.node === name && a.mcp === r.mcp && a.tool === r.tool)?.hash !== r.hash)
            if (stale.length) return json(res, 409, { error: 'tools changed since they were listed', stale: stale.map((r) => ({ node: name, ...r })) })
            st.approvals = st.approvals.filter((a) => !(a.node === name && listed.some((r) => r.mcp === a.mcp && r.tool === a.tool)))
            for (const a of st.approvals) if (a.node === name && a.state === 'node_pending') a.state = 'new'
            if (node) node.trust = { ...node.trust, approved: true }
            log('node.approve', name)
            syncApprovals()
            return json(res, 200, node?.trust ?? { level: 'approve', explicit: true, approved: true })
          }
          if (m === 'PUT') {
            const b = (await body(req)) as { level?: TrustLevel | null }
            if (b.level !== null && !['trust', 'review', 'approve'].includes(b.level ?? '')) return err(res, 400, 'level must be "trust", "review", "approve" or null')
            const level = b.level ?? st.settings.trust_default
            const t = { level, explicit: b.level !== null, approved: level !== 'approve' || !!node?.trust.approved }
            if (node) node.trust = t
            if (level === 'trust') st.approvals = st.approvals.filter((a) => a.node !== name)
            log('trust.level', name, b.level ?? 'default')
            syncApprovals()
            return json(res, 200, t)
          }
        }
        if (path === '/admin-log') {
          const before = Number(url.searchParams.get('before_id') ?? 0)
          const limit = Number(url.searchParams.get('limit') ?? 100)
          const page = st.adminLog.filter((e) => !before || e.id < before).slice(0, limit)
          return json(res, 200, { entries: page, next_before_id: page.length === limit ? page[page.length - 1]!.id : 0 })
        }
        if (path === '/audit') {
          const q = url.searchParams
          let list = st.audit
          for (const f of ['client_id', 'node', 'mcp', 'tool', 'outcome'] as const) {
            const v = q.get(f)
            if (v) list = list.filter((e) => e[f] === v)
          }
          const since = q.get('since')
          if (since) list = list.filter((e) => e.time >= since)
          const until = q.get('until')
          if (until) list = list.filter((e) => e.time < until)
          const before = Number(q.get('before_id') ?? 0)
          if (before) list = list.filter((e) => e.id < before)
          const limit = Number(q.get('limit') ?? 100)
          const page = list.slice(0, limit)
          return json(res, 200, { entries: page, next_before_id: page.length === limit ? page[page.length - 1]!.id : 0 })
        }
        if (!authOn && (path.startsWith('/keys') || path.startsWith('/admins') || path.startsWith('/pairings'))) return err(res, 404, 'auth is off')
        if (path === '/pairings' && m === 'GET') return json(res, 200, pairingViews())
        const pairing = path.match(/^\/pairings\/([^/]+)(\/approve)?$/)
        if (pairing && ((m === 'POST' && pairing[2]) || (m === 'DELETE' && !pairing[2]))) {
          const i = st.pairings.findIndex((p) => p.id === decodeURIComponent(pairing[1]!))
          if (i < 0) return err(res, 404, 'no such pairing request; the machine may have disconnected')
          const p = st.pairings[i]!
          if (m === 'DELETE') {
            st.pairings.splice(i, 1)
            log('pairing.deny', p.node, p.ip)
            emit('pairings')
            return json(res, 204)
          }
          const b = (await body(req)) as { name?: string; replace?: boolean }
          const name = (b.name ?? '').trim() || p.node
          if (/\s/.test(name) || name.length > 64) return err(res, 400, `name ${JSON.stringify(name)} contains whitespace or control characters`)
          const old = st.keys.filter((k) => k.kind === 'node' && k.name === name)
          if (old.length && !b.replace) return err(res, 409, `a node key for ${JSON.stringify(name)} already exists; replace it (its node disconnects) or choose another name`)
          for (const k of old) {
            st.keys.splice(st.keys.indexOf(k), 1)
            for (const n of st.nodes) if (n.key_id === k.id && n.online) Object.assign(n, { online: false, disconnected_at: new Date().toISOString() })
            log('key.revoke', k.id, `node ${k.name}`)
          }
          const key: Key = { id: 'k_' + Math.random().toString(16).slice(2, 10), kind: 'node', name, created: new Date().toISOString() }
          st.keys.push(key)
          st.pairings.splice(i, 1)
          log('pairing.approve', key.id, `node ${name} from ${p.ip}`)
          emit('pairings')
          emit('keys')
          emit('nodes')
          return json(res, 200, { key })
        }
        if (path === '/groups') {
          if (m === 'GET') return json(res, 200, st.groups)
          const b = (await body(req)) as { name?: string; description?: string; grants?: Group['grants'] }
          const name = (b.name ?? '').trim()
          if (!name) return err(res, 400, 'invalid group: name is empty')
          if (st.groups.some((g) => g.name.toLowerCase() === name.toLowerCase())) return err(res, 409, `a group with that name exists: "${name}"`)
          if (!(b.grants ?? []).every(validGrant)) return err(res, 400, 'invalid group: bad grant')
          const g: Group = { id: 'g_' + Math.random().toString(16).slice(2, 10), name, description: b.description ?? '', created: new Date().toISOString(), grants: b.grants ?? [], keys: [] }
          st.groups.push(g)
          st.groups.sort((a, x) => a.name.localeCompare(x.name))
          log('group.create', g.id, g.name)
          emit('groups')
          return json(res, 201, g)
        }
        const groupM = path.match(/^\/groups\/([^/]+)$/)
        if (groupM) {
          const g = st.groups.find((x) => x.id === decodeURIComponent(groupM[1]!))
          if (!g) return err(res, 404, 'no such group')
          if (m === 'DELETE') {
            st.groups = st.groups.filter((x) => x !== g)
            log('group.delete', g.id, g.name)
            emit('groups')
            emit('keys')
            return json(res, 204)
          }
          const b = (await body(req)) as { name?: string; description?: string; grants?: Group['grants'] }
          const name = (b.name ?? '').trim()
          if (!name) return err(res, 400, 'invalid group: name is empty')
          if (st.groups.some((x) => x !== g && x.name.toLowerCase() === name.toLowerCase())) return err(res, 409, `a group with that name exists: "${name}"`)
          if (!(b.grants ?? []).every(validGrant)) return err(res, 400, 'invalid group: bad grant')
          Object.assign(g, { name, description: b.description ?? '', grants: b.grants ?? [] })
          log('group.update', g.id, g.name)
          emit('groups')
          return json(res, 200, g)
        }
        const keySub = path.match(/^\/keys\/([^/]+)\/(groups|tools)$/)
        if (keySub) {
          const k = st.keys.find((x) => x.id === decodeURIComponent(keySub[1]!))
          if (!k) return err(res, 404, 'no key with that ID')
          if (k.kind !== 'client') return err(res, 400, 'only client keys have groups and tools')
          const mine = () => st.groups.filter((g) => g.keys.includes(k.id))
          if (keySub[2] === 'tools') {
            const grants = mine().flatMap((g) => g.grants)
            const ok = (t: Tool) => t.approval === 'approved' && grants.some((g) => (g.node === '*' || g.node === t.node) && (g.mcp === '*' || g.mcp === t.mcp) && (g.tool === '*' || g.tool === t.original_name))
            return json(res, 200, (st.tools as Tool[]).filter(ok))
          }
          const b = (await body(req)) as { groups?: string[] }
          const ids = b.groups ?? []
          if (!ids.every((id) => st.groups.some((g) => g.id === id))) return err(res, 400, 'no such group')
          for (const g of st.groups) g.keys = g.keys.filter((x) => x !== k.id).concat(ids.includes(g.id) ? [k.id] : [])
          log('key.groups', k.id, ids.join(', '))
          emit('groups')
          emit('keys')
          return json(res, 200, { key: keyViews().find((x) => x.id === k.id) })
        }
        if (path === '/keys') {
          if (m === 'GET') return json(res, 200, keyViews())
          const b = await body(req)
          if (b.kind !== 'node' && b.kind !== 'client') return err(res, 400, 'kind must be node or client')
          if (!b.name?.trim()) return err(res, 400, 'name is empty')
          if (/\s/.test(b.name)) return err(res, 400, `name "${b.name}" contains whitespace or control characters`)
          if (b.kind === 'node' && !/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(b.name)) return err(res, 400, 'node name may only use letters, digits, ".", "_" and "-"')
          const id = 'k_' + Math.random().toString(16).slice(2, 10)
          const key: Key = { id, kind: b.kind, name: b.name.trim(), created: new Date().toISOString() }
          const raw = b as unknown as { groups?: string[]; trust?: TrustLevel }
          if (b.kind === 'client') {
            const ids = raw.groups ?? (st.groups.some((g) => g.id === 'g_everything') ? ['g_everything'] : [])
            if (!ids.every((gid) => st.groups.some((g) => g.id === gid))) return err(res, 400, 'no such group')
            for (const g of st.groups) if (ids.includes(g.id)) g.keys.push(id)
            emit('groups')
          }
          st.keys.push(key)
          log('key.create', id, `${b.kind} ${key.name}`)
          emit('keys')
          const secret = `mm_${b.kind}_DEMO_${Math.random().toString(36).slice(2, 14)}`
          const setup = b.kind === 'node'
            ? {
                hub_url: fx.info.node_url,
                init_command: `mcp-mesh-node init --hub ${fx.info.node_url} --name ${key.name} --key ${secret}`,
                command: `mcp-mesh-node key set ${secret}`,
              }
            : { mcp_url: fx.info.client_url, header: `Authorization: Bearer ${secret}`, claude_code: `claude mcp add --transport http mcp-mesh ${fx.info.client_url} --header "Authorization: Bearer ${secret}"` }
          return json(res, 201, { key, secret, setup })
        }
        const keyMatch = path.match(/^\/keys\/([^/]+)$/)
        if (keyMatch && m === 'DELETE') {
          const i = st.keys.findIndex((k) => k.id === decodeURIComponent(keyMatch[1]!))
          if (i < 0) return err(res, 404, 'no such key')
          const [key] = st.keys.splice(i, 1)
          for (const n of st.nodes) if (n.key_id === key!.id && n.online) Object.assign(n, { online: false, disconnected_at: new Date().toISOString() })
          emit('keys')
          emit('nodes')
          return json(res, 200, { key })
        }
        if (path === '/admins') {
          if (m === 'GET') return json(res, 200, st.admins)
          const b = await body(req)
          if (!/^[a-z][a-z0-9._-]{0,31}$/.test(b.username ?? '')) return err(res, 400, 'invalid username')
          if ((b.password ?? '').length < 12) return err(res, 400, 'password must be at least 12 characters')
          if (st.admins.some((a) => a.username === b.username)) return err(res, 409, 'username already exists')
          const now = new Date().toISOString()
          const a = { username: b.username!, created: now, password_changed: now }
          st.admins.push(a)
          emit('admins')
          return json(res, 201, a)
        }
        const pw = path.match(/^\/admins\/([^/]+)\/password$/)
        if (pw && m === 'PUT') {
          const name = decodeURIComponent(pw[1]!)
          const a = st.admins.find((x) => x.username === name)
          if (!a) return err(res, 404, 'no such admin')
          const b = await body(req)
          if (name === st.session && !b.current_password) return err(res, 403, 'current password required')
          if ((b.password ?? '').length < 12) return err(res, 400, 'password must be at least 12 characters')
          a.password_changed = new Date().toISOString()
          if (name === st.session) st.session = ''
          emit('admins')
          return json(res, 204)
        }
        const del = path.match(/^\/admins\/([^/]+)$/)
        if (del && m === 'DELETE') {
          const name = decodeURIComponent(del[1]!)
          const i = st.admins.findIndex((x) => x.username === name)
          if (i < 0) return err(res, 404, 'no such admin')
          if (st.admins.length === 1) return err(res, 409, "the last admin can't be deleted")
          st.admins.splice(i, 1)
          if (name === st.session) st.session = ''
          emit('admins')
          return json(res, 204)
        }
        return err(res, 404, 'not found')
      })
    },
  }
}
