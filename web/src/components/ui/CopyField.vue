<script setup lang="ts">
// A value to paste somewhere: mono text in a paper well with a copy button.
// `secret` is the dark well used for one-time secrets; multi-line values
// (commands) wrap.
import CopyButton from './CopyButton.vue'

defineProps<{ value: string; display?: string; secret?: boolean; multiline?: boolean; label?: string }>()
</script>

<template>
  <div class="well" :class="{ secret, multiline }">
    <code class="value" :title="multiline ? undefined : value">{{ display ?? value }}</code>
    <CopyButton :value="value" :label="label ?? 'Copy'" :inverse="secret" />
  </div>
</template>

<style scoped>
.well {
  display: flex;
  align-items: center;
  gap: 12px;
  min-height: 48px;
  padding: 8px 8px 8px 14px;
  background: var(--paper);
  border: 1px solid var(--rule);
  border-radius: var(--radius);
}
.value {
  flex: 1;
  min-width: 0;
  font-size: 12px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--ink);
}
.multiline { align-items: flex-start; padding-top: 14px; padding-bottom: 14px; }
.multiline .value { white-space: pre-wrap; word-break: break-all; line-height: 1.5; }
.multiline :deep(.copy) { margin-top: -6px; }
.secret { min-height: 56px; background: var(--ink); border-color: var(--ink); }
.secret .value { color: var(--surface); font-size: 14px; font-weight: 500; }
</style>
