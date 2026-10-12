<script setup lang="ts">
// One machine: a collapsed row, or the expanded card with its MCP table.
import { computed, ref } from 'vue'
import { ChevronRight, Monitor, TriangleAlert } from 'lucide-vue-next'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import UiSelect from '@/components/ui/UiSelect.vue'
import { api } from '@/lib/api'
import { toast } from '@/lib/toast'
import { refresh } from '@/lib/live'
import { trustChoice, trustLabel, trustOptions, trustText } from '@/lib/trust'
import type { TrustLevel } from '@/lib/types'
import { agoLong, host, osLabel, plural, since, versionLabel } from '@/lib/format'
import { live } from '@/lib/live'
import { mcpStatus, nodeStatus, startLabel, transportLabel, type Tone } from '@/lib/status'
import type { MCP, Node } from '@/lib/types'
import McpProblems from './McpProblems.vue'
import { clientsLabel, isPerClient, offersLabel, timeoutLabel } from './mcpinfo'

const props = defineProps<{ node: Node; expanded: boolean; now: number }>()
const emit = defineEmits<{ toggle: []; revoke: []; forget: [] }>()

const status = computed(() => nodeStatus(props.node, live.info?.started_at, props.now))

const trustOpts = computed(() => trustOptions(live.settings?.trust_default))
const trustNote = computed(() => trustText(props.node.trust.level))
const saving = ref(false)
const approving = ref(false)
const trustModel = computed({
  get: () => trustChoice(props.node.trust),
  set: (v: string) => { void setTrust(v ? (v as TrustLevel) : null) },
})

async function setTrust(level: TrustLevel | null) {
  saving.value = true
  try {
    await api.setNodeTrust(props.node.name, level)
    toast('Trust level changed')
    await Promise.all([refresh('nodes'), refresh('approvals')])
  } catch (e) {
    toast("Couldn't change the trust level", e instanceof Error ? e.message : undefined, 'err')
  } finally {
    saving.value = false
  }
}

async function approveNode() {
  approving.value = true
  try {
    // Only the node: its tools are reviewed in Approvals, where their
    // definitions are shown.
    await api.approveNode(props.node.name, [])
    const n = props.node.pending_tools
    toast(`${props.node.name} approved`, n > 0 ? `${plural(n, 'tool')} ${n === 1 ? 'waits' : 'wait'} for review in Approvals.` : undefined)
    await Promise.all([refresh('nodes'), refresh('approvals')])
  } catch (e) {
    toast("Couldn't approve the node", e instanceof Error ? e.message : undefined, 'err')
  } finally {
    approving.value = false
  }
}
const tools = computed(() => props.node.mcps.reduce((s, m) => s + m.tools, 0))
const counts = computed(() => `${plural(props.node.mcps.length, 'MCP')} / ${plural(tools.value, 'tool')}`)
const address = computed(() => host(props.node.remote_addr))
const lifetime = computed(() => {
  if (props.node.online) return `Connected for ${since(props.node.connected_at, props.now)}`
  return props.node.disconnected_at ? `Last seen ${agoLong(props.node.disconnected_at, props.now)}` : 'Offline'
})
// Collapsed rows show just the span, as in the design.
const shortLifetime = computed(() => lifetime.value.replace(/^Connected for /, ''))
const sub = computed(() => [osLabel(props.node.os), props.expanded ? `node ${versionLabel(props.node.node_version)}` : '', address.value].filter(Boolean).join(' · '))

// The key's name when the Keys list knows it (it won't with auth off).
const keyName = computed(() => live.keys.find((k) => k.id === props.node.key_id)?.name ?? props.node.key_id)

