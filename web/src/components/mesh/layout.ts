// Geometry of the mesh map, in a fixed coordinate space the view scales to
// the panel width. Pure so it can be tested: the view only draws what this says.

export const cardW = 230
export const cardH = 98
export const hubR = 50
export const clientR = 22
const clientGap = 180

export interface Point { x: number; y: number }

export interface NodePlacement {
  // x, y is the card's top-left corner.
  x: number
  y: number
  cx: number
  cy: number
  // line runs from the hub's edge to the card centre (cards are opaque and
  // drawn over it, so it appears to end at the card's border).
  line: { from: Point; to: Point }
}

export interface ClientPlacement { x: number; y: number }

export interface MapLayout {
  width: number
  height: number
  hub: Point
  nodes: NodePlacement[]
  clients: ClientPlacement[]
  // stem is the vertical line below the hub; bus the horizontal one that
  // branches to the clients (null for fewer than two clients).
  stem: { from: Point; to: Point } | null
  bus: { y: number; x1: number; x2: number } | null
}

const baseWidth = 760

// Below the hub's name label, where the line to the clients starts.
const stemStart = hubR + 34

function nodePlacement(hub: Point, cx: number, cy: number): NodePlacement {
  const dx = cx - hub.x
  const dy = cy - hub.y
  const d = Math.hypot(dx, dy) || 1
  return {
    x: cx - cardW / 2,
    y: cy - cardH / 2,
    cx,
    cy,
    line: { from: { x: hub.x + (dx / d) * hubR, y: hub.y + (dy / d) * hubR }, to: { x: cx, y: cy } },
  }
}

// nodeCenters puts up to four nodes in the design's quadrants and more on an
// ellipse around the hub. Angles avoid straight down so the stem to the
// clients never runs through a card.
export function nodeCenters(n: number, hub: Point): { centers: Point[]; rx: number; ry: number } {
  if (n === 0) return { centers: [], rx: 0, ry: 0 }
  if (n <= 4) {
    const dx = 215
    const dy = 156
    const quad: Point[] = [
      { x: hub.x - dx, y: hub.y - dy },
      { x: hub.x + dx, y: hub.y - dy },
      { x: hub.x - dx, y: hub.y + dy },
      { x: hub.x + dx, y: hub.y + dy },
    ]
    return { centers: quad.slice(0, n), rx: dx, ry: dy }
  }
  const rx = 290 + 30 * (n - 4)
  const ry = 170 + 20 * (n - 4)
  const step = 360 / n
  const start = n % 2 === 0 ? -90 - step / 2 : -90
  const centers: Point[] = []
  for (let i = 0; i < n; i++) {
    const a = ((start + i * step) * Math.PI) / 180
    centers.push({ x: hub.x + rx * Math.cos(a), y: hub.y + ry * Math.sin(a) })
  }
  return { centers, rx, ry }
}

export function mapLayout(nodeCount: number, clientCount: number): MapLayout {
  const probe = nodeCenters(nodeCount, { x: 0, y: 0 })
  const reach = probe.ry + (nodeCount > 0 ? cardH / 2 : 0)
  const width = Math.max(baseWidth, nodeCount > 4 ? 2 * (probe.rx + cardW / 2) + 40 : baseWidth)
  const hub: Point = { x: width / 2, y: nodeCount === 0 ? 120 : 40 + reach }
  const { centers } = nodeCenters(nodeCount, hub)
  const nodes = centers.map((c) => nodePlacement(hub, c.x, c.y))

  const bottom = Math.max(hub.y + hubR + 40, ...nodes.map((p) => p.y + cardH))
  const clientY = bottom + (nodeCount > 0 ? 90 : 60) + clientR
  const busY = clientY - clientR - 36
  const xs = Array.from({ length: clientCount }, (_, i) => hub.x + (i - (clientCount - 1) / 2) * clientGap)
  const clients = xs.map((x) => ({ x, y: clientY }))

  let stem: MapLayout['stem'] = null
  let bus: MapLayout['bus'] = null
  if (clientCount === 1) {
    stem = { from: { x: hub.x, y: hub.y + stemStart }, to: { x: hub.x, y: clientY - clientR } }
  } else if (clientCount > 1) {
    stem = { from: { x: hub.x, y: hub.y + stemStart }, to: { x: hub.x, y: busY } }
    bus = { y: busY, x1: xs[0]!, x2: xs[xs.length - 1]! }
  }

  const height = clientCount > 0 ? clientY + clientR + 50 : bottom + 30
  return { width, height, hub, nodes, clients, stem, bus }
}
