// The access tree of the group editor: node → MCP → tool, and the conversion
// between its check boxes and the minimal list of grants the API stores.
//
// The grant list is the single source of truth. Check state is derived from
// it, and every toggle rewrites it, so wildcards survive edits that don't
// touch them and grants for nodes or MCPs that aren't connected right now are
// never dropped (they show up as "not connected" rows).
import type { Grant, Node, Tool } from '@/lib/types'

export interface TreeTool { name: string; connected: boolean }
export interface TreeMCP { name: string; connected: boolean; tools: TreeTool[] }
export interface TreeNode { name: string; connected: boolean; mcps: TreeMCP[] }

// A path addresses a row: [] is the root ("All nodes"), [node], [node, mcp],
// [node, mcp, tool] (tool is the original name).
export type Path = string[]
export type Check = 'on' | 'off' | 'partial'

const ANY = '*'

type Triple = [string, string, string]

const triple = (g: Grant): Triple => [g.node, g.mcp, g.tool]
const toGrant = (t: Triple): Grant => ({ node: t[0], mcp: t[1], tool: t[2] })
const pad = (p: Path): Triple => [p[0] ?? ANY, p[1] ?? ANY, p[2] ?? ANY]

// covers: does a cover everything b names? (a ⊇ b)
function covers(a: Triple, b: Triple): boolean {
  return a.every((x, i) => x === ANY || x === b[i])
}

// pathCovers: is the row at p inside what g grants?
function pathCovers(g: Grant, p: Path): boolean {
  return covers(triple(g), pad(p))
}

export function buildTree(nodes: Node[], tools: Tool[], grants: Grant[]): TreeNode[] {
  const byNode = new Map<string, TreeNode>()
  const nodeOf = (n: string, connected: boolean): TreeNode => {
    let node = byNode.get(n)
    if (!node) {
      node = { name: n, connected, mcps: [] }
      byNode.set(n, node)
    }
    return node
  }
  const mcpOf = (n: string, m: string, connected: boolean): TreeMCP => {
    const node = nodeOf(n, connected)
    let mcp = node.mcps.find((x) => x.name === m)
    if (!mcp) {
      mcp = { name: m, connected, tools: [] }
      node.mcps.push(mcp)
    }
    return mcp
  }
  for (const n of nodes) {
    nodeOf(n.name, n.online)
    for (const m of n.mcps) mcpOf(n.name, m.name, n.online)
  }
  for (const t of tools) {
    const mcp = mcpOf(t.node, t.mcp, true)
    if (!mcp.tools.some((x) => x.name === t.original_name)) mcp.tools.push({ name: t.original_name, connected: true })
  }
  // Grants naming things that aren't connected become rows of their own.
  for (const g of grants) {
    if (g.node === ANY) continue
    nodeOf(g.node, false)
    if (g.mcp === ANY) continue
    const mcp = mcpOf(g.node, g.mcp, false)
    if (g.tool !== ANY && !mcp.tools.some((x) => x.name === g.tool)) mcp.tools.push({ name: g.tool, connected: false })
  }
  const out = [...byNode.values()]
  const byName = (a: { name: string }, b: { name: string }) => a.name.localeCompare(b.name)
  out.sort(byName)
  for (const n of out) {
    n.mcps.sort(byName)
    for (const m of n.mcps) m.tools.sort(byName)
  }
  return out
}

function childrenOf(tree: TreeNode[], p: Path): { name: string }[] {
  if (p.length === 0) return tree
  const node = tree.find((n) => n.name === p[0])
  if (!node) return []
  if (p.length === 1) return node.mcps
  return node.mcps.find((m) => m.name === p[1])?.tools ?? []
}

// check derives the state of the row at p from the grants.
export function check(grants: Grant[], tree: TreeNode[], p: Path): Check {
  if (grants.some((g) => pathCovers(g, p))) return 'on'
  if (p.length === 3) return 'off'
  let on = 0
  let partial = false
  const kids = childrenOf(tree, p)
  for (const k of kids) {
    const s = check(grants, tree, [...p, k.name])
    if (s === 'on') on++
    else if (s === 'partial') partial = true
  }
  if (kids.length > 0 && on === kids.length) return 'on'
  return on > 0 || partial ? 'partial' : 'off'
}

