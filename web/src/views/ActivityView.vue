<script setup lang="ts">
// Activity: live feed of recent calls and the searchable audit log.
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Activity } from 'lucide-vue-next'
import { live } from '@/lib/live'
import { activeClients } from '@/lib/status'
import { plural } from '@/lib/format'
import { openCreateKey } from '@/lib/dialogs'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiSegmented from '@/components/ui/UiSegmented.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import CallTable from '@/components/activity/CallTable.vue'
import AuditLog from '@/components/activity/AuditLog.vue'
import AdminLog from '@/components/activity/AdminLog.vue'

const route = useRoute()
const router = useRouter()

const tab = computed({
  get: () => (route.query.tab === 'audit' ? 'audit' : route.query.tab === 'admin' && !live.authOff ? 'admin' : 'live'),
  set: (v: string) => router.replace({ query: { ...route.query, tab: v } }),
})
// The admin log only exists with sign-in on.
const tabs = computed(() => [
  { value: 'live', label: 'Live', dot: true },
  { value: 'audit', label: 'Audit log' },
  ...(live.authOff ? [] : [{ value: 'admin', label: 'Admin log' }]),
])

const clients = computed(() => activeClients(live.keys, live.calls).length)
</script>

<template>
  <PageHeader title="Activity" />

  <div class="bar">
    <UiSegmented v-model="tab" :options="tabs" label="Activity view" />
    <span v-if="tab === 'live'" class="side mono muted">{{ plural(clients, 'client') }} connected</span>
    <span v-else-if="tab === 'audit'" class="side mono muted">Stored for 30 days</span>
    <span v-else class="side mono muted">Changes made by admins</span>
  </div>

  <template v-if="tab === 'live'">
    <UiNotice v-if="live.connection === 'live'" tone="ok" class="strip">
      <span class="strip-text"><span class="dot" aria-hidden="true" />Streaming calls from all connected clients</span>
    </UiNotice>
    <UiNotice v-else tone="warn" class="strip">
      <span class="strip-text"><span class="dot" aria-hidden="true" />Reconnecting…</span>
    </UiNotice>

    <p class="caption muted">Latest calls · newest first</p>
    <section class="panel" aria-label="Latest calls">
      <SkeletonRows v-if="!live.loaded.calls" :rows="5" />
      <EmptyState
        v-else-if="!live.calls.length"
        :icon="Activity"
        title="No calls yet"
        text="Calls appear when an agent uses a tool."
        :action="live.authOff ? undefined : 'Connect a client'"
        @action="openCreateKey('client')"
      />
      <CallTable v-else :calls="live.calls" />
    </section>
  </template>
  <AuditLog v-else-if="tab === 'audit'" />
  <AdminLog v-else />
</template>

<style scoped>
.bar { display: flex; align-items: center; justify-content: space-between; gap: 16px; }
.side { font-size: 12px; padding-right: 8px; }
.strip { margin-top: 28px; padding-top: 14px; padding-bottom: 14px; }
.strip-text { display: inline-flex; align-items: center; gap: 12px; font-size: 13px; }
.dot { width: 6px; height: 6px; border-radius: 50%; background: currentColor; flex: none; }
.caption { margin: 24px 0 14px; font-size: 12px; }
</style>
