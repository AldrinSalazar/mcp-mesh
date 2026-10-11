<script setup lang="ts">
// The map itself: lines and the hub in SVG, node cards as real buttons laid
// over it with percentages so everything scales with the panel width.
import { computed } from 'vue'
import { Monitor } from 'lucide-vue-next'
import StatusDot from '@/components/ui/StatusDot.vue'
import { ago, plural } from '@/lib/format'
import type { Node } from '@/lib/types'
import { cardH, cardW, clientR, hubR, mapLayout } from './layout'

const props = defineProps<{
  nodes: Node[]
  clients: { id: string; name: string }[]
  selected: string
  hubName: string
  now: number
}>()
const emit = defineEmits<{ select: [name: string] }>()

const layout = computed(() => mapLayout(props.nodes.length, props.clients.length))
const items = computed(() => props.nodes.map((n, i) => ({ node: n, at: layout.value.nodes[i]! })))
const dots = computed(() => props.clients.map((c, i) => ({ client: c, at: layout.value.clients[i]! })))

const pctX = (v: number) => `${(v / layout.value.width) * 100}%`
const pctY = (v: number) => `${(v / layout.value.height) * 100}%`

function sub(n: Node): string {
  if (!n.online) return n.disconnected_at ? `Last seen ${ago(n.disconnected_at, props.now).toLowerCase()}` : 'Offline'
  const tools = n.mcps.reduce((s, m) => s + m.tools, 0)
  return `${plural(tools, 'tool')} · ${plural(n.mcps.length, 'MCP')}`
}
</script>

<template>
  <div class="scroller">
    <div class="canvas" :style="{ aspectRatio: `${layout.width} / ${layout.height}`, minWidth: `${Math.round(layout.width * 0.82)}px` }">
      <svg class="lines" :viewBox="`0 0 ${layout.width} ${layout.height}`" preserveAspectRatio="none" aria-hidden="true">
        <line
          v-for="({ node, at }, i) in items"
          :key="`l${i}`"
          class="rule"
          :class="{ dashed: !node.online }"
          :x1="at.line.from.x" :y1="at.line.from.y" :x2="at.line.to.x" :y2="at.line.to.y"
        />
        <line v-if="layout.stem" class="rule" :x1="layout.stem.from.x" :y1="layout.stem.from.y" :x2="layout.stem.to.x" :y2="layout.stem.to.y" />
        <template v-if="layout.bus">
          <line class="rule" :x1="layout.bus.x1" :y1="layout.bus.y" :x2="layout.bus.x2" :y2="layout.bus.y" />
          <line v-for="(c, i) in layout.clients" :key="`d${i}`" class="rule" :x1="c.x" :y1="layout.bus.y" :x2="c.x" :y2="c.y - clientR" />
        </template>
        <circle class="hub" :cx="layout.hub.x" :cy="layout.hub.y" :r="hubR" />
        <circle class="hub-dot" :cx="layout.hub.x" :cy="layout.hub.y" r="14" />
        <circle v-for="(c, i) in layout.clients" :key="`c${i}`" class="client" :cx="c.x" :cy="c.y" :r="clientR" />
      </svg>

      <div class="hub-label" :style="{ left: pctX(layout.hub.x), top: pctY(layout.hub.y - hubR - 14) }">HUB</div>
      <div class="hub-name mono" :style="{ left: pctX(layout.hub.x), top: pctY(layout.hub.y + hubR + 10) }" :title="hubName">{{ hubName }}</div>

      <button
        v-for="{ node, at } in items"
        :key="node.name"
        type="button"
        class="card"
        :class="{ selected: node.name === selected }"
        :style="{ left: pctX(at.x), top: pctY(at.y), width: pctX(cardW), height: pctY(cardH) }"
        :aria-pressed="node.name === selected"
        :aria-label="`${node.name}, ${node.online ? 'online' : 'offline'}`"
        @click="emit('select', node.name)"
      >
        <span class="top">
          <Monitor :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
          <span class="name truncate" :title="node.name">{{ node.name }}</span>
          <span v-if="node.pending_tools > 0 || !node.trust.approved" class="pend" :title="node.trust.approved ? `${node.pending_tools} waiting for approval` : 'Waiting for approval'">{{ node.trust.approved ? node.pending_tools : '!' }}</span>
          <StatusDot :tone="node.online ? 'ok' : 'err'" />
        </span>
        <span class="sub">{{ sub(node) }}</span>
      </button>

      <div
        v-for="{ client, at } in dots"
        :key="client.id"
        class="client-wrap"
        :style="{ left: pctX(at.x), top: pctY(at.y) }"
      >
        <span class="initial">{{ (client.name[0] ?? '?').toUpperCase() }}</span>
        <span class="client-name truncate" :title="client.name">{{ client.name }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.scroller { overflow-x: auto; padding: 8px 0; }
.canvas { position: relative; width: 100%; }
.lines { position: absolute; inset: 0; width: 100%; height: 100%; }
.rule { stroke: var(--rule); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.dashed { stroke-dasharray: 5 5; }
.hub { fill: var(--cobalt-soft); stroke: var(--cobalt); stroke-width: 1.5; }
.hub-dot { fill: var(--cobalt-fill); }
.client { fill: var(--ink); }

.hub-label, .hub-name { position: absolute; white-space: nowrap; }
.hub-label { transform: translate(-50%, -50%); font-size: 10px; font-weight: 600; color: var(--muted); }
.hub-name { transform: translateX(-50%); font-size: 13px; max-width: 160px; overflow: hidden; text-overflow: ellipsis; }

.card {
  position: absolute;
  display: flex;
  flex-direction: column;
  justify-content: center;
  gap: 14px;
  padding: 0 16px;
  text-align: left;
  background: var(--paper);
  border: 1px solid var(--rule);
  border-radius: 0;
  cursor: pointer;
  transition: border-color 0.12s, background-color 0.12s;
}
.pend { margin-left: auto; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; background: var(--warn-soft); color: var(--warn); font-size: 11px; font-weight: 600; line-height: 18px; text-align: center; }
.card:hover { border-color: var(--muted); }
.card.selected { background: var(--cobalt-soft); border: 1.5px solid var(--cobalt); }
.top { display: flex; align-items: center; gap: 12px; min-width: 0; }
.icon { flex: none; color: var(--muted); }
.selected .icon { color: var(--cobalt); }
.name { flex: 1; font-family: var(--font-display); font-size: 18px; font-weight: 500; }
.sub { font-size: 12px; color: var(--muted); }

.client-wrap {
  position: absolute;
  display: flex;
  flex-direction: column;
  align-items: center;
  width: 140px;
  transform: translate(-50%, -22px);
}
/* The disc is drawn in SVG; this only carries the letter on top of it. */
.initial {
  display: grid;
  place-items: center;
  width: 44px;
  height: 44px;
  font-family: var(--font-display);
  font-size: 20px;
  font-weight: 600;
  color: var(--surface);
}
.client-name { max-width: 100%; margin-top: 8px; font-size: 12px; color: var(--muted); }
</style>
