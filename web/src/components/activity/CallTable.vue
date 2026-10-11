<script setup lang="ts">
// Calls table shared by the Live and Audit tabs. Failed calls expand to show
// the error text; ok calls don't.
import { ref } from 'vue'
import type { Call } from '@/lib/types'
import { clock, dayTime, duration, shortID } from '@/lib/format'
import { outcomeStatus } from '@/lib/status'
import { useNow } from '@/lib/live'
import UiBadge from '@/components/ui/UiBadge.vue'

// Rows are keyed by key(); audit entries have an id, live calls use time+tool.
const props = defineProps<{ calls: readonly Call[]; dated?: boolean }>()

const now = useNow()
const open = ref(new Set<string>())

// Live calls have no id; the same client can't finish the same tool in the
// same millisecond-stamped instant twice in practice, so this is stable.
function key(c: Call): string {
  return 'id' in c ? String((c as { id: number }).id) : `${c.time}|${c.client_id}|${c.tool}`
}
function toggle(c: Call) {
  const k = key(c)
  const s = new Set(open.value)
  if (!s.delete(k)) s.add(k)
  open.value = s
}
function when(c: Call): string {
  const t = new Date(c.time)
  const start = new Date(now.value)
  start.setHours(0, 0, 0, 0)
  return props.dated && t.getTime() < start.getTime() ? dayTime(t, now.value) : clock(t)
}
const failed = (c: Call) => c.outcome !== 'ok'
</script>

<template>
  <div class="calls" role="table" aria-label="Calls">
    <div class="head label" role="row">
      <span role="columnheader">Time</span>
      <span role="columnheader">Client / key</span>
      <span role="columnheader">Tool / source</span>
      <span role="columnheader" class="dur">Duration</span>
      <span role="columnheader">Outcome</span>
    </div>
    <div v-for="c in calls" :key="key(c)" class="item" :class="{ err: failed(c), open: open.has(key(c)) }" role="rowgroup">
      <component
        :is="failed(c) ? 'button' : 'div'"
        class="row"
        role="row"
        v-bind="failed(c) ? { type: 'button', 'aria-expanded': open.has(key(c)) } : {}"
        @click="failed(c) && toggle(c)"
      >
        <span class="time mono muted" role="cell">{{ when(c) }}</span>
        <span class="client" role="cell">
          <span class="truncate" :title="c.client">{{ c.client || c.client_id }}</span>
          <span class="mono muted small truncate" :title="c.client_id">{{ shortID(c.client_id) }}</span>
        </span>
        <span class="tool" role="cell">
          <span class="mono truncate" :title="c.tool">{{ c.tool }}</span>
          <span class="muted small truncate" :title="`${c.node} / ${c.mcp}`"><template v-if="c.kind && c.kind !== 'tool'">{{ c.kind === 'prompt' ? 'Prompt' : 'Resource read' }} · </template>{{ c.node }} / {{ c.mcp }}</span>
        </span>
        <span class="dur mono muted" role="cell">{{ duration(c.duration_ms) }}</span>
        <span class="outcome" role="cell">
          <span v-if="open.has(key(c))" class="err-text plain">{{ outcomeStatus(c.outcome).label }}</span>
          <UiBadge v-else :tone="outcomeStatus(c.outcome).tone">{{ outcomeStatus(c.outcome).label }}</UiBadge>
        </span>
      </component>
      <div v-if="failed(c) && open.has(key(c))" class="detail">
        <p class="mono err-text msg">{{ c.error || 'No error text recorded.' }}</p>
        <router-link class="arrow-link" :to="{ path: '/nodes', query: { node: c.node } }">Inspect {{ c.node }}</router-link>
      </div>
    </div>
  </div>
</template>

<style scoped>
.calls { --cols: 110px minmax(0, 1.2fr) minmax(0, 2.4fr) 110px 120px; }
.head, .row { display: grid; grid-template-columns: var(--cols); gap: 12px; align-items: center; padding: 0 24px; }
.head { height: 40px; background: var(--paper); }
.item { position: relative; }
.item::after { content: ''; position: absolute; left: 24px; right: 24px; bottom: 0; border-bottom: 1px solid var(--rule); }
.item:last-child::after, .item.err.open::after { display: none; }
.item.err.open { background: var(--err-soft); padding-bottom: 24px; }
.item .detail { margin: 0 24px; }
.row { width: 100%; min-height: 78px; padding-top: 14px; padding-bottom: 14px; border: 0; background: none; text-align: left; color: inherit; }
button.row { cursor: pointer; }
button.row:hover { background: color-mix(in srgb, var(--err-soft) 55%, transparent); }
.row:focus-visible { outline-offset: -2px; }
.time { font-size: 12px; }
.client, .tool { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.client > :first-child { font-size: 13px; }
.tool > .mono { font-size: 12px; }
.row .dur { font-size: 12px; }
.plain { font-size: 12px; padding-left: 10px; }
.detail { background: var(--surface); border-left: 3px solid var(--err); padding: 20px 24px; display: flex; flex-direction: column; gap: 20px; }
.detail .arrow-link { white-space: pre; align-self: flex-start; }
.msg { font-size: 12px; line-height: 1.5; overflow-wrap: anywhere; }

@media (max-width: 700px) {
  .calls { --cols: minmax(0, 1fr) 96px; }
  .head { padding: 0 16px; }
  .row { padding: 14px 16px; gap: 6px 12px; }
  .head span:nth-child(n+2):not(:last-child) { display: none; }
  .head span:first-child::after { content: ' / client'; }
  .item::after { left: 16px; right: 16px; }
  .item .detail { margin: 0 16px; }
  .row { grid-template-areas: 'time outcome' 'client outcome' 'tool tool'; }
  .time { grid-area: time; }
  .client { grid-area: client; }
  .tool { grid-area: tool; }
  .outcome { grid-area: outcome; justify-self: end; }
  .dur { display: none; }
  .head .dur { display: none; }
}
</style>
