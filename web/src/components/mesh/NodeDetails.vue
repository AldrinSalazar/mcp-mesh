<script setup lang="ts">
// Right-hand panel of the mesh map: what the selected node is and what it shares.
import { computed } from 'vue'
import { ArrowRight } from 'lucide-vue-next'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { host, osLabel, plural, since } from '@/lib/format'
import { mcpStatus, nodeStatus } from '@/lib/status'
import { trustLabel } from '@/lib/trust'
import type { Node } from '@/lib/types'
import { isPerClient } from '@/components/nodes/mcpinfo'
import McpProblems from '@/components/nodes/McpProblems.vue'

const props = defineProps<{ node: Node; now: number }>()
defineEmits<{ view: [] }>()

const status = computed(() => nodeStatus(props.node))
const tools = computed(() => props.node.mcps.reduce((s, m) => s + m.tools, 0))
const rows = computed(() => props.node.mcps.map((m) => ({ mcp: m, status: mcpStatus(m, props.node.online) })))
// Apps whose MCP isn't reachable; the app name is the MCP name, capitalised.
const waiting = computed(() => rows.value.filter((r) => r.status.label === 'Waiting for app').map((r) => cap(r.mcp.name)))

function cap(s: string): string {
  return s ? s[0]!.toUpperCase() + s.slice(1) : s
}
</script>

<template>
  <div class="details">
    <h2 class="name truncate" :title="node.name">{{ node.name }}</h2>
    <UiBadge :tone="status.tone" class="state">{{ status.label }}</UiBadge>
    <p class="counts muted">{{ plural(tools, 'tool') }} · {{ plural(node.mcps.length, 'MCP server') }}</p>

    <dl class="facts">
      <div><dt>OS</dt><dd>{{ osLabel(node.os) }}</dd></div>
      <div><dt>Address</dt><dd class="mono">{{ host(node.remote_addr) || '-' }}</dd></div>
      <div><dt>Node version</dt><dd>v{{ node.node_version }}</dd></div>
      <div><dt>Trust level</dt><dd>{{ trustLabel(node.trust.level) }}</dd></div>
      <div v-if="node.pending_tools > 0 || !node.trust.approved">
        <dt>Approval</dt>
        <dd><RouterLink to="/approvals" class="warnlink">{{ node.trust.approved ? `${node.pending_tools} waiting` : 'Node not approved' }}</RouterLink></dd>
      </div>
      <div v-if="node.online"><dt>Connected</dt><dd>{{ since(node.connected_at, now) }}</dd></div>
      <div v-else-if="node.disconnected_at"><dt>Offline for</dt><dd>{{ since(node.disconnected_at, now) }}</dd></div>
    </dl>

    <h3 class="section-title">MCP servers</h3>
    <ul v-if="rows.length" class="mcps">
      <li v-for="r in rows" :key="r.mcp.name">
        <span class="mcp-name truncate" :title="r.mcp.name">{{ r.mcp.name }}</span>
        <span class="right">
          <span v-if="isPerClient(r.mcp)" class="pc muted">per-client</span>
          <UiBadge :tone="r.status.tone">{{ r.status.label }}</UiBadge>
        </span>
      </li>
    </ul>
    <p v-else class="muted none">No MCP servers.</p>

    <McpProblems v-for="r in rows" :key="`p-${r.mcp.name}`" :mcp="r.mcp" named />

    <UiNotice v-for="w in waiting" :key="w" tone="warn" class="notice">
      {{ w }} is waiting for the app. Open {{ w }} with its MCP add-on enabled.
    </UiNotice>

    <UiButton block :icon="ArrowRight" class="view" @click="$emit('view')">View node</UiButton>
  </div>
</template>

<style scoped>
.details { display: flex; flex-direction: column; align-items: flex-start; padding: 24px; }
.name { font-size: 28px; line-height: 1.2; max-width: 100%; }
.state { margin-top: 10px; }
.counts { margin: 20px 0 22px; font-size: 13px; }
.facts { width: 100%; margin: 0 0 30px; padding: 4px 0; border-top: 1px solid var(--rule); border-bottom: 1px solid var(--rule); }
.facts div { display: grid; grid-template-columns: 116px 1fr; align-items: center; height: 43px; font-size: 12px; }
.facts dt { color: var(--muted); }
.facts dd { margin: 0; }
.warnlink { color: var(--warn); font-weight: 500; }
.facts dd.mono { font-size: 13px; }
.section-title { margin-bottom: 14px; }
.mcps { list-style: none; margin: 0; padding: 0; width: 100%; }
.mcps li { display: flex; align-items: center; justify-content: space-between; gap: 12px; height: 46px; font-size: 13px; }
.none { font-size: 13px; }
.right { display: inline-flex; align-items: center; gap: 10px; flex: none; }
.pc { font-size: 12px; }
.notice { width: 100%; margin-top: 20px; }
.view { margin-top: 36px; }
</style>
