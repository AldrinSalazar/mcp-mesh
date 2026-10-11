<script setup lang="ts">
// Keys: every node and client key, who is using it, and how to revoke it.
import { computed, ref } from 'vue'
import { KeyRound, Monitor, Plus } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiInput from '@/components/ui/UiInput.vue'
import UiSegmented from '@/components/ui/UiSegmented.vue'
import CopyField from '@/components/ui/CopyField.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import { RouterLink } from 'vue-router'
import RevokeKeyDialog from '@/components/keys/RevokeKeyDialog.vue'
import { openCreateKey } from '@/lib/dialogs'
import { ago, date, shortID } from '@/lib/format'
import { live, useNow } from '@/lib/live'
import { keyConnection } from '@/lib/status'
import type { Key } from '@/lib/types'

const now = useNow()
const filter = ref('all')
const search = ref('')
const revoking = ref<Key | null>(null)

const filters = [
  { value: 'all', label: 'All keys' },
  { value: 'node', label: 'Node keys' },
  { value: 'client', label: 'Client keys' },
]

// live is readonly; keyConnection only reads.
// Group names of a client key, in the hub's group order.
const groupNames = (k: Key) => live.groups.filter((g) => (k.groups ?? []).includes(g.id)).map((g) => g.name)
const conn = (k: Key) => keyConnection(k, live.nodes, now.value)

// Node keys first, then clients, each by name.
const rows = computed(() => {
  const q = search.value.trim().toLowerCase()
  return live.keys
    .filter((k) => filter.value === 'all' || k.kind === filter.value)
    .filter((k) => !q || k.name.toLowerCase().includes(q) || k.id.toLowerCase().includes(q))
    .slice().sort((a, b) => (a.kind === b.kind ? a.name.localeCompare(b.name) : a.kind === 'node' ? -1 : 1))
})
</script>

<template>
  <PageHeader title="Keys">
    <UiButton variant="primary" :icon="Plus" @click="openCreateKey('node')">Create key</UiButton>
  </PageHeader>

  <div v-if="live.loaded.keys && live.keys.length === 0" class="panel empty">
    <EmptyState :icon="KeyRound" title="No keys yet" text="Create a key to connect your devices." action="Create key" @action="openCreateKey('node')" />
  </div>

  <template v-else>
    <div class="toolbar">
      <UiSegmented v-model="filter" :options="filters" label="Key type" />
      <div class="search"><UiInput v-model="search" search placeholder="Search keys..." autocomplete="off" /></div>
    </div>

    <div class="panel tablebox">
      <SkeletonRows v-if="!live.loaded.keys" :rows="5" />
      <table v-else-if="rows.length" class="table">
        <thead>
          <tr>
            <th>Name / ID</th>
            <th class="hide-s">Kind</th>
            <th class="hide-s">Groups</th>
            <th class="hide-s">Created</th>
            <th class="hide-s">Last used / IP</th>
            <th>Connection</th>
            <th><span class="visually-hidden">Actions</span></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="k in rows" :key="k.id">
            <td>
              <div class="name">
                <component :is="k.kind === 'node' ? Monitor : KeyRound" :size="22" :stroke-width="1.6" class="muted ic" aria-hidden="true" />
                <div class="who">
                  <RouterLink v-if="k.kind === 'client'" :to="{ name: 'key', params: { id: k.id } }" class="nm truncate" :title="k.name">{{ k.name }}</RouterLink>
                  <span v-else class="nm truncate" :title="k.name">{{ k.name }}</span>
                  <span class="mono muted" :title="k.id">{{ shortID(k.id) }}</span>
                  <UiBadge v-if="k.oauth_client" tone="info" class="oauth">OAuth · {{ k.oauth_client.name }}</UiBadge>
                </div>
              </div>
            </td>
            <td class="hide-s"><UiBadge :tone="k.kind === 'node' ? 'idle' : 'info'">{{ k.kind === 'node' ? 'Node' : 'Client' }}</UiBadge></td>
            <td class="hide-s">
              <template v-if="k.kind === 'client'">
                <div v-if="groupNames(k).length" class="chips">
                  <span v-for="n in groupNames(k)" :key="n" class="chip truncate" :title="n">{{ n }}</span>
                </div>
                <span v-else class="nogroup">No groups</span>
              </template>
              <span v-else class="muted">–</span>
            </td>
            <td class="hide-s muted small">{{ date(k.created) }}</td>
            <td class="hide-s">
              <div class="used">
                <span>{{ k.last_used ? ago(k.last_used, now) : 'Never' }}</span>
                <span class="mono muted">{{ k.last_ip || '–' }}</span>
              </div>
            </td>
            <td>
              <UiBadge :tone="conn(k).tone">{{ conn(k).label }}</UiBadge>
            </td>
            <td class="act"><button type="button" class="revoke" @click="revoking = k">Revoke</button></td>
          </tr>
        </tbody>
      </table>
      <EmptyState v-else title="No matching keys" text="Try a different name or filter." />
    </div>
  </template>

  <section class="panel addr">
    <h2 class="section-title">Connection addresses</h2>
    <p class="muted sub">Use a key with these addresses to join your mesh.</p>
    <div class="cols">
      <div>
        <p class="label">Node URL</p>
        <CopyField :value="live.info?.node_url ?? ''" label="Copy node URL" />
      </div>
      <div>
        <p class="label">MCP URL</p>
        <CopyField :value="live.info?.client_url ?? ''" label="Copy MCP URL" />
      </div>
    </div>
    <div v-if="live.info?.oauth" class="oauth-box">
      <p class="label">Apps that support OAuth</p>
      <p class="muted sub2">In claude.ai, Claude on your phone and other apps that add MCP servers by URL, add the MCP URL. You’ll sign in here to approve it and choose its groups.</p>
      <CopyField :value="live.info?.client_url ?? ''" label="Copy MCP URL for OAuth apps" />
    </div>
  </section>

  <RevokeKeyDialog v-if="revoking" :key-info="revoking" @close="revoking = null" />
