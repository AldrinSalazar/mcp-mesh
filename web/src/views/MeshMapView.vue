<script setup lang="ts">
// Mesh map: the hub in the middle, the machines around it, who is connected
// to it, and details for the machine you pick.
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Network } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import MeshCanvas from '@/components/mesh/MeshCanvas.vue'
import NodeDetails from '@/components/mesh/NodeDetails.vue'
import { hubName, live, useNow } from '@/lib/live'
import { activeClients } from '@/lib/status'
import { openCreateKey } from '@/lib/dialogs'
import { plural } from '@/lib/format'
import type { Call, Key, Node } from '@/lib/types'

const router = useRouter()
const now = useNow()
const picked = ref('')

// Online machines first (stable), so the default selection and the quadrants
// favour what is up.
const nodes = computed(() => ([...live.nodes] as Node[]).sort((a, b) => Number(b.online) - Number(a.online)))
const online = computed(() => nodes.value.filter((n) => n.online).length)
const clients = computed(() => activeClients(live.keys as Key[], live.calls as Call[], now.value))
const selected = computed(() => nodes.value.find((n) => n.name === picked.value) ?? nodes.value[0])
</script>

<template>
  <PageHeader title="Mesh map">
    <UiButton v-if="!live.authOff" variant="primary" :icon="Plus" @click="openCreateKey('node')">Connect</UiButton>
  </PageHeader>

  <div class="layout">
    <section class="panel map" aria-label="Mesh map">
      <header class="strip">
        <span class="muted">{{ online }} online · {{ nodes.length - online }} offline</span>
        <span class="chip">{{ plural(clients.length, 'client') }}</span>
      </header>
      <SkeletonRows v-if="!live.loaded.nodes" :rows="4" />
      <MeshCanvas
        v-else
        :nodes="nodes"
        :clients="clients"
        :selected="selected?.name ?? ''"
        :hub-name="hubName()"
        :now="now"
        @select="picked = $event"
      />
      <footer class="legend">
        <span><i class="sq" />Node</span>
        <span><i class="ci" />Hub</span>
        <span><i class="ci filled" />Client</span>
      </footer>
    </section>

    <aside class="panel side" aria-label="Selected node">
      <SkeletonRows v-if="!live.loaded.nodes" :rows="5" :avatar="false" />
      <NodeDetails v-else-if="selected" :node="selected" :now="now" @view="router.push({ path: '/nodes', query: { node: selected.name } })" />
      <EmptyState
        v-else
        :icon="Network"
        title="No machines yet"
        text="Connect a node to share its tools."
        :action="live.authOff ? undefined : 'Create node key'"
        @action="openCreateKey('node')"
      />
    </aside>
  </div>
</template>

<style scoped>
.layout { display: grid; grid-template-columns: minmax(0, 1fr) 320px; gap: 24px; align-items: start; }
.map { display: flex; flex-direction: column; }
.strip { display: flex; align-items: center; justify-content: space-between; height: 62px; padding: 0 24px; border-bottom: 1px solid var(--rule); font-size: 12px; }
.chip { display: inline-flex; align-items: center; height: 26px; padding: 0 10px; background: var(--rule); color: var(--muted); border-radius: var(--radius); font-size: 12px; }
.legend { display: flex; gap: 40px; align-items: center; margin: 0 24px; padding: 18px 0 20px; border-top: 1px solid var(--rule); font-size: 11px; color: var(--muted); }
.legend span { display: inline-flex; align-items: center; gap: 8px; }
.sq { width: 14px; height: 14px; border: 1px solid var(--muted); }
.ci { width: 16px; height: 16px; border-radius: 50%; border: 1.5px solid var(--cobalt); }
.ci.filled { background: var(--ink); border-color: var(--ink); width: 14px; height: 14px; }
@media (max-width: 1100px) {
  .layout { grid-template-columns: minmax(0, 1fr); }
}
</style>
