<script setup lang="ts">
// The fields of a tool's definition besides name, description and input
// schema (annotations, output schema, title, ...), for a tool with nothing to
// compare against: the admin sees everything they are approving.
import { computed } from 'vue'
import type { PendingTool } from '@/lib/types'
import { fieldLabel, otherFields } from './diff'

const props = defineProps<{ tool: PendingTool }>()
const fields = computed(() => otherFields(props.tool.definition))
</script>

<template>
  <section v-for="f in fields" :key="f.key" class="field">
    <h4 class="label">{{ fieldLabel(f.key) }}</h4>
    <pre class="value mono" :aria-label="`${fieldLabel(f.key)} of ${tool.name}`">{{ f.text }}</pre>
  </section>
</template>

<style scoped>
.field { display: grid; gap: 6px; }
.value { margin: 0; padding: 10px 12px; background: var(--paper); border: 1px solid var(--rule); font-size: 11.5px; line-height: 1.55; overflow-x: auto; }
</style>
