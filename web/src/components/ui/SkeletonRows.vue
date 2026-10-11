<script setup lang="ts">
// Loading placeholder for lists.
withDefaults(defineProps<{ rows?: number; avatar?: boolean }>(), { rows: 3, avatar: true })
</script>

<template>
  <div class="sk" aria-busy="true" aria-label="Loading">
    <div v-for="i in rows" :key="i" class="row">
      <span v-if="avatar" class="circle" />
      <span class="lines"><span class="l1" /><span class="l2" /></span>
      <span class="chip" />
    </div>
  </div>
</template>

<style scoped>
.sk { padding: 12px 20px; }
.row { display: flex; align-items: center; gap: 18px; height: 50px; }
.circle { width: 28px; height: 28px; border-radius: 50%; background: var(--skeleton); flex: none; }
.lines { flex: 1; display: flex; flex-direction: column; gap: 10px; max-width: 400px; }
.l1 { height: 9px; width: 48%; background: var(--skeleton); }
.l2 { height: 6px; width: 100%; background: var(--skeleton-soft); }
.chip { margin-left: auto; width: 80px; height: 24px; background: var(--skeleton-soft); }
.sk span { animation: pulse 1.4s ease-in-out infinite; }
@keyframes pulse { 50% { opacity: 0.55; } }
</style>
