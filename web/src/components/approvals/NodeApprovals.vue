<script setup lang="ts">
// One node's pending tools, grouped by MCP, with the node-level approval
// when the node itself is still waiting.
import { computed } from 'vue'
import { Monitor, TriangleAlert } from 'lucide-vue-next'
import type { Node, PendingTool } from '@/lib/types'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { plural } from '@/lib/format'
import { trustLabel } from '@/lib/trust'
import PendingRow from './PendingRow.vue'
import type { NodeGroup } from './group'

const props = defineProps<{ group: NodeGroup; node?: Node; now: number; busy: string }>()
defineEmits<{ approveNode: []; approveMcp: [mcp: string]; approveTool: [tool: PendingTool] }>()

// The node's own state decides; a node missing from the list but with
// node_pending tools is treated as waiting too.
const waiting = computed(() =>
  props.node ? !props.node.trust.approved : props.group.mcps.some((m) => m.tools.some((t) => t.state === 'node_pending')),
)
</script>

<template>
  <section class="panel node" :aria-label="`Approvals for ${group.node}`">
    <header class="head">
      <Monitor :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
      <RouterLink :to="{ path: '/nodes', query: { node: group.node } }" class="name truncate" :title="group.node">{{ group.node }}</RouterLink>
      <UiBadge v-if="node" tone="idle" class="level">{{ trustLabel(node.trust.level) }}</UiBadge>
      <span class="count muted">{{ plural(group.count, 'tool') }} waiting</span>
    </header>

    <div v-if="waiting" class="nodewait">
      <UiNotice tone="warn" :icon="TriangleAlert" class="notice">
        <span class="msg">{{ group.node }} waits for approval. None of its tools are available until you approve it. Approving it here also approves the {{ plural(group.count, 'tool') }} listed below, as shown.</span>
      </UiNotice>
      <UiButton variant="primary" :loading="busy === `node/${group.node}`" @click="$emit('approveNode')">Approve node</UiButton>
    </div>

    <div v-for="m in group.mcps" :key="m.mcp" class="mcp">
      <div class="mcphead">
        <span class="label mcpname truncate" :title="m.mcp">{{ m.mcp }}</span>
        <UiButton
          :loading="busy === `mcp/${group.node}/${m.mcp}`"
          :disabled="waiting"
          :title="waiting ? 'Approve the node first' : undefined"
          :aria-label="`Approve all ${m.tools.length} tools of ${m.mcp}`"
          @click="$emit('approveMcp', m.mcp)"
        >Approve all {{ m.tools.length }}</UiButton>
      </div>
      <ul class="tools">
        <PendingRow
          v-for="t in m.tools"
          :key="t.name"
          :tool="t"
          :now="now"
          :locked="waiting"
          :busy="busy === `tool/${t.name}`"
          @approve="$emit('approveTool', t)"
        />
      </ul>
    </div>
  </section>
</template>

<style scoped>
.node { padding-bottom: 8px; }
.head { display: flex; align-items: center; gap: 14px; padding: 20px 24px; min-width: 0; }
.icon { color: var(--muted); flex: none; }
.name { font-family: var(--font-display); font-size: 20px; font-weight: 500; color: var(--ink); }
.name:hover { text-decoration: underline; }
.level { min-width: 0; }
.count { margin-left: auto; font-size: 12px; white-space: nowrap; }
.nodewait { display: flex; align-items: center; gap: 16px; margin: 0 24px 16px; }
.notice { flex: 1; }
.msg { font-size: 13px; }
.mcphead { display: flex; align-items: center; justify-content: space-between; gap: 12px; padding: 14px 24px 12px; }
.mcpname { max-width: 60%; }
.tools { list-style: none; margin: 0; padding: 0; }
@media (max-width: 700px) {
  .head { padding: 16px; flex-wrap: wrap; }
  .nodewait { flex-direction: column; align-items: stretch; margin: 0 16px 16px; }
  .mcphead { padding: 12px 16px; }
  .count { margin-left: 0; flex-basis: 100%; }
}
</style>
