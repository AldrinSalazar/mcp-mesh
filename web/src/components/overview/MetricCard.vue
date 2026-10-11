<script setup lang="ts">
// One number on the Overview: label, big value, icon top-right, sub line.
// The sub line has a short form for phones (the 2x2 grid is narrow).
defineProps<{
  label: string
  value: string | number
  total?: number
  sub: string
  subShort?: string
  bad?: boolean
  loading?: boolean
}>()
</script>

<template>
  <div class="card panel" :aria-busy="loading || undefined">
    <div class="label name">{{ label }}</div>
    <span class="icon" aria-hidden="true"><slot name="icon" /></span>
    <template v-if="loading">
      <span class="sk big-sk" />
      <span class="sk sub-sk" />
    </template>
    <template v-else>
      <div class="value">{{ value }}<span v-if="total !== undefined" class="total"><span class="slash">/</span> {{ total }}</span></div>
      <div class="sub" :class="{ bad }">
        <span :class="{ 'long': subShort }">{{ sub }}</span><span v-if="subShort" class="short">{{ subShort }}</span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.card { position: relative; padding: 24px 20px 22px; min-height: 150px; display: flex; flex-direction: column; min-width: 0; }
.icon { position: absolute; top: 46px; right: 20px; display: grid; color: var(--cobalt); }
.value { font-family: var(--font-display); font-size: 48px; line-height: 1; font-weight: 500; margin-top: 18px; }
.total { font-size: 24px; color: var(--muted); margin-left: 14px; }
.slash { margin-right: 2px; }
.sub { margin-top: auto; font-size: 12px; color: var(--muted); }
.sub.bad { color: var(--err); }
.short { display: none; }
.sk { display: block; background: var(--skeleton); animation: pulse 1.4s ease-in-out infinite; }
.big-sk { width: 70px; height: 40px; margin-top: 18px; }
.sub-sk { width: 55%; height: 8px; margin-top: auto; background: var(--skeleton-soft); }
@keyframes pulse { 50% { opacity: 0.55; } }

@media (max-width: 700px) {
  .card { padding: 16px 16px 14px; min-height: 122px; }
  .icon { display: none; }
  .value { font-size: 36px; margin-top: 10px; }
  .total { font-size: 36px; color: inherit; margin-left: 0; }
  .total .slash { margin: 0 2px 0 8px; }
  .long { display: none; }
  .short { display: inline; }
}
</style>
