<script setup lang="ts">
// Shown while the live stream is down; it reconnects by itself.
import { computed, ref, watch } from 'vue'
import { live } from '@/lib/live'

// Only after a moment, so a quick reconnect doesn't flash it.
const show = ref(false)
let t: ReturnType<typeof setTimeout> | undefined
const down = computed(() => live.connection === 'reconnecting')
watch(down, (d) => {
  clearTimeout(t)
  if (d) t = setTimeout(() => (show.value = true), 1500)
  else show.value = false
})
</script>

<template>
  <div v-if="show" class="banner" role="status">
    <span class="dot" aria-hidden="true" />
    <div>
      <p class="title">Reconnecting…</p>
      <p class="text">The live stream will reconnect automatically.</p>
    </div>
  </div>
</template>

<style scoped>
.banner {
  display: flex;
  gap: 14px;
  align-items: flex-start;
  margin-bottom: 24px;
  padding: 16px 20px;
  background: var(--surface);
  border: 1px solid var(--rule);
}
.dot { width: 7px; height: 7px; border-radius: 50%; background: var(--warn); margin-top: 6px; flex: none; }
.title { font-size: 14px; font-weight: 600; }
.text { font-size: 11px; color: var(--muted); margin-top: 4px; }
</style>
