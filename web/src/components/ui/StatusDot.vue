<script setup lang="ts">
import type { Tone } from '@/lib/status'

withDefaults(defineProps<{ tone?: Tone; size?: number }>(), { tone: 'ok', size: 6 })
</script>

<template>
  <span class="dot" :class="`tone-${tone}`" :style="{ width: `${size}px`, height: `${size}px` }" aria-hidden="true" />
</template>

<style scoped>
.dot { display: inline-block; border-radius: 50%; flex: none; background: var(--muted); }
.tone-ok { background: var(--ok); }
.tone-warn { background: var(--warn); }
.tone-err { background: var(--err); }
.tone-info { background: var(--cobalt); }
</style>
