<script setup lang="ts">
// One row of the resources list: name, exposed URI, source and MIME type.
import type { ResourceItem } from '@/lib/types'
import UiBadge from '@/components/ui/UiBadge.vue'
import CopyButton from '@/components/ui/CopyButton.vue'

defineProps<{ resource: ResourceItem; selected: boolean }>()
defineEmits<{ select: [] }>()
</script>

<template>
  <li class="row" :class="{ selected }">
    <button type="button" class="pick" :aria-current="selected || undefined" @click="$emit('select')">
      <span class="what">
        <span class="nm">
          <span class="rname truncate" :title="resource.name">{{ resource.name || resource.original_uri }}</span>
          <UiBadge v-if="resource.template" tone="info" class="ab">Template</UiBadge>
        </span>
        <span class="uri mono truncate" :title="resource.uri">{{ resource.uri }}</span>
      </span>
      <span class="source">
        <span class="truncate" :title="resource.node">{{ resource.node }}</span>
        <span class="muted truncate" :title="resource.mcp">{{ resource.mcp }}</span>
      </span>
      <span class="mime mono muted truncate" :title="resource.mime_type">{{ resource.mime_type || '-' }}</span>
    </button>
    <CopyButton :value="resource.uri" label="Copy exposed URI" />
  </li>
</template>

<style scoped>
.row { display: flex; align-items: center; gap: 8px; padding-right: 20px; border-bottom: 1px solid var(--rule); }
.row.selected { background: var(--cobalt-soft); }
.pick {
  flex: 1; min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 190px) minmax(0, 120px); gap: 16px;
  align-items: center; padding: 18px 0 18px 24px; border: 0; background: none; text-align: left; cursor: pointer;
}
.pick:focus-visible { outline-offset: -2px; }
.what, .source { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.nm { display: flex; align-items: center; gap: 10px; min-width: 0; }
.ab { flex: none; min-width: 0; height: 22px; }
.rname { font-size: 13px; font-weight: 500; }
.selected .rname { color: var(--cobalt); }
.uri { font-size: 12px; color: var(--muted); }
.source { font-size: 12px; }
.mime { font-size: 11px; }
.row:not(.selected):hover { background: color-mix(in srgb, var(--paper) 60%, transparent); }

@media (max-width: 700px) {
  .pick { grid-template-columns: minmax(0, 1fr); gap: 8px; padding-left: 16px; }
  .source { flex-direction: row; gap: 6px; }
  .source .muted::before { content: '/ '; }
  .mime { display: none; }
  .row { padding-right: 8px; }
}
</style>
