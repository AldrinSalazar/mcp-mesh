<script setup lang="ts">
// Nodes: every machine in the mesh; expand one to see its MCPs.
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { Monitor, Plus } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiSelect from '@/components/ui/UiSelect.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import NodeCard from '@/components/nodes/NodeCard.vue'
import RevokeNodeKeyDialog from '@/components/nodes/RevokeNodeKeyDialog.vue'
import ForgetNodeDialog from '@/components/nodes/ForgetNodeDialog.vue'
import { filterNodes, type StateFilter } from '@/components/nodes/filter'
import { live, useNow } from '@/lib/live'
import { openCreateKey } from '@/lib/dialogs'
import type { Node } from '@/lib/types'

const route = useRoute()
const now = useNow()

const query = ref('')
const state = ref<StateFilter>('')
const open = reactive(new Set<string>())
const revoking = ref<{ node: string; keyId: string } | null>(null)
const forgetting = ref<string | null>(null)

const stateOptions = [
  { value: '', label: 'All states' },
  { value: 'online', label: 'Online' },
  { value: 'offline', label: 'Offline' },
  { value: 'attention', label: 'Needs attention' },
]

const nodes = computed(() => filterNodes(live.nodes as Node[], query.value, state.value))
const online = computed(() => live.nodes.filter((n) => n.online).length)

function toggle(name: string) {
  if (open.has(name)) open.delete(name)
  else open.add(name)
}

// Open the node named in ?node= and bring it into view; with nothing asked
// for, a hub with exactly one machine opens it. Waits for the first list.
let defaulted = false
watch(
  () => [live.loaded.nodes, route.query.node] as const,
  async ([loaded, wanted]) => {
    if (!loaded) return
    if (typeof wanted === 'string' && wanted) {
      if (!live.nodes.some((n) => n.name === wanted)) return
      open.add(wanted)
      // The card must be rendered expanded before scrolling to it.
      query.value = ''
      state.value = ''
      await nextTick()
      document.getElementById(`node-${wanted}`)?.scrollIntoView({ block: 'start', behavior: 'smooth' })
      return
    }
    if (!defaulted && live.nodes.length === 1) {
      const only = live.nodes[0]!
      if (only.online) open.add(only.name)
    }
    defaulted = true
  },
  { immediate: true },
)
</script>

<template>
  <PageHeader title="Nodes">
    <UiButton v-if="!live.authOff" variant="primary" :icon="Plus" @click="openCreateKey('node')">Connect a node</UiButton>
  </PageHeader>

  <template v-if="live.loaded.nodes && live.nodes.length === 0">
    <EmptyState
      framed
      :icon="Monitor"
      title="No machines yet"
      text="Connect a node to share its tools."
      :action="live.authOff ? undefined : 'Create node key'"
      @action="openCreateKey('node')"
    />
  </template>

  <template v-else>
    <div class="toolbar">
      <div class="search">
        <label for="node-search" class="visually-hidden">Search machines</label>
        <UiInput id="node-search" v-model="query" search placeholder="Search machines..." autocomplete="off" />
      </div>
      <div class="state"><UiSelect v-model="state" :options="stateOptions" label="Filter by state" /></div>
      <span class="total mono muted">{{ online }} online / {{ live.nodes.length }} total</span>
    </div>

    <div v-if="!live.loaded.nodes" class="panel"><SkeletonRows :rows="3" /></div>
    <div v-else-if="nodes.length === 0" class="panel">
      <EmptyState :icon="Monitor" title="No machines match" text="Try a different search or state." />
    </div>
    <div v-else class="list">
      <NodeCard
        v-for="n in nodes"
        :key="n.name"
        :node="n"
        :expanded="open.has(n.name)"
        :now="now"
        @toggle="toggle(n.name)"
        @revoke="revoking = { node: n.name, keyId: n.key_id }"
        @forget="forgetting = n.name"
      />
    </div>
  </template>

  <RevokeNodeKeyDialog v-if="revoking" :node="revoking.node" :key-id="revoking.keyId" @close="revoking = null" />
  <ForgetNodeDialog v-if="forgetting" :node="forgetting" @close="forgetting = null" />
</template>

<style scoped>
.toolbar { display: flex; align-items: center; gap: 16px; margin-bottom: 24px; }
.search { flex: 0 1 368px; min-width: 0; }
.state { flex: 0 0 190px; }
.total { margin-left: auto; font-size: 12px; white-space: nowrap; padding-right: 36px; }
.list { display: flex; flex-direction: column; gap: 16px; }
@media (max-width: 700px) {
  .toolbar { flex-wrap: wrap; }
  .search { flex: 1 1 100%; }
  .state { flex: 1; }
  .total { padding-right: 0; }
}
</style>
