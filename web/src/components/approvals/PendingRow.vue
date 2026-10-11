<script setup lang="ts">
// One tool waiting for approval: a row that expands to what changed (or, for
// a new tool, what it is) and the Approve button.
import { computed, ref } from 'vue'
import { ChevronRight } from 'lucide-vue-next'
import type { PendingTool } from '@/lib/types'
import UiBadge from '@/components/ui/UiBadge.vue'
import UiButton from '@/components/ui/UiButton.vue'
import { ago } from '@/lib/format'
import { paramLine, parameters } from '@/components/tools/schema'
import { approvalBadges } from '@/lib/trust'
import ToolDiff from './ToolDiff.vue'
import ToolFields from './ToolFields.vue'

const props = defineProps<{ tool: PendingTool; now: number; busy: boolean; locked: boolean }>()
defineEmits<{ approve: [] }>()

const open = ref(false)
const badge = computed(() => approvalBadges[props.tool.state])
const params = computed(() => parameters(props.tool.input_schema))
const isChange = computed(() => props.tool.state === 'changed' && !!props.tool.previous)
const id = computed(() => `pt-${props.tool.name}`)
</script>

<template>
  <li class="row" :class="{ open }">
    <div class="line">
      <button type="button" class="toggle" :aria-expanded="open" :aria-controls="id" :aria-label="`${open ? 'Hide' : 'Show'} details of ${tool.name}`" @click="open = !open">
        <ChevronRight :size="18" :stroke-width="1.6" class="chev" aria-hidden="true" />
        <span class="name mono truncate" :title="tool.name">{{ tool.name }}</span>
      </button>
      <UiBadge :tone="badge.tone" class="badge">{{ badge.label }}</UiBadge>
      <span class="since muted">{{ ago(tool.since, now) }}</span>
      <UiButton
        class="approve"
        :loading="busy"
        :disabled="locked"
        :title="locked ? 'Approve the node first' : undefined"
        :aria-label="`${isChange ? 'Approve change to' : 'Approve'} ${tool.name}`"
        @click="$emit('approve')"
      >{{ isChange ? 'Approve change' : 'Approve' }}</UiButton>
    </div>
    <div v-if="open" :id="id" class="details">
      <template v-if="isChange">
        <ToolDiff :tool="tool" />
        <p class="by muted">
          Previous version approved by {{ tool.previous!.approved_by || 'someone' }} {{ ago(tool.previous!.approved_at, now) }}.
        </p>
      </template>
      <template v-else>
        <p class="desc">{{ tool.description || 'No description.' }}</p>
        <h4 class="label">Input parameters</h4>
        <p v-if="!params.length" class="muted none">No parameters.</p>
        <ul v-else class="plist">
          <li v-for="p in params" :key="p.name">
            <span class="mono pn">{{ p.name }}</span>
            <UiBadge v-if="p.required" tone="info">Required</UiBadge>
            <span class="muted pl">{{ paramLine(p) }}</span>
          </li>
        </ul>
        <ToolFields :tool="tool" />
      </template>
    </div>
  </li>
</template>

<style scoped>
.row { border-top: 1px solid var(--rule); }
.line { display: grid; grid-template-columns: minmax(0, 1fr) 140px 90px 150px; gap: 16px; align-items: center; padding: 10px 24px 10px 20px; min-height: 58px; }
.toggle { display: flex; align-items: center; gap: 10px; min-width: 0; padding: 0; border: 0; background: none; text-align: left; cursor: pointer; color: var(--ink); }
.chev { flex: none; color: var(--muted); transition: transform 0.12s; }
.open .chev { transform: rotate(90deg); }
.name { font-size: 12px; }
.since { font-size: 12px; white-space: nowrap; }
.approve { justify-self: end; }
.details { padding: 6px 24px 24px 48px; display: grid; gap: 10px; }
.desc { font-size: 13px; line-height: 1.5; overflow-wrap: anywhere; }
.by { font-size: 12px; }
.none { font-size: 12px; }
.plist { list-style: none; margin: 0; padding: 0; display: grid; gap: 10px; }
.plist li { display: flex; flex-wrap: wrap; align-items: center; gap: 4px 10px; font-size: 12px; }
.pn { font-size: 12px; }
.pl { flex-basis: 100%; line-height: 1.45; }

@media (max-width: 700px) {
  .line { grid-template-columns: minmax(0, 1fr) auto; grid-template-areas: 'name name' 'badge approve'; row-gap: 8px; padding: 12px 16px; }
  .toggle { grid-area: name; }
  .badge { grid-area: badge; justify-self: start; }
  .since { display: none; }
  .approve { grid-area: approve; }
  .details { padding: 4px 16px 20px; }
}
</style>