// minimize drops duplicates and grants another grant already covers, then
// orders them (wildcards first) so equal sets compare equal.
export function minimize(grants: Grant[]): Grant[] {
  const ts = grants.map(triple)
  const kept = ts.filter((a, i) => !ts.some((b, j) => j !== i && covers(b, a) && (!covers(a, b) || j < i)))
  const rank = (s: string) => (s === ANY ? '' : s)
  return kept
    .sort((a, b) => {
      for (let i = 0; i < 3; i++) {
        const c = rank(a[i]!).localeCompare(rank(b[i]!))
        if (c) return c
      }
      return 0
    })
    .map(toGrant)
}

// without removes p's area from grant g. A wider grant is split into its
// siblings (from the tree) so the rest of what it granted stays granted.
function without(g: Triple, p: Path, tree: TreeNode[]): Triple[] {
  for (let i = 0; i < p.length; i++) {
    if (g[i] !== ANY && g[i] !== p[i]) return [g] // unrelated
  }
  const i = g.findIndex((x, k) => k < p.length && x === ANY)
  if (i < 0) return [] // g lies inside p
  const out: Triple[] = []
  for (const sib of childrenOf(tree, p.slice(0, i))) {
    if (sib.name === p[i]) continue
    const s: Triple = [...g]
    s[i] = sib.name
    out.push(s)
  }
  const rest: Triple = [...g]
  rest[i] = p[i]!
  return out.concat(without(rest, p, tree))
}

// toggle checks or unchecks the row at p. Checking a node or MCP row grants
// it whole, now and later (`node/mcp/*`); checking every tool of an MCP one
// by one grants those tools only, so a tool the MCP adds later isn't
// granted by surprise.
export function toggle(grants: Grant[], tree: TreeNode[], p: Path, on: boolean): Grant[] {
  if (!on) return minimize(grants.flatMap((g) => without(triple(g), p, tree).map(toGrant)))
  return minimize([...grants, toGrant(pad(p))])
}

// future reports whether the row at p is granted whole, so whatever appears
// under it later (a new tool, a new MCP) is granted too.
export function future(grants: Grant[], p: Path): boolean {
  return grants.some((g) => pathCovers(g, p))
}

export function countTools(tree: TreeNode[], grants: Grant[]): { granted: number; total: number } {
  let granted = 0
  let total = 0
  for (const n of tree) {
    for (const m of n.mcps) {
      for (const t of m.tools) {
        total++
        if (grants.some((g) => pathCovers(g, [n.name, m.name, t.name]))) granted++
      }
    }
  }
  return { granted, total }
}

// summary is the one-line description of what a group grants.
export function summary(grants: Grant[]): string {
  if (grants.length === 0) return 'No access'
  if (grants.some((g) => g.node === ANY)) return 'Every tool on every node'
  const nodes: string[] = []
  const mcps = new Map<string, string[]>() // mcp → nodes
  const toolNodes = new Set<string>()
  let tools = 0
  for (const g of grants) {
    if (g.mcp === ANY) nodes.push(g.node)
    else if (g.tool === ANY) mcps.set(g.mcp, [...(mcps.get(g.mcp) ?? []), g.node])
    else {
      tools++
      toolNodes.add(g.node)
    }
  }
  const parts: string[] = []
  if (nodes.length) parts.push(`All of ${nodes.join(', ')}`)
  for (const [m, ns] of mcps) parts.push(`All of ${m} on ${ns.join(', ')}`)
  if (tools) parts.push(`${tools} ${tools === 1 ? 'tool' : 'tools'} on ${[...toolNodes].join(', ')}`)
  return parts.join(' · ')
}

// filterTree keeps the rows whose name (or an ancestor's) contains q.
export function filterTree(tree: TreeNode[], q: string): TreeNode[] {
  const s = q.trim().toLowerCase()
  if (!s) return tree
  const has = (n: string) => n.toLowerCase().includes(s)
  const out: TreeNode[] = []
  for (const n of tree) {
    if (has(n.name)) {
      out.push(n)
      continue
    }
    const mcps: TreeMCP[] = []
    for (const m of n.mcps) {
      if (has(m.name)) mcps.push(m)
      else {
        const tools = m.tools.filter((t) => has(t.name))
        if (tools.length) mcps.push({ ...m, tools })
      }
    }
    if (mcps.length) out.push({ ...n, mcps })
  }
  return out
}
