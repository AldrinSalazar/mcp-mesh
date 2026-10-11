<script setup lang="ts">
// Definitions the hub dropped from an MCP's last listing, with the reasons.
// Collapsed to a warn-toned line; the reasons open on demand.
import { computed } from 'vue'
import { TriangleAlert } from 'lucide-vue-next'
import type { MCP } from '@/lib/types'
import { problemsLabel } from './mcpinfo'

// named: write the MCP's name in the line, where the row doesn't show it.
const props = defineProps<{ mcp: Pick<MCP, 'name' | 'problems'>; named?: boolean }>()
const label = computed(() => problemsLabel(props.mcp))
</script>

<template>
  <details v-if="label" class="problems">
    <summary :title="mcp.problems.join('\n')"><TriangleAlert :size="13" :stroke-width="1.8" aria-hidden="true" /> <template v-if="named">{{ mcp.name }}: </template>{{ label }}<span v-if="!named" class="visually-hidden"> from {{ mcp.name }}</span></summary>
    <ul>
      <li v-for="(p, i) in mcp.problems" :key="i" class="mono">{{ p }}</li>
    </ul>
  </details>
</template>

<style scoped>
.problems { margin-top: 8px; font-size: 11px; line-height: 1.4; color: var(--warn); }
summary { display: inline-flex; align-items: center; gap: 5px; cursor: pointer; font-weight: 600; }
ul { list-style: none; margin: 6px 0 0; padding: 0; display: grid; gap: 4px; color: var(--muted); }
li { font-size: 11px; overflow-wrap: anywhere; }
</style>
