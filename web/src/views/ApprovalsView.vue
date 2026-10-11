<script setup lang="ts">
// Approvals: tools (and whole nodes) held back by a node's trust level until
// an admin approves them. Changed tools show what changed.
import { computed, ref } from 'vue'
import { ShieldCheck } from 'lucide-vue-next'
import PageHeader from '@/components/ui/PageHeader.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SkeletonRows from '@/components/ui/SkeletonRows.vue'
import NodeApprovals from '@/components/approvals/NodeApprovals.vue'
import { groupApprovals, type NodeGroup } from '@/components/approvals/group'
import { mcpRefs, nodeRefs, refOf, reportApproveError } from '@/components/approvals/approve'
import type { PendingTool } from '@/lib/types'
import { api } from '@/lib/api'
import { live, refresh, useNow } from '@/lib/live'
import { plural } from '@/lib/format'
import { toast } from '@/lib/toast'

const now = useNow()
const busy = ref('')

const groups = computed(() => groupApprovals(live.approvals))

async function run(key: string, call: () => Promise<unknown>, done: string) {
  busy.value = key
  try {
    await call()
    toast(done)
    await Promise.all([refresh('approvals'), refresh('nodes')])
  } catch (e) {
    await reportApproveError(e, { toast, refresh })
  } finally {
    busy.value = ''
  }
}

// Every call lists the tools as rendered now, with the hash the admin saw.
const approveNode = (g: NodeGroup) =>
  run(`node/${g.node}`, () => api.approveNode(g.node, nodeRefs(g).map(({ mcp, tool, hash }) => ({ mcp, tool, hash }))), `${g.node} approved`)
const approveMcp = (g: NodeGroup, mcp: string) => {
  const refs = mcpRefs(g, mcp)
  return run(`mcp/${g.node}/${mcp}`, () => api.approve(refs), `${plural(refs.length, 'tool')} approved`)
}
const approveTool = (t: PendingTool) => run(`tool/${t.name}`, () => api.approve([refOf(t)]), 'Tool approved')
</script>

<template>
  <PageHeader title="Approvals" />
  <p class="levels muted">
    A node's trust level decides what waits here: Trust holds nothing, Review changes holds tools whose definition changes (description, inputs, annotations or anything else clients see), Approve everything holds the node and every tool.
    <RouterLink to="/nodes" class="arrow-link">Trust levels are set per node</RouterLink>
  </p>

  <div v-if="!live.loaded.approvals" class="panel"><SkeletonRows :rows="4" /></div>
  <EmptyState
    v-else-if="!groups.length"
    framed
    :icon="ShieldCheck"
    title="Nothing to approve"
    text="New and changed tools appear here when a node's trust level asks for approval."
  >
    <RouterLink to="/nodes" class="arrow-link go">View nodes</RouterLink>
  </EmptyState>
  <div v-else class="list">
    <NodeApprovals
      v-for="g in groups"
      :key="g.node"
      :group="g"
      :node="live.nodes.find((n) => n.name === g.node)"
      :now="now"
      :busy="busy"
      @approve-node="approveNode(g)"
      @approve-mcp="(mcp) => approveMcp(g, mcp)"
      @approve-tool="approveTool"
    />
  </div>
</template>

<style scoped>
.levels { font-size: 12px; line-height: 1.6; margin: -8px 0 24px; max-width: 760px; }
.levels .arrow-link { margin-left: 6px; white-space: nowrap; }
.list { display: flex; flex-direction: column; gap: 16px; }
.go { display: inline-block; margin-top: 36px; }
</style>
