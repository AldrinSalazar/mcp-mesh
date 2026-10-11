<script setup lang="ts">
// Previous vs current description (by word), input schema (by line) and every
// other field of the definition (by line) of a changed tool. Additions are the loud part: that is where injected
// instructions show up.
import { computed } from 'vue'
import type { PendingTool } from '@/lib/types'
import { changed, diffFields, diffSchema, diffWords, fieldLabel } from './diff'

const props = defineProps<{ tool: PendingTool }>()

const prev = computed(() => props.tool.previous)
const words = computed(() => diffWords(prev.value?.description ?? '', props.tool.description))
const schema = computed(() => diffSchema(prev.value?.input_schema, props.tool.input_schema))
const descChanged = computed(() => changed(words.value))
const schemaChanged = computed(() => changed(schema.value))
// Title, annotations, output schema, icons, _meta and anything else the MCP sends.
const fields = computed(() => diffFields(prev.value?.definition, props.tool.definition))
const statusLabel = { added: 'added', removed: 'removed', changed: 'changed' } as const
</script>

<template>
  <div class="diff">
    <section>
      <h4 class="label">Description</h4>
      <p v-if="!descChanged" class="same muted">Unchanged.</p>
      <p v-else class="words" aria-label="Description changes">
        <template v-for="(w, i) in words" :key="i">
          <del v-if="w.op === 'del'" class="del"><span class="visually-hidden">Removed: </span>{{ w.text }}</del>
          <ins v-else-if="w.op === 'add'" class="add"><span class="visually-hidden">Added: </span>{{ w.text }}</ins>
          <span v-else>{{ w.text }}</span>
        </template>
      </p>
    </section>
    <section>
      <h4 class="label">Input schema</h4>
      <p v-if="!schemaChanged" class="same muted">Unchanged.</p>
      <div v-else class="lines mono" role="group" aria-label="Input schema changes">
        <span v-for="(l, i) in schema" :key="i" class="ln" :class="`ln-${l.op}`"><span class="sign" aria-hidden="true">{{ l.op === 'add' ? '+' : l.op === 'del' ? '−' : ' ' }}</span>{{ l.text }}</span>
      </div>
    </section>
    <section v-for="f in fields" :key="f.key">
      <h4 class="label">{{ fieldLabel(f.key) }} <span class="fstate">{{ statusLabel[f.status] }}</span></h4>
      <div class="lines mono" role="group" :aria-label="`${fieldLabel(f.key)} changes`">
        <span v-for="(l, i) in f.lines" :key="i" class="ln" :class="`ln-${l.op}`"><span class="sign" aria-hidden="true">{{ l.op === 'add' ? '+' : l.op === 'del' ? '−' : ' ' }}</span>{{ l.text }}</span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.diff { display: grid; gap: 20px; }
h4 { margin-bottom: 8px; }
.fstate { margin-left: 6px; color: var(--warn); text-transform: none; }
.same { font-size: 12px; }
.words { font-size: 13px; line-height: 1.6; white-space: pre-wrap; overflow-wrap: anywhere; }
.del { background: var(--err-soft); color: var(--err); text-decoration: line-through; }
.add { background: var(--warn-soft); color: var(--warn); text-decoration: none; font-weight: 600; }
.lines { padding: 10px 0; background: var(--paper); border: 1px solid var(--rule); font-size: 11.5px; line-height: 1.55; overflow-x: auto; }
.ln { display: block; padding-right: 12px; white-space: pre; }
.sign { display: inline-block; width: 22px; text-align: center; color: var(--muted); user-select: none; }
.ln-add { background: var(--warn-soft); color: var(--warn); }
.ln-del { background: var(--err-soft); color: var(--err); }
</style>
