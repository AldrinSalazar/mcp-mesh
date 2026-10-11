<script setup lang="ts">
// One key: who it is, its groups, and (for client keys) every tool it can
// actually use right now.
import { computed, ref } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { KeyRound } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiBadge from '@/components/ui/UiBadge.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import RevokeKeyDialog from '@/components/keys/RevokeKeyDialog.vue'
import EffectiveTools from '@/components/keydetail/EffectiveTools.vue'
import KeyGroups from '@/components/keydetail/KeyGroups.vue'
import { ago, dayTime } from '@/lib/format'
import { live, refresh, useNow } from '@/lib/live'
import { keyConnection } from '@/lib/status'

const route = useRoute()
const router = useRouter()
const now = useNow()

const key = computed(() => live.keys.find((k) => k.id === route.params.id))
const conn = computed(() => (key.value ? keyConnection(key.value, live.nodes, now.value) : null))
const revoking = ref(false)

// The dialog closes on cancel and on success alike; a revoked key is gone
// from the list once the keys are refreshed.
async function closeRevoke() {
  revoking.value = false
  await refresh('keys')
  if (!key.value) await router.push({ name: 'keys' })
}
</script>

<template>
  <PageHeader :title="key?.name ?? 'Key'">
    <UiButton v-if="key" variant="danger" @click="revoking = true">Revoke key</UiButton>
  </PageHeader>
  <p class="back"><RouterLink to="/keys" class="muted">← All keys</RouterLink></p>

  <SkeletonRows v-if="!live.loaded.keys" :rows="3" />

  <div v-else-if="!key" class="panel empty">
    <EmptyState :icon="KeyRound" title="No such key" text="It may have been revoked." action="Back to keys" @action="router.push({ name: 'keys' })" />
  </div>

  <template v-else>
    <section class="panel basics">
      <div class="badges">
        <UiBadge :tone="key.kind === 'node' ? 'idle' : 'info'">{{ key.kind === 'node' ? 'Node key' : 'Client key' }}</UiBadge>
        <UiBadge v-if="key.oauth_client" tone="info" wide>via OAuth · {{ key.oauth_client.name }}</UiBadge>
        <UiBadge v-if="conn" :tone="conn.tone">{{ conn.label }}</UiBadge>
      </div>
      <dl>
        <div><dt class="label">Key ID</dt><dd class="mono">{{ key.id }}</dd></div>
        <div><dt class="label">Created</dt><dd>{{ dayTime(key.created, now) }}</dd></div>
        <div><dt class="label">Last used</dt><dd>{{ key.last_used ? ago(key.last_used, now) : 'Never' }}</dd></div>
        <div><dt class="label">Last IP</dt><dd class="mono">{{ key.last_ip || '–' }}</dd></div>
      </dl>
      <p v-if="key.kind === 'node'" class="node muted">
        This key connects a machine to the hub. Node keys don’t belong to groups.
        <RouterLink :to="{ name: 'nodes', query: { node: key.name } }" class="arrow-link">View node {{ key.name }}</RouterLink>
      </p>
    </section>

    <template v-if="key.kind === 'client'">
      <KeyGroups :key-info="key" />
      <EffectiveTools :key-info="key" />
    </template>
  </template>

  <RevokeKeyDialog v-if="revoking && key" :key-info="key" @close="closeRevoke" />
</template>

<style scoped>
.back { margin: -20px 0 24px; font-size: 13px; }
.empty { padding: 8px; }
.basics { padding: 24px; }
.badges { display: flex; gap: 10px; flex-wrap: wrap; margin-bottom: 24px; }
dl { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 24px; }
dt { margin-bottom: 8px; }
dd { margin: 0; font-size: 14px; overflow-wrap: anywhere; }
dd.mono { font-size: 13px; }
.node { margin-top: 24px; font-size: 13px; display: flex; gap: 16px; flex-wrap: wrap; align-items: center; }
@media (max-width: 700px) {
  dl { grid-template-columns: 1fr 1fr; }
}
</style>
