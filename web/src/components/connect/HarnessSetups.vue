<script setup lang="ts">
// How to add the hub to each MCP client: one collapsible section per
// client, with its mark, where the snippet goes and the snippet to copy.
// One is open at a time; the last one opened is remembered.
import { computed, ref, watch } from 'vue'
import { ChevronRight, SquareTerminal } from 'lucide-vue-next'
import CopyField from '@/components/ui/CopyField.vue'
import { display, snippet, snippetOptions, type SnippetId, type SnippetInput } from '@/lib/snippets'
import { brandPaths } from './brands'

const props = defineProps<{ input: SnippetInput }>()

const storeKey = 'mcpmesh.harness'
function remembered(): SnippetId {
  try {
    const v = localStorage.getItem(storeKey)
    if (snippetOptions.some((o) => o.value === v)) return v as SnippetId
  } catch {
    // no storage
  }
  return 'claude-code'
}
const open = ref<SnippetId | null>(remembered())
watch(open, (v) => {
  try {
    if (v) localStorage.setItem(storeKey, v)
  } catch {
    // no storage
  }
})

function toggle(id: SnippetId) {
  open.value = open.value === id ? null : id
}

const items = computed(() => snippetOptions.map((o) => ({ ...o, value: o.value, text: snippet(o.value, props.input), shown: display(o.value, props.input) })))
</script>

<template>
  <ul class="harnesses">
    <li v-for="h in items" :key="h.value" class="harness" :class="{ open: open === h.value }">
      <h3>
        <button
          :id="`harness-${h.value}`"
          type="button"
          class="head"
          :aria-expanded="open === h.value"
          :aria-controls="`harness-${h.value}-body`"
          @click="toggle(h.value)"
        >
          <svg v-if="brandPaths[h.logo]" class="logo brand" viewBox="0 0 24 24" aria-hidden="true"><path :d="brandPaths[h.logo]" /></svg>
          <SquareTerminal v-else class="logo" :size="20" :stroke-width="1.6" aria-hidden="true" />
          <span class="name">{{ h.label }}</span>
          <ChevronRight class="chev" :size="18" :stroke-width="1.6" aria-hidden="true" />
        </button>
      </h3>
      <div v-if="open === h.value" :id="`harness-${h.value}-body`" class="body" role="region" :aria-labelledby="`harness-${h.value}`">
        <p class="where muted">{{ h.where }}</p>
        <CopyField :value="h.text" :display="h.shown" multiline :label="`Copy ${h.label} setup`" />
      </div>
    </li>
  </ul>
</template>

<style scoped>
.harnesses { list-style: none; margin: 0; padding: 0; border: 1px solid var(--rule); border-radius: var(--radius); }
.harness + .harness { border-top: 1px solid var(--rule); }
h3 { margin: 0; font: inherit; }
.head {
  display: flex; align-items: center; gap: 12px; width: 100%; padding: 14px 16px;
  border: 0; background: none; color: var(--ink); font: inherit; font-size: 14px; font-weight: 500; text-align: left; cursor: pointer;
}
.head:hover .name { text-decoration: underline; }
.logo { flex: none; width: 20px; height: 20px; }
.brand { fill: currentColor; }
.name { flex: 1; }
.chev { color: var(--muted); transition: transform 0.15s; }
.open .chev { transform: rotate(90deg); }
.body { padding: 0 16px 16px 48px; }
.where { font-size: 13px; margin-bottom: 10px; }

@media (max-width: 700px) {
  .body { padding-left: 16px; }
}
</style>