interface Row { mcp: MCP; label: string; tone: Tone; detail: string; hint?: string; detailTone: Tone }
const rows = computed<Row[]>(() =>
  props.node.mcps.map((mcp) => {
    const s = mcpStatus(mcp, props.node.online)
    const waiting = s.label === 'Waiting for app'
    return {
      mcp,
      label: s.label,
      tone: s.tone,
      detail: waiting ? "Waiting for app: the app isn't running or its MCP server is off." : (s.detail ?? ''),
      hint: waiting ? s.detail : undefined,
      detailTone: s.tone,
    }
  }),
)
</script>

<template>
  <section :id="`node-${node.name}`" class="card panel" :class="{ expanded, offline: !node.online }">
    <button v-if="!expanded" type="button" class="row" aria-expanded="false" :aria-label="`Show ${node.name}`" @click="emit('toggle')">
      <Monitor :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
      <span class="who">
        <span class="name truncate" :title="node.name">{{ node.name }}</span>
        <span class="sub mono truncate">{{ sub }}</span>
        <UiBadge v-if="!node.trust.approved" tone="warn" class="mini">Not approved</UiBadge>
        <UiBadge v-else-if="node.pending_tools > 0" tone="warn" class="mini">{{ node.pending_tools }} waiting</UiBadge>
        <UiBadge v-else-if="node.outdated" tone="info" class="mini" :title="`Runs ${versionLabel(node.node_version)}; a newer mcp-mesh-node is out`">Update available</UiBadge>
      </span>
      <UiBadge :tone="status.tone" class="state">{{ status.label }}</UiBadge>
      <span class="span">{{ shortLifetime }}</span>
      <span class="counts">{{ counts }}</span>
      <ChevronRight :size="22" :stroke-width="1.6" class="chev" aria-hidden="true" />
    </button>

    <template v-else>
      <div class="row open">
        <Monitor :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
        <div class="who">
          <button type="button" class="name truncate collapse" aria-expanded="true" :title="`Hide ${node.name}`" @click="emit('toggle')">{{ node.name }}</button>
          <span class="sub mono truncate">{{ sub }}</span>
          <span class="life">{{ lifetime }} · Trust: {{ trustLabel(node.trust.level) }}</span>
          <span v-if="node.outdated" class="life upgrade">Update available. On {{ node.name }}, run <code>brew upgrade mcp-mesh-node</code> (or download the latest release), then restart the node.</span>
        </div>
        <UiBadge :tone="status.tone" class="state">{{ status.label }}</UiBadge>
        <span class="counts">{{ counts }}</span>
        <div v-if="(!live.authOff && node.key_id) || !node.online" class="actions">
          <RouterLink v-if="!live.authOff && node.key_id" to="/keys" class="keylink truncate" :title="keyName">Key: {{ keyName }} <span aria-hidden="true">→</span></RouterLink>
          <UiButton v-if="node.online && !live.authOff && node.key_id" @click="emit('revoke')">Revoke key</UiButton>
          <UiButton v-if="!node.online" @click="emit('forget')">Forget</UiButton>
        </div>
      </div>

      <div class="trust">
        <div class="trust-head">
          <span class="label">Trust level</span>
          <div class="trust-pick">
            <UiSelect v-model="trustModel" :options="trustOpts" label="Trust level" />
          </div>
          <p class="trust-note muted">
            <template v-if="!node.trust.explicit">Following the hub default: {{ trustLabel(node.trust.level) }}. </template>{{ trustNote }}
          </p>
          <RouterLink v-if="node.pending_tools > 0" to="/approvals" class="pending-link" :aria-label="`${node.pending_tools} tools waiting for approval on ${node.name}`">
            <UiBadge tone="warn">{{ node.pending_tools }} waiting</UiBadge>
          </RouterLink>
        </div>
        <div v-if="!node.trust.approved" class="unapproved">
          <UiNotice tone="warn" :icon="TriangleAlert" class="un-notice">
            {{ node.name }} waits for approval. Approving it here makes the node visible; its
            {{ plural(node.pending_tools, 'tool') }} are reviewed one by one in
            <RouterLink to="/approvals" class="arrow-link">Approvals</RouterLink>, where you can see what each does.
          </UiNotice>
          <UiButton variant="primary" :loading="approving" @click="approveNode">Approve node</UiButton>
        </div>
      </div>

      <div v-if="rows.length" class="tablewrap">
        <table class="table">
          <thead>
            <tr>
              <th>MCP server</th>
              <th>Connection</th>
              <th class="opt">Start mode</th>
              <th>Tools</th>
              <th>State</th>
              <th class="opt">Load</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="r in rows" :key="r.mcp.name">
              <td class="mcpname"><span class="truncate" :title="r.mcp.name">{{ r.mcp.name }}</span></td>
              <td>
                <span class="conn">{{ transportLabel(r.mcp) }}</span>
                <span v-if="r.mcp.call_timeout_ms > 0" class="timeout muted" title="Call timeout of this MCP">Timeout {{ timeoutLabel(r.mcp.call_timeout_ms) }}</span>
                <span v-if="r.detail" class="detail" :class="`d-${r.detailTone}`" :title="r.hint">{{ r.detail }}</span>
                <McpProblems :mcp="r.mcp" />
              </td>
              <td class="opt conn">{{ isPerClient(r.mcp) ? clientsLabel(r.mcp) : startLabel(r.mcp) }}</td>
              <td>
                <RouterLink :to="{ path: '/tools', query: { node: node.name, mcp: r.mcp.name } }" class="toolcount" :aria-label="`${plural(r.mcp.tools, 'tool')} of ${r.mcp.name}`">{{ r.mcp.tools }}</RouterLink>
                <span v-if="r.mcp.prompts > 0 || r.mcp.resources > 0" class="offers muted" :title="offersLabel(r.mcp)"> · <RouterLink v-if="r.mcp.prompts > 0" :to="{ path: '/tools', query: { kind: 'prompts', node: node.name, mcp: r.mcp.name } }" class="offer">{{ plural(r.mcp.prompts, 'prompt') }}</RouterLink><template v-if="r.mcp.prompts > 0 && r.mcp.resources > 0"> · </template><RouterLink v-if="r.mcp.resources > 0" :to="{ path: '/tools', query: { kind: 'resources', node: node.name, mcp: r.mcp.name } }" class="offer">{{ plural(r.mcp.resources, 'resource') }}</RouterLink></span>
              </td>
              <td><UiBadge :tone="r.tone">{{ r.label }}</UiBadge></td>
              <td class="opt mono muted load">{{ r.mcp.in_flight }} running<template v-if="r.mcp.queued"> · {{ r.mcp.queued }} queued</template></td>
            </tr>
          </tbody>
        </table>
      </div>
      <p v-else class="none muted">No MCP servers{{ node.online ? '' : ' while offline' }}.</p>
    </template>
  </section>
