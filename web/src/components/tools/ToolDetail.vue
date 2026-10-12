<script setup lang="ts">
// Right-hand panel: one tool's description, source and input schema.
import { computed, ref, watch } from 'vue'
import { ArrowLeft, Copy, Pencil, X } from 'lucide-vue-next'
import type { Tool } from '@/lib/types'
import { live } from '@/lib/live'
import { mcpStatus } from '@/lib/status'
import { copyText } from '@/lib/clipboard'
import { toast } from '@/lib/toast'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiButton from '@/components/ui/UiButton.vue'
import UiNotice from '@/components/ui/UiNotice.vue'
import { approvalBadges } from '@/lib/trust'
import RichText from '@/components/ui/RichText.vue'
import EditToolDialog from './EditToolDialog.vue'
import { paramLine, parameters } from './schema'

const props = defineProps<{ tool: Tool }>()
const emit = defineEmits<{ close: []; renamed: [name: string] }>()
const editing = ref(false)

const raw = ref(false)
const copied = ref(false)
let timer: ReturnType<typeof setTimeout> | undefined
// A different tool starts on the parameter list again.
watch(() => props.tool.name, () => { raw.value = false; copied.value = false })

const params = computed(() => parameters(props.tool.input_schema))
const json = computed(() => JSON.stringify(props.tool.input_schema ?? {}, null, 2))
const status = computed(() => {
  const n = live.nodes.find((x) => x.name === props.tool.node)
  const m = n?.mcps.find((x) => x.name === props.tool.mcp)
  return m ? mcpStatus(m, n?.online) : null
})

async function copy() {
  if (await copyText(props.tool.name)) {
    copied.value = true
    clearTimeout(timer)
    timer = setTimeout(() => (copied.value = false), 1600)
  } else {
    toast("Couldn't copy", 'Select the text and copy it by hand.', 'err')
  }
}
</script>

<template>
  <aside class="detail panel" aria-label="Tool detail">
    <div class="top">
      <button type="button" class="back" @click="$emit('close')"><ArrowLeft :size="18" :stroke-width="1.6" aria-hidden="true" />Back to tools</button>
      <span class="label">Tool detail</span>
      <button type="button" class="close" aria-label="Close tool detail" @click="$emit('close')"><X :size="22" :stroke-width="1.6" aria-hidden="true" /></button>
    </div>
    <h2 class="orig truncate" :title="tool.alias ? tool.name : tool.original_name">{{ tool.alias ? tool.name : tool.original_name }}</h2>
    <p v-if="tool.alias" class="gen mono muted" :title="tool.generated_name">Generated name: {{ tool.generated_name }}</p>
    <div class="badges">
      <UiBadge v-if="tool.alias" tone="info" title="Clients see this tool under an alias">Alias</UiBadge>
      <UiBadge v-if="status" :tone="status.tone" :title="status.detail">{{ status.label }}</UiBadge>
      <UiBadge v-if="tool.approval !== 'approved'" :tone="approvalBadges[tool.approval].tone">{{ approvalBadges[tool.approval].label }}</UiBadge>
    </div>
    <UiNotice v-if="tool.approval !== 'approved'" tone="warn" class="unapproved">
      Clients can't see this tool until it's approved. <RouterLink to="/approvals" class="arrow-link">Review in Approvals</RouterLink>
    </UiNotice>
    <RichText v-if="tool.description_override || tool.description" class="text" :text="tool.description_override || tool.description" />
    <p v-else class="text muted">No description.</p>
    <p v-if="tool.description_override" class="edited muted">Description edited. The MCP's own is shown in the editor.</p>

    <section class="sec">
      <span class="label">Source</span>
      <p class="mono src" :title="`${tool.node} / ${tool.mcp}`">{{ tool.node }} / {{ tool.mcp }}</p>
      <p class="mono muted orig-line">Original name: {{ tool.original_name }}</p>
    </section>

    <section class="sec params">
      <div class="params-head">
        <h3 class="section-title">Input parameters</h3>
        <button type="button" class="chip" :class="{ on: raw }" :aria-pressed="raw" @click="raw = !raw">Raw JSON</button>
      </div>
      <pre v-if="raw" class="json mono">{{ json }}</pre>
      <p v-else-if="!params.length" class="none muted">No parameters.</p>
      <ul v-else class="plist">
        <li v-for="p in params" :key="p.name">
          <div class="pname">
            <span class="mono truncate" :title="p.name">{{ p.name }}</span>
            <UiBadge v-if="p.required" tone="info">Required</UiBadge>
          </div>
          <p class="pdesc muted">{{ paramLine(p) }}</p>
        </li>
      </ul>
    </section>

    <div class="foot">
      <UiButton block :icon="Pencil" @click="editing = true">Edit name and description</UiButton>
      <UiButton block :icon="Copy" @click="copy">{{ copied ? 'Copied' : 'Copy exposed name' }}</UiButton>
    </div>
    <EditToolDialog v-if="editing" :tool="tool" @close="editing = false" @saved="(n) => emit('renamed', n)" />
  </aside>
</template>

<style scoped>
.detail { padding: 24px; min-width: 0; align-self: start; }
.top { display: flex; align-items: center; justify-content: space-between; min-height: 24px; }
.close { border: 0; background: none; color: var(--ink); cursor: pointer; padding: 0; display: grid; }
.back { display: none; align-items: center; gap: 6px; border: 0; background: none; color: var(--cobalt); font-size: 13px; font-weight: 500; cursor: pointer; padding: 0; }
.orig { font-family: var(--font-display); font-size: 28px; font-weight: 500; margin-top: 18px; }
.gen { margin-top: 8px; font-size: 12px; overflow-wrap: anywhere; }
.edited { margin-top: 10px; font-size: 12px; }
.badges { display: flex; flex-wrap: wrap; gap: 8px; margin-top: 12px; }
.unapproved { margin-top: 20px; }
.text { margin-top: 28px; font-size: 14px; line-height: 1.45; }
.sec { margin-top: 28px; padding-top: 24px; border-top: 1px solid var(--rule); }
.src { margin-top: 14px; font-size: 12px; overflow-wrap: anywhere; }
.orig-line { margin-top: 10px; font-size: 12px; overflow-wrap: anywhere; }
.params { margin-top: 24px; }
.params-head { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.chip {
  height: 26px; padding: 0 10px; border: 0; border-radius: var(--radius);
  background: var(--rule); color: var(--muted); font-size: 12px; font-weight: 500; cursor: pointer;
}
.chip.on { background: var(--cobalt-soft); color: var(--cobalt); }
.none { margin-top: 16px; font-size: 13px; }
.plist { list-style: none; margin: 8px 0 0; padding: 0; }
.plist li { padding: 18px 0; border-bottom: 1px solid var(--rule); }
.plist li:last-child { border-bottom: 0; }
.pname { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 26px; }
.pname .mono { font-size: 12px; }
.pdesc { margin-top: 10px; font-size: 12px; line-height: 1.45; }
.json {
  margin: 16px 0 0; padding: 14px; max-height: 360px; overflow: auto;
  background: var(--paper); border: 1px solid var(--rule); font-size: 11.5px; line-height: 1.5;
  white-space: pre-wrap; overflow-wrap: anywhere;
}
.foot { margin-top: 12px; padding-top: 16px; border-top: 1px solid var(--rule); }
.foot { display: flex; flex-direction: column; gap: 10px; }
.foot :deep(.btn) { justify-content: center; }

@media (max-width: 1100px) {
  .back { display: inline-flex; }
  .close, .top .label { display: none; }
}
</style>
