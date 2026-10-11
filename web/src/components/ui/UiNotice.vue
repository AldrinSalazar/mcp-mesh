<script setup lang="ts">
// Tinted message box: warnings, errors, and the green "streaming" line.
import type { Component } from 'vue'

withDefaults(defineProps<{ tone?: 'ok' | 'warn' | 'err' | 'info'; icon?: Component; title?: string; bar?: boolean }>(), { tone: 'warn' })
</script>

<template>
  <div class="notice" :class="[`tone-${tone}`, { bar }]" :role="tone === 'err' ? 'alert' : 'status'">
    <component :is="icon" v-if="icon" :size="22" :stroke-width="1.6" class="icon" aria-hidden="true" />
    <div class="content">
      <p v-if="title" class="title">{{ title }}</p>
      <div class="text"><slot /></div>
    </div>
  </div>
</template>

<style scoped>
.notice { display: flex; gap: 14px; padding: 18px 16px; border-radius: var(--radius); font-size: 13px; }
.bar { border-left: 3px solid currentColor; border-radius: 0; }
.icon { flex: none; margin-top: 2px; }
.title { font-size: 14px; font-weight: 600; margin-bottom: 6px; }
.text { font-size: 12px; line-height: 1.5; }
.tone-warn { background: var(--warn-soft); color: var(--warn); }
.tone-err { background: var(--err-soft); color: var(--err); }
.tone-ok { background: var(--ok-soft); color: var(--ok); }
.tone-info { background: var(--cobalt-soft); color: var(--cobalt); }
</style>