</template>

<style scoped>
.card { position: relative; background: var(--surface); }
.card.expanded { border-left: 3px solid var(--cobalt); }
.row {
  display: grid;
  grid-template-columns: 24px 255px 100px minmax(0, 1fr) 190px 190px;
  column-gap: 18px;
  align-items: start;
  width: 100%;
  padding: 22px 24px 22px 26px;
  border: 0;
  background: none;
  text-align: left;
}
button.row { cursor: pointer; align-items: center; grid-template-columns: 24px 255px 100px minmax(0, 1fr) 190px 40px; }
button.row:hover { background: color-mix(in srgb, var(--paper) 55%, transparent); }
.row.open { padding-bottom: 28px; }
.icon { color: var(--muted); }
.row.open .icon { margin-top: 3px; }
.who { display: flex; flex-direction: column; align-items: flex-start; gap: 6px; min-width: 0; max-width: 100%; }
.name { max-width: 100%; font-family: var(--font-display); font-size: 20px; font-weight: 500; line-height: 1.3; }
.collapse { padding: 0; border: 0; background: none; color: inherit; cursor: pointer; text-align: left; }
.collapse:hover { text-decoration: underline; }
.sub { max-width: 100%; font-size: 12px; color: var(--muted); }
.life { margin-top: 10px; font-size: 12px; color: var(--muted); }
.upgrade { margin-top: 6px; color: var(--cobalt); white-space: normal; }
.span, .counts { font-size: 13px; }
.row.open .state, .row.open .counts { margin-top: 4px; }
.row.open .state { align-self: start; justify-self: start; }
.chev { justify-self: end; color: var(--muted); }
.actions { display: flex; flex-direction: column; align-items: flex-end; gap: 22px; min-width: 0; }
.keylink { max-width: 100%; margin-top: 4px; font-size: 12px; font-weight: 500; }
.row.open .counts { grid-column: 5; grid-row: 1; }
.row.open .actions { grid-column: 6; grid-row: 1; }
.row.open .icon { grid-column: 1; grid-row: 1; }
.row.open .who { grid-column: 2; grid-row: 1; }
.row.open .state { grid-column: 3; grid-row: 1; }

