<script setup lang="ts">
// Form row: label, control (slot), and a hint or error underneath.
defineProps<{ label: string; for?: string; hint?: string; error?: string; ok?: boolean }>()
</script>

<template>
  <div class="row">
    <label class="lbl" :for="$props.for">{{ label }}</label>
    <slot />
    <p v-if="error" class="hint err" role="alert">{{ error }}</p>
    <p v-else-if="hint" class="hint" :class="{ okh: ok }">
      <svg v-if="ok" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M4 13l5 5L20 6" /></svg>
      {{ hint }}
    </p>
  </div>
</template>

<style scoped>
.row { display: flex; flex-direction: column; gap: 10px; }
.row + .row { margin-top: 26px; }
.lbl { font-size: 14px; font-weight: 500; }
.hint { font-size: 12px; color: var(--muted); display: flex; align-items: center; gap: 4px; }
.okh { color: var(--ok); }
.err { color: var(--err); }
</style>
