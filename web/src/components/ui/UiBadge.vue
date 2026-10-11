<script setup lang="ts">
// Status badge: a word on a soft tint, 26 px tall. Colour never stands alone.
import type { Tone } from '@/lib/status'

withDefaults(defineProps<{ tone?: Tone; wide?: boolean }>(), { tone: 'neutral' })
</script>

<template>
  <span class="badge" :class="[`tone-${tone}`, { wide }]"><slot /></span>
</template>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  height: var(--badge-h);
  min-width: 76px;
  padding: 0 10px;
  border-radius: var(--radius);
  font-size: 12px;
  font-weight: 500;
  white-space: nowrap;
}
.wide { min-width: 100px; }
.tone-ok { background: var(--ok-soft); color: var(--ok); }
.tone-idle, .tone-neutral { background: var(--rule); color: var(--muted); }
.tone-warn { background: var(--warn-soft); color: var(--warn); }
.tone-err { background: var(--err-soft); color: var(--err); }
.tone-info { background: var(--cobalt-soft); color: var(--cobalt); }
</style>
