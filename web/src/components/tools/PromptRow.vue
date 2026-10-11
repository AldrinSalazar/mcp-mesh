<script setup lang="ts">
// One row of the prompts list; same layout as ToolRow.
import type { Prompt } from '@/lib/types'
import CopyButton from '@/components/ui/CopyButton.vue'

defineProps<{ prompt: Prompt; selected: boolean }>()
defineEmits<{ select: [] }>()
</script>

<template>
  <li class="row" :class="{ selected }">
    <button type="button" class="pick" :aria-current="selected || undefined" @click="$emit('select')">
      <span class="what">
        <span class="name mono truncate" :title="prompt.name">{{ prompt.name }}</span>
        <span class="desc truncate" :title="prompt.description">{{ prompt.description || 'No description' }}</span>
      </span>
      <span class="source">
        <span class="truncate" :title="prompt.node">{{ prompt.node }}</span>
        <span class="muted truncate" :title="prompt.mcp">{{ prompt.mcp }}</span>
      </span>
    </button>
    <CopyButton :value="prompt.name" label="Copy exposed name" />
  </li>
</template>

<style scoped>
.row { display: flex; align-items: center; gap: 8px; padding-right: 20px; border-bottom: 1px solid var(--rule); }
.row.selected { background: var(--cobalt-soft); }
.pick {
  flex: 1; min-width: 0; display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 190px); gap: 16px;
  align-items: center; padding: 18px 0 18px 24px; border: 0; background: none; text-align: left; cursor: pointer;
}
.pick:focus-visible { outline-offset: -2px; }
.what, .source { display: flex; flex-direction: column; gap: 10px; min-width: 0; }
.name { font-size: 12px; }
.selected .name { color: var(--cobalt); }
.desc { font-size: 12px; color: var(--muted); }
.source { font-size: 12px; }
.row:not(.selected):hover { background: color-mix(in srgb, var(--paper) 60%, transparent); }

@media (max-width: 700px) {
  .pick { grid-template-columns: minmax(0, 1fr); gap: 8px; padding-left: 16px; }
  .source { flex-direction: row; gap: 6px; }
  .source .muted::before { content: '/ '; }
  .row { padding-right: 8px; }
}
</style>
