<script setup lang="ts">
// Last five calls as a table on desktop; on a phone only the latest call, as a card.
import { Activity } from 'lucide-vue-next'
import type { Call } from '@/lib/types'
import { clock, duration } from '@/lib/format'
import { outcomeStatus } from '@/lib/status'
import UiBadge from '@/components/ui/UiBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'

defineProps<{ calls: Call[]; loaded: boolean }>()
const emit = defineEmits<{ connectClient: [] }>()
</script>

<template>
  <div>
  <section class="panel recent desktop-only" aria-labelledby="recent-title">
    <header class="head">
      <h2 id="recent-title" class="section-title">Recent calls</h2>
      <RouterLink class="arrow-link all" to="/activity">All activity</RouterLink>
    </header>
    <SkeletonRows v-if="!loaded" :rows="4" :avatar="false" />
    <EmptyState v-else-if="!calls.length" :icon="Activity" title="No calls yet" text="Calls appear when an agent uses a tool." action="Connect a client" @action="emit('connectClient')" />
    <table v-else class="table">
      <thead>
        <tr><th>Time</th><th>Client</th><th>Tool</th><th class="dur">Duration</th><th>Outcome</th></tr>
      </thead>
      <tbody>
        <tr v-for="(c, i) in calls" :key="`${c.time}-${c.tool}-${i}`">
          <td class="mono muted time">{{ clock(c.time) }}</td>
          <td class="client truncate" :title="c.client || c.client_id">{{ c.client || c.client_id }}</td>
          <td class="tool mono truncate" :title="c.tool">{{ c.tool }}</td>
          <td class="mono muted dur">{{ duration(c.duration_ms) }}</td>
          <td class="out"><UiBadge wide :tone="outcomeStatus(c.outcome).tone">{{ outcomeStatus(c.outcome).label }}</UiBadge></td>
        </tr>
      </tbody>
    </table>
  </section>

  <section class="panel latest mobile-only" aria-labelledby="latest-title">
    <SkeletonRows v-if="!loaded" :rows="1" :avatar="false" />
    <EmptyState v-else-if="!calls.length" :icon="Activity" title="No calls yet" text="Calls appear when an agent uses a tool." action="Connect a client" @action="emit('connectClient')" />
    <template v-else>
      <header class="lhead">
        <h2 id="latest-title" class="section-title">Latest call</h2>
        <UiBadge :tone="outcomeStatus(calls[0]!.outcome).tone">{{ outcomeStatus(calls[0]!.outcome).label }}</UiBadge>
      </header>
      <p class="ltool mono">{{ calls[0]!.tool }}</p>
      <p class="lmeta small muted">{{ calls[0]!.client || calls[0]!.client_id }} · {{ clock(calls[0]!.time) }} · {{ duration(calls[0]!.duration_ms) }}</p>
    </template>
  </section>
  </div>
</template>

<style scoped>
.recent { padding-bottom: 20px; }
.head { display: flex; align-items: center; padding: 0 24px; height: 64px; }
.all { margin-left: auto; margin-right: 52px; }
.table { table-layout: fixed; }
.table td { padding: 14px 12px; }
.table th:nth-child(1) { width: 129px; }
.table th:nth-child(2) { width: 200px; }
.table th:nth-child(4) { width: 116px; }
.table th:nth-child(5) { width: 136px; }
.table td.out { padding-right: 24px; }
.table th, .table td { padding-left: 0; }
.table th:first-child, .table td:first-child { padding-left: 24px; }
.table td { font-size: 13px; }
.mono { font-size: 12px; }
.mobile-only { display: none; }

@media (max-width: 700px) {
  .desktop-only { display: none; }
  .mobile-only { display: block; padding: 18px 16px 20px; }
  .lhead { display: flex; align-items: center; justify-content: space-between; margin-bottom: 14px; }
  .ltool { font-size: 13px; overflow-wrap: anywhere; line-height: 1.5; }
  .lmeta { margin-top: 16px; }
}
</style>
