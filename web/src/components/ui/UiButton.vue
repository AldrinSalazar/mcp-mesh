<script setup lang="ts">
// Buttons: 42 px, 3 px corners. primary = cobalt, secondary = outlined
// surface, danger = brick red, link = cobalt text.
import type { Component } from 'vue'

withDefaults(
  defineProps<{
    variant?: 'primary' | 'secondary' | 'danger' | 'link'
    icon?: Component
    type?: 'button' | 'submit'
    disabled?: boolean
    loading?: boolean
    block?: boolean
  }>(),
  { variant: 'secondary', type: 'button' },
)
</script>

<template>
  <button
    :type="type"
    class="btn"
    :class="[`btn-${variant}`, { block, loading }]"
    :disabled="disabled || loading"
    :aria-busy="loading || undefined"
  >
    <component :is="icon" v-if="icon" class="btn-icon" :size="18" :stroke-width="1.6" aria-hidden="true" />
    <slot />
  </button>
</template>

<style scoped>
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  height: var(--control-h);
  padding: 0 18px;
  border-radius: var(--radius);
  border: 1px solid transparent;
  font-size: 14px;
  font-weight: 600;
  white-space: nowrap;
  cursor: pointer;
  transition: background-color 0.12s, border-color 0.12s, opacity 0.12s;
}
.btn:disabled { cursor: default; opacity: 0.55; }
.btn.loading { cursor: progress; }
.block { width: 100%; justify-content: flex-start; padding: 0 16px; }

.btn-primary { background: var(--cobalt-fill); color: var(--on-cobalt); }
.btn-primary:not(:disabled):hover { background: color-mix(in srgb, var(--cobalt-fill) 88%, black); }

.btn-secondary { background: var(--surface); color: var(--ink); border-color: var(--rule); }
.btn-secondary:not(:disabled):hover { border-color: var(--muted); }

.btn-danger { background: var(--danger-fill); color: #fff; }
.btn-danger:not(:disabled):hover { background: color-mix(in srgb, var(--danger-fill) 88%, black); }

.btn-link {
  height: auto;
  padding: 0;
  background: none;
  color: var(--cobalt);
  font-size: 13px;
  font-weight: 500;
}
.btn-link:not(:disabled):hover { text-decoration: underline; }

.btn-icon { flex: none; }
</style>
