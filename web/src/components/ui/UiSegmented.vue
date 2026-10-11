<script setup lang="ts">
// Segmented tabs on a rule-coloured track ("Live | Audit log",
// "All keys | Node keys | Client keys"). The selected one is a surface chip.
defineProps<{ options: { value: string; label: string; dot?: boolean }[]; label?: string }>()
const model = defineModel<string>({ required: true })
</script>

<template>
  <div class="seg" role="tablist" :aria-label="label">
    <button
      v-for="o in options"
      :key="o.value"
      type="button"
      role="tab"
      :aria-selected="model === o.value"
      :class="{ on: model === o.value }"
      @click="model = o.value"
    >
      <span v-if="o.dot" class="dot" aria-hidden="true" />{{ o.label }}
    </button>
  </div>
</template>

<style scoped>
.seg {
  display: inline-flex;
  height: var(--control-h);
  padding: 3px;
  background: var(--rule);
  border-radius: var(--radius);
}
button {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  min-width: 104px;
  padding: 0 16px;
  border: 0;
  border-radius: 2px;
  background: transparent;
  color: var(--muted);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
}
button.on { background: var(--surface); color: var(--ink); font-weight: 600; }
.dot { width: 5px; height: 5px; border-radius: 50%; background: currentColor; }
button.on .dot { background: var(--ok); }
</style>
