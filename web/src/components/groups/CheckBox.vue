<script setup lang="ts">
// Native check box with the tri-state look (checked / indeterminate / off)
// and its label in the default slot. `state` drives it; the parent decides
// what a change means.
import type { Check } from './grants'

defineProps<{ state: Check; disabled?: boolean; label?: string }>()
const emit = defineEmits<{ change: [on: boolean] }>()

function onChange(e: Event) {
  emit('change', (e.target as HTMLInputElement).checked)
}
</script>

<template>
  <label class="cb" :class="{ disabled }">
    <input
      type="checkbox"
      :checked="state === 'on'"
      :indeterminate="state === 'partial'"
      :disabled="disabled"
      :aria-label="label"
      @change="onChange"
    />
    <span class="txt"><slot /></span>
  </label>
</template>

<style scoped>
.cb { display: flex; align-items: center; gap: 12px; min-width: 0; cursor: pointer; font-size: 14px; }
.cb.disabled { cursor: default; opacity: 0.6; }
input { flex: none; width: 16px; height: 16px; margin: 0; accent-color: var(--cobalt); cursor: inherit; }
.txt { min-width: 0; display: flex; align-items: center; gap: 10px; }
</style>