</template>

<style scoped>
.toolbar { display: flex; justify-content: space-between; align-items: center; gap: 16px; margin-bottom: 24px; flex-wrap: wrap; }
.search { width: 456px; max-width: 100%; }
.tablebox { margin-bottom: 24px; overflow-x: auto; }
.empty { padding: 8px; }
.table td { padding-top: 16px; padding-bottom: 16px; }
.name { display: flex; align-items: center; gap: 16px; min-width: 0; }
.ic { flex: none; }
.who { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.nm { font-size: 14px; max-width: 240px; }
a.nm { color: var(--ink); }
a.nm:hover { color: var(--cobalt); text-decoration: underline; }
.oauth { min-width: 0; align-self: flex-start; height: 22px; font-size: 11px; }
.chips { display: flex; flex-wrap: wrap; gap: 6px; max-width: 240px; }
.chip { max-width: 140px; padding: 3px 8px; font-size: 12px; background: var(--rule); border-radius: var(--radius); }
.nogroup { font-size: 12px; color: var(--warn); }
.oauth-box { margin-top: 28px; padding-top: 24px; border-top: 1px solid var(--rule); max-width: 640px; }
.oauth-box .label { margin-bottom: 8px; }
.sub2 { font-size: 13px; margin-bottom: 14px; }
.used { display: flex; flex-direction: column; gap: 6px; font-size: 13px; }
.act { text-align: right; }
.revoke { border: 0; background: none; padding: 0; font-size: 12px; color: var(--err); cursor: pointer; }
.revoke:hover { text-decoration: underline; }
.addr { padding: 24px; }
.sub { font-size: 13px; margin: 8px 0 28px; }
.cols { display: grid; grid-template-columns: 1fr 1fr; gap: 24px; }
.cols .label { margin-bottom: 10px; }
@media (max-width: 700px) {
  .hide-s { display: none; }
  .cols { grid-template-columns: 1fr; }
  .toolbar { flex-direction: column; align-items: stretch; }
  .search { width: 100%; }
}
</style>