.mini { min-width: 0; margin-top: 4px; }
.trust { margin: 0 24px 20px; padding-top: 18px; border-top: 1px solid var(--rule); display: grid; gap: 16px; }
.trust-head { display: flex; align-items: center; gap: 16px; flex-wrap: wrap; }
.trust-pick { width: 220px; }
.trust-note { flex: 1 1 260px; font-size: 12px; line-height: 1.5; margin: 0; }
.pending-link:hover { text-decoration: none; }
.unapproved { display: flex; align-items: center; gap: 16px; }
.un-notice { flex: 1; }
.tablewrap { margin: 0 24px 24px 24px; border-top: 1px solid var(--rule); overflow-x: auto; }
.table { table-layout: fixed; }
.table th:first-child, .table td:first-child { padding-left: 0; }
.table th:last-child, .table td:last-child { padding-right: 0; }
.table th { background: transparent; padding: 18px 12px 12px; height: auto; }
.table td { padding: 14px 12px; vertical-align: top; }
.mcpname { font-size: 14px; font-weight: 500; max-width: 200px; }
.mcpname span { display: block; }
.conn { font-size: 12px; color: var(--muted); }
.timeout { display: block; margin-top: 4px; font-size: 12px; }
.offers { font-size: 12px; }
.offer { color: var(--muted); }
.offer:hover { color: var(--cobalt); }
.detail { display: block; margin-top: 8px; font-size: 11px; line-height: 1.4; max-width: 340px; }
.d-warn { color: var(--warn); }
.d-err { color: var(--err); }
.d-ok, .d-idle, .d-info, .d-neutral { color: var(--muted); }
.toolcount { color: var(--ink); font-size: 13px; }
.toolcount:hover { color: var(--cobalt); }
.none { padding: 0 26px 26px; font-size: 13px; }

@media (min-width: 901px) {
  .table th:nth-child(1) { width: 220px; }
  .table th:nth-child(2) { width: 250px; }
  .table th:nth-child(3) { width: 190px; }
  .table th:nth-child(4) { width: 183px; }
  .table th:nth-child(5) { width: 140px; }
}
@media (max-width: 700px) {
  .trust-pick { width: 100%; }
  .unapproved { flex-direction: column; align-items: stretch; }
}
@media (max-width: 900px) {
  .row, button.row { grid-template-columns: 24px minmax(0, 1fr) auto; row-gap: 10px; }
  .row .span, button.row .chev { display: none; }
  .row .counts, .row.open .counts { grid-column: 2 / 4; grid-row: auto; }
  .row.open .actions { grid-column: 2 / 4; grid-row: auto; flex-direction: row; align-items: center; justify-content: space-between; }
  .row .state, .row.open .state { grid-column: 3; grid-row: 1; margin-top: 0; }
  .opt { display: none; }
  .table { table-layout: auto; }
}

</style>
