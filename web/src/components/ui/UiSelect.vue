<script setup lang="ts">
// Native select styled as the design's dropdown field ("All nodes ⌄").
defineProps<{ options: { value: string; label: string }[]; label?: string }>()
const model = defineModel<string>({ default: '' })
</script>

<template>
  <label class="select">
    <span v-if="label" class="visually-hidden">{{ label }}</span>
    <select v-model="model">
      <option v-for="o in options" :key="o.value" :value="o.value">{{ o.label }}</option>
    </select>
  </label>
</template>

<style scoped>
.select { position: relative; display: block; }
select {
  appearance: none;
  width: 100%;
  height: var(--control-h);
  padding: 0 34px 0 14px;
  background: var(--surface);
  border: 1px solid var(--rule);
  border-radius: var(--radius);
  font-size: 13px;
  color: var(--muted);
  cursor: pointer;
}
select:focus-visible { outline: none; border-color: var(--cobalt); }
.select::after {
  content: '';
  position: absolute;
  right: 16px;
  top: 50%;
  width: 6px;
  height: 6px;
  border-right: 1.3px solid var(--muted);
  border-bottom: 1.3px solid var(--muted);
  transform: translateY(-70%) rotate(45deg);
  pointer-events: none;
}
</style>
