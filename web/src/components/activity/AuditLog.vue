<script setup lang="ts">
// Audit tab: a query over the stored calls (30 days), not a live feed, so it
// has filters, paging and a Refresh link.
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Activity } from 'lucide-vue-next'
import { api } from '@/lib/api'
import { live } from '@/lib/live'
import type { AuditEntry, Outcome } from '@/lib/types'
import UiSelect from '@/components/ui/UiSelect.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import CallTable from './CallTable.vue'
import { buildQuery, defaultFilter, isFiltered, rangeOptions, type AuditFilter, type Range } from './query'

const filter = reactive<AuditFilter>({ ...defaultFilter })
// The tool box applies on Enter/blur, not per keystroke.
const toolText = ref('')

const entries = ref<AuditEntry[]>([])
const next = ref(0)
const loading = ref(true)
const paging = ref(false)
const error = ref('')
// Guards against a slow earlier response overwriting a newer one.
let seq = 0
let alive = true
onBeforeUnmount(() => { alive = false })

async function load(more = false) {
  const id = ++seq
  if (more) paging.value = true
  else { loading.value = true; error.value = '' }
  try {
    const page = await api.audit(buildQuery(filter, more ? next.value : 0))
    if (!alive || id !== seq) return
    entries.value = more ? [...entries.value, ...page.entries] : page.entries
    next.value = page.next_before_id
    error.value = ''
  } catch (e) {
    if (!alive || id !== seq) return
    error.value = e instanceof Error ? e.message : 'Could not load the audit log.'
    if (!more) entries.value = []
  } finally {
    if (alive && id === seq) { loading.value = false; paging.value = false }
  }
}

onMounted(() => load())
watch(() => [filter.range, filter.client, filter.node, filter.mcp, filter.outcome, filter.tool], () => load())

function applyTool() {
  const v = toolText.value.trim()
  if (v !== filter.tool) filter.tool = v
}

const outcomeOptions: { value: Outcome | ''; label: string }[] = [
  { value: '', label: 'All outcomes' },
  { value: 'ok', label: 'OK' },
  { value: 'tool_error', label: 'Tool error' },
  { value: 'error', label: 'Error' },
]

// Options come from what the hub knows now plus what the results mention, so
// a deleted key or gone node can still be filtered on.
const clientOptions = computed(() => {
  const m = new Map<string, string>()
  for (const k of live.keys) if (k.kind === 'client') m.set(k.id, k.name)
  for (const e of entries.value) if (!m.has(e.client_id)) m.set(e.client_id, e.client || e.client_id)
  return [{ value: '', label: 'All clients' }, ...[...m].map(([value, label]) => ({ value, label }))]
})
const nodeOptions = computed(() => {
  const s = new Set([...live.nodes.map((n) => n.name), ...entries.value.map((e) => e.node)])
  if (filter.node) s.add(filter.node)
  return [{ value: '', label: 'All nodes' }, ...[...s].sort().map((v) => ({ value: v, label: v }))]
})
const mcpOptions = computed(() => {
  const s = new Set<string>()
  for (const n of live.nodes) if (!filter.node || n.name === filter.node) for (const m of n.mcps) s.add(m.name)
  for (const e of entries.value) if (!filter.node || e.node === filter.node) s.add(e.mcp)
  if (filter.mcp) s.add(filter.mcp)
  return [{ value: '', label: 'All MCPs' }, ...[...s].sort().map((v) => ({ value: v, label: v }))]
})

const errors = computed(() => entries.value.filter((e) => e.outcome !== 'ok').length)
const caption = computed(() => {
  const n = entries.value.length
  const c = `${n}${next.value ? '+' : ''} ${n === 1 && !next.value ? 'call' : 'calls'}`
  return `${c} · ${errors.value} ${errors.value === 1 ? 'error' : 'errors'}`
})
</script>

<template>
  <div class="filters">
    <UiSelect v-model="filter.range as string" :options="rangeOptions" label="Time range" @update:model-value="filter.range = $event as Range" />
    <UiSelect v-model="filter.client" :options="clientOptions" label="Client" />
    <UiSelect v-model="filter.node" :options="nodeOptions" label="Node" />
    <UiSelect v-model="filter.mcp" :options="mcpOptions" label="MCP" />
    <UiSelect v-model="filter.outcome as string" :options="outcomeOptions" label="Outcome" @update:model-value="filter.outcome = $event as Outcome | ''" />
    <div class="tool" @keydown.enter="applyTool" @focusout="applyTool">
      <UiInput v-model="toolText" search placeholder="Tool..." aria-label="Tool (exact exposed name)" />
    </div>
  </div>

  <p class="caption muted">
    <span v-if="!loading || entries.length">{{ caption }}</span>
    <span v-else>Loading…</span>
    <button type="button" class="refresh" :disabled="loading" @click="load()">Refresh</button>
  </p>

  <UiNotice v-if="error" tone="err" class="notice">{{ error }}</UiNotice>
  <section v-else class="panel" aria-label="Audit log">
    <SkeletonRows v-if="loading" :rows="5" />
    <EmptyState
      v-else-if="!entries.length && isFiltered(filter)"
      :icon="Activity"
      title="No calls match these filters"
      text="Try a wider time range or clear a filter."
    />
    <EmptyState v-else-if="!entries.length" :icon="Activity" title="No calls yet" text="Calls appear when an agent uses a tool." />
    <CallTable v-else :calls="entries" dated />
  </section>

  <div v-if="!loading && !error && next > 0" class="more">
    <UiButton :loading="paging" @click="load(true)">Load more calls</UiButton>
  </div>
</template>

<style scoped>
.filters { display: grid; grid-template-columns: repeat(5, minmax(0, 1fr)) minmax(0, 1.1fr); gap: 16px; margin-top: 28px; }
.caption { margin: 24px 0 14px; font-size: 12px; display: flex; gap: 14px; align-items: baseline; }
.refresh { border: 0; background: none; padding: 0; color: var(--cobalt); font-size: 12px; font-weight: 500; cursor: pointer; }
.refresh:disabled { opacity: 0.5; cursor: default; }
.refresh:hover:not(:disabled) { text-decoration: underline; }
.notice { margin-top: 4px; }
.more { display: flex; justify-content: center; margin-top: 28px; }

@media (max-width: 1100px) {
  .filters { grid-template-columns: repeat(3, minmax(0, 1fr)); }
}
@media (max-width: 700px) {
  .filters { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 10px; }
  .tool { grid-column: 1 / -1; }
}
</style>
