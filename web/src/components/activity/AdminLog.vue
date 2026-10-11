<script setup lang="ts">
// Admin log tab: who changed what on the hub (sign-ins, keys, groups, trust,
// approvals). Newest first, paged with "Load more".
import { onBeforeUnmount, onMounted, ref } from 'vue'
import { ScrollText } from 'lucide-vue-next'
import { api } from '@/lib/api'
import { dayTime } from '@/lib/format'
import { live, useNow } from '@/lib/live'
import type { AdminLogEntry } from '@/lib/types'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import { actionLabel, actorLabel, isFailure, targetView } from './adminlog'

const now = useNow(30_000)
const entries = ref<AdminLogEntry[]>([])
const next = ref(0)
const loading = ref(true)
const paging = ref(false)
const error = ref('')
let alive = true
onBeforeUnmount(() => { alive = false })

async function load(more = false) {
  if (more) paging.value = true
  else { loading.value = true; error.value = '' }
  try {
    const page = await api.adminLog(more ? { before_id: next.value } : {})
    if (!alive) return
    entries.value = more ? [...entries.value, ...page.entries] : page.entries
    next.value = page.next_before_id
    error.value = ''
  } catch (e) {
    if (!alive) return
    error.value = e instanceof Error ? e.message : 'Could not load the admin log.'
    if (!more) entries.value = []
  } finally {
    if (alive) { loading.value = false; paging.value = false }
  }
}
onMounted(() => load())
</script>

<template>
  <UiNotice v-if="error" tone="err" class="err">
    {{ error }} <button type="button" class="arrow-link retry" @click="load()">Try again</button>
  </UiNotice>
  <section class="panel" aria-label="Admin log">
    <SkeletonRows v-if="loading" :rows="6" />
    <EmptyState v-else-if="!entries.length && !error" :icon="ScrollText" title="Nothing logged yet" text="Sign-ins and changes to keys, groups, trust levels and approvals appear here." />
    <div v-else-if="entries.length" class="wrap">
      <table class="table">
        <thead>
          <tr>
            <th>Time</th>
            <th>Who</th>
            <th>Action</th>
            <th>Target / detail</th>
            <th class="hide-s">IP</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in entries" :key="e.id" :class="{ failed: isFailure(e) }">
            <td class="time mono">{{ dayTime(e.time, now) }}</td>
            <td><span class="truncate who" :class="{ cli: e.actor === 'cli' }" :title="actorLabel(e.actor)">{{ actorLabel(e.actor) }}</span></td>
            <td class="act">{{ actionLabel(e.action) }}</td>
            <td>
              <span class="target truncate" :class="{ mono: targetView(e, live.keys).mono }" :title="targetView(e, live.keys).text">{{ targetView(e, live.keys).text }}</span>
            </td>
            <td class="hide-s mono muted ip">{{ e.ip || '—' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-if="next" class="more">
        <UiButton :loading="paging" @click="load(true)">Load more</UiButton>
      </div>
    </div>
  </section>
</template>

<style scoped>
.err { margin-bottom: 16px; }
.retry { border: 0; background: none; padding: 0; cursor: pointer; margin-left: 8px; }
.wrap { overflow-x: auto; }
.table td { padding-top: 14px; padding-bottom: 14px; font-size: 13px; }
.time { font-size: 12px; white-space: nowrap; }
.who { display: block; max-width: 160px; }
.who.cli { font-style: italic; color: var(--muted); }
.act { white-space: nowrap; }
.target { display: block; max-width: 340px; font-size: 12px; }
.ip { font-size: 12px; white-space: nowrap; }
tr.failed td { background: var(--err-soft); }
.more { display: flex; justify-content: center; padding: 20px; border-top: 1px solid var(--rule); }
@media (max-width: 700px) {
  .hide-s { display: none; }
  .target { max-width: 140px; }
}
</style>
