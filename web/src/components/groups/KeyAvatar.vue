<script setup lang="ts">
// Round initial for a key; stacked in a row they show who is in a group.
defineProps<{ name: string }>()
</script>

<template>
  <span class="av" :title="name" aria-hidden="true">{{ (name[0] ?? '?').toUpperCase() }}</span>
</template>

<style scoped>
.av {
  display: inline-grid;
  place-items: center;
  width: 26px;
  height: 26px;
  border-radius: 50%;
  background: var(--cobalt-soft);
  color: var(--cobalt);
  border: 2px solid var(--surface);
  font-size: 11px;
  font-weight: 600;
}
.av + .av { margin-left: -8px; }
</style>
