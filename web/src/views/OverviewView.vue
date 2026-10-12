<script setup lang="ts">
// Overview: the hub at a glance. Metrics, what needs attention, recent calls;
// a fresh hub with no nodes gets the connect guide instead.
import { computed } from 'vue'
import { Activity, CircleDot, Code, Plus } from 'lucide-vue-next'
import { live, useNow } from '@/lib/live'
import { openCreateKey } from '@/lib/dialogs'
import { problems } from '@/lib/status'
import { plural } from '@/lib/format'
import PageHeader from '@/components/ui/PageHeader.vue'
import UiButton from '@/components/ui/UiButton.vue'
import MetricCard from '@/components/overview/MetricCard.vue'
import AttentionPanel from '@/components/overview/AttentionPanel.vue'
import RecentCalls from '@/components/overview/RecentCalls.vue'
import ConnectGuide from '@/components/overview/ConnectGuide.vue'
import { approvalItems, callsLine, groupsLine, metrics, nodesLine } from '@/components/overview/summary'

const now = useNow()

const loaded = computed(() => live.loaded.nodes)
const fresh = computed(() => loaded.value && live.nodes.length === 0)
const m = computed(() => metrics(live.nodes, live.tools))
const issues = computed(() => problems(live.nodes))
const waiting = computed(() => approvalItems(live.nodes, live.approvals, live.pairings))
const calls = computed(() => callsLine(live.stats))
const recent = computed(() => live.calls.slice(0, 5))
const offline = computed(() => m.value.total - m.value.online)
const mcpSub = computed(() => groupsLine(m.value.groups) || 'No MCPs yet')
// On a phone the cards are narrow; show only the group that needs a look.
const mcpShort = computed(() => (m.value.groups.waiting ? `${m.value.groups.waiting} waiting` : m.value.groups.error ? `${m.value.groups.error} in error` : undefined))
</script>

<template>
  <PageHeader title="Overview">
    <UiButton v-if="!live.authOff && !fresh" class="desktop-btn" variant="primary" :icon="Plus" @click="openCreateKey('node')">Connect something</UiButton>
  </PageHeader>

  <ConnectGuide v-if="fresh" :info="live.info" :auth-off="live.authOff" :pairing="live.settings?.pairing" :requests="live.pairings" @key="openCreateKey" />

  <template v-else>
    <div class="metrics">
      <MetricCard label="Nodes online" :value="m.online" :total="m.total" :sub="nodesLine(m)" :sub-short="offline ? `${offline} offline` : 'All online'" :loading="!loaded">
        <template #icon><CircleDot :size="24" :stroke-width="1.6" style="color: var(--ok)" /></template>
      </MetricCard>
      <MetricCard label="MCP servers" :value="m.mcps" :sub="mcpSub" :sub-short="mcpShort" :loading="!loaded">
        <template #icon><span class="sq" /></template>
      </MetricCard>
      <MetricCard label="Available tools" :value="live.tools.length" :sub="`Across ${plural(m.toolNodes, 'connected machine')}`" :sub-short="plural(m.toolNodes, 'machine')" :loading="!live.loaded.tools">
        <template #icon><Code :size="24" :stroke-width="1.6" /></template>
      </MetricCard>
      <MetricCard label="Calls / last hour" :value="live.stats?.calls_last_hour ?? 0" :sub="calls.text" :sub-short="calls.bad ? calls.text.split(' · ')[0] : undefined" :bad="calls.bad" :loading="!live.loaded.stats">
        <template #icon><Activity :size="24" :stroke-width="1.6" /></template>
      </MetricCard>
    </div>

    <AttentionPanel v-if="issues.length || waiting.length" :items="issues" :approvals="waiting" :now="now" class="gap" />

    <RecentCalls class="gap" :calls="recent" :loaded="live.loaded.calls" @connect-client="openCreateKey('client')" />

    <UiButton v-if="!live.authOff" class="mobile-btn" variant="primary" block :icon="Plus" @click="openCreateKey('node')">Connect something</UiButton>
  </template>
</template>

<style scoped>
.metrics { display: grid; grid-template-columns: repeat(4, 1fr); gap: 16px; }
.gap { margin-top: 24px; }
.sq { display: block; width: 24px; height: 24px; border: 1.6px solid var(--cobalt); background: linear-gradient(var(--cobalt), var(--cobalt)) center / 12px 12px no-repeat; }
.mobile-btn { display: none; margin-top: 24px; }

@media (max-width: 700px) {
  .desktop-btn { display: none; }
  .mobile-btn { display: inline-flex; }
  .metrics { grid-template-columns: 1fr 1fr; gap: 12px; }
  .gap { margin-top: 20px; }
}
</style>
