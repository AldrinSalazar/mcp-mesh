<script setup lang="ts">
// Page title with an optional primary action on the right.
defineProps<{ title: string }>()
</script>

<template>
  <header class="page-head">
    <h1 class="page-title">{{ title }}</h1>
    <div class="actions"><slot /></div>
  </header>
</template>

<style scoped>
.page-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  min-height: 42px;
  margin-bottom: 36px;
}
.actions { display: flex; gap: 12px; }
@media (max-width: 700px) {
  .page-head { margin-bottom: 20px; }
  .page-title { font-size: 30px; }
}
</style>
