<script setup lang="ts">
// "Needs attention": offline nodes and MCPs that are failing or waiting.
// Each item opens the Nodes screen on that node.
import type { Problem } from '@/lib/status'
import { agoLong } from '@/lib/format'
import StatusDot from '@/components/ui/StatusDot.vue'
import type { ApprovalItem } from './summary'

withDefaults(defineProps<{ items: Problem[]; now: number; approvals?: ApprovalItem[] }>(), { approvals: () => [] })
</script>

<template>
  <section class="panel attention" aria-labelledby="attn-title">
    <header class="head">
      <h2 id="attn-title" class="section-title">Needs attention</h2>
      <span class="chip">{{ items.length + approvals.length }}</span>
      <RouterLink class="arrow-link all" to="/nodes">View nodes</RouterLink>
    </header>
    <ul class="list">
      <li v-for="a in approvals" :key="a.key">
        <RouterLink class="item" :to="a.to">
          <StatusDot tone="warn" :size="8" class="dot" />
          <span class="title truncate" :title="a.title">{{ a.title }}</span>
          <span class="detail small muted truncate" :title="a.detail">{{ a.detail }}</span>
        </RouterLink>
      </li>
      <li v-for="p in items" :key="`${p.node}/${p.mcp ?? ''}`">
        <RouterLink class="item" :to="p.node ? { path: '/nodes', query: { node: p.node } } : '/nodes'">
          <StatusDot :tone="p.tone" :size="8" class="dot" />
          <span class="title truncate" :title="p.title">{{ p.title }}</span>
          <span v-if="p.kind === 'node'" class="detail small muted truncate" :title="p.detail">{{ [p.detail, p.since ? `Last seen ${agoLong(p.since, now)}` : 'Offline'].filter(Boolean).join(' · ') }}</span>
          <span v-else class="detail mono muted truncate" :title="p.detail">{{ p.detail }}</span>
        </RouterLink>
      </li>
    </ul>
  </section>
</template>

<style scoped>
.attention { padding: 0 0 12px; }
.head { display: flex; align-items: center; gap: 20px; padding: 18px 24px 4px; height: 66px; }
.chip { background: var(--warn-soft); color: var(--warn); font-size: 12px; min-width: 38px; height: 26px; display: inline-grid; place-items: center; padding: 0 8px; }
.all { margin-left: auto; margin-right: 56px; }
.list { list-style: none; margin: 0; padding: 0 24px; display: grid; grid-template-columns: 1fr 1fr; }
.list li:nth-child(odd) { border-right: 1px solid var(--rule); }
.list li:nth-child(even) .item { padding-left: 32px; }
.list li:nth-child(odd) .item { padding-right: 32px; }
.item {
  display: grid; grid-template-columns: 8px 1fr; column-gap: 12px; row-gap: 12px;
  padding: 14px 0; color: var(--ink); min-width: 0; align-items: center;
}
.item:hover { text-decoration: none; }
.item:hover .title { text-decoration: underline; }
.dot { margin-top: 2px; }
.title { font-size: 16px; font-weight: 500; }
.detail { grid-column: 2; }

@media (max-width: 700px) {
  .head { padding: 16px 16px 0; height: 60px; }
  .all { display: none; }
  .chip { margin-left: auto; }
  .list { grid-template-columns: 1fr; padding: 0 16px; }
  .list li:nth-child(odd) { border-right: 0; }
  .list li + li { border-top: 1px solid var(--rule); }
  .list li .item { padding: 14px 0 !important; }
  .title { font-size: 14px; }
  .item { row-gap: 8px; }
}
</style>
